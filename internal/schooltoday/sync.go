package schooltoday

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

// Defaults applied when a config field is left at its zero value.
const (
	defaultWeeksAhead = 3
	defaultInterval   = 12 * time.Hour
)

// portalTimeFormat is the layout the timetable JSON uses for start/end — naive
// local wall-clock with seconds. Stored trimmed to model.LocalDatetime.
const portalTimeFormat = "2006-01-02T15:04:05"

// Config is what the sync needs beyond the client and store. BaseURL lives on
// the client (NewClient); everything here is per-family.
type Config struct {
	Email      string
	Password   string
	PupilID    int64
	WeeksAhead int           // weeks of timetable to mirror, starting this week
	Interval   time.Duration // how often to re-sync
}

func (c Config) weeksAhead() int {
	if c.WeeksAhead > 0 {
		return c.WeeksAhead
	}
	return defaultWeeksAhead
}

func (c Config) interval() time.Duration {
	if c.Interval > 0 {
		return c.Interval
	}
	return defaultInterval
}

// Service mirrors the portal timetable into the store on a schedule. now is
// injectable so the window maths is testable without waiting for a real clock.
type Service struct {
	store  *store.Store
	client *Client
	cfg    Config
	loc    *time.Location
	log    *slog.Logger
	now    func() time.Time

	// portal serialises everything that touches the client. Client is
	// documented as single-goroutine, and it is no longer driven by only the
	// sync loop: the bot's Friday review calls CollectWeek from the digest
	// goroutine, which can tick while a sync is mid-flight. Without this the
	// two would interleave logins around each other's requests, and "one login
	// for the whole batch" would stop being true.
	portal sync.Mutex
}

func NewService(st *store.Store, client *Client, cfg Config, loc *time.Location,
	logger *slog.Logger, now func() time.Time) *Service {
	if loc == nil {
		loc = time.Local
	}
	if now == nil {
		now = time.Now
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

// RunSyncer syncs immediately, then on every Interval tick, until ctx is done.
// It blocks; meant for its own goroutine. A failed sync is logged, never fatal:
// a portal outage must not take the rest of family-hub down, and the previous
// cache stays served until the next tick succeeds.
func (s *Service) RunSyncer(ctx context.Context) {
	s.log.Info("schooltoday: syncer started",
		"pupil", s.cfg.PupilID, "weeks", s.cfg.weeksAhead(), "interval", s.cfg.interval())

	if err := s.Sync(ctx); err != nil {
		s.log.Error("schooltoday: initial sync", "err", err)
	}

	ticker := time.NewTicker(s.cfg.interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Sync(ctx); err != nil {
				s.log.Error("schooltoday: sync", "err", err)
			}
		}
	}
}

// Sync logs in, fetches the timetable for [this week, +WeeksAhead), and swaps
// the mirror for that whole window in one transaction. It re-logs in every run
// rather than holding a session: the sync is infrequent and the portal session
// outlives no reasonable interval, so a fresh login is simpler than detecting
// and refreshing an expired one.
//
// A fetch error aborts before the store is touched, so a portal that is down or
// a session that failed to establish leaves the last good cache in place rather
// than replacing a populated week with an empty one.
func (s *Service) Sync(ctx context.Context) error {
	s.portal.Lock()
	defer s.portal.Unlock()

	if err := s.client.Login(ctx, s.cfg.Email, s.cfg.Password); err != nil {
		return err
	}

	from := StartOfWeek(s.now())
	weeks := s.cfg.weeksAhead()
	to := from.AddDate(0, 0, 7*weeks)

	// Deduped across weeks by event id: adjacent weeks should not overlap, but
	// keying on the portal's own id makes a stray repeat harmless instead of a
	// UNIQUE violation mid-transaction.
	seen := map[int64]bool{}
	var lessons []model.SchoolLesson
	for i := 0; i < weeks; i++ {
		weekAnchor := from.AddDate(0, 0, 7*i)
		events, err := s.client.Timetable(ctx, s.cfg.PupilID, weekAnchor)
		if err != nil {
			return err
		}
		for _, e := range events {
			l, ok := s.toLesson(e)
			if !ok || seen[l.EventID] {
				continue
			}
			seen[l.EventID] = true
			lessons = append(lessons, l)
		}
	}

	if err := s.store.ReplaceSchoolLessons(s.cfg.PupilID,
		from.Format(model.LocalDatetime), to.Format(model.LocalDatetime), lessons); err != nil {
		return err
	}

	s.log.Info("schooltoday: synced", "pupil", s.cfg.PupilID,
		"lessons", len(lessons), "from", from.Format("2006-01-02"), "weeks", weeks)
	return nil
}

// toLesson maps a portal event to a stored lesson, or reports ok=false for one
// that should not be mirrored: a cancelled/deleted slot (the calendar shows
// what is happening, not what was called off) or an all-day banner (no lesson
// time to place on the schedule), or one whose times will not parse.
func (s *Service) toLesson(e Event) (model.SchoolLesson, bool) {
	if e.IsDeleted || e.IsCanceled || e.IsFullDay {
		return model.SchoolLesson{}, false
	}
	start, err := time.ParseInLocation(portalTimeFormat, e.Start, s.loc)
	if err != nil {
		return model.SchoolLesson{}, false
	}
	end, err := time.ParseInLocation(portalTimeFormat, e.End, s.loc)
	if err != nil {
		return model.SchoolLesson{}, false
	}
	// Trimmed: the portal pads a filled-in topic with trailing whitespace,
	// which shows up as a gap before the closing tag in every consumer.
	topic := ""
	if e.Topic != nil {
		topic = strings.TrimSpace(*e.Topic)
	}
	return model.SchoolLesson{
		EventID:    e.EventID,
		PupilID:    s.cfg.PupilID,
		Subject:    e.Subject,
		StartsAt:   start.Format(model.LocalDatetime),
		EndsAt:     end.Format(model.LocalDatetime),
		Topic:      topic,
		HasMarks:   e.HasMarks,
		ThemeColor: e.ThemeColor,
	}, true
}

// StartOfWeek returns Monday 00:00 of t's week, in t's location. The portal
// weeks run Monday–Sunday, so the replace window aligns to that boundary and a
// week is never half-fetched.
//
// Exported because the bot needs the same boundary to select a stored week for
// /schoolweek, and two implementations of "which Monday" would eventually
// disagree by a day at a DST edge.
func StartOfWeek(t time.Time) time.Time {
	// Go's Weekday has Sunday=0; shift so Monday=0.
	offset := (int(t.Weekday()) + 6) % 7
	d := t.AddDate(0, 0, -offset)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, t.Location())
}

