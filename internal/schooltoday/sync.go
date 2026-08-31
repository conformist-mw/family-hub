package schooltoday

import (
	"context"
	"log/slog"
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
	if err := s.client.Login(ctx, s.cfg.Email, s.cfg.Password); err != nil {
		return err
	}

	from := startOfWeek(s.now())
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
	topic := ""
	if e.Topic != nil {
		topic = *e.Topic
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

// startOfWeek returns Monday 00:00 of t's week, in t's location. The portal
// weeks run Monday–Sunday, so the replace window aligns to that boundary and a
// week is never half-fetched.
func startOfWeek(t time.Time) time.Time {
	// Go's Weekday has Sunday=0; shift so Monday=0.
	offset := (int(t.Weekday()) + 6) % 7
	d := t.AddDate(0, 0, -offset)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, t.Location())
}
