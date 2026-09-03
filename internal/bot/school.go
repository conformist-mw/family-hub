package bot

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/audit"
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

// schoolWeekReviewEnabled gates the Friday review. It needs a Service as well
// as a time, unlike the evening digest: the digest reads the mirror the syncer
// already fills, while the review goes to the portal itself, so without portal
// credentials there is nothing to send rather than an empty message.
func (c Config) schoolWeekReviewEnabled() bool {
	return c.SchoolWeekReviewTime != "" && c.SchoolWeekReviewDOW >= 0 && c.School != nil
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

// The Friday review of the school week just gone: per subject, what was
// covered, what was set as homework and what was graded.
//
// Grouped by subject rather than played back chronologically. A parent reading
// on Friday evening is asking "how did the week go in maths", not "what
// happened at 09:50 on Tuesday" — the chronology is what the evening digest is
// for, and it answers a different question.

// skippedUnknown is the skipped-lesson count for a review rendered from
// storage rather than from a fresh collect. /schoolweek reads rows, not the
// portal, and cannot know how many lessons the original Friday run failed to
// read — so it says nothing rather than implying none were missed.
const skippedUnknown = -1

// subjectRun is one subject's whole week, in the order its lessons happened.
type subjectRun struct {
	subject string
	lessons []model.SchoolLessonDetail
}

// schoolWeekReviewText renders the review of the week starting at weekStart,
// or ok=false to send nothing.
//
// Nothing is sent for a week with no lessons at all — the holidays, a week off
// — matching the evening digest, where silence is the honest answer and a
// message saying "nothing happened" is noise.
func schoolWeekReviewText(weekStart time.Time, details []model.SchoolLessonDetail,
	skipped int, loc *time.Location) (string, bool) {
	runs := groupBySubject(details, loc)
	if len(runs) == 0 {
		return "", false
	}

	var b strings.Builder
	end := weekStart.AddDate(0, 0, 4) // Monday–Friday; the school week has no weekend
	fmt.Fprintf(&b, "📚 <b>Тиждень %d %s – %d %s</b>\n",
		weekStart.Day(), schoolMonths[weekStart.Month()-1],
		end.Day(), schoolMonths[end.Month()-1])

	for _, run := range runs {
		b.WriteString("\n")
		fmt.Fprintf(&b, "<b>%s</b>%s%s\n",
			html.EscapeString(run.subject), lessonCount(len(run.lessons)), marksSummary(run))

		var wrote bool
		for _, l := range run.lessons {
			if topic := strings.TrimSpace(l.Topic); topic != "" {
				fmt.Fprintf(&b, "• %s\n", html.EscapeString(topic))
				wrote = true
			}
			if notes := strings.TrimSpace(l.Notes); notes != "" {
				fmt.Fprintf(&b, "  <i>%s</i>\n", html.EscapeString(notes))
				wrote = true
			}
			if hw := strings.TrimSpace(l.Homework); hw != "" {
				fmt.Fprintf(&b, "  📕 %s\n", html.EscapeString(hw))
				wrote = true
			}
		}
		// A subject that met but has nothing written up is worth a line of its
		// own: "the teacher wrote nothing" and "the collector missed it" look
		// identical otherwise.
		if !wrote {
			b.WriteString("  <i>без записів</i>\n")
		}
	}

	if skipped > 0 {
		fmt.Fprintf(&b, "\n⚠️ Не вдалося прочитати уроків: %d", skipped)
	}
	return strings.TrimRight(b.String(), "\n"), true
}

// groupBySubject collapses the week into one run per subject, ordered by when
// the subject first met and, within a subject, chronologically.
//
// Sorted here rather than trusted from the store: the renderer's output order
// is a promise to the reader, and making it this function's own invariant
// keeps it from depending on which query happened to supply the rows.
func groupBySubject(details []model.SchoolLessonDetail, loc *time.Location) []subjectRun {
	type bucket struct {
		run   subjectRun
		first time.Time
	}
	order := map[string]*bucket{}
	var subjects []string

	for _, d := range details {
		start, err := time.ParseInLocation(model.LocalDatetime, d.StartsAt, loc)
		if err != nil {
			// A row whose start will not parse cannot be placed in the week;
			// dropping it beats rendering a lesson under the wrong subject.
			continue
		}
		name := stripGroupTag(d.Subject)
		b, ok := order[name]
		if !ok {
			b = &bucket{run: subjectRun{subject: name}, first: start}
			order[name] = b
			subjects = append(subjects, name)
		}
		if start.Before(b.first) {
			b.first = start
		}
		b.run.lessons = append(b.run.lessons, d)
	}

	sort.SliceStable(subjects, func(i, j int) bool {
		return order[subjects[i]].first.Before(order[subjects[j]].first)
	})

	out := make([]subjectRun, 0, len(subjects))
	for _, name := range subjects {
		run := order[name].run
		sort.SliceStable(run.lessons, func(i, j int) bool {
			return run.lessons[i].StartsAt < run.lessons[j].StartsAt
		})
		out = append(out, run)
	}
	return out
}

// lessonCount renders "· 3 уроки" with the Ukrainian plural the count needs.
func lessonCount(n int) string {
	return fmt.Sprintf(" · %d %s", n, pluralLessons(n))
}

// pluralLessons picks урок/уроки/уроків. Ukrainian needs three forms and the
// teens are the exception that a naive last-digit rule gets wrong.
func pluralLessons(n int) string {
	if n%100 >= 11 && n%100 <= 14 {
		return "уроків"
	}
	switch n % 10 {
	case 1:
		return "урок"
	case 2, 3, 4:
		return "уроки"
	default:
		return "уроків"
	}
}

// marksSummary lists the week's marks for one subject next to its heading —
// the line a parent scans for first. Values are the portal's own ("9,00"),
// trimmed of a trailing ",00" because a whole grade reads as a grade.
func marksSummary(run subjectRun) string {
	var values []string
	for _, l := range run.lessons {
		for _, m := range l.Marks {
			values = append(values, html.EscapeString(tidyMark(m.Value)))
		}
	}
	if len(values) == 0 {
		return ""
	}
	return " · " + strings.Join(values, ", ")
}

// tidyMark drops the portal's decimal padding: "9,00" is a nine. A mark with a
// real fraction ("9,50") keeps it.
func tidyMark(value string) string {
	return strings.TrimSuffix(strings.TrimSpace(value), ",00")
}

// reviewCollectTimeout bounds one Friday collect. A week is ~29 sequential
// requests against a portal whose client allows 30s each, so the worst case
// without a bound is a quarter of an hour; ten minutes is well past the
// observed time and still short enough that a stuck run gives up rather than
// overlapping next week's.
const reviewCollectTimeout = 10 * time.Minute

// sendSchoolWeekReview posts the review of the week containing now.
//
// The collect runs in its own goroutine, not on the caller's. RunDigests ticks
// once a minute and does its work inline, and time.Ticker drops ticks while
// the receiver is busy — so a slow Friday collect on the ticker's goroutine
// would swallow whatever else was due in those minutes, the evening chore nag
// among them. Errors are logged and nothing is sent: half a review is worse
// than none, because a short one reads as a quiet week.
func (b *Bot) sendSchoolWeekReview(ctx context.Context, now time.Time) {
	if b.cfg.School == nil {
		return
	}
	// A previous run still going means the portal is crawling; starting a
	// second collect would only queue behind it on the service mutex and post
	// the same week twice.
	if !b.reviewRunning.CompareAndSwap(false, true) {
		b.logger.Warn("bot: school week review still running, skipping this one")
		return
	}

	weekStart := schooltoday.StartOfWeek(now.In(b.cfg.Loc))
	go func() {
		defer b.reviewRunning.Store(false)

		ctx, cancel := context.WithTimeout(ctx, reviewCollectTimeout)
		defer cancel()

		details, skipped, err := b.cfg.School.CollectWeek(ctx, weekStart)
		if err != nil {
			b.logger.Error("bot: school week review collect", "err", err)
			return
		}
		text, ok := schoolWeekReviewText(weekStart, details, skipped, b.cfg.Loc)
		if !ok {
			return
		}
		if err := b.notify(text, tele.ModeHTML); err != nil {
			b.logger.Error("bot: send school week review", "err", err)
		}
	}()
}

// maxWeeksBack bounds how far /schoolweek will look. Records only start when
// the feature was deployed, so a request for the hundredth week back is a typo
// rather than an intention, and answering it with a scan and a shrug is fine —
// but a bound keeps a mistyped year out of the store's index.
const maxWeeksBack = 52

// cmdSchoolWeek replays a stored week: /schoolweek for the current one,
// /schoolweek 2 for a fortnight ago.
//
// It reads rows and never touches the portal. That is what makes it useful
// after the Telegram message has scrolled away — and what makes it honest
// about what it does not know: the count of lessons the original Friday
// collect failed to read is not stored, so the reply omits that line entirely
// rather than implying nothing was missed.
func (b *Bot) cmdSchoolWeek(c tele.Context) error {
	back, ok := parseWeeksBack(c.Message().Payload)
	if !ok {
		return c.Send("Вкажіть, скільки тижнів тому: /schoolweek 2")
	}

	chunks, err := b.schoolWeekChunks(b.now(), back)
	if err != nil {
		b.logger.Error("bot: school week query", "err", err)
		return c.Send("Не вдалося прочитати записи за той тиждень.")
	}
	for _, chunk := range chunks {
		if err := c.Send(chunk, tele.ModeHTML); err != nil {
			return err
		}
	}
	return nil
}

// schoolWeekChunks builds the reply for a week `back` weeks ago, already split
// into sendable pieces.
//
// Separate from cmdSchoolWeek so the week maths, the empty case and the split
// are reachable from a test without standing up a telebot Context — the same
// reason dueThisMinute lives apart from RunDigests. now is a parameter for the
// same reason: Bot.now() reads the wall clock, and "which week is this" is
// exactly what a test needs to pin.
func (b *Bot) schoolWeekChunks(now time.Time, back int) ([]string, error) {
	weekStart := schooltoday.StartOfWeek(now).AddDate(0, 0, -7*back)
	details, err := b.store.LessonDetails(
		weekStart.Format(model.LocalDatetime),
		weekStart.AddDate(0, 0, 7).Format(model.LocalDatetime))
	if err != nil {
		return nil, err
	}

	text, rendered := schoolWeekReviewText(weekStart, details, skippedUnknown, b.cfg.Loc)
	if !rendered {
		return []string{fmt.Sprintf("За тиждень з %d %s записів немає.",
			weekStart.Day(), schoolMonths[weekStart.Month()-1])}, nil
	}
	// Split for the same reason the Friday push is: a full week of fourteen
	// subjects is several times Telegram’s 4096-byte cap, and one Send would
	// simply fail on it.
	return audit.SplitMessage(text, 4000), nil
}

// parseWeeksBack reads the command argument: empty means this week, a
// non-negative number means that many weeks back. Anything else is a typo, and
// answering a typo with "this week" would quietly show the wrong data.
func parseWeeksBack(payload string) (int, bool) {
	arg := strings.TrimSpace(payload)
	if arg == "" {
		return 0, true
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 0 || n > maxWeeksBack {
		return 0, false
	}
	return n, true
}
