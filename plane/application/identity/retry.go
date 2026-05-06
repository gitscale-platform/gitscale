package identity

import (
	"context"
	"math/rand"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
)

// retryMaxAttempts bounds the number of times WithSerializableRetry will
// reinvoke fn after observing a 40001 error. Beyond this the helper returns
// ErrRetryExhausted; callers can surface 503 Service Unavailable.
const retryMaxAttempts = 3

// WithSerializableRetry runs fn with bounded retry on serializable failure.
// On store.IsRetryable(err) it sleeps for a jittered backoff and retries up
// to retryMaxAttempts times. Any other non-nil error is returned verbatim.
// Returns nil on first success.
func WithSerializableRetry(ctx context.Context, fn func() error) error {
	delay := 10 * time.Millisecond
	for attempt := 0; attempt < retryMaxAttempts; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		if !store.IsRetryable(err) {
			return err
		}
		// Last attempt: no point sleeping.
		if attempt == retryMaxAttempts-1 {
			break
		}
		jitter := time.Duration(rand.Int63n(int64(delay)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay + jitter):
		}
		delay *= 2
		if delay > 100*time.Millisecond {
			delay = 100 * time.Millisecond
		}
	}
	return ErrRetryExhausted
}
