package jobs

import (
	"errors"
	"time"
)

// Error is a handler outcome (backend.md §4.4). Detail must follow the logging rules
// (e.g. "http 503"; never content).
type Error struct {
	Category   string
	Detail     string
	RetryAfter time.Duration
	Permanent  bool
}

func (e *Error) Error() string {
	if e.Permanent {
		return "jobs: permanent " + e.Category
	}
	return "jobs: retryable " + e.Category
}

func Retryable(category, detail string, retryAfter time.Duration) error {
	return &Error{Category: category, Detail: detail, RetryAfter: retryAfter}
}

func Permanent(category, detail string) error {
	return &Error{Category: category, Detail: detail, Permanent: true}
}

// classify maps any handler error to an outcome; unknown errors are retryable internal.
func classify(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Category: CatInternal}
}
