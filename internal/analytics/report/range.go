package report

import (
	"errors"
	"fmt"
	"time"
)

// dayLayout is the wire form of a report day and of the from/to parameters.
const dayLayout = "2006-01-02"

// Range is the half-open instant range [From, To) a report answers for: From is
// 00:00 UTC of the first day, To is 00:00 UTC of the day AFTER the last day, so
// both requested days are included.
type Range struct {
	From time.Time
	To   time.Time
}

// FirstDay is the first included UTC day (YYYY-MM-DD).
func (r Range) FirstDay() string { return r.From.Format(dayLayout) }

// LastDay is the last included UTC day (YYYY-MM-DD).
func (r Range) LastDay() string { return r.To.Add(-day()).Format(dayLayout) }

// FormatDay renders a report day as YYYY-MM-DD in UTC.
func FormatDay(t time.Time) string { return t.UTC().Format(dayLayout) }

// DefaultDays is the length of the range when from/to are omitted: the 30 UTC
// days ending today. MaxDays caps a caller-chosen range. They are functions,
// not constants, so mutation testing reaches (and the tests pin) the
// arithmetic.
func DefaultDays() int { return 30 }

// MaxDays is the longest range a caller may ask for: 366 days.
func MaxDays() int { return 366 }

func day() time.Duration { return 24 * time.Hour }

// Parameter errors; the HTTP adapter maps each to a 400 problem.
var (
	ErrInvalidFrom    = errors.New("from must be a date (YYYY-MM-DD)")
	ErrInvalidTo      = errors.New("to must be a date (YYYY-MM-DD)")
	ErrInvertedRange  = errors.New("from must not be after to")
	ErrRangeTooLarge  = errors.New("the range must not exceed 366 days")
	errParameterKinds = []error{ErrInvalidFrom, ErrInvalidTo, ErrInvertedRange, ErrRangeTooLarge}
)

// IsParameterError reports whether err is one of the range errors above (a
// client error, never a server one).
func IsParameterError(err error) bool {
	for _, k := range errParameterKinds {
		if errors.Is(err, k) {
			return true
		}
	}
	return false
}

// ParseRange validates the raw `from` / `to` query values (YYYY-MM-DD, "" =
// omitted, both inclusive) against now. Omitted bounds default so the range is
// DefaultDays long: no bounds -> the 30 days ending today; only `to` -> the 30
// days ending on `to`; only `from` -> from .. today. It is rejected when `from`
// is after `to` or the range is longer than MaxDays.
func ParseRange(fromRaw, toRaw string, now time.Time) (Range, error) {
	last := now.UTC().Truncate(day())
	if toRaw != "" {
		t, err := time.Parse(dayLayout, toRaw)
		if err != nil {
			return Range{}, fmt.Errorf("%w: %q", ErrInvalidTo, toRaw)
		}
		last = t
	}
	first := last.AddDate(0, 0, 1-DefaultDays())
	if fromRaw != "" {
		t, err := time.Parse(dayLayout, fromRaw)
		if err != nil {
			return Range{}, fmt.Errorf("%w: %q", ErrInvalidFrom, fromRaw)
		}
		first = t
	}
	if first.Compare(last) > 0 {
		return Range{}, ErrInvertedRange
	}
	if int(last.Sub(first)/day())+1 > MaxDays() {
		return Range{}, ErrRangeTooLarge
	}
	return Range{From: first, To: last.Add(day())}, nil
}
