package bot

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"lessons/internal/model"
)

// onReminderTap handles the evening-reminder buttons.
// Data: "<enrollment_id>:<YYYY-MM-DD>:<status>".
func (b *Bot) onReminderTap(c tele.Context) error {
	eid, date, status, err := parseReminderData(c.Data())
	if err != nil {
		_ = c.Respond(&tele.CallbackResponse{Text: "Неверные данные"})
		return nil
	}
	exists, err := b.store.VisitExistsForDate(eid, date)
	if err != nil {
		b.logger.Error("bot: visit-exists", "err", err)
		_ = c.Respond(&tele.CallbackResponse{Text: "Ошибка"})
		return nil
	}
	if exists {
		_ = c.Respond(&tele.CallbackResponse{Text: "Уже отмечено"})
		return c.Edit(reminderFinalText(c.Message().Text, "уже отмечено"), &tele.ReplyMarkup{})
	}
	if _, err := b.store.CreateVisit(eid, date, status, ""); err != nil {
		b.logger.Error("bot: create visit", "err", err)
		_ = c.Respond(&tele.CallbackResponse{Text: "Не удалось записать"})
		return nil
	}
	_ = c.Respond(&tele.CallbackResponse{Text: "Записано"})
	text := reminderFinalText(c.Message().Text, model.StatusLabels[status])
	if line := b.balanceLineFor(eid); line != "" {
		text += "\n" + line
	}
	return c.Edit(text, &tele.ReplyMarkup{})
}

// reminderFinalText turns the reminder question into its final state: the
// "— было?" tail is replaced with the chosen outcome so the answered message
// reads as a statement, not a question.
func reminderFinalText(question, outcome string) string {
	return strings.TrimSuffix(question, " — было?") + " — " + outcome
}

func parseReminderData(data string) (int64, string, string, error) {
	parts := strings.SplitN(data, ":", 3)
	if len(parts) != 3 {
		return 0, "", "", fmt.Errorf("bad reminder data %q", data)
	}
	eid, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, "", "", err
	}
	if _, err := model.ParseDate(parts[1]); err != nil {
		return 0, "", "", err
	}
	if _, ok := model.StatusLabels[parts[2]]; !ok {
		return 0, "", "", fmt.Errorf("bad status %q", parts[2])
	}
	return eid, parts[1], parts[2], nil
}

// cmdAdd starts an inline three-step entry flow: course → date → status.
func (b *Bot) cmdAdd(c tele.Context) error {
	courses, err := b.store.FrequentActiveEnrollments(8)
	if err != nil {
		return c.Send("Не удалось получить курсы.")
	}
	if len(courses) == 0 {
		return c.Send("Активных курсов нет.")
	}
	m := &tele.ReplyMarkup{}
	var rows []tele.Row
	for i := 0; i < len(courses); i += 2 {
		btn1 := courseBtn(m, courses[i])
		if i+1 < len(courses) {
			rows = append(rows, m.Row(btn1, courseBtn(m, courses[i+1])))
		} else {
			rows = append(rows, m.Row(btn1))
		}
	}
	rows = append(rows, m.Row(m.Data("Отмена", "add_cancel", "")))
	m.Inline(rows...)
	return c.Send("Какой курс?", m)
}

func courseBtn(m *tele.ReplyMarkup, e model.Enrollment) tele.Btn {
	label := e.Person + " · " + e.Name
	if e.Description != "" {
		label += " (" + e.Description + ")"
	}
	return m.Data(label, "add_course", strconv.FormatInt(e.ID, 10))
}

