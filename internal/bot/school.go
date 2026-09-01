package bot

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/model"
	"familyhub/internal/schooltoday"
)

// The evening school digest: tomorrow's timetable, sent to the family group.
//
// This lives in the bot rather than in a Home Assistant template — unlike the
// appointment digests, which HA renders from the ICS feed — because HA cannot
// see what the message needs. Its calendar API hands a template only
// summary/start/end/description; the ICS CATEGORIES line never reaches Jinja.
// So the school/not-school split (schooltoday.Classify) and "free at HH:MM"
// would have to be re-derived by matching subject text inside the template,
// duplicating a classifier that already exists in Go with tests behind it.
// Same reasoning as the chore nag: HA reads a calendar, and this is not a
// calendar question.

// Ukrainian weekday and month names, in the genitive the date reads in
// ("1 вересня"). Weekdays are indexed Monday-first, matching the shift below.
var (
	schoolWeekdays = [7]string{"понеділок", "вівторок", "середа", "четвер",
		"пʼятниця", "субота", "неділя"}
	schoolMonths = [12]string{"січня", "лютого", "березня", "квітня", "травня",
		"червня", "липня", "серпня", "вересня", "жовтня", "листопада", "грудня"}
)

// mergeGap is the largest break between two same-subject slots that still
// reads as one stretch. The school's breaks are 10 minutes, so 15 merges the
// two consecutive after-school blocks into "14:10–15:40" without also merging
// across the long midday gap.
const mergeGap = 15 * time.Minute

// schoolDigestEnabled gates the digest on a configured time alone.
//
// Deliberately not behind NotificationsEnabled, for the same reason the chore
// nag is not: that flag is off in prod because HA sends the appointment
// summaries, and gating this on it would leave it permanently silent.
func (c Config) schoolDigestEnabled() bool {
	return c.SchoolDigestTime != ""
}

// sendSchoolDigest posts tomorrow's timetable. Errors are logged, never fatal:
// a digest that fails must not take the ticker down.
func (b *Bot) sendSchoolDigest(now time.Time) {
	day := startOfDay(now).AddDate(0, 0, 1)
	lessons, err := b.store.SchoolLessons(
		day.Format(model.LocalDatetime),
		day.AddDate(0, 0, 1).Format(model.LocalDatetime))
	if err != nil {
		b.logger.Error("bot: school digest query", "err", err)
		return
	}
	text, ok := schoolDigestText(day, lessons, b.cfg.Loc)
	if !ok {
		return
	}
	if _, err := b.sendToGroup(text, tele.ModeHTML); err != nil {
		b.logger.Error("bot: send school digest", "err", err)
	}
}

// slot is one rendered stretch of the day: a lesson, or a run of merged
// non-lesson blocks.
type slot struct {
	start, end time.Time
	subject    string
	topic      string
	cat        schooltoday.Category
}

// schoolDigestText renders the digest for day, or ok=false to send nothing.
//
// Nothing is sent when the day holds no slots at all — a weekend or a holiday,
// where silence is the right answer and matches the other digests. A day that
// holds slots but no lessons *is* sent, saying the timetable is not published
// yet: that case (the school fills in meals and after-school care before it
// fills in subjects) used to produce no message, which is indistinguishable
// from the integration being broken.
//
// All pupils in the window are rendered together. Only one is configured, and
// a second would want its own message rather than a merged list.
func schoolDigestText(day time.Time, lessons []model.SchoolLesson, loc *time.Location) (string, bool) {
	slots := toSlots(lessons, loc)
	if len(slots) == 0 {
		return "", false
	}

	var b strings.Builder
	fmt.Fprintf(&b, "📚 <b>Уроки на завтра — %s, %d %s</b>\n\n",
		schoolWeekdays[(int(day.Weekday())+6)%7], day.Day(), schoolMonths[day.Month()-1])

	if !anyLesson(slots) {
		b.WriteString("<i>Розклад уроків ще не опубліковано.</i>\n\n")
	}

	// Chronological throughout: a slot's place in the day is its place in the
	// message. The runs are separated by a blank line wherever the day crosses
	// between lessons and everything else, which groups them without a heading
	// — a heading would have to claim the breakfast comes after the afternoon,
	// or that the midday walk between two lessons is the end of the day.
	for i, s := range slots {
		if i > 0 && isLesson(slots[i-1]) != isLesson(s) {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s %s — %s\n",
			slotIcon(s), timeRange(s), html.EscapeString(s.subject))
		if s.topic != "" {
			fmt.Fprintf(&b, "     <i>%s</i>\n", html.EscapeString(s.topic))
		}
	}

	// The end of the last slot of the day, whatever kind it is — the answer to
	// "when do I drive over", which the lessons-only view got wrong by up to
	// two and a half hours.
	last := slots[len(slots)-1].end
	for _, s := range slots {
		if s.end.After(last) {
			last = s.end
		}
	}
	fmt.Fprintf(&b, "\n🏁 <b>Вільний у %s</b>", last.Format("15:04"))

	return b.String(), true
}

