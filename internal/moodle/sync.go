package moodle

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

// defaultInterval is how often the grades are re-read when Config.Interval is
// left at zero. Teachers grade in batches over the day; two hours keeps a new
// grade fresh news without hammering the school's site.
const defaultInterval = 2 * time.Hour

// Quiet hours, in the service's location: no sync between quietFrom and
// quietUntil. Nobody grades at night, and a notification at 23:00 about a
// grade from the afternoon is noise rather than news.
const (
	quietFrom  = 22
	quietUntil = 7
)

// Config is what the service needs beyond the client and store. The site URL
// lives on the client (NewClient).
type Config struct {
	Username string
	Password string
	Interval time.Duration // how often to re-sync; 0 means defaultInterval
	// Week0 is a day in school week 0 (normally its Monday); Week counts weeks
	// from the Monday of that week. Zero means there is no anchor and weeks
	// cannot be numbered.
	Week0 time.Time
}

func (c Config) interval() time.Duration {
	if c.Interval > 0 {
		return c.Interval
	}
	return defaultInterval
}

// Service mirrors the pupil's Moodle grades into the store and reports the
// ones that are new. now is injectable so quiet hours and week numbers are
// testable without a real clock.
type Service struct {
	store  *store.Store
	client *Client
	cfg    Config
	loc    *time.Location
	log    *slog.Logger
	now    func() time.Time

	// site serialises everything that touches the client, which is not safe
	// for concurrent use: the sync loop and the bot's weekly review both log
	// in and read through it, and the review's clock can fire mid-sync.
	site sync.Mutex

	notifyMu sync.Mutex
	notify   func([]model.SchoolGrade)
}

// NewService builds the sync service. A nil loc means time.Local, a nil now
// means time.Now, and a nil logger means slog.Default().
func NewService(st *store.Store, client *Client, cfg Config, loc *time.Location,
	logger *slog.Logger, now func() time.Time) *Service {
	if loc == nil {
		loc = time.Local
	}
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:  st,
		client: client,
		cfg:    cfg,
		loc:    loc,
		log:    logger,
		now:    func() time.Time { return now().In(loc) },
	}
}

// SetNotify sets the callback that receives the grades each sync found new.
// The bot is built after the service, so the callback is attached late; until
// it is (or when there is no bot at all) grades are still stored, just not
// announced.
func (s *Service) SetNotify(fn func([]model.SchoolGrade)) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	s.notify = fn
}

func (s *Service) notifyFunc() func([]model.SchoolGrade) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	return s.notify
}

// Run syncs immediately, then on every Interval tick, until ctx is done. It
// blocks; meant for its own goroutine. Ticks inside quiet hours are skipped. A
// failed sync is logged, never fatal: a school site outage must not take the
// rest of family-hub down, and the stored grades stay served meanwhile.
func (s *Service) Run(ctx context.Context) {
	s.log.Info("moodle: syncer started", "interval", s.cfg.interval())
	s.syncUnlessQuiet(ctx)

	ticker := time.NewTicker(s.cfg.interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncUnlessQuiet(ctx)
		}
	}
}

// syncUnlessQuiet runs one sync unless the clock is in quiet hours, and logs
// the outcome. It reports whether a sync was attempted.
func (s *Service) syncUnlessQuiet(ctx context.Context) bool {
	if quiet(s.now()) {
		s.log.Debug("moodle: quiet hours, sync skipped")
		return false
	}
	if err := s.SyncOnce(ctx); err != nil {
		s.log.Error("moodle: sync", "err", err)
	}
	return true
}

func quiet(t time.Time) bool {
	h := t.Hour()
	return h >= quietFrom || h < quietUntil
}

