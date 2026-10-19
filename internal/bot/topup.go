package bot

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/audit"
	"familyhub/internal/model"
	"familyhub/internal/payments"
)

const paidUnique = "bal_paid"

// topUpOption is one "pay this much to get this far" line of the pre-lesson
// warning.
type topUpOption struct {
	Label   string // "сьогодні", "до кінця тижня", "+2 тижні"
	Through string // date of the last lesson it pays for
	Lessons int    // lessons to pay, the existing debt included
}

// topUpOptions answers "how much do I send?" at the moment the warning asks
// for money. The last payment is no guide — a pack is sized to whatever the
// schedule needs that time, five lessons to cover Thursday and the two weeks
// after — so the amounts come from the schedule ahead and the course price
// instead: today alone, through the end of this week, and two weeks past
// that.
//
// The dates are the real upcoming lessons, trainer absences dropped, so a
// week the trainer is away costs nothing. Debt — lessons held but never paid
// for — rides on every option, since no payment can skip it. A horizon that
// adds no lesson over the one before it is left out rather than repeated.
//
// Per-lesson courses only, and only with a price: without one there is
// nothing to multiply, and a monthly pass is one fixed amount.
func topUpOptions(bal model.Balance, slots []model.Slot, absences []model.TrainerAbsence, now time.Time) []topUpOption {
	if bal.BillingType == model.BillingMonthly || bal.CurrentPrice <= 0 {
		return nil
	}
	var active []model.Slot
	for _, sl := range slots {
		if sl.Active {
			active = append(active, sl)
		}
	}

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	weekEnd := today.AddDate(0, 0, (7-int(today.Weekday()))%7) // Sunday
	horizons := []struct {
		label string
		end   time.Time
	}{
		{"сьогодні", today},
		{"до кінця тижня", weekEnd},
		{"+2 тижні", weekEnd.AddDate(0, 0, 14)},
	}

	last := horizons[len(horizons)-1].end
	days := int(last.Sub(today).Hours()/24) + 1
	dates := audit.UpcomingDates(active, absences, today.Format("2006-01-02"), false, days)

	debt := 0
	if bal.Remaining < 0 {
		debt = -bal.Remaining
	}
	var out []topUpOption
	prev := 0
	for _, h := range horizons {
		end := h.end.Format("2006-01-02")
		n, through := 0, ""
		for _, d := range dates {
			if d > end {
				break
			}
			n++
			through = d
		}
		if n == 0 || n == prev {
			continue
		}
		prev = n
		out = append(out, topUpOption{Label: h.label, Through: through, Lessons: n + debt})
	}
	return out
}

// topUpLines renders the options under the warning's headline, one per line.
func topUpLines(bal model.Balance, opts []topUpOption) string {
	if len(opts) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\nДо оплати, по %s ₴ за заняття", formatAmount(bal.CurrentPrice))
	if bal.Remaining < 0 {
		fmt.Fprintf(&sb, " (разом із боргом %d зан.)", -bal.Remaining)
	}
	sb.WriteString(":")
	for i, o := range opts {
		fmt.Fprintf(&sb, "\n• %s", o.Label)
		// Today's date is already in the headline.
		if i > 0 {
			fmt.Fprintf(&sb, " (по %s)", formatDate(o.Through))
		}
		fmt.Fprintf(&sb, " — %d зан. = %s ₴", o.Lessons, formatAmount(float64(o.Lessons)*bal.CurrentPrice))
	}
	return sb.String()
}

// topUpMarkup puts one "оплатив" button under the warning per option, so the
// payment that answers it is a tap rather than a trip to the payment form.
// Nil when there are no options: the warning then carries the app button
// alone, which is where any other amount gets entered.
//
// The amount rides in the button rather than being recomputed on tap: what is
// recorded is what the message said, even if the price changed in between.
func topUpMarkup(bal model.Balance, opts []topUpOption) *tele.ReplyMarkup {
	if len(opts) == 0 {
		return nil
	}
	m := &tele.ReplyMarkup{}
	var row []tele.Btn
	for _, o := range opts {
		amount := formatAmount(float64(o.Lessons) * bal.CurrentPrice)
		row = append(row, m.Data(
			fmt.Sprintf("✓ Оплатив %d", o.Lessons),
			paidUnique,
			fmt.Sprintf("%d:%d:%s", bal.ID, o.Lessons, amount),
		))
	}
	m.Inline(m.Row(row...))
	return m
}

// parsePaidData reads "<enrollment_id>:<lessons>:<amount>".
func parsePaidData(data string) (eid int64, lessons int, amount string, err error) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 {
		return 0, 0, "", fmt.Errorf("bad paid data %q", data)
	}
	if eid, err = strconv.ParseInt(parts[0], 10, 64); err != nil {
		return 0, 0, "", err
	}
	if lessons, err = strconv.Atoi(parts[1]); err != nil || lessons <= 0 {
		return 0, 0, "", fmt.Errorf("bad lesson count %q", parts[1])
	}
	if _, err = strconv.ParseFloat(parts[2], 64); err != nil {
		return 0, 0, "", err
	}
	return eid, lessons, parts[2], nil
}

// onPaidTap records the payment a warning button names, dated today, and turns
// the warning into its answer: the headline, what was paid and by whom, and
// the balance it leaves. The edited message is the announcement — a second
// "💸 Оплата" message would say the same thing twice — so the payment is
// written through a service with no notifier.
func (b *Bot) onPaidTap(c tele.Context) error {
	eid, lessons, amount, err := parsePaidData(c.Data())
	if err != nil {
		_ = c.Respond(&tele.CallbackResponse{Text: "Невірні дані"})
		return nil
	}
	msg := c.Message()
	key := fmt.Sprintf("%d:%d", msg.Chat.ID, msg.ID)
	if _, taken := b.paidTaps.LoadOrStore(key, true); taken {
		_ = c.Respond(&tele.CallbackResponse{Text: "Вже записано"})
		return nil
	}

	svc := payments.NewService(b.store, nil, b.logger)
	_, err = svc.Create(eid, payments.Form{
		Kind:    model.PaymentKindCourse,
		Date:    time.Now().Format("2006-01-02"),
		Amount:  amount,
		Lessons: strconv.Itoa(lessons),
	}, b.senderName(c))
	if err != nil {
		b.paidTaps.Delete(key) // nothing was written; let the tap be retried
		b.logger.Error("bot: record payment from warning", "err", err, "eid", eid)
		return c.Respond(&tele.CallbackResponse{Text: "Не вдалося записати оплату 😕", ShowAlert: true})
	}
	_ = c.Respond(&tele.CallbackResponse{Text: "Оплату записано"})

	text := paidWarningText(msg.Text, lessons, amount, b.senderName(c))
	if line := b.balanceLineFor(eid); line != "" {
		text += "\n" + line
	}
	return c.Edit(text, b.appMarkup())
}

// paidWarningText keeps the warning's headline, with its alarm swapped for a
// check mark, and replaces the amounts and payment details below it — both
// answered now — with what was paid.
func paidWarningText(warning string, lessons int, amount, by string) string {
	headline, _, _ := strings.Cut(warning, "\n")
	headline = strings.Replace(headline, "🔴", "✅", 1)
	line := fmt.Sprintf("Оплачено %d зан. = %s ₴", lessons, amount)
	if by != "" {
		line += " · " + by
	}
	return headline + "\n" + line
}
