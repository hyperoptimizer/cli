package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var sensitiveLogKey = regexp.MustCompile(`(?i)^(authorization|credential|proxyAuthorization|secretHash|cookies?|headers|password|secret|token|.*(token|secret|password)|api[_-]?key|envVars|env_vars|parameters|body|payload)$`)

// NewLogHandler gives every process the same searchable JSON envelope.
func NewLogHandler(w io.Writer) slog.Handler {
	return slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if sensitiveLogKey.MatchString(a.Key) {
			return slog.Attr{}
		}
		switch a.Key {
		case slog.TimeKey:
			a.Key = "timestamp"
		case slog.MessageKey:
			a.Key = "message"
		case slog.LevelKey:
			a.Value = slog.StringValue(strings.ToLower(a.Value.String()))
		}
		if a.Value.Kind() == slog.KindString {
			a.Value = slog.StringValue(redact(a.Value.String()))
		}
		if err, ok := a.Value.Any().(error); ok {
			a.Value = slog.StringValue(redact(err.Error()))
		}
		return a
	}})
}
func ConfigureLogging(service string) {
	slog.SetDefault(slog.New(NewLogHandler(os.Stdout)).With("service", service, "environment", envDefault("BETTER_STACK_ENVIRONMENT", "development"), "release", os.Getenv("BETTER_STACK_RELEASE")))
}
func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func Log(event string, fields map[string]interface{}) {
	attrs := []interface{}{"event", event}
	for key, value := range fields {
		if value != nil {
			attrs = append(attrs, key, value)
		}
	}
	slog.Info(event, attrs...)
}
func LogError(event string, err error, fields map[string]interface{}) {
	attrs := []interface{}{"event", event, "error", map[string]interface{}{"type": fmt.Sprintf("%T", err), "message": redact(fmt.Sprint(err))}}
	for key, value := range fields {
		if value != nil {
			attrs = append(attrs, key, value)
		}
	}
	slog.Warn(event, attrs...)
}

// Correlation is carried on a cloned request, never shared client headers.
type correlationKey struct{}

func WithCorrelation(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey{}, id)
}
func NewCorrelationID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("request-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}

type CorrelatedTransport struct{ Base http.RoundTripper }

func (transport CorrelatedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	if value, ok := request.Context().Value(correlationKey{}).(string); ok {
		clone.Header.Set("x-correlation-id", value)
	}
	base := transport.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}
