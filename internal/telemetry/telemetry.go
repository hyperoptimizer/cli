package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
)

var urls = regexp.MustCompile(`https?://[^\s"<>]+`)
var credentials = regexp.MustCompile(`(?i)(Bearer\s+|(?:password|secret|token|api[_-]?key)\s*[:=]\s*)[^\s,;]+`)
var lastReported = struct {
	sync.Mutex
	times map[string]time.Time
}{times: make(map[string]time.Time)}

func redact(value string) string {
	value = urls.ReplaceAllStringFunc(value, func(value string) string {
		u, err := url.Parse(value)
		if err != nil {
			return "[URL]"
		}
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	})
	return credentials.ReplaceAllString(value, "${1}[Filtered]")
}

func sanitize(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	event.Request = nil
	event.User = sentry.User{}
	event.Contexts = nil
	event.Breadcrumbs = nil
	event.Message = redact(event.Message)
	for i := range event.Exception {
		event.Exception[i].Value = redact(event.Exception[i].Value)
		if stack := event.Exception[i].Stacktrace; stack != nil {
			for j := range stack.Frames {
				stack.Frames[j].Vars = nil
			}
		}
	}
	return event
}

// Empty configuration performs no network activity. Call the returned function before exit.
func Init(service string) func() {
	ConfigureLogging(service)
	dsn := os.Getenv("BETTER_STACK_" + strings.ToUpper(strings.ReplaceAll(service, "-", "_")) + "_DSN")
	if dsn == "" && service != "cli" {
		dsn = os.Getenv("BETTER_STACK_DSN")
	}
	if dsn == "" {
		return func() {}
	}
	environment := os.Getenv("BETTER_STACK_ENVIRONMENT")
	if environment == "" {
		environment = "development"
	}
	if err := sentry.Init(sentry.ClientOptions{
		Dsn: dsn, Environment: environment, Release: os.Getenv("BETTER_STACK_RELEASE"),
		SendDefaultPII: false, AttachStacktrace: true, BeforeSend: sanitize,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "Better Stack monitoring could not initialize; check its DSN configuration")
		return func() {}
	}
	sentry.ConfigureScope(func(scope *sentry.Scope) { scope.SetTag("service", service) })
	return func() { sentry.Flush(2 * time.Second) }
}

// Retry loops produce at most one report per operation per minute in each process.
func Report(err error, operation string) {
	ReportWithFields(err, operation, nil)
}

// Correlation tags are allowlisted; request bodies and credentials are never attached.
func ReportWithFields(err error, operation string, fields map[string]interface{}) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	client := sentry.CurrentHub().Client()
	if client == nil || client.Options().Dsn == "" {
		return
	}
	lastReported.Lock()
	if time.Since(lastReported.times[operation]) < time.Minute {
		lastReported.Unlock()
		return
	}
	lastReported.times[operation] = time.Now()
	lastReported.Unlock()
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("operation", operation)
		for _, key := range []string{"org_id", "project_id", "experiment_id", "trial_id", "observation_id", "correlation_id", "k8s_job_uid"} {
			if value, ok := fields[key]; ok {
				scope.SetTag(key, fmt.Sprint(value))
			}
		}
		sentry.CaptureException(err)
	})
}

// Preserve crash semantics after delivering the panic report, including in goroutines.
func Recover() {
	if value := recover(); value != nil {
		sentry.CurrentHub().Recover(value)
		sentry.Flush(2 * time.Second)
		panic(value)
	}
}
