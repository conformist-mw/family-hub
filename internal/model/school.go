package model

// SchoolLesson is one occurrence of the child's academic timetable, mirrored
// from the school-today.com portal. It is the source of truth for nothing — the
// portal is — and is stored only so the ICS feed and the evening digest read a
// local cache instead of hitting a slow, rate-limited login on every poll.
//
// Deliberately unrelated to Enrollment: a school subject is not a billed
// course. See migration 0007 for why the two models are kept apart.
type SchoolLesson struct {
	ID       int64
	EventID  int64
	PupilID  int64
	Subject  string
	StartsAt string // LocalDatetime
	EndsAt   string // LocalDatetime
	Topic    string
	HasMarks bool
	// ThemeColor is the portal's subject colour (e.g. "#1E983B"), "" if none.
	ThemeColor string
}
