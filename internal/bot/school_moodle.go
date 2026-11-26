package bot

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"time"

	tele "gopkg.in/telebot.v4"

	"familyhub/internal/model"
	"familyhub/internal/moodle"
)

// The school's Moodle in the family chat: new grades as the sync finds them,
// /grades for the whole gradebook, and the Friday review of the school week.
// The formatters below are pure — the sync and the store hand them data, the
// handlers send what they return. In the plain-HTML ones every line closes its
// own tags, so NotifyHTML may cut the text between any two lines; /grades is
// one rich message and is never cut.

// NotifyNewGrades posts the grades one sync found to the family group, as a
// rich-message table. It is the sync's notify callback, so it has nobody to
// return an error to: a failed send is logged, and those grades stay visible
// in /grades.
func (b *Bot) NotifyNewGrades(grades []model.SchoolGrade) {
	text := formatNewGrades(grades)
	if text == "" {
		return
	}
	if _, err := b.sendToGroup(&tele.InputRichMessage{HTML: text}); err != nil {
		b.logger.Error("bot: notify new grades", "err", err, "grades", len(grades))
	}
}

// sendSchoolReview posts the review of the school week containing now. Like
// the dish suggestions it runs off the ticker's goroutine, so a slow Moodle
// does not hold back the other clocks, and one run at a time. A failed read is
// logged and the review skipped: half a review would read as a quiet week.
// The service bounds how long the reads may take; ctx is only the server's.
//
// A week before the anchor is the summer before the school year: there is no
// school week to review, so nothing is posted.
func (b *Bot) sendSchoolReview(ctx context.Context, now time.Time) {
	// RunDigests only calls this when schoolReviewEnabled, which requires the
	// service; the check stays because a nil one reached from that goroutine
	// would take the whole server down.
	svc := b.cfg.Moodle
	if svc == nil {
		return
	}
	week := svc.Week(now)
	if week.N < 0 {
		b.logger.Info("bot: school review skipped, the school year has not started", "week", week.N)
		return
	}
	if !b.schoolReviewRunning.CompareAndSwap(false, true) {
		b.logger.Warn("bot: school review still running, skipping this one")
		return
	}
	go func() {
		defer b.schoolReviewRunning.Store(false)

		report, err := svc.WeekProgress(ctx, week)
		if err != nil {
			b.logger.Error("bot: school review", "week", week.N, "err", err)
			return
		}
		if err := b.NotifyHTML(formatWeekReview(week, report)); err != nil {
			b.logger.Error("bot: send school review", "week", week.N, "err", err)
		}
	}()
}

// cmdGrades answers /grades from the store, never from Moodle: it is what
// the last sync saw, and it answers even while Moodle is down. The answer is a
// rich message: tables exist nowhere else in Telegram.
func (b *Bot) cmdGrades(c tele.Context) error {
	courses, err := b.store.SchoolSummary()
	if err != nil {
		b.logger.Error("bot: grades summary", "err", err)
		return c.Send("Не вдалося прочитати оцінки.")
	}
	if len(courses) == 0 {
		return c.Send("📚 Оцінок поки немає — Moodle ще не синхронізувався.")
	}
	return c.Send(&tele.InputRichMessage{HTML: formatGradesTable(courses, b.now())})
}

// lowAverage marks a subject whose average is below it with ⚠️.
const lowAverage = 4.0

// formatNewGrades is the Rich HTML table of the grades one sync found: per
// grade the subject with its level, the grade, and the subject's average as it
// moved («7,5 → 6,3»). What kind of grade it is goes under the table when all
// are of one kind, else beside each grade — never the item's running number,
// which read as a second grade. No grades, no message ("").
func formatNewGrades(grades []model.SchoolGrade) string {
	if len(grades) == 0 {
		return ""
	}
	kinds := map[string]bool{}
	for _, g := range grades {
		kinds[gradeKind(g)] = true
	}
	mixed := len(kinds) > 1

	var sb strings.Builder
	if len(grades) == 1 {
		sb.WriteString("<h4>🆕 Нова оцінка</h4>")
	} else {
		fmt.Fprintf(&sb, "<h4>🆕 Нові оцінки · %d</h4>", len(grades))
	}
	sb.WriteString(`<table compact><tr><th>Предмет</th><th align="center">Оцінка</th><th align="center">Середній</th></tr>`)
	for _, g := range grades {
		grade := "<b>" + html.EscapeString(tidyGrade(g.GradeText)) + "</b>"
		if mixed {
			grade += " <i>" + html.EscapeString(shortKind(g)) + "</i>"
		}
		fmt.Fprintf(&sb, `<tr><td>%s</td><td align="center">%s</td><td align="center">%s</td></tr>`,
			strings.TrimSpace(gradeLevel(g.CourseAverageNum)+" "+html.EscapeString(g.CourseName)),
			grade, averageMove(g.CoursePrevAverage, g.CourseAverage))
	}
	sb.WriteString("</table>")
	if !mixed {
		fmt.Fprintf(&sb, "<footer>%s</footer>", html.EscapeString(gradeKind(grades[0])))
	}
	return sb.String()
}

// averageMove is a subject's average across one sync: «7,5 → 6,3» when it
// moved, just the new one when it did not or there was none before.
func averageMove(was, now string) string {
	was, now = html.EscapeString(tidyGrade(was)), html.EscapeString(tidyGrade(now))
	if was == "" || was == now {
		return now
	}
	return was + " → " + now
}

