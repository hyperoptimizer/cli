package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func responseClient(t *testing.T, body string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
			t.Errorf("unexpected GraphQL request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return New(server.URL+"/graphql", server.Client(), "hpo/test")
}
func TestGraphQLErrorsNeverExposePartialDataOrSubmittedSecrets(t *testing.T) {
	client := responseClient(t, `{"data":{"cliIdentity":{"keyId":"partial","organizationId":"org"}},"errors":[{"message":"Invalid input secret-value","extensions":{"code":"BAD_USER_INPUT"}}]}`)
	result, err := client.Identity(context.Background())
	var failure *Error
	if result != nil || !errors.As(err, &failure) || failure.Code != "BAD_USER_INPUT" || failure.Message == "Invalid input secret-value" {
		t.Fatalf("unsafe partial response: %v %v", result, err)
	}
}
func TestMetricReadsKeepFrozenResultsAuthoritative(t *testing.T) {
	client := responseClient(t, `{"data":{"runs":[
 {"id":"frozen","status":"succeeded","finalizationState":"complete","optimization":{"number":1},"finalMetrics":[{"key":"score","value":"0.25"}],"metrics":[{"key":"score","aggregation":"last","lastValue":"999"},{"key":"missing-final","lastValue":"888"}]},
 {"id":"live","status":"running","finalizationState":"pending","optimization":{"number":2},"finalMetrics":[],"metrics":[{"key":"score","aggregation":"mean","meanValue":"0.75","lastValue":"100"}]}
 ]}}`)
	runs, err := client.Runs(context.Background(), "project", "experiment", "", 101, []string{"score"})
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].Metrics["score"] != 0.25 || len(runs[0].Metrics) != 1 || runs[1].Metrics["score"] != 0.75 {
		t.Fatalf("incorrect metric ownership: %v", runs)
	}
	for _, raw := range []string{"NaN", "Infinity", "not-a-number"} {
		if _, err := metricNumber(raw); err == nil {
			t.Errorf("accepted non-finite metric %q", raw)
		}
	}
}
func TestPublicLoginAndPrivateIdentityUseGraphQLAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			OperationName string `json:"operationName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if request.OperationName == "CliStartLogin" {
			if r.Header.Get("Authorization") != "" {
				t.Error("public login must not carry a stored credential")
			}
			_, _ = w.Write([]byte(`{"data":{"startCliLogin":{"deviceCode":"opaque","userCode":"H-123-456-789","verificationUri":"http://localhost:3003/connect","expiresIn":600,"interval":5}}}`))
		} else {
			if r.Header.Get("Authorization") != "Bearer hyperoptimizer_test" {
				t.Error("API key was not supplied")
			}
			_, _ = w.Write([]byte(`{"data":{"cliIdentity":{"keyId":"key","organizationId":"org","scopes":[]}}}`))
		}
	}))
	defer server.Close()
	client := New(server.URL+"/graphql", server.Client(), "hpo/test")
	if _, err := client.StartLogin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Identity(WithToken(context.Background(), "hyperoptimizer_test")); err != nil {
		t.Fatal(err)
	}
}
