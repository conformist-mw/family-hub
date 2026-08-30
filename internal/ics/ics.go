// Package ics renders the family schedule as an RFC 5545 VCALENDAR feed for
// Home Assistant's Remote Calendar to poll. Everything in it arrives already
// expanded — one VEVENT per lesson, per chore occurrence, per appointment —
// and trainer absences additionally render as all-day events.
//
// Nothing is sent as an RRULE any more. A recurrence from a UTC DTSTART is
// expanded in UTC, so its instances are 168h apart rather than at the same
// wall-clock time each week, and every weekly lesson shifted an hour at the
// October clock change and stayed wrong all winter. Expansion in Go goes
// through internal/recur, which keeps wall-clock time across a transition and
// is pinned by tests.
//
// The expansion also has to happen anyway — the Mini App list and the evening
// nag both need it — so handing HA a rule to expand as well would be a second,
// independent implementation of the same thing, free to drift from the first.
package ics

import (
	"fmt"
	"strings"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/reminders"
	"familyhub/internal/schedule"
)

const utcStamp = "20060102T150405Z"

// absenceSummaryPrefix maps an absence kind to the all-day event title.
var absenceSummaryPrefix = map[string]string{
	model.AbsenceVacation: "Відпустка",
	model.AbsenceSick:     "Хвороба",
	model.AbsenceOther:    "Відсутність",
}

// Render builds the VCALENDAR body. loc places the stored wall-clock times
// (appointment starts_at) and is the zone every DTSTART is converted from;
// now stamps DTSTAMP. Absences render as one all-day VEVENT each — the
// lessons they cancel are already absent from `lessons`, because the caller
// expanded them out. Appointments are rendered as-is: filtering (deleted,
// cancelled, too far in the past) belongs to the caller's query.
func Render(lessons []schedule.Lesson, absences []model.TrainerAbsence,
	appointments []model.Appointment, chores []reminders.Occurrence,
	loc *time.Location, now time.Time) []byte {
	var b strings.Builder
	writeLine(&b, "BEGIN:VCALENDAR")
	writeLine(&b, "VERSION:2.0")
	writeLine(&b, "PRODID:-//lessons//EN")
	writeLine(&b, "CALSCALE:GREGORIAN")

	stamp := now.UTC().Format(utcStamp)
	for _, l := range lessons {
		start := l.Start.In(loc)
		writeLine(&b, "BEGIN:VEVENT")
		// Keyed by the instant, the way reminders are: one slot now produces
		// many events, so the slot id alone no longer identifies one of them.
		writeLine(&b, "UID:slot-"+fmt.Sprint(l.SlotID)+"-"+
			start.Format("20060102T1504")+"@familyhub")
		writeLine(&b, "DTSTAMP:"+stamp)
		writeLine(&b, "DTSTART:"+start.UTC().Format(utcStamp))
		writeLine(&b, "DTEND:"+l.End().UTC().Format(utcStamp))
		writeLine(&b, "SUMMARY:"+escape(summary(l.Enrollment)))
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
		writeLine(&b, "UID:absence-"+fmt.Sprint(a.ID)+"@familyhub")
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
		// Same suffix as every other uid here. It is an opaque identifier a
		// person never sees; what matters is that it stays stable, because HA
		// and any subscribed calendar key their events on it.
		writeLine(&b, "UID:appointment-"+fmt.Sprint(a.ID)+"@familyhub")
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

	for _, o := range chores {
		start := o.Due.In(loc)
		dur := o.DurationMin
		if dur <= 0 {
			dur = 15
		}
		writeLine(&b, "BEGIN:VEVENT")
		// Keyed by the instant, not the date: a full RRULE can put two
		// occurrences on one day, and a date-based uid would collapse them.
		writeLine(&b, "UID:reminder-"+fmt.Sprint(o.ReminderID)+"-"+
			start.Format("20060102T1504")+"@familyhub")
		writeLine(&b, "DTSTAMP:"+stamp)
		writeLine(&b, "DTSTART:"+start.UTC().Format(utcStamp))
		writeLine(&b, "DTEND:"+start.Add(time.Duration(dur)*time.Minute).UTC().Format(utcStamp))
		writeLine(&b, "SUMMARY:"+escape(reminderSummary(o)))
		writeLine(&b, "END:VEVENT")
	}

	writeLine(&b, "END:VCALENDAR")
	return []byte(b.String())
}

// reminderSummary marks what has been dealt with. A closed occurrence stays in
// the feed rather than vanishing — the calendar is a record of what was
// planned, and an entry that disappears once acted on cannot show that it was.
//
// done and skipped get different marks instead of one shared tick: "I did it"
// and "not needed this time" are different answers, and the morning summary is
// where that difference is worth seeing.
func reminderSummary(o reminders.Occurrence) string {
	title := o.Title
	if o.Person != "" {
		title += " · " + o.Person
	}
	switch o.Status {
	case model.OccDone:
		return "✓ " + title
	case model.OccSkipped:
		return "✗ " + title
	default:
		return title
	}
}

// appointmentSummary is "Ортодонт · <хто>", or just the title. Named apart from
// summary (the lesson one) — same shape, different domain, one package.
func appointmentSummary(a model.Appointment) string {
	if a.Person != "" {
		return a.Title + " · " + a.Person
	}
	return a.Title
}

// summary is "Person · Class" (e.g. "Маша · Балет"), or just the class name.
func summary(e model.Enrollment) string {
	if e.Person != "" {
		return e.Person + " · " + e.Name
	}
	return e.Name
}

func writeLine(b *strings.Builder, s string) {
	b.WriteString(s)
	b.WriteString("\r\n")
}

func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `;`, `\;`, `,`, `\,`, "\n", `\n`, "\r", "").Replace(s)
}
