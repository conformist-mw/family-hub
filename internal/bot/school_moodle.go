package bot

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/audit"
	"familyhub/internal/model"
	"familyhub/internal/moodle"
)

// The school's Moodle in the family chat: new grades as the sync finds them,
// /grades for the whole gradebook, and the Friday review of the school week.
// The formatters below are pure — the sync and the store hand them data, the
// handlers send what they return. Every line closes its own tags, so
// NotifyHTML and audit.SplitMessage may cut the text between any two lines.

// NotifyNewGrades posts the grades one sync found to the family group. It is
// the sync's notify callback, so it has nobody to return an error to: a failed
// send is logged, and those grades stay visible in /grades.
func (b *Bot) NotifyNewGrades(grades []model.SchoolGrade) {
	text := formatNewGrades(grades)
	if text == "" {
		return
	}
	if err := b.NotifyHTML(text); err != nil {
		b.logger.Error("bot: notify new grades", "err", err, "grades", len(grades))
	}
}

// schoolReviewTimeout bounds the whole review: it may first wait out a sync
// holding the site, then reads two pages per course from Moodle, each of
// which the client gives up on after 30 seconds.
const schoolReviewTimeout = 5 * time.Minute

// sendSchoolReview posts the review of the school week containing now. Like
// the dish suggestions it runs off the ticker's goroutine, so a slow Moodle
// does not hold back the other clocks, and one run at a time. A failed read is
// logged and the review skipped: half a review would read as a quiet week.
func (b *Bot) sendSchoolReview(ctx context.Context, now time.Time) {
	// RunDigests only calls this when schoolReviewEnabled, which requires the
	// service; the check stays because a nil one reached from that goroutine
	// would take the whole server down.
	svc := b.cfg.Moodle
	if svc == nil {
		return
	}
	if !b.schoolReviewRunning.CompareAndSwap(false, true) {
		b.logger.Warn("bot: school review still running, skipping this one")
		return
	}
	go func() {
		defer b.schoolReviewRunning.Store(false)

		ctx, cancel := context.WithTimeout(ctx, schoolReviewTimeout)
		defer cancel()

		week := svc.Week(now)
		progress, err := svc.WeekProgress(ctx, week)
		if err != nil {
			b.logger.Error("bot: school review", "week", week.N, "err", err)
			return
		}
		if err := b.NotifyHTML(formatWeekReview(week, progress)); err != nil {
			b.logger.Error("bot: send school review", "week", week.N, "err", err)
		}
	}()
}

// cmdGrades answers /grades from the store, never from Moodle: it is what
// the last sync saw, and it answers even while Moodle is down.
func (b *Bot) cmdGrades(c tele.Context) error {
	chunks, err := b.gradesChunks()
	if err != nil {
		b.logger.Error("bot: grades summary", "err", err)
		return c.Send("Не вдалося прочитати оцінки.")
	}
	for _, chunk := range chunks {
		if err := c.Send(chunk, tele.ModeHTML); err != nil {
			return err
		}
	}
	return nil
}

// gradesChunks is the /grades reply split into sendable pieces: twenty
// subjects with a term of grades each outgrow one Telegram message.
func (b *Bot) gradesChunks() ([]string, error) {
	courses, err := b.store.SchoolSummary()
	if err != nil {
		return nil, err
	}
	return audit.SplitMessage(formatGradesSummary(courses), messageLimit), nil
}

// lowAverage marks a subject whose average is below it with ⚠️.
const lowAverage = 4.0

// formatNewGrades lists the grades one sync found, one line each:
// "<b>Алгебра</b>: ОВ 12 — 2,2 (сер. 5,5)". No grades, no message ("").
func formatNewGrades(grades []model.SchoolGrade) string {
	if len(grades) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("🆕 Нові оцінки")
	for _, g := range grades {
		fmt.Fprintf(&sb, "\n<b>%s</b>: %s", html.EscapeString(g.CourseName), gradeEntry(g))
		if avg := tidyGrade(g.CourseAverage); avg != "" {
			fmt.Fprintf(&sb, " (сер. %s)", html.EscapeString(avg))
		}
	}
	return sb.String()
}

// formatGradesSummary is the /grades answer: every subject that has grades,
// in Moodle's order, with its average and its grades in the order they were
// given; the subjects without any are only counted at the bottom.
func formatGradesSummary(courses []model.SchoolCourse) string {
	if len(courses) == 0 {
		return "📚 Оцінок поки немає — Moodle ще не синхронізувався."
	}
	var sb strings.Builder
	sb.WriteString("📚 Оцінки")
	empty := 0
	for _, c := range courses {
		if len(c.Grades) == 0 && c.Average == nil {
			empty++
			continue
		}
		fmt.Fprintf(&sb, "\n<b>%s</b>", html.EscapeString(c.Name))
		if avg := averageText(c.Average, c.AverageText); avg != "" {
			fmt.Fprintf(&sb, " — %s", avg)
		}
		if len(c.Grades) > 0 {
			entries := make([]string, len(c.Grades))
			for i, g := range c.Grades {
				entries[i] = compactGradeEntry(g)
			}
			fmt.Fprintf(&sb, "\n   <i>%s</i>", strings.Join(entries, " · "))
		}
	}
	if empty > 0 {
		fmt.Fprintf(&sb, "\n<i>Без оцінок: %s</i>", model.Plural(empty, "предмет", "предмети", "предметів"))
	}
	return sb.String()
}