// SyncOnce logs in, reads every enrolled course with its average and graded
// items, writes the lot to the store and hands the grades that are news to the
// notify callback.
//
// It logs in on every run rather than holding a token: a sync every couple of
// hours costs one extra request, and there is then no expired-token path to
// get wrong.
//
// Any failed read aborts before the store is touched. The store treats a sync
// as the whole truth and deletes what it no longer contains, so writing a sync
// where one course timed out would wipe that course's grades and later re-seed
// it silently, losing whatever was graded in between.
func (s *Service) SyncOnce(ctx context.Context) error {
	fresh, err := s.sync(ctx)
	if err != nil {
		return err
	}
	if notify := s.notifyFunc(); len(fresh) > 0 && notify != nil {
		notify(fresh)
	}
	return nil
}

// sync does the reading and writing under the client lock; SyncOnce notifies
// after it is released, so a slow Telegram send does not hold up the weekly
// review.
func (s *Service) sync(ctx context.Context) ([]model.SchoolGrade, error) {
	s.site.Lock()
	defer s.site.Unlock()

	now := s.now()
	if err := s.client.Login(ctx, s.cfg.Username, s.cfg.Password); err != nil {
		return nil, err
	}
	info, err := s.client.SiteInfo(ctx)
	if err != nil {
		return nil, err
	}
	enrolled, err := s.client.Courses(ctx, info.UserID)
	if err != nil {
		return nil, err
	}
	averages, err := s.client.CourseAverages(ctx, info.UserID)
	if err != nil {
		return nil, err
	}
	avgByCourse := make(map[int64]CourseAverage, len(averages))
	for _, a := range averages {
		avgByCourse[a.CourseID] = a
	}

	stored, err := s.storedGradeDates()
	if err != nil {
		return nil, err
	}

	courses := make([]model.SchoolCourse, 0, len(enrolled))
	var grades []model.SchoolGrade
	for i, c := range enrolled {
		courses = append(courses, toCourse(c, i, avgByCourse[c.ID]))
		items, err := s.client.GradeItems(ctx, c.ID, info.UserID)
		if err != nil {
			return nil, fmt.Errorf("course %d: %w", c.ID, err)
		}
		for _, it := range items {
			if g, ok := s.toGrade(c.ID, it, now, stored); ok {
				grades = append(grades, g)
			}
		}
	}

	fresh, err := s.store.ApplySchoolSync(courses, grades, now)
	if err != nil {
		return nil, err
	}
	s.log.Info("moodle: synced", "courses", len(courses), "grades", len(grades), "new", len(fresh))
	return fresh, nil
}

func toCourse(c Course, position int, avg CourseAverage) model.SchoolCourse {
	out := model.SchoolCourse{ID: c.ID, Name: model.CourseName(c.FullName), Position: position}
	if avg.RawGrade.Valid {
		v := avg.RawGrade.V
		out.Average = &v
	}
	if text := strings.TrimSpace(avg.Grade); text != "-" {
		out.AverageText = text
	}
	return out
}

// storedGrade is what the store already holds for one grade item, for keeping
// the date of a grade Moodle gives no date for.
type storedGrade struct {
	raw      float64
	gradedAt string
}

func (s *Service) storedGradeDates() (map[int64]storedGrade, error) {
	courses, err := s.store.SchoolSummary()
	if err != nil {
		return nil, err
	}
	out := map[int64]storedGrade{}
	for _, c := range courses {
		for _, g := range c.Grades {
			out[g.ItemID] = storedGrade{raw: g.GradeRaw, gradedAt: g.GradedAt}
		}
	}
	return out, nil
}