func isLesson(s slot) bool { return s.cat == schooltoday.CategoryLesson }

func anyLesson(slots []slot) bool {
	for _, s := range slots {
		if isLesson(s) {
			return true
		}
	}
	return false
}

// toSlots parses the stored wall-clock times, classifies each row, strips the
// portal's group tag, and merges adjacent same-subject runs. Rows whose times
// do not parse are dropped rather than rendered as a broken line.
func toSlots(lessons []model.SchoolLesson, loc *time.Location) []slot {
	var parsed []slot
	for _, l := range lessons {
		start, err := time.ParseInLocation(model.LocalDatetime, l.StartsAt, loc)
		if err != nil {
			continue
		}
		end, err := time.ParseInLocation(model.LocalDatetime, l.EndsAt, loc)
		if err != nil {
			continue
		}
		s := slot{
			start:   start,
			end:     end,
			subject: stripGroupTag(l.Subject),
			topic:   strings.TrimSpace(l.Topic),
			// Classified on the raw subject: Classify reads the subject text
			// and is documented to ignore the tag, so it does not need it
			// removed first.
			cat: schooltoday.Classify(l.Subject),
		}
		parsed = append(parsed, s)
	}

	// Sorted here rather than trusted from the caller: the message is built on
	// the assumption that a slot's order is its time, and so is the merge
	// below. The store already orders by start; this makes it this function's
	// own invariant instead of a second place that has to remember.
	sort.SliceStable(parsed, func(i, j int) bool {
		return parsed[i].start.Before(parsed[j].start)
	})

	var out []slot
	for _, s := range parsed {
		// Two consecutive blocks of the same thing are one stretch to a
		// parent. Only merged for non-lessons: two periods of the same subject
		// back to back are still two lessons, and a topic belongs to one of
		// them.
		if n := len(out); n > 0 && !isLesson(s) &&
			out[n-1].subject == s.subject && out[n-1].cat == s.cat &&
			!s.start.After(out[n-1].end.Add(mergeGap)) {
			out[n-1].end = s.end
			continue
		}
		out = append(out, s)
	}
	return out
}

// stripGroupTag removes the portal's trailing group marker — "Алгебра [9]",
// "Обід [Food Hub]" — which names the teaching group, not the subject, and is
// noise in a message to the family. Only a trailing bracket is touched, so a
// subject with brackets of its own mid-title keeps them.
func stripGroupTag(subject string) string {
	s := strings.TrimSpace(subject)
	if !strings.HasSuffix(s, "]") {
		return s
	}
	i := strings.LastIndex(s, " [")
	if i < 0 {
		return s
	}
	return strings.TrimSpace(s[:i])
}

func timeRange(s slot) string {
	return s.start.Format("15:04") + "–" + s.end.Format("15:04")
}

// slotIcon carries what a heading no longer does. With everything in one
// chronological run, the glyph is what separates a lesson from the day around
// it: the meals, the walk and the morning check-in are structure, and the
// after-school block is the one that decides when pickup is.
func slotIcon(s slot) string {
	switch s.cat {
	case schooltoday.CategoryMeal:
		return "🍽"
	case schooltoday.CategoryDaycare:
		return "🎒"
	case schooltoday.CategoryLesson:
		return "🕐"
	default:
		return "🚶"
	}
}
