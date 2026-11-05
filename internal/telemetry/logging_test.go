package telemetry

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestStructuredLogRedaction(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(NewLogHandler(&buffer)).With("service", "informer", "trial_id", "trial")
	logger.Info("token=private https://u:private@host/path?secret=private", "event", "trial.observation.published", "claim_token", "private", "observation_id", "observation")
	if strings.Contains(buffer.String(), "private") {
		t.Fatal("credential leaked")
	}
	var record map[string]interface{}
	if err := json.Unmarshal(buffer.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["trial_id"] != "trial" || record["observation_id"] != "observation" || record["timestamp"] == nil {
		t.Fatal(record)
	}
}

type testRoundTripper func(*http.Request) (*http.Response, error)

func (fn testRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
func TestCorrelationHeadersAreRequestScoped(t *testing.T) {
	request, err := http.NewRequest("GET", "http://fixture/acknowledgements", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("x-internal-key", "fixture-key")
	transport := CorrelatedTransport{Base: testRoundTripper(func(clone *http.Request) (*http.Response, error) {
		if clone.Header.Get("x-correlation-id") != "correlation" || clone.Header.Get("x-internal-key") != "fixture-key" {
			t.Fatal("correlation or original auth missing")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	response, err := transport.RoundTrip(request.WithContext(WithCorrelation(request.Context(), "correlation")))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if request.Header.Get("x-correlation-id") != "" {
		t.Fatal("shared headers mutated")
	}
}
