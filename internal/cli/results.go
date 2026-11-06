package cli

import (
	"fmt"
	"math"

	"github.com/hyperoptimizer/cli/internal/api"
	"github.com/spf13/cobra"
)

type objective struct {
	Key       string `json:"key"`
	Direction string `json:"direction"`
}

func resultObjectives(exp *api.ExperimentDetails, selected string) ([]objective, error) {
	objectives := []objective{}
	for _, metric := range exp.Metrics {
		if selected != "" && metric.Key != selected {
			continue
		}
		if metric.Direction == nil || (*metric.Direction != "minimize" && *metric.Direction != "maximize") {
			return nil, failure(5, "Objective "+metric.Key+" has an unsupported direction")
		}
		objectives = append(objectives, objective{metric.Key, *metric.Direction})
	}
	if len(objectives) == 0 {
		return nil, failure(2, "No matching optimization objective; inspect objectives with hpo experiment show")
	}
	return objectives, nil
}

// Match the dashboard Pareto rule: complete successful results, strict dominance,
// and all ties retained. Only immutable, optimizer-accepted results are eligible.
func bestRuns(runs []api.RunDetails, objectives []objective) []api.RunDetails {
	eligible := []api.RunDetails{}
	for _, run := range runs {
		if run.Status != "succeeded" || run.FinalizationState != "complete" || run.ResultsAcceptedAt == nil {
			continue
		}
		complete := true
		for _, obj := range objectives {
			value, ok := run.Metrics[obj.Key]
			if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
				complete = false
				break
			}
		}
		if complete {
			eligible = append(eligible, run)
		}
	}
	best := []api.RunDetails{}
	for _, candidate := range eligible {
		dominated := false
		for _, other := range eligible {
			strictlyBetter, noWorse := false, true
			for _, obj := range objectives {
				a, b := other.Metrics[obj.Key], candidate.Metrics[obj.Key]
				if (obj.Direction == "minimize" && a > b) || (obj.Direction == "maximize" && a < b) {
					noWorse = false
					break
				}
				if a != b {
					strictlyBetter = true
				}
			}
			if noWorse && strictlyBetter {
				dominated = true
				break
			}
		}
		if !dominated {
			best = append(best, candidate)
		}
	}
	return best
}
func (a *App) experimentBestCommand() *cobra.Command {
	var selected string
	var pareto bool
	cmd := &cobra.Command{Use: "best <id>", Short: "Retrieve winning parameters or the multi-objective Pareto front", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if pareto && selected != "" {
			return failure(2, "Use either --objective or --pareto")
		}
		client, ctx, source, err := a.authorized(cmd.Context())
		if err != nil {
			return err
		}
		exp, err := a.resolveExperiment(ctx, client, args[0], source)
		if err != nil {
			return err
		}
		if err := managedExperiment(exp); err != nil {
			return err
		}
		objectives, err := resultObjectives(exp, selected)
		if err != nil {
			return err
		}
		runs, err := a.allRuns(ctx, client, exp, source)
		if err != nil {
			return err
		}
		// Missing other objectives must not make a partially finalized trial eligible.
		allObjectives, err := resultObjectives(exp, "")
		if err != nil {
			return err
		}
		complete := []api.RunDetails{}
		for _, run := range runs {
			valid := true
			for _, obj := range allObjectives {
				if _, ok := run.Metrics[obj.Key]; !ok {
					valid = false
					break
				}
			}
			if valid {
				complete = append(complete, run)
			}
		}
		best := bestRuns(complete, objectives)
		mode := "single_objective"
		if len(objectives) > 1 {
			mode = "pareto"
		}
		empty := "No eligible results: successful trials must finish finalization, contain every objective, and be accepted by the optimizer."
		if exp.Optimization != nil && !terminalStatus(exp.Optimization.Status) {
			empty += " Experiment is still active; results may be pending."
		}
		if len(best) > 0 && a.Output != "jsonl" && a.Output != "csv" && a.Output != "markdown" {
			fmt.Fprintf(a.Err, "%d result(s), %s; tied results are retained.\n", len(best), mode)
		}
		return a.printRuns("best_results", exp.Id, best, empty, map[string]any{"mode": mode, "objectives": objectives, "eligibility": "successful_finalized_optimizer_accepted", "emptyReason": func() string {
			if len(best) == 0 {
				return empty
			}
			return ""
		}()})
	}}
	cmd.Flags().StringVar(&selected, "objective", "", "Rank by one objective's configured direction")
	cmd.Flags().BoolVar(&pareto, "pareto", false, "Return non-dominated results across all objectives (default for multiple objectives)")
	return cmd
}
