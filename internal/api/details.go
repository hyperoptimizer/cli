package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	gql "github.com/hyperoptimizer/cli/internal/graphql"
)

type ExperimentDetails = gql.CliExperimentDetailsExperiments
type LogEntry = gql.CliLogsLogsLogPageHitsLogEntry
type LogPage = gql.CliLogsLogsLogPage
type LogsInput = gql.LogsInput

type RunDetails struct {
	Run
	Name         string         `json:"name"`
	ProjectID    string         `json:"projectId"`
	ExperimentID string         `json:"experimentId"`
	Parameters   map[string]any `json:"parameters"`
	CreatedAt    time.Time      `json:"createdAt"`
	FinalizedAt  *time.Time     `json:"finalizedAt"`
	LastError    *string        `json:"lastError"`
}

func equal(value string) map[string]any           { return map[string]any{"_eq": value} }
func filter(value map[string]any) json.RawMessage { data, _ := json.Marshal(value); return data }

func (c *Client) Details(ctx context.Context, id string) (*ExperimentDetails, error) {
	result, err := gql.CliExperimentDetails(ctx, c.wire, filter(map[string]any{"id": equal(id)}))
	if err != nil {
		return nil, clientError(err)
	}
	if len(result.Experiments) == 0 {
		return nil, &Error{Code: "NOT_FOUND", Message: "Experiment unavailable in the selected organization"}
	}
	row := result.Experiments[0]
	if row.Id != id || row.ProjectId == "" {
		return nil, incompatible()
	}
	return &row, nil
}

func (c *Client) Intent(ctx context.Context, project, id, action string) (string, error) {
	input := gql.ExperimentCommandInput{ProjectId: project, Id: id}
	switch action {
	case "pause":
		result, err := gql.CliPause(ctx, c.wire, input)
		if err != nil {
			return "", clientError(err)
		}
		if !result.PauseExperiment.Success || result.PauseExperiment.Id != id {
			return "", incompatible()
		}
		return result.PauseExperiment.DesiredStatus, nil
	case "resume":
		result, err := gql.CliResume(ctx, c.wire, input)
		if err != nil {
			return "", clientError(err)
		}
		if !result.ResumeExperiment.Success || result.ResumeExperiment.Id != id {
			return "", incompatible()
		}
		return result.ResumeExperiment.DesiredStatus, nil
	default:
		return "", fmt.Errorf("unsupported experiment action %q", action)
	}
}

// DetailedRuns supports either an exact run ID or a keyset-paginated experiment.
func (c *Client) DetailedRuns(ctx context.Context, project, experiment, id, after string, limit int) ([]RunDetails, error) {
	where := map[string]any{"id": map[string]any{"_gt": after}}
	if project != "" {
		where["project_id"] = equal(project)
	}
	if experiment != "" {
		where["experiment_id"] = equal(experiment)
	}
	if id != "" {
		where["id"] = equal(id)
	}
	result, err := gql.CliRunDetailsPage(ctx, c.wire, filter(where), limit)
	if err != nil {
		return nil, clientError(err)
	}
	if result.Runs == nil {
		return nil, incompatible()
	}
	rows := make([]RunDetails, 0, len(result.Runs))
	for _, item := range result.Runs {
		row := item.CliRunDetails
		if row.Id == "" || row.ProjectId == "" || row.ExperimentId == "" {
			return nil, incompatible()
		}
		if (id != "" && row.Id != id) || (project != "" && row.ProjectId != project) || (experiment != "" && row.ExperimentId != experiment) {
			return nil, incompatible()
		}
		run := RunDetails{Run: Run{Id: row.Id, Status: row.Status, FinalizationState: row.FinalizationState, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Metrics: map[string]float64{}}, Name: row.Name, ProjectID: row.ProjectId, ExperimentID: row.ExperimentId, Parameters: map[string]any{}, CreatedAt: row.CreatedAt, FinalizedAt: row.FinalizedAt, LastError: row.LastError}
		if row.Optimization != nil {
			run.Number = row.Optimization.Number
			run.ResultsAcceptedAt = row.Optimization.ResultsAcceptedAt
		}
		for _, p := range row.Parameters {
			switch {
			case p.NumberValue != nil:
				value, err := metricNumber(*p.NumberValue)
				if err != nil {
					return nil, err
				}
				run.Parameters[p.Key] = value
			case p.StringValue != nil:
				run.Parameters[p.Key] = *p.StringValue
			case p.BooleanValue != nil:
				run.Parameters[p.Key] = *p.BooleanValue
			default:
				return nil, incompatible()
			}
		}
		if row.FinalizationState == "complete" {
			for _, m := range row.FinalMetrics {
				value, err := metricNumber(m.Value)
				if err != nil {
					return nil, err
				}
				run.Metrics[m.Key] = value
			}
		} else {
			for _, m := range row.Metrics {
				value := m.LastValue
				if m.Aggregation != nil {
					switch *m.Aggregation {
					case "min":
						value = m.MinValue
					case "max":
						value = m.MaxValue
					case "mean":
						value = m.MeanValue
					case "explicit":
						value = m.ExplicitValue
					}
				}
				if value != nil {
					number, err := metricNumber(*value)
					if err != nil {
						return nil, err
					}
					run.Metrics[m.Key] = number
				}
			}
		}
		rows = append(rows, run)
	}
	return rows, nil
}

func (c *Client) Logs(ctx context.Context, input LogsInput) (*LogPage, error) {
	result, err := gql.CliLogs(ctx, c.wire, input)
	if err != nil {
		return nil, clientError(err)
	}
	if result.Logs.WindowEnd.IsZero() || result.Logs.TotalHits < 0 || result.Logs.Hits == nil {
		return nil, incompatible()
	}
	return &result.Logs, nil
}
