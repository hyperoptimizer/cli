package cli

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/hyperoptimizer/cli/internal/api"
	"github.com/spf13/cobra"
)

func (a *App) logsCommand() *cobra.Command {
	var runID, since string
	var follow bool
	var limit int
	cmd := &cobra.Command{Use: "logs <experiment-id>", Short: "Read workload logs, optionally following new entries", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if a.Output == "table" {
			return failure(2, "Logs support --output auto, plain, or jsonl")
		}
		if limit < 1 || limit > 10000 {
			return failure(2, "--limit must be between 1 and 10000")
		}
		duration, err := time.ParseDuration(since)
		if err != nil || duration <= 0 {
			return failure(2, "--since must be a positive duration, such as 30m or 24h")
		}
		client, ctx, source, err := a.authorized(cmd.Context())
		if err != nil {
			return err
		}
		exp, err := a.resolveExperiment(ctx, client, args[0], source)
		if err != nil {
			return err
		}
		if runID != "" {
			rows, err := client.DetailedRuns(ctx, exp.ProjectId, exp.Id, runID, "", 1)
			if err != nil {
				return apiError(err, source)
			}
			if len(rows) == 0 {
				return failure(2, "Run unavailable in this experiment")
			}
		}
		start := time.Now().UTC().Add(-duration)
		seen := map[string]int{}
		for {
			end := time.Now().UTC()
			entries, err := a.logWindow(ctx, client, exp, runID, start, end, limit, source)
			if err != nil {
				return err
			}
			fresh, next := freshLogs(entries, seen)
			for _, entry := range fresh {
				if a.Output == "jsonl" {
					if err := a.event("log", map[string]any{"experimentId": exp.Id, "log": entry}, ""); err != nil {
						return err
					}
				} else {
					level, run := "", ""
					if entry.Level != nil {
						level = *entry.Level
					}
					if entry.TrialId != nil {
						run = *entry.TrialId
					}
					if _, err := fmt.Fprintf(a.Out, "%s %s %s %s\n", entry.Timestamp, displayCell(level), displayCell(run), displayCell(entry.Message)); err != nil {
						return err
					}
				}
			}
			if !follow {
				if len(entries) == 0 && a.Output != "jsonl" {
					_, err = fmt.Fprintln(a.Out, "No logs in this window. Logs may still be ingesting or outside retention.")
					return err
				}
				return nil
			}
			seen = next
			// Re-read an overlap window to catch delayed ingestion. Multiset counts
			// preserve repeated identical messages instead of deduplicating them away.
			start = maxTime(start, end.Add(-30*time.Second))
			if err := wait(ctx, 2*time.Second); err != nil {
				return err
			}
		}
	}}
	cmd.Flags().StringVar(&runID, "run", "", "Restrict logs to a run in this experiment")
	cmd.Flags().StringVar(&since, "since", "1h", "Look-back duration (for example 30m or 24h)")
	cmd.Flags().IntVar(&limit, "limit", 1000, "Maximum entries per window (1–10000); narrow --since if exceeded")
	cmd.Flags().BoolVar(&follow, "follow", false, "Poll for new logs, rechecking a 30-second ingestion overlap")
	return cmd
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (a *App) logWindow(ctx context.Context, client *api.Client, exp *api.ExperimentDetails, runID string, start, end time.Time, limit int, source CredentialSource) ([]api.LogEntry, error) {
	entries := []api.LogEntry{}
	for offset := 0; ; {
		pageSize := min(100, limit-offset)
		input := api.LogsInput{ProjectId: &exp.ProjectId, ExperimentId: &exp.Id, Start: &start, End: &end, Limit: &pageSize, Offset: &offset}
		if runID != "" {
			input.RunId = &runID
		}
		var page *api.LogPage
		err := a.retry(ctx, 4, 0, func() error { var err error; page, err = client.Logs(ctx, input); return apiError(err, source) })
		if err != nil {
			return nil, err
		}
		if page.TotalHits > float64(limit) {
			return nil, failure(2, "Log window contains "+strconv.FormatFloat(page.TotalHits, 'f', 0, 64)+" entries, exceeding --limit "+strconv.Itoa(limit)+". Narrow --since or increase --limit (maximum 10000); no partial window was printed.")
		}
		if !page.WindowEnd.Equal(end) {
			return nil, failure(5, "Log server did not preserve the requested pagination window")
		}
		if len(page.Hits) > pageSize {
			return nil, failure(5, "Log server exceeded the requested page size")
		}
		for _, entry := range page.Hits {
			timestamp, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
			if err != nil || timestamp.Before(start) || !timestamp.Before(end) {
				return nil, failure(5, "Log server returned an invalid timestamp or out-of-window entry")
			}
			if entry.ExperimentId != nil && *entry.ExperimentId != exp.Id {
				return nil, failure(5, "Log server returned an entry from another experiment")
			}
			if runID != "" && entry.TrialId != nil && *entry.TrialId != runID {
				return nil, failure(5, "Log server returned an entry from another run")
			}
		}
		entries = append(entries, page.Hits...)
		offset += len(page.Hits)
		if float64(offset) >= page.TotalHits {
			break
		}
		if len(page.Hits) == 0 || offset >= limit {
			return nil, failure(5, "Log pagination ended before all matching entries were returned")
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, entries[i].Timestamp)
		b, _ := time.Parse(time.RFC3339Nano, entries[j].Timestamp)
		return a.Before(b)
	})
	return entries, nil
}
func freshLogs(entries []api.LogEntry, previous map[string]int) ([]api.LogEntry, map[string]int) {
	counts := map[string]int{}
	fresh := []api.LogEntry{}
	for _, entry := range entries {
		key := jsonText(entry)
		counts[key]++
		if counts[key] > previous[key] {
			fresh = append(fresh, entry)
		}
	}
	return fresh, counts
}
