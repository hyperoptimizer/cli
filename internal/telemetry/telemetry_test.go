package telemetry

import (
	"github.com/getsentry/sentry-go"
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	event := &sentry.Event{
		Message:   "request https://user:secret@example.com/path?token=private failed: Bearer private",
		Contexts:  map[string]sentry.Context{"payload": {"token": "private"}},
		Request:   &sentry.Request{Data: "private"},
		User:      sentry.User{Email: "private@example.com"},
		Exception: []sentry.Exception{{Value: "token=private", Stacktrace: &sentry.Stacktrace{Frames: []sentry.Frame{{Filename: "worker.go", Lineno: 42, Vars: map[string]interface{}{"secret": "private"}}}}}},
	}
	result := sanitize(event, nil)
	if strings.Contains(result.Message, "private") || strings.Contains(result.Exception[0].Value, "private") {
		t.Fatal("credentials were retained")
	}
	if result.Contexts != nil || result.Request != nil || result.User.Email != "" {
		t.Fatal("payload or identity was retained")
	}
	frame := result.Exception[0].Stacktrace.Frames[0]
	if frame.Vars != nil || frame.Filename != "worker.go" || frame.Lineno != 42 {
		t.Fatal("frame sanitization lost location or retained locals")
	}
}
