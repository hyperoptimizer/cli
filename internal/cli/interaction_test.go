package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/hyperoptimizer/cli/internal/api"
)

func loginServer(t *testing.T, expiry float64, poll func(http.ResponseWriter, *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		operation := decodeGraphQL(t, r)
		switch operation.OperationName {
		case "CliStartLogin":
			writeGraphQL(w, "startCliLogin", map[string]any{"deviceCode": "private-device-secret", "expiresIn": max(2, int(expiry)), "interval": 1, "userCode": "H-321-123-132", "verificationUri": "https://app.hyperoptimizer.com/connect"})
		case "CliIdentity":
			writeGraphQL(w, "cliIdentity", map[string]any{"keyId": "test", "kind": "personal_cli", "organizations": []any{}, "scopes": []string{}})
		case "CliPollLogin":
			response := httptest.NewRecorder()
			poll(response, r)
			if response.Code >= 400 {
				w.WriteHeader(response.Code)
				w.Write(response.Body.Bytes())
				return
			}
			writeGraphQL(w, "pollCliLogin", json.RawMessage(response.Body.Bytes()))
		default:
			t.Errorf("Unexpected GraphQL operation: %s", operation.OperationName)
			w.WriteHeader(400)
		}
	}))
}

func TestStandardAPIValidationError(t *testing.T) {
	for name, payload := range map[string]string{
		"standard":  `{"errors":[{"message":"Invalid request","extensions":{"code":"BAD_USER_INPUT"}}]}`,
		"variables": `{"errors":[{"message":"Invalid variable value","extensions":{"code":"BAD_USER_INPUT"}}]}`,
		"extended":  `{"errors":[{"message":"Invalid request","extensions":{"code":"BAD_USER_INPUT","futureMetadata":true}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "request-123")
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, payload)
			}))
			defer server.Close()
			client, err := newClient(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Projects(context.Background(), "", 100)
			var exit *ExitError
			if !errors.As(apiError(err, credentialEnvironment), &exit) {
				t.Fatal("expected a CLI error")
			}
			if exit.Code != 2 || exit.Message != "Invalid request or configuration. Run hpo validate and check the deployed schema." {
				t.Fatalf("unexpected CLI error: %#v", exit)
			}
		})
	}
}
func loginApp(t *testing.T, endpoint string) *App {
	t.Helper()
	app, _, _ := testApp(endpoint)
	app.Interactive = func() bool { return true }
	app.Accessible = true
	app.CredentialFile = filepath.Join(t.TempDir(), "key")
	return app
}
func TestBrowserConsentAndManualFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix explicit credential storage")
	}
	for _, input := range []string{"y\n", "n\n", "\n", "invalid\ny\n"} {
		t.Run(fmt.Sprintf("answer-%q", input), func(t *testing.T) {
			server := loginServer(t, 1, func(w http.ResponseWriter, _ *http.Request) {
				io.WriteString(w, `{"status":"approved","apiKey":"hpo_test_login"}`)
			})
			defer server.Close()
			app := loginApp(t, server.URL)
			app.In = strings.NewReader(input)
			app.Output = "jsonl"
			opens := 0
			app.OpenBrowser = func(url string) error {
				opens++
				if url != "https://app.hyperoptimizer.com/connect" {
					t.Fatal("not server URL")
				}
				return errors.New("browser unavailable")
			}
			if err := app.login(context.Background(), false, false); err != nil {
				t.Fatal(err)
			}
			wantsOpen := strings.Contains(input, "y\n")
			if (opens == 1) != wantsOpen {
				t.Fatalf("opens=%d", opens)
			}
			out := app.Out.(interface{ String() string }).String()
			var event map[string]any
			if err := json.Unmarshal([]byte(out), &event); err != nil || event["event"] != "authenticated" {
				t.Fatalf("stdout not JSONL: %s", out)
			}
			diagnostic := app.Err.(interface{ String() string }).String()
			if !strings.Contains(diagnostic, "Open in browser?") || !strings.Contains(diagnostic, "[y/N]") {
				t.Fatal("missing default-No prompt")
			}
			if !strings.Contains(diagnostic, "Enter this code in your browser: H-321-123-132") {
				t.Fatal("missing manual-entry code")
			}
			if strings.Contains(out+diagnostic, "private-device-secret") || strings.Contains(out+diagnostic, "hpo_test_login") {
				t.Fatal("secret printed")
			}
		})
	}
}
func TestLoginNoBrowserNoninteractiveAndEOF(t *testing.T) {
	for _, test := range []struct {
		name                string
		manual, interactive bool
		input               string
		wantCode            int
	}{
		{"manual", true, false, "", 3}, {"noninteractive", false, false, "y\n", 2}, {"eof", false, true, "", 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := loginServer(t, 1, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"status":"denied"}`) })
			defer server.Close()
			app := loginApp(t, server.URL)
			app.In = strings.NewReader(test.input)
			app.Interactive = func() bool { return test.interactive }
			app.OpenBrowser = func(string) error { t.Fatal("opened browser"); return nil }
			err := app.login(context.Background(), false, test.manual)
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != test.wantCode {
				t.Fatalf("wrong error: %v", err)
			}
		})
	}
}
func TestLoginExpiryAndCancellationBeforeConsent(t *testing.T) {
	for _, expiry := range []bool{true, false} {
		server := loginServer(t, 0.01, func(http.ResponseWriter, *http.Request) { t.Error("polled after canceled prompt") })
		app := loginApp(t, server.URL)
		app.OpenBrowser = func(string) error { t.Error("opened browser after cancellation"); return nil }
		ctx, cancel := context.WithCancel(context.Background())
		app.ConfirmBrowser = func(promptCtx context.Context) (bool, error) {
			if !expiry {
				cancel()
			}
			<-promptCtx.Done()
			return false, promptCtx.Err()
		}
		err := app.login(ctx, false, false)
		cancel()
		server.Close()
		if expiry {
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != 3 {
				t.Fatalf("wrong expiry: %v", err)
			}
		} else if !errors.Is(err, context.Canceled) {
			t.Fatalf("wrong cancellation: %v", err)
		}
	}
}
func TestAccessiblePromptCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix pipe cancel-reader; Windows console exercised by interactive driver")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	app, _, _ := testApp("http://localhost:1")
	app.Accessible = true
	app.In = reader
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = app.confirmBrowser(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read ignored cancellation: %v", err)
	}
}
func TestAccessibleProjectSelectionAndEOF(t *testing.T) {
	projects := []api.Project{{Id: "first", Name: "First"}, {Id: "second", Name: "Second"}}
	app, _, _ := testApp("http://localhost:1")
	app.Accessible = true
	app.In = strings.NewReader("invalid\n2\n")
	selected, err := app.selectProject(context.Background(), projects)
	if err != nil || selected != "second" {
		t.Fatalf("wrong selection: %s %v", selected, err)
	}
	app.In = strings.NewReader("")
	if selected, err := app.selectProject(context.Background(), projects); err == nil || selected != "" {
		t.Fatal("EOF selected a project")
	}
}
func TestRetryClassificationLimitsAndCancellation(t *testing.T) {
	for _, status := range []int{200, 400, 401, 403, 429, 500} {
		app, _, _ := testApp("http://localhost:1")
		app.BackOff = func() backoff.BackOff { return &backoff.ZeroBackOff{} }
		calls := 0
		err := app.retry(context.Background(), 3, 0, func() error {
			calls++
			return apiError(&api.Error{Status: status}, "test")
		})
		want := 1
		if status == 429 || status >= 500 {
			want = 3
		}
		if err == nil || calls != want {
			t.Fatalf("status %d calls=%d error=%v", status, calls, err)
		}
	}
	app, _, _ := testApp("http://localhost:1")
	ctx, cancel := context.WithCancel(context.Background())
	err := app.retry(ctx, 3, 0, func() error { cancel(); return &ExitError{Code: 5, Retryable: true, Message: "temporary"} })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}
