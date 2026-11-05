package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

type tokenContextKey struct{}
type organizationContextKey struct{}

func WithOrganization(ctx context.Context, organization string) context.Context {
	return context.WithValue(ctx, organizationContextKey{}, organization)
}

func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenContextKey{}, token)
}

type Error struct {
	Status        int
	Code, Message string
	RetryAfter    time.Duration
}

func (e *Error) Error() string { return e.Message }
func incompatible() error {
	return &Error{Code: "INCOMPATIBLE", Message: "GraphQL response is incompatible with this CLI. Upgrade hpo or check the API deployment."}
}

type authenticatedDoer struct {
	client    *http.Client
	userAgent string
}

func (d authenticatedDoer) Do(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("User-Agent", d.userAgent)
	request.Header.Set("Accept", "application/graphql-response+json, application/json")
	if organization, ok := request.Context().Value(organizationContextKey{}).(string); ok && organization != "" {
		request.Header.Set("X-HPO-Organization", organization)
	}
	if token, ok := request.Context().Value(tokenContextKey{}).(string); ok {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := d.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, nil
	}
	defer response.Body.Close()
	var body struct {
		Errors gqlerror.List `json:"errors"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 1048576)).Decode(&body)
	failure := codedError(body.Errors)
	failure.Status = response.StatusCode
	failure.RetryAfter = RetryAfter(response)
	return nil, failure
}
func clientError(err error) error {
	var known *Error
	if errors.As(err, &known) {
		return known
	}
	var graphqlErrors gqlerror.List
	if errors.As(err, &graphqlErrors) {
		return codedError(graphqlErrors)
	}
	return err
}
func codedError(list gqlerror.List) *Error {
	result := &Error{Message: "GraphQL request failed"}
	if len(list) == 0 {
		return result
	}
	result.Code, _ = list[0].Extensions["code"].(string)
	// Display domain diagnostics, not raw provider/transport bodies or schema coercion values.
	switch result.Code {
	case "FORBIDDEN", "NOT_FOUND", "CONFLICT", "SERVICE_UNAVAILABLE", "RATE_LIMITED":
		result.Message = list[0].Message
	case "BAD_USER_INPUT", "GRAPHQL_VALIDATION_FAILED":
		result.Message = "Invalid request or configuration. Run hpo validate and check the deployed schema."
	case "UNAUTHENTICATED":
		result.Message = "Authentication required"
	default:
		result.Code = "INCOMPATIBLE"
		result.Message = incompatible().Error()
	}
	if seconds, ok := list[0].Extensions["retryAfter"].(float64); ok && seconds > 0 && seconds <= 86400 {
		result.RetryAfter = time.Duration(seconds * float64(time.Second))
	}
	return result
}
func RetryAfter(response *http.Response) time.Duration {
	if response == nil {
		return 0
	}
	value := response.Header.Get("Retry-After")
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 && seconds <= int64(time.Duration(1<<63-1)/time.Second) {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		return max(0, time.Until(when))
	}
	return 0
}
