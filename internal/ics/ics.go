// Package ics renders the family schedule as an RFC 5545 VCALENDAR feed for
// Home Assistant's Remote Calendar to poll — a forward-looking summary of
// what's expected. Each active lesson slot becomes one weekly recurring event
// (RRULE, duration from the slot), trainer absences punch EXDATE holes and
// render as all-day events, and one-off appointments become plain VEVENTs.
package ics

import (
	"fmt"
	"strings"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

const utcStamp = "20060102T150405Z"

// byDay maps a weekday index (0=Sun..6=Sat) to its iCalendar day token.
var byDay = [7]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

// absenceSummaryPrefix maps an absence kind to the all-day event title.
var absenceSummaryPrefix = map[string]string{
	model.AbsenceVacation: "Отпуск",
	model.AbsenceSick:     "Болезнь",
	model.AbsenceOther:    "Отсутствие",
}

// Render builds the VCALENDAR body. loc places the stored wall-clock times
// (slot "HH:MM", appointment starts_at); now anchors each recurrence at its
// next occurrence and stamps DTSTAMP. Absences of a slot's trainer punch
// EXDATE holes into the recurrence and additionally render as one all-day
// VEVENT each. Appointments are rendered as-is — filtering (deleted,
// cancelled, too far in the past) belongs to the caller's query.
func Render(slots []store.SlotWithEnrollment, absences []model.TrainerAbsence, appointments []model.Appointment, loc *time.Location, now time.Time) []byte {
	var b strings.Builder
	writeLine(&b, "BEGIN:VCALENDAR")
	writeLine(&b, "VERSION:2.0")
	writeLine(&b, "PRODID:-//lessons//EN")
	writeLine(&b, "CALSCALE:GREGORIAN")

	byTrainer := make(map[int64][]model.TrainerAbsence)
	for _, a := range absences {
		byTrainer[a.TrainerID] = append(byTrainer[a.TrainerID], a)
	}

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
		if s.Enrollment.TrainerID != nil {
			for _, d := range excludedDates(start, loc, byTrainer[*s.Enrollment.TrainerID]) {
				writeLine(&b, "EXDATE:"+d)
			}
		}
		writeLine(&b, "SUMMARY:"+escape(summary(s.Enrollment)))
		writeLine(&b, "END:VEVENT")
	}

	for _, a := range absences {
		from, err1 := model.ParseDate(a.DateFrom)
		to, err2 := model.ParseDate(a.DateTo)
		if err1 != nil || err2 != nil {
			continue
		}
		prefix := absenceSummaryPrefix[a.Kind]
		if prefix == "" {
			prefix = absenceSummaryPrefix[model.AbsenceOther]
		}
		writeLine(&b, "BEGIN:VEVENT")
		writeLine(&b, "UID:absence-"+fmt.Sprint(a.ID)+"@lessons")
		writeLine(&b, "DTSTAMP:"+stamp)
		writeLine(&b, "DTSTART;VALUE=DATE:"+from.Format("20060102"))
		// DTEND is exclusive for all-day events (RFC 5545); date_to is inclusive.
		writeLine(&b, "DTEND;VALUE=DATE:"+to.AddDate(0, 0, 1).Format("20060102"))
		writeLine(&b, "SUMMARY:"+escape(prefix+": "+a.Trainer))
		writeLine(&b, "END:VEVENT")
	}

	for _, a := range appointments {
		start, err := a.Start(loc)
		if err != nil {
			continue // unparseable start — skip rather than emit a broken VEVENT
		}
		end := start.Add(time.Hour) // no explicit end: assume an hour
		if a.EndsAt != "" {
			if e, err := time.ParseInLocation(model.LocalDatetime, a.EndsAt, loc); err == nil {
				end = e
			}
		}

		writeLine(&b, "BEGIN:VEVENT")
		// Same @lessons suffix as the slot/absence uids above: HA keys events
		// on the uid, so changing the suffix would make it delete and recreate
		// every event for a string no user ever sees.
		writeLine(&b, "UID:appointment-"+fmt.Sprint(a.ID)+"@lessons")
		writeLine(&b, "DTSTAMP:"+stamp)
		writeLine(&b, "DTSTART:"+start.UTC().Format(utcStamp))
		writeLine(&b, "DTEND:"+end.UTC().Format(utcStamp))
		writeLine(&b, "SUMMARY:"+escape(appointmentSummary(a)))
		if a.Location != "" {
			writeLine(&b, "LOCATION:"+escape(a.Location))
		}
		if a.Raw != "" {
			writeLine(&b, "DESCRIPTION:"+escape(a.Raw))
		}
		writeLine(&b, "END:VEVENT")
	}

	writeLine(&b, "END:VCALENDAR")
	return []byte(b.String())
}

// appointmentSummary is "Ортодонт · Демид", or just the title. Named apart from
// summary (the lesson one) — same shape, different domain, one package.
func appointmentSummary(a model.Appointment) string {
	if a.Person != "" {
		return a.Title + " · " + a.Person
	}
	return a.Title
}

// excludedDates returns EXDATE values for the recurrence anchored at start
// (the VEVENT's DTSTART) that fall inside any of the absences. Values are
// computed as fixed 7-day UTC offsets from DTSTART — exactly the instants
// FREQ=WEEKLY generates from a UTC DTSTART — never re-derived from local
// wall-clock time, which would drift an hour across a DST transition and
// silently stop matching. The occurrence's calendar date (in loc, as the
// user sees it) is what's checked against the absence range.
func excludedDates(start time.Time, loc *time.Location, absences []model.TrainerAbsence) []string {
	if len(absences) == 0 {
		return nil
	}
	maxTo := ""
	for _, a := range absences {
		if a.DateTo > maxTo {
			maxTo = a.DateTo
		}
	}
	var out []string
	for occ := start.UTC(); ; occ = occ.Add(7 * 24 * time.Hour) {
		date := occ.In(loc).Format("2006-01-02")
		if date > maxTo {
			return out
		}
		for _, a := range absences {
			if a.DateFrom <= date && date <= a.DateTo {
				out = append(out, occ.Format(utcStamp))
				break
			}
		}
	}
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
