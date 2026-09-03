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

// SchoolLessonDetail is what actually happened at one lesson, read from the
// portal's lesson detail page: the topic, the teacher's notes, the homework
// and the marks. Unlike SchoolLesson, this is a record rather than a mirror —
// it is collected once a week and never swept, because the timetable window it
// came from scrolls away and the portal offers no way back to an old week.
//
// Marks and Files are carried inline: a lesson and its marks are written and
// read as one thing, and splitting the store API by table would put the
// stitching in every caller.
type SchoolLessonDetail struct {
	EventID  int64
	PupilID  int64
	StartsAt string // LocalDatetime, copied from the timetable event
	// Subject keeps the portal's group tag ("Алгебра [9]"), the same shape
	// SchoolLesson carries, so stripGroupTag and Classify work unchanged.
	Subject  string
	Teacher  string
	Topic    string
	Notes    string
	Homework string
	Marks    []SchoolMark
	Files    []SchoolFile
}

// SchoolMark is one mark given at a lesson. Value is the portal's own
// rendering ("9,00"): nothing computes with it, so nothing needs it parsed.
type SchoolMark struct {
	Kind  string // "Поточна", "Тематична"
	Value string
}

// SchoolFile is an attachment hung on a lesson, stored as a link only. Kind is
// "homework" or "lesson", after the tab it appeared on.
type SchoolFile struct {
	Kind  string
	URL   string
	Title string
}