// CollectWeek reads what actually happened during the week containing
// weekStart — topic, teacher's notes, homework and marks for every academic
// lesson — and records it. It returns what it collected and how many lessons
// it could not read.
//
// Unlike Sync, this does not go through the school_lessons mirror at all. It
// re-fetches the week's timetable live, because it logs in anyway and because
// a review that quoted a twelve-hour-old cache would be answering a different
// question than the one it is asked ("what happened this week", not "what was
// scheduled this morning").
//
// A failure to read one lesson is not a failure of the week: the review is
// worth sending with 28 lessons out of 29, and the count of what was missed
// goes into the message so a bad week is visible rather than silently short.
// A login failure is different — nothing was read, so nothing is written.
func (s *Service) CollectWeek(ctx context.Context, weekStart time.Time) ([]model.SchoolLessonDetail, int, error) {
	s.portal.Lock()
	defer s.portal.Unlock()

	if err := s.client.Login(ctx, s.cfg.Email, s.cfg.Password); err != nil {
		return nil, 0, err
	}

	from := StartOfWeek(weekStart)
	events, err := s.client.Timetable(ctx, s.cfg.PupilID, from)
	if err != nil {
		return nil, 0, err
	}

	var details []model.SchoolLessonDetail
	var skipped int
	for _, e := range events {
		if !isAcademicLesson(e) {
			continue
		}
		body, err := s.client.LessonView(ctx, e.EventID, e.Type)
		if err != nil {
			// The portal has no detail page for this slot after all — some
			// oddity Classify let through. Not a failure, and not worth
			// reporting to the family as a missed lesson.
			if errors.Is(err, ErrNotALesson) {
				continue
			}
			// A dead session will fail every remaining lesson the same way;
			// stopping keeps one expired cookie from reading as 29 outages.
			if errors.Is(err, ErrSessionExpired) || ctx.Err() != nil {
				return nil, skipped, err
			}
			s.log.Warn("schooltoday: lesson detail", "event", e.EventID, "err", err)
			skipped++
			continue
		}
		parsed, err := ParseLessonDetail(body)
		if err != nil {
			s.log.Warn("schooltoday: parse lesson detail", "event", e.EventID, "err", err)
			skipped++
			continue
		}
		details = append(details, s.toDetail(e, parsed))
	}

	if err := s.store.SaveLessonDetails(details); err != nil {
		return nil, skipped, err
	}

	s.log.Info("schooltoday: week collected", "pupil", s.cfg.PupilID,
		"week", from.Format("2006-01-02"), "lessons", len(details), "skipped", skipped)
	return details, skipped, nil
}

// isAcademicLesson reports whether an event is a school subject worth reading
// a detail page for.
//
// Both tests are needed. Classify answers on the subject text and is
// documented to call anything it does not recognise a lesson, so on its own it
// lets the odd assembly or homeroom slot through — each of which 404s on the
// detail endpoint. Type is the portal's own answer (1 for a lesson, 0 and 3
// for meals and after-school care) and is the cheaper, sharper filter; Classify
// still earns its place by removing the meals that share type 1's shape.
func isAcademicLesson(e Event) bool {
	if e.IsDeleted || e.IsCanceled || e.IsFullDay {
		return false
	}
	return e.Type == lessonEventType && Classify(e.Subject) == CategoryLesson
}

// lessonEventType is the portal's `type` for an academic lesson, and the value
// its detail endpoint expects back as lessonType.
const lessonEventType = 1

// toDetail joins the timetable event with its parsed detail page. Subject and
// start come from the event, not the page: that is the spelling the rest of
// the school code strips and classifies, and the page renders the subject
// without its group tag.
func (s *Service) toDetail(e Event, d LessonDetail) model.SchoolLessonDetail {
	startsAt := e.Start
	if t, err := time.ParseInLocation(portalTimeFormat, e.Start, s.loc); err == nil {
		startsAt = t.Format(model.LocalDatetime)
	}

	out := model.SchoolLessonDetail{
		EventID:  e.EventID,
		PupilID:  s.cfg.PupilID,
		StartsAt: startsAt,
		Subject:  e.Subject,
		Teacher:  d.Teacher,
		Topic:    d.Topic,
		Notes:    d.Notes,
		Homework: d.Homework,
	}
	for _, m := range d.Marks {
		out.Marks = append(out.Marks, model.SchoolMark{Kind: m.Kind, Value: m.Value})
	}
	for _, f := range d.Files {
		out.Files = append(out.Files, model.SchoolFile{Kind: f.Kind, URL: f.URL, Title: f.Title})
	}
	return out
}