// toGrade maps a gradebook row to a stored grade, or reports ok=false for one
// that is not a grade worth keeping: a course or category total (the course
// average already carries that) or an activity nobody has graded yet.
//
// A grade with no date in Moodle is dated by the sync that first saw it, and
// keeps that date while its value stands. Re-dating it on every sync would
// move it into the current week forever, so the weekly review would list the
// same old grade every Friday.
func (s *Service) toGrade(courseID int64, it GradeItem, now time.Time, stored map[int64]storedGrade) (model.SchoolGrade, bool) {
	if it.ItemType != "mod" || it.GradeRaw == nil {
		return model.SchoolGrade{}, false
	}
	raw := *it.GradeRaw

	var gradedAt string
	switch old, ok := stored[it.ID]; {
	case it.GradeDateGraded > 0:
		gradedAt = time.Unix(it.GradeDateGraded, 0).In(s.loc).Format(model.LocalDatetime)
	case ok && old.raw == raw:
		gradedAt = old.gradedAt
	default:
		gradedAt = now.Format(model.LocalDatetime)
	}

	text := strings.TrimSpace(it.GradeFormatted)
	if text == "" || text == "-" {
		text = model.FormatGrade(raw)
	}

	return model.SchoolGrade{
		ItemID:       it.ID,
		CourseID:     courseID,
		ItemModule:   it.ItemModule,
		ItemInstance: it.ItemInstance,
		Name:         itemName(it),
		GradeRaw:     raw,
		GradeText:    text,
		GradedAt:     gradedAt,
	}, true
}

// itemName is the grade item's name, falling back to its module type: the
// store refuses an empty name, and one unnamed item must not fail the sync.
func itemName(it GradeItem) string {
	if name := strings.TrimSpace(it.ItemName); name != "" {
		return name
	}
	if it.ItemModule != "" {
		return it.ItemModule
	}
	return "Оцінка"
}

// Week is one school week: its number counted from Config.Week0, and its
// bounds as the half-open range [Start, End) — Monday 00:00 to the next
// Monday 00:00 in the service's location.
type Week struct {
	N     int
	Start time.Time
	End   time.Time
}

// HasWeekAnchor reports whether weeks can be numbered (Config.Week0 is set).
func (s *Service) HasWeekAnchor() bool { return !s.cfg.Week0.IsZero() }

// Week returns the school week containing t. Weeks are counted in calendar
// days from the Monday of Config.Week0's week, so a DST change does not shift
// a boundary by an hour, and a day before the anchor falls in week -1, -2...
// (the summer before the year starts). Only meaningful when HasWeekAnchor.
//
// Week0 is read as a calendar date, not an instant: a date parsed as UTC
// midnight would otherwise land on the previous day west of UTC.
func (s *Service) Week(t time.Time) Week {
	w0 := s.cfg.Week0
	anchor := mondayOf(time.Date(w0.Year(), w0.Month(), w0.Day(), 0, 0, 0, 0, s.loc))
	day := civilDay(t.In(s.loc))
	days := int(day.Sub(civilDay(anchor)).Hours() / 24)
	n := days / 7
	if days < 0 && days%7 != 0 {
		n--
	}
	start := time.Date(anchor.Year(), anchor.Month(), anchor.Day()+7*n, 0, 0, 0, 0, s.loc)
	end := time.Date(start.Year(), start.Month(), start.Day()+7, 0, 0, 0, 0, s.loc)
	return Week{N: n, Start: start, End: end}
}

// mondayOf returns Monday 00:00 of t's week, in t's location.
func mondayOf(t time.Time) time.Time {
	offset := (int(t.Weekday()) + 6) % 7 // Monday=0
	return time.Date(t.Year(), t.Month(), t.Day()-offset, 0, 0, 0, 0, t.Location())
}

// civilDay is t's calendar date at midnight UTC, so the difference between two
// of them is a whole number of 24-hour days whatever the DST rules.
func civilDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// CourseProgress is one course's line in the weekly review: how many of the
// week's quizzes carry a grade, the grades given that week, and the course
// average.
type CourseProgress struct {
	CourseID    int64
	Name        string
	Average     *float64 // nil while the course has no average
	AverageText string   // Moodle's rendering ("5,5"), "" when there is none

	// QuizzesGraded of QuizzesTotal quizzes in the week's section have a
	// grade. The school hardly records completion, so a graded quiz is the
	// only reliable sign that the week's work was done; lessons, labels and
	// books carry no such signal and are not counted.
	QuizzesGraded, QuizzesTotal int
	// NoSection: the course has no "Тиждень N" section for this week. Every
	// course missing one at once means the week anchor is off (holidays).
	NoSection bool
	// Unavailable: the course's contents could not be read, so the quiz counts
	// and NoSection say nothing. Grades and average come from the store and
	// are still valid.
	Unavailable bool

	Grades []model.SchoolGrade // graded within the week, oldest first
}

