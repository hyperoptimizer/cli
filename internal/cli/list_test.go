package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListCommandsPaginateWithDefaultAndOverride(t *testing.T) {
	for _, kind := range []string{"project", "experiment"} {
		for _, override := range []string{"", "org_b"} {
			t.Run(kind+"/"+override, func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					wantOrg := "org_a"
					if override != "" {
						wantOrg = override
					}
					if r.Header.Get("X-HPO-Organization") != wantOrg || r.Header.Get("Authorization") != "Bearer ho_list_test" {
						t.Error("incorrect credential or organization")
					}
					request := decodeGraphQL(t, r)
					wantOperation := "CliProjects"
					field := "projects"
					if kind == "experiment" {
						wantOperation = "CliExperiments"
						field = "experiments"
					}
					if request.OperationName != wantOperation {
						t.Errorf("operation=%s", request.OperationName)
					}
					var after string
					json.Unmarshal(request.Variables["after"], &after)
					start := 0
					if after != "" {
						fmt.Sscanf(after, "id_%03d", &start)
						start++
					}
					var limit int
					json.Unmarshal(request.Variables["limit"], &limit)
					if limit != 101 {
						t.Errorf("limit=%d", limit)
					}
					rows := []map[string]any{}
					for i := start; i < 205 && i < start+limit; i++ {
						row := map[string]any{"id": fmt.Sprintf("id_%03d", i), "name": fmt.Sprintf("Result %d", i)}
						if kind == "experiment" {
							row["projectId"] = "project_a"
							row["kind"] = "managed_optimization"
							row["optimization"] = map[string]any{"status": "paused", "desiredStatus": "stopped"}
						}
						rows = append(rows, row)
					}
					writeGraphQL(w, field, rows)
				}))
				defer server.Close()
				app, out, _ := testApp(server.URL)
				setEnvironmentKey(app, "", false)
				if err := app.saveCredential("ho_list_test"); err != nil {
					t.Fatal(err)
				}
				if err := app.saveDefaultOrganization("ho_list_test", "org_a"); err != nil {
					t.Fatal(err)
				}
				args := []string{kind, "list", "--output", "jsonl"}
				if override != "" {
					args = append(args, "--org", override)
				}
				cmd := app.Command()
				cmd.SetArgs(args)
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
				var result map[string]json.RawMessage
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				var rows []map[string]any
				json.Unmarshal(result[kind+"s"], &rows)
				if len(rows) != 205 || calls != 3 {
					t.Fatalf("rows=%d calls=%d", len(rows), calls)
				}
				if rows[100]["id"] != "id_100" || rows[204]["id"] != "id_204" {
					t.Fatal("pagination lost rows")
				}
				if got, err := app.defaultOrganization("ho_list_test"); got != "org_a" || err != nil {
					t.Fatalf("default changed: %s %v", got, err)
				}
			})
		}
	}
}

func TestExperimentProjectFilterAndTrackingDisplay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodeGraphQL(t, r)
		if request.OperationName != "CliProjectExperiments" || string(request.Variables["projectId"]) != `"project_selected"` {
			t.Errorf("wrong project filter: %+v", request)
		}
		writeGraphQL(w, "experiments", []map[string]any{{"id": "id_1", "name": "Tracked experiment", "projectId": "project_selected", "kind": "external_tracking", "optimization": nil}})
	}))
	defer server.Close()
	app, out, _ := testApp(server.URL)
	cmd := app.Command()
	cmd.SetArgs([]string{"experiments", "list", "--org", "org_a", "--project", "project_selected"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Tracked experiment") || !strings.Contains(out.String(), "external_tracking") {
		t.Fatal(out.String())
	}
}

func TestListEmptyAndAuthorizationFailures(t *testing.T) {
	for _, kind := range []string{"project", "experiment"} {
		for _, denied := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", kind, denied), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if denied {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"errors":[{"message":"Organization unavailable","extensions":{"code":"FORBIDDEN"}}]}`)
						return
					}
					writeGraphQL(w, kind+"s", []any{})
				}))
				defer server.Close()
				app, out, _ := testApp(server.URL)
				cmd := app.Command()
				cmd.SetArgs([]string{kind, "list", "--org", "org_a", "--output", "jsonl"})
				err := cmd.Execute()
				if denied {
					exit, ok := err.(*ExitError)
					if !ok || exit.Code != 4 || out.Len() != 0 {
						t.Fatalf("err=%v output=%s", err, out)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), `"`+kind+`s":[]`) {
					t.Fatal(out.String())
				}
			})
		}
	}
}

func TestPaginationRejectsNonAdvancingCursor(t *testing.T) {
	calls := 0
	_, err := collectPages(func(after string, limit int) ([]string, error) {
		calls++
		rows := make([]string, 101)
		for i := range rows {
			rows[i] = "same"
		}
		return rows, nil
	}, func(id string) string { return id })
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