func TestRetryAfterAndProtocolMinimum(t *testing.T) {
	for _, value := range []string{"2", time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat), "invalid", "-1"} {
		response := &http.Response{Header: http.Header{"Retry-After": {value}}}
		delay := retryAfter(response)
		if (value == "2" && delay != 2*time.Second) || (value == "invalid" && delay != 0) || (value == "-1" && delay != 0) {
			t.Fatalf("bad Retry-After %s: %v", value, delay)
		}
	}
	strategy := minimumBackOff{BackOff: &backoff.ZeroBackOff{}, minimum: 5 * time.Second}
	if strategy.NextBackOff() != 5*time.Second {
		t.Fatal("poll interval not honored")
	}
	app, _, _ := testApp("http://localhost:1")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	calls := 0
	err := app.retry(ctx, 3, 0, func() error { calls++; return &ExitError{Code: 5, Retryable: true, RetryAfter: time.Second} })
	if calls != 1 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Retry-After not respected: %d %v", calls, err)
	}
}

func TestTransportAndServiceRetryClassification(t *testing.T) {
	transient := apiError(&url.Error{Op: "Get", URL: "https://graphql.hyperoptimizer.com", Err: io.EOF}, "test")
	var exit *ExitError
	if !errors.As(transient, &exit) || !exit.Retryable {
		t.Fatal("connection failure not retryable")
	}
	permanent := apiError(&url.Error{Op: "Get", URL: "https://graphql.hyperoptimizer.com", Err: &tls.CertificateVerificationError{Err: errors.New("certificate")}}, "test")
	if !errors.As(permanent, &exit) || exit.Retryable {
		t.Fatal("certificate failure retried")
	}
	service := apiError(&api.Error{Status: 503, RetryAfter: 2 * time.Second}, "test")
	if !errors.As(service, &exit) || !exit.Retryable || exit.RetryAfter != 2*time.Second {
		t.Fatal("503 Retry-After lost")
	}
}

