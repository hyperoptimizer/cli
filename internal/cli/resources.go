package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hyperoptimizer/cli/internal/api"
	"github.com/spf13/cobra"
)

func (a *App) createProjectCommand() *cobra.Command {
	return &cobra.Command{Use: "create <name>", Short: "Create a project without writing a local configuration", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		name := strings.TrimSpace(args[0])
		if name == "" || len([]rune(name)) > 255 {
			return failure(2, "Project name must contain 1–255 characters")
		}
		client, ctx, source, err := a.authorized(cmd.Context())
		if err != nil {
			return err
		}
		project, err := client.CreateProject(ctx, name)
		if err != nil {
			return apiError(err, source)
		}
		return a.event("project_created", map[string]any{"project": project}, fmt.Sprintf("Created project %s (%s)", project.Name, project.Id))
	}}
}

// Resolve only within the authorized organization. Explicit --project is a scope
// assertion; local config is unnecessary when a globally unique ID is supplied.
func (a *App) resolveExperiment(ctx context.Context, client *api.Client, id string, source CredentialSource) (*api.ExperimentDetails, error) {
	exp, err := client.Details(ctx, id)
	if err != nil {
		return nil, apiError(err, source)
	}
	if a.Project != "" && exp.ProjectId != a.Project {
		return nil, failure(2, "Experiment unavailable in the selected project")
	}
	return exp, nil
}
func managedExperiment(exp *api.ExperimentDetails) error {
	if exp.Kind != "managed_optimization" || exp.Optimization == nil {
		return failure(2, "This command requires a managed optimization experiment; external tracking experiments support show, run inspection, and logs")
	}
	return nil
}

func (a *App) experimentControlCommand(action string) *cobra.Command {
	return &cobra.Command{Use: action + " <id>", Short: strings.ToUpper(action[:1]) + action[1:] + " an optimization experiment", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
		desired, err := client.Intent(ctx, exp.ProjectId, exp.Id, action)
		if err != nil {
			return apiError(err, source)
		}
		return a.event("experiment_"+action+"_requested", map[string]any{"experimentId": exp.Id, "projectId": exp.ProjectId, "desiredStatus": desired}, fmt.Sprintf("%s requested for %s; desired status: %s. Runtime state may take time to converge.", strings.ToUpper(action[:1])+action[1:], exp.Id, desired))
	}}
}
func (a *App) allRuns(ctx context.Context, client *api.Client, exp *api.ExperimentDetails, source CredentialSource) ([]api.RunDetails, error) {
	rows, err := collectPages(func(after string, limit int) ([]api.RunDetails, error) {
		return client.DetailedRuns(ctx, exp.ProjectId, exp.Id, "", after, limit)
	}, func(row api.RunDetails) string { return row.Id })
	if err != nil {
		return nil, apiError(err, source)
	}
	return rows, nil
}
func (a *App) experimentShowCommand() *cobra.Command {
	return &cobra.Command{Use: "show <id>", Short: "Inspect an experiment and its current progress once", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, ctx, source, err := a.authorized(cmd.Context())
		if err != nil {
			return err
		}
		exp, err := a.resolveExperiment(ctx, client, args[0], source)
		if err != nil {
			return err
		}
		runs, err := a.allRuns(ctx, client, exp, source)
		if err != nil {
			return err
		}
		counts := map[string]int{}
		for _, run := range runs {
			counts[run.Status]++
		}
		rows := [][]string{{"Name", exp.Name}, {"ID", exp.Id}, {"Project", exp.ProjectId}, {"Kind", exp.Kind}, {"Created", exp.CreatedAt.Format(time.RFC3339)}, {"Runs", fmt.Sprint(len(runs))}, {"Run status counts", jsonText(counts)}}
		if exp.Description != nil {
			rows = append(rows, []string{"Description", *exp.Description})
		}
		if opt := exp.Optimization; opt != nil {
			rows = append(rows, [][]string{{"Status", opt.Status}, {"Desired status", opt.DesiredStatus}, {"Trial limit", fmt.Sprint(opt.MaxTrialCount)}, {"Parallel trials", fmt.Sprint(opt.ParallelTrialCount)}, {"Algorithm", opt.Algorithm}}...)
			if opt.DockerImage != nil {
				rows = append(rows, []string{"Image", string(*opt.DockerImage)})
			}
			if opt.CommandTemplate != nil {
				rows = append(rows, []string{"Command", *opt.CommandTemplate})
			}
		}
		for _, parameter := range exp.Parameters {
			rows = append(rows, []string{"Parameter: " + parameter.Key, jsonText(parameter)})
		}
		for _, objective := range exp.Metrics {
			direction := "—"
			if objective.Direction != nil {
				direction = *objective.Direction
			}
			rows = append(rows, []string{"Objective: " + objective.Key, direction})
		}
		return a.listOutput("experiment", map[string]any{"experiment": exp, "runCount": len(runs), "runStatusCounts": counts}, []string{"FIELD", "VALUE"}, rows, "")
	}}
}
func jsonText(value any) string { data, _ := json.Marshal(value); return string(data) }

