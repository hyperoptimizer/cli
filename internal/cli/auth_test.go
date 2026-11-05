package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hyperoptimizer/cli/internal/api"
	"github.com/zalando/go-keyring"
)

type memoryCredentialStore struct {
	values                    map[string]string
	getErr, setErr, deleteErr error
	gets                      int
	service, account          string
}

func (s *memoryCredentialStore) Get(service, account string) (string, error) {
	s.gets++
	s.service, s.account = service, account
	if s.getErr != nil {
		return "", s.getErr
	}
	value, ok := s.values[account]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}
func (s *memoryCredentialStore) Set(service, account, key string) error {
	s.service, s.account = service, account
	if s.setErr != nil {
		return s.setErr
	}
	s.values[account] = key
	return nil
}
func (s *memoryCredentialStore) Delete(service, account string) error {
	s.service, s.account = service, account
	if s.deleteErr != nil {
		return s.deleteErr
	}
	if _, ok := s.values[account]; !ok {
		return keyring.ErrNotFound
	}
	delete(s.values, account)
	return nil
}
func setEnvironmentKey(app *App, value string, present bool) {
	app.Getenv = func(name string) (string, bool) {
		if name == "HPO_API_KEY" {
			return value, present
		}
		return "", false
	}
}
func decodeAuthEvent(t *testing.T, output string) map[string]any {
	t.Helper()
	var event map[string]any
	if err := json.Unmarshal([]byte(output), &event); err != nil {
		t.Fatalf("invalid JSONL %q: %v", output, err)
	}
	return event
}
func assertOverrideWarning(t *testing.T, output string, present, empty bool, login bool) {
	t.Helper()
	count := strings.Count(output, "Warning:")
	want := 0
	if present {
		want = 1
	}
	if count != want {
		t.Fatalf("warning count %d: %s", count, output)
	}
	if !present {
		return
	}
	if empty {
		if !strings.Contains(output, "empty") || !strings.Contains(output, "will fail") {
			t.Fatal(output)
		}
	} else if !strings.Contains(output, "continue using it") || !strings.Contains(output, "Unset HPO_API_KEY") {
		t.Fatal(output)
	}
	if login && !strings.Contains(output, "just saved") {
		t.Fatal(output)
	}
}
func TestLoginOverrideWarningsBothMethods(t *testing.T) {
	for _, stdin := range []bool{false, true} {
		for _, env := range []struct {
			name, value string
			present     bool
		}{{"unset", "", false}, {"set", "hpo_env_secret", true}, {"empty", "", true}, {"whitespace", " \t ", true}} {
			t.Run(env.name+map[bool]string{true: "-stdin", false: "-device"}[stdin], func(t *testing.T) {
				var server *httptest.Server
				if stdin {
					server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Header.Get("Authorization") != "Bearer hpo_new_secret" {
							t.Error("login used environment credential")
						}
						w.Header().Set("Content-Type", "application/json")
						writeGraphQL(w, "cliIdentity", map[string]any{"keyId": "key", "organizationId": "org", "organizationName": "Org", "scopes": []string{}})
					}))
				} else {
					server = loginServer(t, 1, func(w http.ResponseWriter, r *http.Request) {
						io.WriteString(w, `{"status":"approved","apiKey":"hpo_new_secret"}`)
					})
				}
				defer server.Close()
				app, out, diagnostic := testApp(server.URL)
				app.Output = "jsonl"
				app.In = strings.NewReader("hpo_new_secret\n")
				setEnvironmentKey(app, env.value, env.present)
				if err := app.login(context.Background(), stdin, true); err != nil {
					t.Fatal(err)
				}
				if decodeAuthEvent(t, out.String())["event"] != "authenticated" {
					t.Fatal(out)
				}
				store := app.Store.(*memoryCredentialStore)
				if store.values[app.Endpoint] != "hpo_new_secret" || store.service != "hyperoptimizer" {
					t.Fatal("credential not saved under existing identifiers")
				}
				assertOverrideWarning(t, diagnostic.String(), env.present, strings.TrimSpace(env.value) == "", true)
				if strings.Contains(out.String()+diagnostic.String(), "hpo_new_secret") || strings.Contains(out.String()+diagnostic.String(), "hpo_env_secret") {
					t.Fatal("key leaked")
				}
			})
		}
	}
}
func TestLogoutSelectedStoreAndOverrideWarnings(t *testing.T) {
	for _, file := range []bool{false, true} {
		if file && runtime.GOOS == "windows" {
			continue
		}
		for _, exists := range []bool{false, true} {
			for _, env := range []struct {
				name, value string
				present     bool
			}{{"unset", "", false}, {"set", "hpo_env_secret", true}, {"empty", "", true}, {"whitespace", " \t ", true}} {
				t.Run(env.name+map[bool]string{true: "-file", false: "-keyring"}[file]+map[bool]string{true: "-existing", false: "-missing"}[exists], func(t *testing.T) {
					app, out, diagnostic := testApp("https://graphql.hyperoptimizer.com/")
					app.Output = "jsonl"
					setEnvironmentKey(app, env.value, env.present)
					store := app.Store.(*memoryCredentialStore)
					store.values["other-endpoint"] = "untouched"
					if file {
						app.CredentialFile = filepath.Join(t.TempDir(), "key")
						store.values[app.Endpoint] = "untouched"
						if exists {
							if err := os.WriteFile(app.CredentialFile, []byte("hpo_saved_secret"), 0600); err != nil {
								t.Fatal(err)
							}
						}
					} else if exists {
						store.values[app.Endpoint] = "hpo_saved_secret"
					}
					if err := app.logout(); err != nil {
						t.Fatal(err)
					}
					event := decodeAuthEvent(t, out.String())
					if event["event"] != "logged_out" || event["localCredentialRemoved"] != exists {
						t.Fatal(event)
					}
					if store.values["other-endpoint"] != "untouched" || file && store.values[app.Endpoint] != "untouched" {
						t.Fatal("wrong backend cleared")
					}
					assertOverrideWarning(t, diagnostic.String(), env.present, strings.TrimSpace(env.value) == "", false)
					if strings.Contains(out.String()+diagnostic.String(), "hpo_") {
						t.Fatal("key leaked")
					}
				})
			}
		}
	}
}
func TestAuthStatusSourcesAndNoFallback(t *testing.T) {
	for _, source := range []CredentialSource{credentialEnvironment, credentialFile, credentialKeyring} {
		if source == credentialFile && runtime.GOOS == "windows" {
			continue
		}
		t.Run(string(source), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("Authorization") != "Bearer hpo_selected" {
					t.Error("wrong credential")
				}
				w.Header().Set("Content-Type", "application/json")
				writeGraphQL(w, "cliIdentity", map[string]any{"keyId": "key", "organizationId": "org", "organizationName": "Org", "scopes": []string{"projects:read"}})
			}))
			defer server.Close()
			app, out, diagnostic := testApp(server.URL)
			setEnvironmentKey(app, "hpo_selected", source == credentialEnvironment)
			store := app.Store.(*memoryCredentialStore)
			store.values[app.Endpoint] = "hpo_selected"
			args := []string{"auth", "status", "--output", "jsonl"}
			if source == credentialFile {
				path := filepath.Join(t.TempDir(), "key")
				if err := os.WriteFile(path, []byte("hpo_selected"), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--credential-file", path)
			}
			cmd := app.Command()
			cmd.SetArgs(args)
			if err := cmd.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			event := decodeAuthEvent(t, out.String())
			if event["credentialSource"] != string(source) || event["apiUrl"] != server.URL || event["organization"] != "org" || requests != 1 {
				t.Fatal(event)
			}
			if diagnostic.Len() != 0 {
				t.Fatal("routine status emitted warning")
			}
			if source != credentialKeyring && store.gets != 0 {
				t.Fatal("fallback accessed")
			}
			if source == credentialKeyring && (store.account != server.URL+"#default-organization" || store.service != "hyperoptimizer-org") {
				t.Fatal("keyring identifier changed")
			}
		})
	}
	for _, value := range []string{"", " \t ", "hpo_rejected"} {
		t.Run("no-fallback-"+value, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(401) }))
			defer server.Close()
			app, out, _ := testApp(server.URL)
			setEnvironmentKey(app, value, true)
			store := app.Store.(*memoryCredentialStore)
			store.values[app.Endpoint] = "hpo_saved"
			cmd := app.Command()
			cmd.SetArgs([]string{"auth", "status"})
			err := cmd.ExecuteContext(context.Background())
			var failure *ExitError
			if !errors.As(err, &failure) || failure.Code != 3 || store.gets != 0 || out.Len() != 0 {
				t.Fatalf("fallback or wrong error: %v", err)
			}
			wantRequests := 0
			if value != "" && strings.TrimSpace(value) != "" {
				wantRequests = 1
			}
			if requests != wantRequests {
				t.Fatal(requests)
			}
		})
	}
}
func TestCredentialStorageFailuresAnd401Guidance(t *testing.T) {
	app, out, diagnostic := testApp("https://graphql.hyperoptimizer.com/")
	setEnvironmentKey(app, "", false)
	store := app.Store.(*memoryCredentialStore)
	if _, source, err := app.credential(); source != credentialKeyring || err == nil {
		t.Fatal("missing credential accepted")
	}
	store.setErr = errors.New("hpo_secret")
	if err := app.saveCredential("hpo_secret"); err == nil || strings.Contains(err.Error(), "hpo_secret") {
		t.Fatal(err)
	}
	store.deleteErr = errors.New("hpo_secret")
	if err := app.logout(); err == nil || strings.Contains(err.Error(), "hpo_secret") || out.Len() != 0 || diagnostic.Len() != 0 {
		t.Fatal(err)
	}
	for source, phrase := range map[CredentialSource]string{credentialEnvironment: "Replace or unset HPO_API_KEY", credentialFile: "hpo login --credential-file <path>", credentialKeyring: "Run hpo login."} {
		err := apiError(&api.Error{Status: 401}, source)
		if !strings.Contains(err.Error(), phrase) || strings.Contains(err.Error(), "hpo_secret") {
			t.Fatal(err)
		}
	}
	if runtime.GOOS != "windows" {
		app.CredentialFile = filepath.Join(t.TempDir(), "missing")
		if _, source, err := app.credential(); err == nil || source != credentialFile {
			t.Fatal("missing file accepted")
		}
		app.CredentialFile = t.TempDir()
		if err := os.WriteFile(filepath.Join(app.CredentialFile, "keep"), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := app.saveCredential("secret"); err == nil {
			t.Fatal("directory replaced")
		}
		if err := app.logout(); err == nil || out.Len() != 0 {
			t.Fatal("failed removal reported success")
		}
	}
}
func TestEndpointResolutionAfterFlags(t *testing.T) {
	for _, test := range []struct{ name, env, flag, want string }{
		{"default", "", "", "https://graphql.hyperoptimizer.com"}, {"environment", "https://env.example/", "", "https://env.example/"}, {"flag", "https://env.example", "https://flag.example/", "https://flag.example/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := New(strings.NewReader(""), io.Discard, io.Discard)
			store := &memoryCredentialStore{values: map[string]string{}}
			app.Store = store
			cmd := app.Command()
			app.Getenv = func(name string) (string, bool) { return test.env, name == "HPO_API_URL" }
			args := []string{"logout"}
			if test.flag != "" {
				args = append(args, "--api-url", test.flag)
			}
			cmd.SetArgs(args)
			if err := cmd.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if app.Endpoint != test.want || store.account != test.want || store.service != "hyperoptimizer" {
				t.Fatalf("wrong endpoint: %s %s", app.Endpoint, store.account)
			}
		})
	}
}

func TestAuthStatusIncludesCredentialOwnershipAndExpiry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"cliIdentity":{"keyId":"key","organizationId":"org","organizationName":"Team","scopes":["projects:read"],"kind":"personal_cli","ownerUserId":"user-a","expiresAt":"2026-11-24T12:00:00Z"}}}`)
	}))
	defer server.Close()
	for _, output := range []string{"jsonl", "plain"} {
		app, out, _ := testApp(server.URL)
		cmd := app.Command()
		cmd.SetArgs([]string{"auth", "status", "--output", output})
		if err := cmd.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		if output == "jsonl" {
			event := decodeAuthEvent(t, out.String())
			if event["credentialKind"] != "personal_cli" || event["ownerUserId"] != "user-a" || event["expiresAt"] != "2026-11-24T12:00:00Z" {
				t.Fatal(event)
			}
		} else if !strings.Contains(out.String(), "personal_cli") || !strings.Contains(out.String(), "user-a") || !strings.Contains(out.String(), "2026-11-24T12:00:00Z") {
			t.Fatal(out.String())
		}
		if strings.Contains(out.String(), "hpo_synthetic_test") {
			t.Fatal("status leaked credential")
		}
	}
}
