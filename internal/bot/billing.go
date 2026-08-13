package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/model"
)

// RunBillingReminders warns that a paid period is about to run out, so the
// next month gets paid before it starts. It ticks hourly and blocks until ctx
// is done; meant to run in its own goroutine.
//
// Unlike the lesson scheduler this is not tied to a slot time: a school with a
// fixed monthly fee owes the same amount whether or not anyone showed up, so
// the trigger is the coverage boundary alone. How far ahead to warn is set per
// course, not here — see dueForBillingReminder.
func (b *Bot) RunBillingReminders(ctx context.Context) {
	if b.cfg.NotifyChat == 0 {
		b.logger.Info("bot: billing reminders disabled (no notify chat)")
		return
	}
	b.logger.Info("bot: billing reminders started")

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	// Check on start too: the warning window is a few days wide, and a deploy
	// landing inside it should not have to wait for the next hour.
	b.sendDueBillingReminders()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.sendDueBillingReminders()
		}
	}
}

// sendDueBillingReminders warns once per coverage period that is within the
// lead window and still the current one.
//
// Three consequences of keying on coverage rather than on the calendar, all
// wanted: nothing is sent over the summer, because coverage ended in May and
// there is no boundary approaching; an overdue period stops nagging once its
// last day passes, leaving the dashboard badge to carry the debt; and the very
// first payment of a course has to be entered by hand, since until something
// is covered there is no boundary to warn about.
func (b *Bot) sendDueBillingReminders() {
	balances, err := b.store.Balances()
	if err != nil {
		b.logger.Error("bot: billing balances", "err", err)
		return
	}
	for _, bal := range balances {
		if !dueForBillingReminder(bal) {
			continue
		}
		// Claim before sending: the row is the "already warned" mark, and
		// taking it first means a send that fails is not retried forever.
		claimed, err := b.store.ClaimBillingReminder(bal.ID, bal.CoversUntil)
		if err != nil {
			b.logger.Error("bot: claim billing reminder", "err", err, "eid", bal.ID)
			continue
		}
		if !claimed {
			continue
		}
		if _, err := b.b.Send(tele.ChatID(b.cfg.NotifyChat), billingReminderText(bal)); err != nil {
			b.logger.Error("bot: send billing reminder", "err", err, "eid", bal.ID)
		}
	}
}

// dueForBillingReminder reports whether this balance is inside its warning
// window. The window is a range, not a single day: an app that was down on the
// 25th must still warn on the 26th rather than skip the month.
//
// The lead is the course's own payment_notice_min, which also turns the
// dashboard badge yellow (Balance.NoticeDue) — one number on the course form
// drives both, so the badge and the message cannot disagree about when it is
// time to pay. Zero means no warning at all.
//
// Coverage must exist right now. Without it there is no boundary to warn
// about — which is exactly what keeps the summer quiet and stops an overdue
// month from being re-announced every day once its last day has passed.
func dueForBillingReminder(bal model.Balance) bool {
	return bal.BillingType == model.BillingMonthly && bal.NoticeDue()
}

// billingReminderText renders the warning. The amount is the course's current
// price, which for a monthly course is the price of one month.
func billingReminderText(bal model.Balance) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "💳 %s · %s: оплачений період до %s",
		bal.Person, bal.Name, formatDate(bal.CoversUntil))
	switch {
	case bal.DaysLeft <= 0:
		sb.WriteString(" — сьогодні останній день.")
	case bal.DaysLeft == 1:
		sb.WriteString(" — завтра закінчується.")
	default:
		fmt.Fprintf(&sb, " — залишилось %d дн.", bal.DaysLeft)
	}
	if bal.CurrentPrice > 0 {
		fmt.Fprintf(&sb, "\nДо оплати %s ₴.", formatAmount(bal.CurrentPrice))
	}
	if bal.PaymentInstructions != "" {
		sb.WriteString("\nРеквізити: " + bal.PaymentInstructions)
	}
	return sb.String()
}

// formatDate turns a stored "2006-01-02" into "02.01" for a one-line message;
// an unparseable value is passed through rather than hidden.
func formatDate(s string) string {
	t, err := model.ParseDate(s)
	if err != nil {
		return s
	}
	return t.Format("02.01")
}

func formatAmount(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.2f", v)
}