// formatWeekReview is the Friday review: per subject, how much of the week's
// section is done, the grades of the week and the average. A subject with
// nothing to track this week and no grade in it is left out; one whose
// progress could not be read is always shown, with its grades.
//
// A course without a "Тиждень N" section usually just has no weekly layout,
// so it is noted only on a subject that is shown anyway. When no readable
// course has the section, the week number itself is off (holidays moved the
// anchor), and that gets one line instead of twenty.
func formatWeekReview(week moodle.Week, progress []moodle.CourseProgress) string {
	var sb strings.Builder
	last := week.End.AddDate(0, 0, -1)
	fmt.Fprintf(&sb, "📅 Тиждень %d (%s–%s)", week.N, week.Start.Format("02.01"), last.Format("02.01"))

	readable, missing := 0, 0
	for _, p := range progress {
		if !p.Unavailable {
			readable++
			if p.NoSection {
				missing++
			}
		}
	}
	noWeek := readable > 0 && missing == readable
	if noWeek {
		fmt.Fprintf(&sb, "\n<i>Секції «Тиждень %d» немає в жодному предметі.</i>", week.N)
	}

	shown := 0
	for _, p := range progress {
		if !p.Unavailable && p.Total == 0 && len(p.Grades) == 0 {
			continue
		}
		shown++
		var parts []string
		switch {
		case p.Unavailable:
			parts = append(parts, "прогрес недоступний")
		case p.NoSection:
			if !noWeek {
				parts = append(parts, fmt.Sprintf("секції «Тиждень %d» немає", week.N))
			}
		case p.Total > 0:
			parts = append(parts, fmt.Sprintf("пройдено %d/%d", p.Done, p.Total))
		}
		if avg := averageText(p.Average, p.AverageText); avg != "" {
			parts = append(parts, "сер. "+avg)
		}
		fmt.Fprintf(&sb, "\n<b>%s</b>", html.EscapeString(p.Name))
		if len(parts) > 0 {
			sb.WriteString(" — " + strings.Join(parts, " · "))
		}
		if len(p.Grades) > 0 {
			entries := make([]string, len(p.Grades))
			for i, g := range p.Grades {
				entries[i] = gradeEntry(g)
			}
			fmt.Fprintf(&sb, "\n   <i>%s</i>", strings.Join(entries, " · "))
		}
	}
	if shown == 0 && !noWeek {
		sb.WriteString("\nЗа тиждень ні завдань, ні оцінок.")
	}
	return sb.String()
}

// gradeEntry names a grade with its item: "ОВ 12 — 7,8" (escaped HTML).
func gradeEntry(g model.SchoolGrade) string {
	return html.EscapeString(model.ShortItemName(g.Name) + " — " + gradeValue(g))
}

// numberedAbbrev is an abbreviated item that is nothing but its number
// ("ОВ 12", "КР 3").
var numberedAbbrev = regexp.MustCompile(`^(ОВ|КР)(\s+\d+)?$`)

// compactGradeEntry is a grade in the /grades list, where the items follow
// in order: the teacher's and test grades drop their running number ("ОВ
// 7,8", "КР 10"); any other item keeps its name, set off by a dash so a
// number in it is not read as the grade ("Тест 3 — 9").
func compactGradeEntry(g model.SchoolGrade) string {
	name := model.ShortItemName(g.Name)
	if m := numberedAbbrev.FindStringSubmatch(name); m != nil {
		return html.EscapeString(m[1] + " " + gradeValue(g))
	}
	return gradeEntry(g)
}

// gradeValue is Moodle's rendering of the grade, tidied; the raw value when
// Moodle gave none.
func gradeValue(g model.SchoolGrade) string {
	if s := tidyGrade(g.GradeText); s != "" {
		return s
	}
	return model.FormatGrade(g.GradeRaw)
}

// averageText renders a course average with ⚠️ when it is low (judged by the
// number, not the text). "" when the course has no average.
func averageText(avg *float64, text string) string {
	s := html.EscapeString(tidyGrade(text))
	if s == "" && avg != nil {
		s = model.FormatGrade(*avg)
	}
	if s == "" {
		return ""
	}
	if avg != nil && *avg < lowAverage {
		s += " ⚠️"
	}
	return s
}

var decimalGrade = regexp.MustCompile(`^\d+,\d+$`)

// tidyGrade drops the zeros Moodle pads decimals with ("10,0" → "10",
// "7,80" → "7,8"); "-" (no grade) becomes "". Anything that is not a plain
// decimal (a letter grade, a percentage) is left as Moodle wrote it.
func tidyGrade(text string) string {
	text = strings.TrimSpace(text)
	if text == "-" {
		return ""
	}
	if decimalGrade.MatchString(text) {
		text = strings.TrimRight(text, "0")
		text = strings.TrimSuffix(text, ",")
	}
	return text
}
