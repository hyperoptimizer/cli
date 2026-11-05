package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cenkalti/backoff/v5"
)

type minimumBackOff struct {
	backoff.BackOff
	minimum time.Duration
}

func (b minimumBackOff) NextBackOff() time.Duration {
	delay := b.BackOff.NextBackOff()
	if delay == backoff.Stop {
		return delay
	}
	return max(b.minimum, delay)
}

// Retry only explicitly classified transient failures. Scheduling successful
// device/watch polls is separate from recovering a failed request.
func (a *App) retry(ctx context.Context, attempts uint, minimum time.Duration, operation func() error) error {
	policy := backoff.NewExponentialBackOff()
	policy.InitialInterval = max(time.Second, minimum)
	policy.MaxInterval = max(30*time.Second, minimum)
	policy.Multiplier = 2
	policy.RandomizationFactor = 0.2
	var strategy backoff.BackOff = policy
	if a.BackOff != nil {
		strategy = a.BackOff()
	}
	strategy = minimumBackOff{BackOff: strategy, minimum: minimum}
	var last error
	_, err := backoff.Retry(ctx, func() (struct{}, error) {
		if err := ctx.Err(); err != nil {
			return struct{}{}, backoff.Permanent(err)
		}
		last = operation()
		if last == nil {
			return struct{}{}, nil
		}
		var exit *ExitError
		if !errors.As(last, &exit) || !exit.Retryable {
			return struct{}{}, backoff.Permanent(last)
		}
		if exit.RetryAfter > 0 {
			delay := max(minimum, exit.RetryAfter)
			return struct{}{}, errors.Join(last, &backoff.RetryAfterError{Duration: delay})
		}
		return struct{}{}, last
	}, backoff.WithBackOff(strategy), backoff.WithMaxTries(attempts), backoff.WithMaxElapsedTime(0), backoff.WithNotify(func(error, time.Duration) {
		fmt.Fprintln(a.Err, "Connection interrupted or rate limited; retrying…")
	}))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return last
	}
	return nil
}
