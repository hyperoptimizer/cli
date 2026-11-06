package cli

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hyperoptimizer/cli/internal/api"
)

func detailsFixture(kind string) map[string]any {
	row := map[string]any{"id": "exp_a", "projectId": "proj_a", "name": "test", "kind": kind, "createdAt": "2026-11-01T00:00:00Z", "parameters": []any{}, "metrics": []any{map[string]any{"key": "loss", "direction": "minimize"}}}
	if kind == "managed_optimization" {
		row["optimization"] = map[string]any{"status": "paused", "desiredStatus": "paused", "maxTrialCount": 100, "parallelTrialCount": 2, "algorithm": "random", "dockerImage": map[string]any{"image": "example@sha256:abc"}}
	}
	return row
}
func runFixture(id string) map[string]any {
	return map[string]any{"id": id, "projectId": "proj_a", "experimentId": "exp_a", "name": "trial", "status": "succeeded", "finalizationState": "complete", "createdAt": "2026-11-01T00:00:00Z", "optimization": map[string]any{"number": 1, "resultsAcceptedAt": "2026-11-01T00:01:00Z"}, "parameters": []any{map[string]any{"key": "rate", "numberValue": "0.01"}, map[string]any{"key": "enabled", "booleanValue": false}, map[string]any{"key": "label", "stringValue": "hello, \"world\""}}, "finalMetrics": []any{map[string]any{"key": "loss", "value": "0.2"}}, "metrics": []any{map[string]any{"key": "loss", "lastValue": "999"}}}
}
func TestProjectCreateDoesNotWriteConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		operation := decodeGraphQL(t, r)
		if operation.OperationName != "CliCreateProject" {
			t.Errorf("operation %s", operation.OperationName)
		}
		var input map[string]any
		json.Unmarshal(operation.Variables["input"], &input)
		if input["name"] != "New project" {
			t.Errorf("input %v", input)
		}
		writeGraphQL(w, "createProject", map[string]any{"id": "proj_a", "name": "New project"})
	}))
	defer server.Close()
	app, out, _ := testApp(server.URL)
	app.ConfigPath = t.TempDir() + "/missing/hpo.toml"
	cmd := app.Command()
	cmd.SetArgs([]string{"project", "create", "New project", "--output", "jsonl", "--org", "org_a"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"event":"project_created"`) || !strings.Contains(out.String(), "proj_a") {
		t.Fatal(out.String())
	}
}
func TestExperimentCommandsResolveProjectAndPreserveScope(t *testing.T) {
	for _, action := range []string{"pause", "resume", "show"} {
		for _, kind := range []string{"managed_optimization", "external_tracking"} {
			t.Run(action+kind, func(t *testing.T) {
				controls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("X-HPO-Organization") != "org_a" {
						t.Error("organization scope missing")
					}
					op := decodeGraphQL(t, r)
					switch op.OperationName {
					case "CliExperimentDetails":
						writeGraphQL(w, "experiments", []any{detailsFixture(kind)})
					case "CliRunDetailsPage":
						writeGraphQL(w, "runs", []any{})
					case "CliPause", "CliResume":
						controls++
						var input map[string]any
						json.Unmarshal(op.Variables["input"], &input)
						if input["projectId"] != "proj_a" || input["id"] != "exp_a" {
							t.Errorf("input %v", input)
						}
						field, desired := "pauseExperiment", "paused"
						if action == "resume" {
							field, desired = "resumeExperiment", "running"
						}
						writeGraphQL(w, field, map[string]any{"id": "exp_a", "desiredStatus": desired, "success": true})
					default:
						t.Errorf("operation %s", op.OperationName)
					}
				}))
				defer server.Close()
				app, out, _ := testApp(server.URL)
				cmd := app.Command()
				cmd.SetArgs([]string{"experiment", action, "exp_a", "--org", "org_a", "--output", "jsonl"})
				err := cmd.Execute()
				if kind == "external_tracking" && action != "show" {
					if err == nil || controls != 0 {
						t.Fatalf("control allowed: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), "exp_a") {
					t.Fatal(out.String())
				}
			})
		}
	}
}
func TestRunListPaginationAndExports(t *testing.T) {
	for _, mode := range []string{"jsonl", "csv", "markdown", "table"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				op := decodeGraphQL(t, r)
				if op.OperationName == "CliExperimentDetails" {
					writeGraphQL(w, "experiments", []any{detailsFixture("managed_optimization")})
					return
				}
				if op.OperationName != "CliRunDetailsPage" {
					t.Errorf("operation %s", op.OperationName)
				}
				calls++
				var where struct {
					ID struct {
						Gt string `json:"_gt"`
					} `json:"id"`
					Project struct {
						Eq string `json:"_eq"`
					} `json:"project_id"`
					Experiment struct {
						Eq string `json:"_eq"`
					} `json:"experiment_id"`
				}
				json.Unmarshal(op.Variables["where"], &where)
				if where.Project.Eq != "proj_a" || where.Experiment.Eq != "exp_a" {
					t.Error("run scope missing")
				}
				start := 0
				if where.ID.Gt != "" {
					fmt.Sscanf(where.ID.Gt, "run_%03d", &start)
					start++
				}
				rows := []any{}
				for i := start; i < 205 && i < start+101; i++ {
					rows = append(rows, runFixture(fmt.Sprintf("run_%03d", i)))
				}
				writeGraphQL(w, "runs", rows)
			}))
			defer server.Close()
			app, out, _ := testApp(server.URL)
			cmd := app.Command()
			cmd.SetArgs([]string{"run", "list", "--experiment", "exp_a", "--output", mode})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if calls != 3 {
				t.Fatalf("pages=%d", calls)
			}
			if mode == "csv" {
				rows, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
				if err != nil || len(rows) != 206 {
					t.Fatalf("CSV %v rows %d", err, len(rows))
				}
				values := map[string]string{}
				for i, key := range rows[0] {
					values[key] = rows[1][i]
				}
				if values["parameter.enabled"] != "false" || values["parameter.rate"] != "0.01" || values["parameter.label"] != "hello, \"world\"" || values["metric.loss"] != "0.2" {
					t.Fatalf("wrong values: %v", values)
				}
			}
			if mode == "jsonl" {
				var event struct {
					Runs []api.RunDetails `json:"runs"`
				}
				if err := json.Unmarshal(out.Bytes(), &event); err != nil {
					t.Fatal(err)
				}
				if len(event.Runs) != 205 || event.Runs[0].Parameters["enabled"] != false || event.Runs[0].Metrics["loss"] != 0.2 {
					t.Fatalf("bad structured results %+v", event.Runs[0])
				}
			}
		})
	}
}
func TestProjectMismatchPreventsControl(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		decodeGraphQL(t, r)
		writeGraphQL(w, "experiments", []any{detailsFixture("managed_optimization")})
	}))
	defer server.Close()
	app, _, _ := testApp(server.URL)
	cmd := app.Command()
	cmd.SetArgs([]string{"experiment", "resume", "exp_a", "--project", "wrong"})
	if err := cmd.Execute(); err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
func TestBestRunsEligibilityAndPareto(t *testing.T) {
	accepted := time.Now()
	makeRun := func(id string, loss, accuracy float64) api.RunDetails {
		return api.RunDetails{Run: api.Run{Id: id, Status: "succeeded", FinalizationState: "complete", ResultsAcceptedAt: &accepted, Metrics: map[string]float64{"loss": loss, "accuracy": accuracy}}}
	}
	a, b, c, tie := makeRun("a", 1, .9), makeRun("b", 2, .95), makeRun("c", 3, .8), makeRun("tie", 1, .9)
	pending, failed, unaccepted, missing := makeRun("pending", 0, 1), makeRun("failed", 0, 1), makeRun("unaccepted", 0, 1), makeRun("missing", 0, 1)
	pending.FinalizationState = "pending"
	failed.Status = "failed"
	unaccepted.ResultsAcceptedAt = nil
	delete(missing.Metrics, "accuracy")
	runs := []api.RunDetails{a, b, c, tie, pending, failed, unaccepted, missing}
	pareto := bestRuns(runs, []objective{{"loss", "minimize"}, {"accuracy", "maximize"}})
	if len(pareto) != 3 || pareto[0].Id != "a" || pareto[1].Id != "b" || pareto[2].Id != "tie" {
		t.Fatalf("Pareto %+v", pareto)
	}
	best := bestRuns([]api.RunDetails{a, b, c, tie}, []objective{{"loss", "minimize"}})
	if len(best) != 2 {
		t.Fatalf("ties lost %+v", best)
	}
}
func TestFreshLogsRetainsIdenticalOccurrences(t *testing.T) {
	entry := api.LogEntry{Timestamp: "2026-11-01T00:00:00Z", Message: "same"}
	fresh, seen := freshLogs([]api.LogEntry{entry, entry}, nil)
	if len(fresh) != 2 {
		t.Fatal(fresh)
	}
	fresh, _ = freshLogs([]api.LogEntry{entry, entry, entry}, seen)
	if len(fresh) != 1 {
		t.Fatal(fresh)
	}
}
func TestLogWindowPagesAndRejectsOverflow(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(fmt.Sprint(overflow), func(t *testing.T) {
			start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
			end := start.Add(time.Hour)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				op := decodeGraphQL(t, r)
				var input api.LogsInput
				json.Unmarshal(op.Variables["input"], &input)
				if input.End == nil || !input.End.Equal(end) || input.ProjectId == nil || *input.ProjectId != "proj_a" {
					t.Error("window or scope changed")
				}
				offset := *input.Offset
				rows := []any{}
				for i := offset; i < 205 && i < offset+100; i++ {
					rows = append(rows, map[string]any{"timestamp": start.Add(time.Minute).Format(time.RFC3339), "message": "identical", "experimentId": "exp_a"})
				}
				total := 205
				if overflow {
					total = 10001
				}
				writeGraphQL(w, "logs", map[string]any{"windowEnd": end.Format(time.RFC3339), "totalHits": total, "hits": rows})
			}))
			defer server.Close()
			app, _, _ := testApp(server.URL)
			client, ctx, source, err := app.authorized(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			entries, err := app.logWindow(ctx, client, &api.ExperimentDetails{Id: "exp_a", ProjectId: "proj_a"}, "", start, end, 1000, source)
			if overflow {
				if err == nil || entries != nil || calls != 1 {
					t.Fatalf("overflow %v %d", err, calls)
				}
				return
			}
			if err != nil || len(entries) != 205 || calls != 3 {
				t.Fatalf("logs=%d calls=%d err=%v", len(entries), calls, err)
			}
		})
	}
}

func TestBestCommandFormatsAndEmptyReason(t *testing.T) {
	for _, empty := range []bool{false, true} {
		for _, mode := range []string{"jsonl", "csv"} {
			t.Run(fmt.Sprint(empty)+mode, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					op := decodeGraphQL(t, r)
					if op.OperationName == "CliExperimentDetails" {
						writeGraphQL(w, "experiments", []any{detailsFixture("managed_optimization")})
						return
					}
					rows := []any{}
					if !empty {
						rows = append(rows, runFixture("run_a"))
					}
					writeGraphQL(w, "runs", rows)
				}))
				defer server.Close()
				app, out, _ := testApp(server.URL)
				cmd := app.Command()
				cmd.SetArgs([]string{"experiment", "best", "exp_a", "--objective", "loss", "--output", mode})
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
				if mode == "jsonl" {
					var event struct {
						Mode        string           `json:"mode"`
						Runs        []api.RunDetails `json:"runs"`
						EmptyReason string           `json:"emptyReason"`
					}
					if err := json.Unmarshal(out.Bytes(), &event); err != nil {
						t.Fatal(err)
					}
					if event.Mode != "single_objective" || (empty && (len(event.Runs) != 0 || event.EmptyReason == "")) || (!empty && len(event.Runs) != 1) {
						t.Fatalf("event %+v", event)
					}
				} else {
					rows, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
					if err != nil || len(rows) == 0 {
						t.Fatalf("CSV %v", err)
					}
				}
			})
		}
	}
}
func TestStopResolvesProjectWithoutConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := decodeGraphQL(t, r)
		if op.OperationName == "CliExperimentDetails" {
			writeGraphQL(w, "experiments", []any{detailsFixture("managed_optimization")})
			return
		}
		if op.OperationName != "CliStop" {
			t.Errorf("operation %s", op.OperationName)
		}
		var input map[string]any
		json.Unmarshal(op.Variables["input"], &input)
		if input["projectId"] != "proj_a" {
			t.Error("project not resolved")
		}
		writeGraphQL(w, "stopExperiment", map[string]any{"id": "exp_a", "desiredStatus": "stopped", "success": true})
	}))
	defer server.Close()
	app, _, _ := testApp(server.URL)
	app.ConfigPath = t.TempDir() + "/missing.toml"
	cmd := app.Command()
	cmd.SetArgs([]string{"stop", "exp_a"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

type cancelAfterWrite struct {
	cancel context.CancelFunc
	writes int
}

func (w *cancelAfterWrite) Write(data []byte) (int, error) {
	w.writes++
	w.cancel()
	return len(data), nil
}
func TestLogsFollowCancelsPromptlyAndPreservesDuplicates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := decodeGraphQL(t, r)
		if op.OperationName == "CliExperimentDetails" {
			writeGraphQL(w, "experiments", []any{detailsFixture("managed_optimization")})
			return
		}
		var input api.LogsInput
		json.Unmarshal(op.Variables["input"], &input)
		entry := map[string]any{"timestamp": input.End.Add(-time.Second).Format(time.RFC3339Nano), "message": "same", "experimentId": "exp_a"}
		writeGraphQL(w, "logs", map[string]any{"windowEnd": input.End.Format(time.RFC3339Nano), "totalHits": 2, "hits": []any{entry, entry}})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app, _, _ := testApp(server.URL)
	writer := &cancelAfterWrite{cancel: cancel}
	app.Out = writer
	cmd := app.Command()
	cmd.SetArgs([]string{"logs", "exp_a", "--follow", "--output", "jsonl"})
	started := time.Now()
	err := cmd.ExecuteContext(ctx)
	if err != context.Canceled || writer.writes != 2 || time.Since(started) > time.Second {
		t.Fatalf("err=%v writes=%d duration=%s", err, writer.writes, time.Since(started))
	}
}
func TestNewCommandsDoNotEmitPartialDataOnDeniedReads(t *testing.T) {
	for _, args := range [][]string{{"experiment", "show", "exp_a"}, {"experiment", "resume", "exp_a"}, {"run", "show", "run_a"}, {"logs", "exp_a"}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":{"experiments":[{"id":"secret"}]},"errors":[{"message":"Access denied","extensions":{"code":"FORBIDDEN"}}]}`)
		}))
		app, out, _ := testApp(server.URL)
		cmd := app.Command()
		cmd.SetArgs(append(args, "--output", "jsonl"))
		err := cmd.Execute()
		server.Close()
		exit, ok := err.(*ExitError)
		if !ok || exit.Code != 4 || out.Len() != 0 {
			t.Fatalf("args=%v err=%v output=%s", args, err, out.String())
		}
	}
}