// onAddCourse: course picked → ask for date.
// Data: "<eid>".
func (b *Bot) onAddCourse(c tele.Context) error {
	eid, err := strconv.ParseInt(c.Data(), 10, 64)
	if err != nil {
		_ = c.Respond(&tele.CallbackResponse{Text: "Bad data"})
		return nil
	}
	e, err := b.store.GetEnrollment(eid)
	if err != nil {
		_ = c.Respond(&tele.CallbackResponse{Text: "Курс не найден"})
		return nil
	}

	now := time.Now()
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	before := now.AddDate(0, 0, -2).Format("2006-01-02")

	m := &tele.ReplyMarkup{}
	m.Inline(
		m.Row(
			m.Data("Сегодня", "add_date", strconv.FormatInt(eid, 10)+":"+today),
			m.Data("Вчера", "add_date", strconv.FormatInt(eid, 10)+":"+yesterday),
			m.Data("Позавчера", "add_date", strconv.FormatInt(eid, 10)+":"+before),
		),
		m.Row(m.Data("Отмена", "add_cancel", "")),
	)
	label := e.Name
	if e.Description != "" {
		label = e.Name + " (" + e.Description + ")"
	}
	_ = c.Respond(&tele.CallbackResponse{})
	return c.Edit(fmt.Sprintf("%s · %s\nКогда?", e.Person, label), m)
}

// onAddDate: date picked → ask for status.
// Data: "<eid>:<YYYY-MM-DD>".
func (b *Bot) onAddDate(c tele.Context) error {
	parts := strings.SplitN(c.Data(), ":", 2)
	if len(parts) != 2 {
		_ = c.Respond(&tele.CallbackResponse{Text: "Bad data"})
		return nil
	}
	eid, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		_ = c.Respond(&tele.CallbackResponse{Text: "Bad data"})
		return nil
	}
	date := parts[1]

	e, err := b.store.GetEnrollment(eid)
	if err != nil {
		_ = c.Respond(&tele.CallbackResponse{Text: "Курс не найден"})
		return nil
	}

	prefix := strconv.FormatInt(eid, 10) + ":" + date + ":"
	m := &tele.ReplyMarkup{}
	m.Inline(
		m.Row(
			m.Data("✓ Провели", "add_status", prefix+model.StatusDone),
			m.Data("→ Перенесли", "add_status", prefix+model.StatusRescheduled),
		),
		m.Row(
			m.Data("✗ Отменили", "add_status", prefix+model.StatusCancelled),
			m.Data("⤵ Пропустили", "add_status", prefix+model.StatusSkipped),
		),
		m.Row(m.Data("Отмена", "add_cancel", "")),
	)
	label := e.Name
	if e.Description != "" {
		label = e.Name + " (" + e.Description + ")"
	}
	_ = c.Respond(&tele.CallbackResponse{})
	return c.Edit(fmt.Sprintf("%s · %s\n%s — как прошло?", e.Person, label, dateRu(date)), m)
}

// onAddStatus: status picked → create visit.
// Data: "<eid>:<YYYY-MM-DD>:<status>".
func (b *Bot) onAddStatus(c tele.Context) error {
	eid, date, status, err := parseReminderData(c.Data())
	if err != nil {
		_ = c.Respond(&tele.CallbackResponse{Text: "Bad data"})
		return nil
	}
	e, err := b.store.GetEnrollment(eid)
	if err != nil {
		_ = c.Respond(&tele.CallbackResponse{Text: "Курс не найден"})
		return nil
	}
	if exists, _ := b.store.VisitExistsForDate(eid, date); exists {
		_ = c.Respond(&tele.CallbackResponse{Text: "Уже есть запись на эту дату"})
		return c.Edit("Запись на эту дату уже есть.", &tele.ReplyMarkup{})
	}
	if _, err := b.store.CreateVisit(eid, date, status, ""); err != nil {
		b.logger.Error("bot: create visit", "err", err)
		_ = c.Respond(&tele.CallbackResponse{Text: "Не удалось"})
		return nil
	}
	_ = c.Respond(&tele.CallbackResponse{Text: "Записано"})
	label := e.Name
	if e.Description != "" {
		label = e.Name + " (" + e.Description + ")"
	}
	text := fmt.Sprintf("Записано: %s · %s · %s · %s",
		e.Person, label, dateRu(date), model.StatusLabels[status])
	if line := b.balanceLineFor(eid); line != "" {
		text += "\n" + line
	}
	return c.Edit(text, &tele.ReplyMarkup{})
}

func (b *Bot) onAddCancel(c tele.Context) error {
	_ = c.Respond(&tele.CallbackResponse{})
	return c.Edit("Отменено.", &tele.ReplyMarkup{})
}

func dateRu(s string) string {
	t, err := model.ParseDate(s)
	if err != nil {
		return s
	}
	return t.Format("02.01.2006")
}
