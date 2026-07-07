// Package ics renders the recurring lesson schedule as an RFC 5545 VCALENDAR
// feed for Home Assistant's Remote Calendar to poll — a forward-looking
// summary of what's expected each week. Each active slot becomes one weekly
// recurring event (RRULE); duration comes from the slot.
package ics

import (
	"fmt"
	"strings"
	"time"

	"lessons/internal/model"
	"lessons/internal/store"
)

const utcStamp = "20060102T150405Z"

// byDay maps a weekday index (0=Sun..6=Sat) to its iCalendar day token.
var byDay = [7]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

// Render builds the VCALENDAR body. loc places the stored "HH:MM" slot times;
// now anchors each recurrence at its next occurrence and stamps DTSTAMP.
//
// Future support for skipping upcoming lessons (trainer on vacation) fits here
// as EXDATE lines on the relevant VEVENT — the model just needs to carry the
// excluded dates.
func Render(slots []store.SlotWithEnrollment, loc *time.Location, now time.Time) []byte {
	var b strings.Builder
	writeLine(&b, "BEGIN:VCALENDAR")
	writeLine(&b, "VERSION:2.0")
	writeLine(&b, "PRODID:-//lessons//EN")
	writeLine(&b, "CALSCALE:GREGORIAN")

	stamp := now.UTC().Format(utcStamp)
	for _, s := range slots {
		if s.Slot.Weekday < 0 || s.Slot.Weekday > 6 {
			continue
		}
		start, err := nextOccurrence(now, loc, s.Slot.Weekday, s.Slot.Time)
		if err != nil {
			continue
		}
		dur := s.Slot.DurationMin
		if dur <= 0 {
			dur = 60
		}
		end := start.Add(time.Duration(dur) * time.Minute)

		writeLine(&b, "BEGIN:VEVENT")
		writeLine(&b, "UID:slot-"+fmt.Sprint(s.Slot.ID)+"@lessons")
		writeLine(&b, "DTSTAMP:"+stamp)
		writeLine(&b, "DTSTART:"+start.UTC().Format(utcStamp))
		writeLine(&b, "DTEND:"+end.UTC().Format(utcStamp))
		writeLine(&b, "RRULE:FREQ=WEEKLY;BYDAY="+byDay[s.Slot.Weekday])
		writeLine(&b, "SUMMARY:"+escape(summary(s.Enrollment)))
		writeLine(&b, "END:VEVENT")
	}

	writeLine(&b, "END:VCALENDAR")
	return []byte(b.String())
}

// summary is "Person · Class" (e.g. "Маша · Балет"), or just the class name.
func summary(e model.Enrollment) string {
	if e.Person != "" {
		return e.Person + " · " + e.Name
	}
	return e.Name
}

// nextOccurrence returns the soonest datetime at or after the start of today
// (in loc) whose weekday matches, at the slot's "HH:MM". RRULE expands weekly
// from there.
func nextOccurrence(now time.Time, loc *time.Location, weekday int, hhmm string) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, err
	}
	now = now.In(loc)
	delta := (weekday - int(now.Weekday()) + 7) % 7
	d := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, loc)
	return d.AddDate(0, 0, delta), nil
}

func writeLine(b *strings.Builder, s string) {
	b.WriteString(s)
	b.WriteString("\r\n")
}

func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `;`, `\;`, `,`, `\,`, "\n", `\n`, "\r", "").Replace(s)
}
