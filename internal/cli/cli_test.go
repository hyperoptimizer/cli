package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hyperoptimizer/cli/internal/api"
	"github.com/hyperoptimizer/cli/internal/config"
)

const projectID = "01HV2T00010000000000000001"

func TestWatchOutcomeUsesObservedTerminalStatus(t *testing.T) {
	for _, tc := range []struct {
		status, desired string
		wantError       bool
	}{
		{"completed", "stopped", false},
		{"completed", "running", false},
		{"failed", "stopped", true},
		{"cancelled", "stopped", true},
		{"cancelled", "running", true},
	} {
		t.Run(tc.status+"/"+tc.desired, func(t *testing.T) {
			err := outcome(&api.Experiment{Status: tc.status, DesiredStatus: tc.desired})
			if (err != nil) != tc.wantError {
				t.Fatalf("outcome error = %v, want error %v", err, tc.wantError)
			}
		})
	}
}

func testApp(endpoint string) (*App, *bytes.Buffer, *bytes.Buffer) {
	out, err := new(bytes.Buffer), new(bytes.Buffer)
	app := New(strings.NewReader(""), out, err)
	app.Endpoint = endpoint
	app.Store = &memoryCredentialStore{values: map[string]string{}}
	app.Getenv = func(name string) (string, bool) {
		if name == "HPO_API_KEY" {
			return "hpo_synthetic_test", true
		}
		return "", false
	}
	return app, out, err
}
func TestAuthFailureAndCredentialPrecedence(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer hpo_synthetic_test" {
					t.Errorf("environment credential not used")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				io.WriteString(w, `{"error":"Missing required scope: experiments:read"}`)
			}))
			defer server.Close()
			app, _, _ := testApp(server.URL)
			cmd := app.Command()
			cmd.SetArgs([]string{"auth", "status"})
			err := cmd.ExecuteContext(context.Background())
			typed, ok := err.(*ExitError)
			if !ok {
				t.Fatalf("wrong error: %v", err)
			}
			want := 5
			if status == 401 {
				want = 3
			}
			if status == 403 {
				want = 4
			}
			if typed.Code != want {
				t.Fatalf("got exit %d", typed.Code)
			}
			if status == 401 && !strings.Contains(err.Error(), "invalid, expired, or revoked") {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "hpo_synthetic_test") {
				t.Fatal("secret leaked")
			}
		})
	}
}
func TestOptimizeRecoversAmbiguousLaunchAndKeepsJSONLValid(t *testing.T) {
	attempts := 0
	keys := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		operation := decodeGraphQL(t, r)
		switch operation.OperationName {
		case "CliValidateConfiguration":
			var body struct {
				Toml string `json:"toml"`
			}
			if err := json.Unmarshal(operation.Variables["input"], &body); err != nil {
				t.Error(err)
			}
			document, err := config.Parse([]byte(body.Toml))
			if err != nil {
				t.Error(err)
			}
			writeGraphQL(w, "validateCliConfiguration", map[string]any{"configuration": document, "warnings": []string{}})
		case "CliLaunch":
			attempts++
			var input struct {
				RequestKey string `json:"requestKey"`
			}
			if err := json.Unmarshal(operation.Variables["input"], &input); err != nil {
				t.Error(err)
			}
			if input.RequestKey == "" {
				t.Error("idempotency input missing")
			}
			keys[input.RequestKey] = true
			if attempts == 1 {
				w.WriteHeader(503)
				io.WriteString(w, `{"errors":[{"message":"interrupted after commit","extensions":{"code":"SERVICE_UNAVAILABLE"}}]}`)
				return
			}
			writeGraphQL(w, "createManagedExperiment", map[string]any{"id": projectID, "projectId": projectID, "name": "test-run", "dashboardUrl": "https://app.hyperoptimizer.com/server-provided-experiment-link"})
		default:
			t.Errorf("unexpected GraphQL operation: %s", operation.OperationName)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	app, out, _ := testApp(server.URL)
	document, err := config.Parse(config.Template())
	if err != nil {
		t.Fatal(err)
	}
	delete(document, "build")
	document["dockerImage"] = map[string]any{"registry": "registry.example", "image": "org/train", "tag": "v1"}
	source, err := config.Encode(document)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	app.ConfigPath = filepath.Join(dir, "hpo.toml")
	if err = os.WriteFile(app.ConfigPath, source, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := app.Command()
	cmd.SetArgs([]string{"optimize", "--project", projectID, "--name", "test-run", "--detach", "--output", "jsonl"})
	if err = cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || len(keys) != 1 {
		t.Fatalf("launch did not deduplicate: attempts=%d keys=%d", attempts, len(keys))
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var event map[string]any
		if err = json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid JSONL: %s", line)
		}
		if event["event"] == "experiment_created" && event["url"] != "https://app.hyperoptimizer.com/server-provided-experiment-link" {
			t.Fatal("did not display the server-provided dashboard link")
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, ".hpo", "launch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hpo_synthetic_test") {
		t.Fatal("credential in launch state")
	}
}

func TestFormatterCommandNeedsNoCredentialAndCheckDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.toml")
	original := []byte("algorithm='random'\nregion=\"eu-west\"")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	app, _, _ := testApp("http://localhost:1")
	app.Getenv = func(name string) (string, bool) {
		if name == "HPO_API_KEY" {
			t.Fatal("formatter requested credentials")
		}
		return "", false
	}
	cmd := app.Command()
	cmd.SetArgs([]string{"fmt", "--check", "--config", path})
	err := cmd.Execute()
	if failure, ok := err.(*ExitError); !ok || failure.Code != 2 {
		t.Fatalf("wrong check error: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !bytes.Equal(data, original) {
		t.Fatal("check changed file")
	}
	cmd = app.Command()
	cmd.SetArgs([]string{"fmt", "--config", path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cmd = app.Command()
	cmd.SetArgs([]string{"fmt", "--check", "--config", path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestMultipleMetricsRetainedInEveryOutput(t *testing.T) {
	metrics := map[string]float64{"accuracy": 0.9, "latency": 25, "very-long-objective-name": 3}
	trial := api.Run{Id: projectID, Status: "succeeded", Number: 1, Metrics: metrics}
	plain := metricsText(trial)
	for name := range metrics {
		if !strings.Contains(plain, name+": ") {
			t.Fatalf("plain output lost %s", name)
		}
	}
	app, out, _ := testApp("http://localhost:1")
	app.Output = "jsonl"
	if err := app.event("trial_updated", map[string]any{"metrics": metrics}, plain); err != nil {
		t.Fatal(err)
	}
	var event struct{ Metrics map[string]float64 }
	if err := json.Unmarshal(out.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if len(event.Metrics) != len(metrics) {
		t.Fatal("JSONL lost objectives")
	}
	model := watchModel{width: 37, last: snapshot{Runs: []api.Run{trial}}}
	model.refreshTable()
	var wrapped strings.Builder
	for _, row := range model.table.Rows() {
		wrapped.WriteString(row[2])
	}
	if wrapped.String() != plain {
		t.Fatalf("table truncated metrics: %q", wrapped.String())
	}
}
func TestLateMetricsAreEmittedWithoutRepeatingUnchangedRuns(t *testing.T) {
	seen := map[string]string{}
	trial := api.Run{Id: projectID, Status: "succeeded", Metrics: map[string]float64{}}
	if len(changedRuns([]api.Run{trial}, seen)) != 1 {
		t.Fatal("missing initial event")
	}
	if len(changedRuns([]api.Run{trial}, seen)) != 0 {
		t.Fatal("duplicate event")
	}
	trial.Metrics["val_loss"] = 0.50000000001
	if len(changedRuns([]api.Run{trial}, seen)) != 1 {
		t.Fatal("late metric not emitted")
	}
}
func TestInitDoesNotOverwrite(t *testing.T) {
	app, _, _ := testApp("http://localhost:1")
	app.ConfigPath = filepath.Join(t.TempDir(), "hpo.toml")
	app.Project = projectID
	os.WriteFile(app.ConfigPath, []byte("keep me"), 0600)
	if err := app.init(context.Background()); err == nil {
		t.Fatal("overwrote config")
	}
	data, _ := os.ReadFile(app.ConfigPath)
	if string(data) != "keep me" {
		t.Fatal("file changed")
	}
}
func TestEmptyEnvironmentKeyDoesNotFallBack(t *testing.T) {
	app, _, _ := testApp("http://localhost:1")
	app.Getenv = func(name string) (string, bool) { return "", name == "HPO_API_KEY" }
	_, _, err := app.credential()
	if err == nil {
		t.Fatal("empty key accepted")
	}
}

func TestBuildPreservesDockerContextAndPluginDiscovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture; Windows binary is cross-compiled separately")
	}
	dir := t.TempDir()
	docker := filepath.Join(dir, "docker")
	script := `#!/bin/sh
set -eu
if [ "$1" = buildx ]; then exit 0; fi
if [ "$1" = context ] && [ "$2" = show ]; then echo desktop-test; exit 0; fi
if [ "$1" = context ] && [ "$2" = export ]; then echo exported-context; exit 0; fi
[ "$1" = --config ]
config=$2
shift 2
if [ "$1" = context ]; then cat >/dev/null; exit 0; fi
[ "$1" = --context ] && [ "$2" = hpo-build ]
shift 2
if [ "$1" = login ]; then cat >/dev/null; exit 0; fi
while [ "$#" -gt 0 ]; do
 if [ "$1" = --metadata-file ]; then shift; printf '{"containerimage.digest":"%s"}' "${HPO_TEST_DIGEST:-sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}" > "$1"; fi
 shift
done
`
	if err := os.WriteFile(docker, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_CONFIG", dir)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"cliPluginsExtraDirs":["/plugins"],"auths":{"unrelated":{"auth":"must-not-copy"}}}`), 0600)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeGraphQL(w, "createCliRegistryCredentials", map[string]any{"registryHost": "registry.example", "projectName": "team", "username": "robot-test", "password": "synthetic-registry-secret", "expiresAt": "2026-10-08T00:00:00Z"})
	}))
	defer server.Close()
	app, _, _ := testApp(server.URL)
	app.ConfigPath = filepath.Join(dir, "hpo.toml")
	lookup := app.Getenv
	app.Getenv = func(name string) (string, bool) {
		if name == "DOCKER_CONFIG" {
			return dir, true
		}
		return lookup(name)
	}
	client, ctx, source, err := app.authorized(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	document, err := config.Parse(config.Template())
	if err != nil {
		t.Fatal(err)
	}
	if err = app.build(ctx, client, document, source, "request-key"); err != nil {
		t.Fatal(err)
	}
	image := document["dockerImage"].(map[string]any)
	if image["digest"] != "sha256:"+strings.Repeat("a", 64) {
		t.Fatal("digest not pinned")
	}

	for _, invalid := range []string{"sha256:short", "sha256:" + strings.Repeat("z", 64), "sha512:" + strings.Repeat("a", 128)} {
		t.Setenv("HPO_TEST_DIGEST", invalid)
		document, err := config.Parse(config.Template())
		if err != nil {
			t.Fatal(err)
		}
		if err := app.build(ctx, client, document, source, "request-key"); err == nil {
			t.Fatalf("accepted malformed/unsupported digest: %s", invalid)
		}
	}
	isolated := t.TempDir()
	prefix, err := app.dockerContext(context.Background(), docker, isolated)
	if err != nil {
		t.Fatal(err)
	}
	if prefix[len(prefix)-1] != "hpo-build" {
		t.Fatal("context not preserved")
	}
	data, _ := os.ReadFile(filepath.Join(isolated, "config.json"))
	if strings.Contains(string(data), "must-not-copy") {
		t.Fatal("unrelated auth copied")
	}
	if !strings.Contains(string(data), "/plugins") {
		t.Fatal("plugin lookup lost")
	}
}

type graphQLRequest struct {
	OperationName string                     `json:"operationName"`
	Query         string                     `json:"query"`
	Variables     map[string]json.RawMessage `json:"variables"`
}

func decodeGraphQL(t *testing.T, r *http.Request) graphQLRequest {
	t.Helper()
	if r.URL.Path != "/graphql" || r.Method != http.MethodPost {
		t.Error("CLI must use POST /graphql")
	}
	var operation graphQLRequest
	if err := json.NewDecoder(r.Body).Decode(&operation); err != nil {
		t.Fatal(err)
	}
	if operation.OperationName == "" || operation.Query == "" {
		t.Error("Missing generated GraphQL operation")
	}
	return operation
}
func writeGraphQL(w http.ResponseWriter, field string, value any) {
	w.Header().Set("Content-Type", "application/graphql-response+json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{field: value}})
}

func TestFinalizationAndOptimizerAcknowledgementEmitChanges(t *testing.T) {
	run := api.Run{Id: "canonical-run", Status: "succeeded", FinalizationState: "pending", Metrics: map[string]float64{"score": 0.5}}
	seen := map[string]string{}
	if len(changedRuns([]api.Run{run}, seen)) != 1 {
		t.Fatal("initial run not emitted")
	}
	run.FinalizationState = "complete"
	if len(changedRuns([]api.Run{run}, seen)) != 1 {
		t.Fatal("finalization change was hidden")
	}
	now := time.Now()
	run.ResultsAcceptedAt = &now
	if len(changedRuns([]api.Run{run}, seen)) != 1 {
		t.Fatal("optimizer acknowledgement was hidden")
	}
}