func TestDeviceSlowDownRespectsIntervalAndExpiry(t *testing.T) {
	polls := 0
	server := loginServer(t, 0.04, func(w http.ResponseWriter, _ *http.Request) { polls++; io.WriteString(w, `{"status":"slow_down"}`) })
	defer server.Close()
	app := loginApp(t, server.URL)
	app.OpenBrowser = func(string) error { t.Fatal("manual flow opened browser"); return nil }
	err := app.login(context.Background(), false, true)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 3 || polls != 1 {
		t.Fatalf("slow_down ignored: %d %v", polls, err)
	}
}
func TestAPIKeyLoginDoesNotPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("explicit Unix credential file")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if decodeGraphQL(t, r).OperationName != "CliIdentity" || r.Header.Get("Authorization") != "Bearer hpo_stdin_test" {
			t.Error("wrong identity request")
		}
		w.Header().Set("Content-Type", "application/json")
		writeGraphQL(w, "cliIdentity", map[string]any{"keyId": "test", "organizationId": "test", "organizationName": "Test", "scopes": []string{}})
	}))
	defer server.Close()
	app := loginApp(t, server.URL)
	app.In = strings.NewReader("hpo_stdin_test\n")
	app.ConfirmBrowser = func(context.Context) (bool, error) { t.Fatal("key login prompted"); return false, nil }
	app.OpenBrowser = func(string) error { t.Fatal("key login opened browser"); return nil }
	if err := app.login(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
}
func TestTermDumbUsesTextConfirmation(t *testing.T) {
	app, _, errOut := testApp("http://localhost:1")
	app.Getenv = func(name string) (string, bool) { return "dumb", name == "TERM" }
	app.In = strings.NewReader("n\n")
	open, err := app.confirmBrowser(context.Background())
	if open || err != nil || !strings.Contains(errOut.String(), "[y/N]") {
		t.Fatalf("not accessible: %v %v", open, err)
	}
}

func TestWatchModesUseSixAttempts(t *testing.T) {
	for _, mode := range []string{"plain", "table-model"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(503)
				io.WriteString(w, `{"error":"unavailable"}`)
			}))
			defer server.Close()
			app, _, _ := testApp(server.URL)
			app.Output = "plain"
			app.BackOff = func() backoff.BackOff { return &backoff.ZeroBackOff{} }
			client, ctx, source, err := app.authorized(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "plain" {
				err = app.watch(ctx, client, projectID, projectID, source)
			} else {
				model := &watchModel{app: app, ctx: ctx, client: client, project: projectID, id: projectID, source: source}
				model.Update(model.poll(0)())
				err = model.err
			}
			var exit *ExitError
			if calls != 6 || !errors.As(err, &exit) || exit.Code != 5 {
				t.Fatalf("mode %s: calls=%d error=%v", mode, calls, err)
			}
		})
	}
}