func (a *App) runCommand() *cobra.Command {
	root := &cobra.Command{Use: "run", Aliases: []string{"runs"}, Short: "Inspect trials, parameters, and results"}
	var experiment string
	list := &cobra.Command{Use: "list", Short: "List all runs in an experiment, including parameters and metrics", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if experiment == "" {
			return failure(2, "Specify --experiment <id>")
		}
		client, ctx, source, err := a.authorized(cmd.Context())
		if err != nil {
			return err
		}
		exp, err := a.resolveExperiment(ctx, client, experiment, source)
		if err != nil {
			return err
		}
		runs, err := a.allRuns(ctx, client, exp, source)
		if err != nil {
			return err
		}
		return a.printRuns("runs", exp.Id, runs, "No runs in this experiment.", nil)
	}}
	list.Flags().StringVar(&experiment, "experiment", "", "Experiment ID (required)")
	root.AddCommand(list)
	root.AddCommand(&cobra.Command{Use: "show <id>", Short: "Inspect a run's parameters, metrics, timestamps, and errors", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, ctx, source, err := a.authorized(cmd.Context())
		if err != nil {
			return err
		}
		runs, err := client.DetailedRuns(ctx, a.Project, "", args[0], "", 1)
		if err != nil {
			return apiError(err, source)
		}
		if len(runs) == 0 {
			return failure(2, "Run unavailable in the selected organization or project")
		}
		run := runs[0]
		rows := [][]string{{"ID", run.Id}, {"Name", run.Name}, {"Project", run.ProjectID}, {"Experiment", run.ExperimentID}, {"Trial", fmt.Sprint(run.Number)}, {"Status", run.Status}, {"Finalization", run.FinalizationState}, {"Created", run.CreatedAt.Format(time.RFC3339)}}
		for _, entry := range []struct {
			name  string
			value *time.Time
		}{{"Started", run.StartedAt}, {"Finished", run.FinishedAt}, {"Finalized", run.FinalizedAt}, {"Results accepted", run.ResultsAcceptedAt}} {
			if entry.value != nil {
				rows = append(rows, []string{entry.name, entry.value.Format(time.RFC3339Nano)})
			}
		}
		if run.LastError != nil {
			rows = append(rows, []string{"Last error", *run.LastError})
		}
		rows = append(rows, []string{"Parameters", jsonText(run.Parameters)}, []string{"Metrics", jsonText(run.Metrics)})
		return a.listOutput("run", map[string]any{"run": run}, []string{"FIELD", "VALUE"}, rows, "")
	}})
	return root
}

func (a *App) printRuns(event, experiment string, runs []api.RunDetails, empty string, extra map[string]any) error {
	headers := []string{"TRIAL", "STATUS", "FINALIZATION", "PARAMETERS", "METRICS", "ID"}
	rows := make([][]string, 0, len(runs))
	if a.Output == "csv" {
		params, metrics := map[string]bool{}, map[string]bool{}
		for _, run := range runs {
			for key := range run.Parameters {
				params[key] = true
			}
			for key := range run.Metrics {
				metrics[key] = true
			}
		}
		pkeys, mkeys := sortedKeys(params), sortedKeys(metrics)
		headers = []string{"id", "experimentId", "projectId", "number", "status", "finalizationState", "createdAt", "startedAt", "finishedAt", "finalizedAt", "resultsAcceptedAt", "lastError"}
		for _, key := range pkeys {
			headers = append(headers, "parameter."+key)
		}
		for _, key := range mkeys {
			headers = append(headers, "metric."+key)
		}
		for _, run := range runs {
			row := []string{run.Id, run.ExperimentID, run.ProjectID, fmt.Sprint(run.Number), run.Status, run.FinalizationState, run.CreatedAt.Format(time.RFC3339Nano), timeText(run.StartedAt), timeText(run.FinishedAt), timeText(run.FinalizedAt), timeText(run.ResultsAcceptedAt), ""}
			if run.LastError != nil {
				row[11] = *run.LastError
			}
			for _, key := range pkeys {
				value := ""
				if v, ok := run.Parameters[key]; ok {
					value = fmt.Sprint(v)
				}
				row = append(row, value)
			}
			for _, key := range mkeys {
				value := ""
				if v, ok := run.Metrics[key]; ok {
					value = fmt.Sprint(v)
				}
				row = append(row, value)
			}
			rows = append(rows, row)
		}
	} else {
		for _, run := range runs {
			rows = append(rows, []string{fmt.Sprint(run.Number), run.Status, run.FinalizationState, jsonText(run.Parameters), jsonText(run.Metrics), run.Id})
		}
	}
	data := map[string]any{"experimentId": experiment, "runs": runs}
	for key, value := range extra {
		data[key] = value
	}
	return a.listOutput(event, data, headers, rows, empty)
}
func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func timeText(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.Format(time.RFC3339Nano)
}
