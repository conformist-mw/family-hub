package ics

import (
	"fmt"
	"strings"
	"time"

	"familyhub/internal/model"
)

// RenderSchool builds a VCALENDAR of the child's academic timetable, mirrored
// from the school portal. It is a separate feed from Render, not more events in
// the same one: the family calendar is the household's own courses,
// appointments and chores, while this is an external system's read-only day, on
// its own ICS URL and its own HA calendar so either can be shown or hidden
// alone.
//
// loc parses the stored wall-clock starts/ends (model.LocalDatetime) and every
// DTSTART is emitted as the matching UTC instant, exactly as Render does, so a
// lesson keeps its clock time across a DST change. Lessons arrive already
// filtered by category — the caller decides whether meals and recess belong in
// the feed; Render-School only writes what it is handed.
func RenderSchool(lessons []model.SchoolLesson, loc *time.Location, now time.Time) []byte {
	var b strings.Builder
	writeLine(&b, "BEGIN:VCALENDAR")
	writeLine(&b, "VERSION:2.0")
	writeLine(&b, "PRODID:-//lessons//school//EN")
	writeLine(&b, "CALSCALE:GREGORIAN")

	stamp := now.UTC().Format(utcStamp)
	for _, l := range lessons {
		start, err := time.ParseInLocation(model.LocalDatetime, l.StartsAt, loc)
		if err != nil {
			continue // unparseable start — skip rather than emit a broken VEVENT
		}
		end, err := time.ParseInLocation(model.LocalDatetime, l.EndsAt, loc)
		if err != nil || !end.After(start) {
			end = start.Add(time.Hour) // missing or nonsensical end — assume an hour
		}

		writeLine(&b, "BEGIN:VEVENT")
		// The portal's own event id keyed with a distinct prefix, so a school
		// lesson and a family lesson that happened to share a number never
		// collide in a calendar subscribed to both.
		writeLine(&b, "UID:school-"+fmt.Sprint(l.EventID)+"@familyhub")
		writeLine(&b, "DTSTAMP:"+stamp)
		writeLine(&b, "DTSTART:"+start.UTC().Format(utcStamp))
		writeLine(&b, "DTEND:"+end.UTC().Format(utcStamp))
		writeLine(&b, "SUMMARY:"+escape(l.Subject))
		if l.Topic != "" {
			writeLine(&b, "DESCRIPTION:"+escape(l.Topic))
		}
		writeLine(&b, "END:VEVENT")
	}

	writeLine(&b, "END:VCALENDAR")
	return []byte(b.String())
}
