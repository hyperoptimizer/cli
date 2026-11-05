package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/hyperoptimizer/cli/internal/api"
)

type ExitError struct {
	Code       int
	Message    string
	Retryable  bool
	RetryAfter time.Duration
}

func (e *ExitError) Error() string           { return e.Message }
func failure(code int, message string) error { return &ExitError{Code: code, Message: message} }

func localHTTPHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "graphql.127.0.0.1.nip.io":
		return true
	default:
		return false
	}
}
func newClient(endpoint string) (*api.Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && localHTTPHost(u.Hostname()))) {
		return nil, failure(2, "GraphQL URL must use HTTPS (HTTP is allowed on localhost, loopback addresses, and graphql.127.0.0.1.nip.io)")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/graphql"
	} else if !strings.HasSuffix(u.Path, "/graphql") {
		return nil, failure(2, "Use a GraphQL origin or an endpoint ending in /graphql")
	}
	httpClient := &http.Client{Timeout: 45 * time.Second, Transport: http.DefaultTransport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return api.New(u.String(), httpClient, strings.ToLower("hpo/"+Version+"/"+runtime.GOOS)), nil
}
func authContext(ctx context.Context, key string) context.Context { return api.WithToken(ctx, key) }

func apiError(err error, source CredentialSource) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	var known *api.Error
	status, code, message, delay := 0, "", "", time.Duration(0)
	if errors.As(err, &known) {
		status, code, message, delay = known.Status, known.Code, known.Message, known.RetryAfter
	}
	if code == "UNAUTHENTICATED" || status == 401 {
		guidance := "Run hpo login."
		switch source {
		case credentialEnvironment:
			guidance = "Replace or unset HPO_API_KEY; saved login will not be used while it is set."
		case credentialFile:
			guidance = "Run hpo login --credential-file <path> using the same credential file."
		}
		return failure(3, "API key invalid, expired, or revoked ("+source.label()+"). "+guidance)
	}
	if code == "FORBIDDEN" || status == 403 {
		if message == "" {
			message = "Access denied: check API-key scopes and organization access"
		}
		return failure(4, message)
	}
	if code == "RATE_LIMITED" || status == 429 {
		return &ExitError{Code: 5, Message: "API rate limit reached", Retryable: true, RetryAfter: delay}
	}
	if status >= 500 || code == "SERVICE_UNAVAILABLE" {
		return &ExitError{Code: 5, Message: "GraphQL service unavailable. Check your connection and retry.", Retryable: true, RetryAfter: delay}
	}
	if code == "INCOMPATIBLE" {
		return failure(5, message)
	}
	if (status >= 400 && status < 500) || code == "BAD_USER_INPUT" || code == "NOT_FOUND" || code == "CONFLICT" {
		if message == "" {
			message = fmt.Sprintf("Request rejected (HTTP %d)", status)
		}
		return failure(2, message)
	}
	var transport *url.Error
	var certificate *tls.CertificateVerificationError
	return &ExitError{Code: 5, Message: fmt.Sprintf("API unavailable (HTTP %d). Check your connection and retry.", status), Retryable: status == 0 && errors.As(err, &transport) && !errors.As(err, &certificate), RetryAfter: delay}
}
func retryAfter(response *http.Response) time.Duration { return api.RetryAfter(response) }