// shortKind is a grade item without its running number: "ОВ", "КР", or the
// item's own (shortened) name for anything else ("Тест").
func shortKind(g model.SchoolGrade) string {
	if m := numberedItem.FindStringSubmatch(model.ShortItemName(g.Name)); m != nil {
		return m[1]
	}
	return model.ShortItemName(g.Name)
}

// gradeKind spells shortKind out for the line under the table.
func gradeKind(g model.SchoolGrade) string {
	switch k := shortKind(g); k {
	case "ОВ":
		return "Оцінка вчителя"
	case "КР":
		return "Контрольна робота"
	default:
		return k
	}
}

// numberedItem is a grade item that is a kind plus its running number
// ("ОВ 12", "Тест 3").
var numberedItem = regexp.MustCompile(`^(.+?)\s+\d+$`)

// formatGradesTable is the /grades answer as a Rich HTML table: one row per
// subject that has grades, the weakest average first, with its level, its
// average and its grades in the order they were given, tests in bold. Courses
// without a grade or an average are only counted under the table.
func formatGradesTable(courses []model.SchoolCourse, now time.Time) string {
	var shown []model.SchoolCourse
	empty := 0
	for _, c := range courses {
		if len(c.Grades) == 0 && c.Average == nil {
			empty++
			continue
		}
		shown = append(shown, c)
	}
	// Weakest first; a subject with grades but no average yet goes last.
	// Stable, so equal averages keep Moodle's order.
	sort.SliceStable(shown, func(i, j int) bool {
		a, b := shown[i].Average, shown[j].Average
		if a == nil || b == nil {
			return a != nil && b == nil
		}
		return *a < *b
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "<h3>📚 Оцінки · %s</h3>", now.Format("02.01"))
	if len(shown) > 0 {
		sb.WriteString(`<table striped compact><tr><th>Предмет</th><th align="center">Сер.</th><th>Оцінки</th></tr>`)
		for _, c := range shown {
			grades := make([]string, len(c.Grades))
			for i, g := range c.Grades {
				grades[i] = tableGrade(g)
			}
			avg := html.EscapeString(tidyGrade(c.AverageText))
			if avg == "" && c.Average != nil {
				avg = model.FormatGrade(*c.Average)
			}
			fmt.Fprintf(&sb, `<tr><td>%s</td><td align="center"><b>%s</b></td><td>%s</td></tr>`,
				strings.TrimSpace(gradeLevel(c.Average)+" "+html.EscapeString(c.Name)), avg, strings.Join(grades, " · "))
		}
		sb.WriteString("</table>")
	}
	foot := "Жирним — контрольні."
	if empty > 0 {
		foot += fmt.Sprintf(" Ще %s без оцінок.", model.Plural(empty, "предмет", "предмети", "предметів"))
	}
	fmt.Fprintf(&sb, "<footer>%s</footer>", foot)
	return sb.String()
}

// gradeLevel is the level of an average on the school's 12-point scale:
// 🔴 below 4 (початковий), 🟡 4–6 (середній), 🟢 7 and up (достатній,
// високий). "" while there is no average.
func gradeLevel(avg *float64) string {
	switch {
	case avg == nil:
		return ""
	case *avg < lowAverage:
		return "🔴"
	case *avg < 7:
		return "🟡"
	default:
		return "🟢"
	}
}

// tableGrade is one grade in the /grades table: just the grade, in bold for
// a test ("КР …"); the column already says what the numbers are.
func tableGrade(g model.SchoolGrade) string {
	s := html.EscapeString(tidyGrade(g.GradeText))
	if strings.HasPrefix(model.ShortItemName(g.Name), "КР") {
		return "<b>" + s + "</b>"
	}
	return s
}

// formatWeekReview is the Friday review: per subject, how many of the week's
// quizzes are graded, the grades of the week and the average. A subject with
// no quiz this week and no grade in it is left out; one whose progress could
// not be read is always shown, with its grades.
//
// A course with no "Тиждень N" sections at all (only term tests) has no week
// to report on and shows just its grades. A weekly course missing this week's
// section is noted only on a subject that is shown anyway. When no readable
// weekly course has the section, the week number itself is off (holidays
// moved the anchor), and that gets one line instead of twenty.
//
// Before the first sync there is nothing to review, and the message says so
// rather than reporting an empty week. Grades from a sync that has been
// failing for long carry a warning: the week may be missing some.
func formatWeekReview(week moodle.Week, report moodle.WeekReport) string {
	var sb strings.Builder
	last := week.End.AddDate(0, 0, -1)
	fmt.Fprintf(&sb, "📅 Тиждень %d (%s–%s)", week.N, week.Start.Format("02.01"), last.Format("02.01"))
	if len(report.Courses) == 0 {
		sb.WriteString("\nMoodle ще не синхронізувався — огляду немає.")
		return sb.String()
	}
	if report.Stale {
		fmt.Fprintf(&sb, "\n<i>⚠️ Moodle не синхронізувався з %s — оцінки тижня можуть бути неповні.</i>",
			report.SyncedAt.Format("02.01 15:04"))
	}
	progress := report.Courses

	readable, missing := 0, 0
	for _, p := range progress {
		if !p.Unavailable && !p.NotWeekly {
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
		if !p.Unavailable && p.QuizzesTotal == 0 && len(p.Grades) == 0 {
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
		case p.QuizzesTotal > 0:
			parts = append(parts, fmt.Sprintf("оцінено %d/%d тестів", p.QuizzesGraded, p.QuizzesTotal))
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
	return html.EscapeString(model.ShortItemName(g.Name) + " — " + tidyGrade(g.GradeText))
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
