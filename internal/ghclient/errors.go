package ghclient

import (
	"errors"
	"fmt"
	"time"
)

// ErrUnauthorized means GitHub rejected the token twice in a row, the second
// time after re-resolving it.
var ErrUnauthorized = errors.New("ghclient: unauthorized (token rejected)")

// ErrForbidden is a 403 that is not a rate limit.
var ErrForbidden = errors.New("ghclient: forbidden")

// SecondaryLimitError is a GitHub secondary (abuse) rate limit. RetryAfter is
// the server's Retry-After, or 0 when it sent none; the scheduler then picks
// its own D-07 backoff.
type SecondaryLimitError struct {
	RetryAfter time.Duration
	Status     int
}

func (e *SecondaryLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("ghclient: secondary rate limit (status %d, retry after %v)", e.Status, e.RetryAfter)
	}
	return fmt.Sprintf("ghclient: secondary rate limit (status %d)", e.Status)
}

// PrimaryExhaustedError means the hourly GraphQL budget is spent. ResetAt is
// when it refills, or zero if GitHub did not say.
type PrimaryExhaustedError struct {
	ResetAt time.Time
}

func (e *PrimaryExhaustedError) Error() string {
	if e.ResetAt.IsZero() {
		return "ghclient: primary rate limit exhausted"
	}
	return "ghclient: primary rate limit exhausted until " + e.ResetAt.UTC().Format(time.RFC3339)
}

// TransientError is a failure worth retrying soon: a 5xx, a timeout or
// another transport error. Status is 0 for transport errors.
type TransientError struct {
	Status int
	Err    error
}

func (e *TransientError) Error() string {
	switch {
	case e.Err != nil && e.Status != 0:
		return fmt.Sprintf("ghclient: transient error (status %d): %v", e.Status, e.Err)
	case e.Err != nil:
		return "ghclient: transient error: " + e.Err.Error()
	default:
		return fmt.Sprintf("ghclient: transient error (status %d)", e.Status)
	}
}

func (e *TransientError) Unwrap() error { return e.Err }