// weekSection matches a section named after its school week ("Тиждень 12",
// "Тиждень 12. Квадратні рівняння") and captures the number.
var weekSection = regexp.MustCompile(`(?i)^\s*Тиждень\s+(\d+)`)

// quizModule is the module name of a quiz in course contents and the
// itemmodule of its grade item.
const quizModule = "quiz"

// WeekProgress reports, for every course the last sync stored (in Moodle's
// order), how many quizzes of that week's section are graded and which grades
// fell in the week.
//
// Courses and grades come from the store rather than Moodle: the sync keeps
// them current and already carries the averages. Before the first successful
// sync there are none, and the review is empty. A quiz counts as graded
// whenever its grade came: one written in week 12 but marked a week later
// still closes week 12.
//
// A course whose contents cannot be read is marked Unavailable rather than
// failing the review: one slow course should not cost the family the whole
// Friday message. Failing to log in does fail it.
func (s *Service) WeekProgress(ctx context.Context, week Week) ([]CourseProgress, error) {
	courses, err := s.store.SchoolSummary()
	if err != nil {
		return nil, err
	}
	grades, err := s.store.SchoolGradesBetween(
		week.Start.In(s.loc).Format(model.LocalDatetime),
		week.End.In(s.loc).Format(model.LocalDatetime))
	if err != nil {
		return nil, err
	}
	byCourse := map[int64][]model.SchoolGrade{}
	for _, g := range grades {
		byCourse[g.CourseID] = append(byCourse[g.CourseID], g)
	}

	s.site.Lock()
	defer s.site.Unlock()

	if err := s.client.Login(ctx, s.cfg.Username, s.cfg.Password); err != nil {
		return nil, err
	}

	out := make([]CourseProgress, 0, len(courses))
	for _, c := range courses {
		p := CourseProgress{
			CourseID:    c.ID,
			Name:        c.Name,
			Average:     c.Average,
			AverageText: c.AverageText,
			Grades:      byCourse[c.ID],
		}
		if err := s.courseWeek(ctx, &p, week.N, gradedQuizzes(c.Grades)); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			s.log.Warn("moodle: week progress", "course", c.ID, "err", err)
			p.Unavailable = true
		}
		out = append(out, p)
	}
	return out, nil
}

// gradedQuizzes is the set of quiz instances among a course's stored grades.
// The store keeps only graded items, so presence means graded.
func gradedQuizzes(grades []model.SchoolGrade) map[int64]bool {
	set := map[int64]bool{}
	for _, g := range grades {
		if g.ItemModule == quizModule {
			set[g.ItemInstance] = true
		}
	}
	return set
}

// courseWeek fills the quiz counts and NoSection for one course from its
// week-n section. graded holds the course's graded quiz instances; a grade
// item points at its quiz by instance id, the same id the section's module
// carries.
func (s *Service) courseWeek(ctx context.Context, p *CourseProgress, n int, graded map[int64]bool) error {
	sections, err := s.client.Contents(ctx, p.CourseID)
	if err != nil {
		return err
	}
	found := false
	for _, sec := range sections {
		if num, ok := sectionWeek(sec.Name); !ok || num != n {
			continue
		}
		found = true
		for _, m := range sec.Modules {
			if m.ModName != quizModule {
				continue
			}
			p.QuizzesTotal++
			if graded[m.Instance] {
				p.QuizzesGraded++
			}
		}
	}
	p.NoSection = !found
	return nil
}

// sectionWeek reads the week number off a section name, ok=false when the
// section is not a "Тиждень N" one.
func sectionWeek(name string) (int, bool) {
	m := weekSection.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}
