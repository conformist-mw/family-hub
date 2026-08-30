// Package recur expands RFC 5545 recurrence rules into concrete local times.
//
// It is a thin wrapper over rrule-go, and exists so the rest of the app never
// touches that library directly: expansion happens in exactly one place, and
// the timezone contract below is stated once instead of being re-derived at
// every call site.
//
// Timezone contract: an occurrence is a WALL-CLOCK time. The anchor carries
// the zone, and every returned time is in that same zone, so "08:00" stays
// 08:00 across a DST transition rather than drifting an hour the way fixed
// UTC arithmetic does. Two consequences follow, both wanted:
//
//   - Autumn fall-back, where a local time happens twice: exactly one
//     occurrence comes back, at the second (post-transition) instant. A
//     reminder set for 03:30 fires once that day, not twice.
//   - Spring-forward gap, where a local time does not exist at all: it
//     normalises forward, so 03:30 lands at 04:30 that day.
//
// Both are pinned by tests rather than assumed from the library's docs.
//
// Neither case is rejected. There is no local time a person can pick that
// this package refuses to expand.
package recur

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

// maxOccurrences caps a single expansion. A dense rule (FREQ=HOURLY) over the
// feed's 120-day window is a few thousand entries, so this is a fuse against a
// pathological rule eating memory, not a limit anyone should reach. Exceeding
// it is an error rather than a silent truncation: a half-expanded rule would
// read as "nothing more is scheduled" everywhere downstream.
//
// The count is enforced DURING iteration. Asking the library for the whole
// window and checking the length afterwards would allocate the pathological
// result before rejecting it — the opposite of a fuse.
const maxOccurrences = 10000

// ErrEmptyRule is returned for a blank rule string. Worth its own error
// because an empty form field is the common way to get here, and the
// library's own message for it is not something to show a person.
var ErrEmptyRule = errors.New("recur: empty rule")

// ErrBadRule wraps every rejection this package makes about a rule's text, so
// an HTTP layer can tell "the person's rule is wrong" from "our database
// failed" without string-matching. Without it, a store error on the same code
// path gets reported to the user as a bad recurrence rule.
var ErrBadRule = errors.New("recur: rule cannot be expanded")

// tooDense lists frequencies no family chore needs and that no window can
// bound: a SECONDLY rule yields 86400 occurrences a day, so any useful window
// trips the cap. Rejecting them at Validate keeps the promise the API makes —
// a rule that validates can be expanded — instead of storing a rule that
// errors on every later read and quietly drops the chore from the calendar.
var tooDense = map[string]bool{"SECONDLY": true, "MINUTELY": true}

// parse builds an rrule from the stored string. It tolerates a leading
// "RRULE:" because that is how rules appear in every ICS file and in every
// example on the web, so it is what people paste into the raw-rule field.
func parse(rule string) (*rrule.RRule, error) {
	rule = strings.TrimSpace(rule)
	rule = strings.TrimPrefix(rule, "RRULE:")
	if rule == "" {
		return nil, ErrEmptyRule
	}
	r, err := rrule.StrToRRule(rule)
	if err != nil {
		return nil, fmt.Errorf("%w: parse %q: %v", ErrBadRule, rule, err)
	}
	if f := strings.ToUpper(r.OrigOptions.Freq.String()); tooDense[f] {
		return nil, fmt.Errorf("%w: %s is too dense to expand", ErrBadRule, f)
	}
	return r, nil
}

// Validate reports whether a rule string can be expanded at all. The Mini App
// calls it before storing a hand-written rule, so the same library that will
// later expand the rule is the one that accepts it — a rule that validates
// here cannot fail to expand later.
func Validate(rule string) error {
	_, err := parse(rule)
	return err
}

// Expand returns every occurrence of rule in [from, to], both ends inclusive.
//
// anchor is the rule's DTSTART: it fixes the time of day and the phase of any
// INTERVAL. "Every two weeks" is meaningless without it — the anchor decides
// which of the two weeks is yours. Its location is the zone the results come
// back in; see the package comment.
func Expand(anchor time.Time, rule string, from, to time.Time) ([]time.Time, error) {
	if to.Before(from) {
		return nil, nil
	}
	r, err := parse(rule)
	if err != nil {
		return nil, err
	}
	r.DTStart(anchor)

	loc := anchor.Location()
	var out []time.Time
	// Iterate rather than call Between: the cap has to stop the allocation,
	// not measure it after the fact.
	it := r.Iterator()
	for {
		t, ok := it()
		if !ok || t.After(to) {
			break
		}
		if t.Before(from) {
			continue
		}
		if len(out) >= maxOccurrences {
			return nil, fmt.Errorf("%w: %q yields more than %d occurrences in the window",
				ErrBadRule, rule, maxOccurrences)
		}
		out = append(out, t.In(loc))
	}
	return out, nil
}

// Next returns up to n occurrences strictly after `after`, for the rule
// preview in the Mini App form. Unlike Expand it has no upper bound in time:
// a yearly rule still yields five dates without the caller guessing how wide
// a window to ask for.
//
// A rule that runs out (COUNT, UNTIL) simply returns fewer than n.
func Next(anchor time.Time, rule string, after time.Time, n int) ([]time.Time, error) {
	if n <= 0 {
		return nil, nil
	}
	r, err := parse(rule)
	if err != nil {
		return nil, err
	}
	r.DTStart(anchor)

	loc := anchor.Location()
	out := make([]time.Time, 0, n)
	cur := after
	for len(out) < n {
		nxt := r.After(cur, false)
		if nxt.IsZero() {
			break // the rule is exhausted, not an error
		}
		out = append(out, nxt.In(loc))
		cur = nxt
	}
	return out, nil
}
