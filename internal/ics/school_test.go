package ics

import (
	"strings"
	"testing"
	"time"

	"familyhub/internal/model"
)

func TestRenderSchoolBasics(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 8, 31, 6, 0, 0, 0, time.UTC)
	lessons := []model.SchoolLesson{
		{EventID: 11714596, Subject: "Алгебра [9]", StartsAt: "2026-09-01T09:00", EndsAt: "2026-09-01T09:40", Topic: "Рівняння"},
		{EventID: 11714608, Subject: "Обід [Food Hub]", StartsAt: "2026-09-01T13:40", EndsAt: "2026-09-01T14:00"},
	}
	out := string(RenderSchool(lessons, loc, now))

	for _, want := range []string{
		"BEGIN:VCALENDAR", "END:VCALENDAR",
		"UID:school-11714596@familyhub",
		"DTSTART:20260901T090000Z",
		"DTEND:20260901T094000Z",
		"SUMMARY:Алгебра [9]",
		"DESCRIPTION:Рівняння",
		"UID:school-11714608@familyhub",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("feed missing %q\n---\n%s", want, out)
		}
	}
	if n := strings.Count(out, "BEGIN:VEVENT"); n != 2 {
		t.Errorf("want 2 VEVENTs, got %d", n)
	}
	// A lesson without a topic must not emit an empty DESCRIPTION.
	if strings.Contains(out, "DESCRIPTION:\r\n") {
		t.Error("emitted an empty DESCRIPTION line")
	}
}

// A missing or backwards end falls back to a one-hour event rather than an
// empty or negative span.
func TestRenderSchoolEndFallback(t *testing.T) {
	out := string(RenderSchool([]model.SchoolLesson{
		{EventID: 1, Subject: "X", StartsAt: "2026-09-01T09:00", EndsAt: ""},
	}, time.UTC, time.Now()))
	if !strings.Contains(out, "DTSTART:20260901T090000Z") ||
		!strings.Contains(out, "DTEND:20260901T100000Z") {
		t.Errorf("want a one-hour fallback event\n%s", out)
	}
}
