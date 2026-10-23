// Package shared holds what more than one aggregate of the order-sourcing
// domain raises or implements: the shared domain errors, the Event interface
// and the identifier checks. It imports nothing.
//
// ErrNotFound is deliberately NOT here: a missing aggregate is a repository
// miss, so it is the port sentinel ports.ErrNotFound of the application layer.
// ErrConcurrentUpdate is raised by the repository on a version mismatch, also
// not by the domain (ADR 0002).
package shared

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	// ErrValidation marks a value that breaks a value-object rule (problem
	// type validation-failed, 422). Specific causes wrap it.
	ErrValidation = errors.New("validation failed")
	// ErrIllegalTransition marks a command that the aggregate's current state
	// does not allow (problem type illegal-transition, 409).
	ErrIllegalTransition = errors.New("illegal transition")
	// ErrInvalidSnapshot marks a persisted row that breaks an invariant. It is
	// an internal error, never a client error.
	ErrInvalidSnapshot = errors.New("invalid snapshot")
)

// Event is implemented by every domain event of every aggregate.
type Event interface {
	// EventName is the PascalCase event name, e.g. "FulfillmentQueued".
	EventName() string
	// Entity is the lowercase, separator-free aggregate segment of the
	// CloudEvents type: assignment.
	Entity() string
	// SubjectID is the aggregate root id (assignmentId): the CloudEvents subject and the Kafka key.
	SubjectID() string
	// Stream is StreamEvents or StreamAnalytics: the topic family the event
	// is published to (analytics-only events never go to the events topic).
	Stream() string
	// AggregateVersion is the aggregate version AFTER the change.
	AggregateVersion() int64
	// OccurredAt is the instant of the change, from the caller's clock, in UTC.
	OccurredAt() time.Time
}

// The two streams of an event (topics retailnetwork.order-sourcing.events and
// retailnetwork.order-sourcing.analytics).
const (
	StreamEvents    = "events"
	StreamAnalytics = "analytics"
)

// Header is embedded in every event for the version and the instant.
type Header struct {
	Version int64
	At      time.Time
}

// AggregateVersion returns the aggregate version after the change.
func (h Header) AggregateVersion() int64 { return h.Version }

// OccurredAt returns when the change happened.
func (h Header) OccurredAt() time.Time { return h.At }

// Invalid returns an error wrapping ErrValidation with a formatted cause.
func Invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrValidation, fmt.Sprintf(format, args...))
}

// Corrupt returns an error wrapping ErrInvalidSnapshot with a formatted cause.
func Corrupt(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSnapshot, fmt.Sprintf(format, args...))
}

// MaxID is the longest accepted aggregate id.
const MaxID = 64

// ValidID reports whether s is an opaque aggregate id: 1 to MaxID characters
// from A-Z a-z 0-9 . _ : -, starting with a letter or a digit.
func ValidID(s string) bool {
	if s == "" || len(s) > MaxID {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isAlnum(c) {
			continue
		}
		if i > 0 && (c == '.' || c == '_' || c == ':' || c == '-') {
			continue
		}
		return false
	}
	return true
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// MaxText is the longest accepted free-form vendor string.
const MaxText = 100

// ValidText reports whether s is a trimmed, non-blank string of at most
// MaxText characters without control characters.
func ValidText(s string) bool { return ValidTextN(s, MaxText) }

// ValidTextN is ValidText with a caller-chosen limit in characters.
func ValidTextN(s string, limit int) bool {
	if s == "" || s != strings.TrimSpace(s) || utf8.RuneCountInString(s) > limit {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// MaxInt32 is the Postgres integer ceiling every bounded integer of the
// domain respects (ADR 0002, A7). It is a plain literal on purpose.
const MaxInt32 = 2147483647

// UTC returns t in UTC, keeping the zero time zero.
func UTC(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.UTC()
}
