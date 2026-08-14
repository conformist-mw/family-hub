package bot

import (
	"testing"

	tele "gopkg.in/telebot.v3"
)

func testBot(username string) *Bot {
	inner := &tele.Bot{}
	if username != "" {
		inner.Me = &tele.User{Username: username}
	}
	return &Bot{b: inner}
}

func TestMiniAppURL(t *testing.T) {
	// A t.me deep link, because it is the only launch affordance a group
	// accepts — see miniapp.go.
	if got, want := testBot("family_core_hub_bot").miniAppURL(),
		"https://t.me/family_core_hub_bot?startapp"; got != want {
		t.Errorf("miniAppURL = %q, want %q", got, want)
	}
	// getMe never answered: no link is better than a broken one.
	if got := testBot("").miniAppURL(); got != "" {
		t.Errorf("miniAppURL = %q, want empty without a username", got)
	}
}

// The app button must not displace the buttons a message already carries —
// answering the reminder is what that message is for.
func TestWithAppButtonKeepsTheExistingKeyboard(t *testing.T) {
	b := testBot("family_core_hub_bot")
	m := buildReminderMarkup(7, "2026-08-14")
	before := len(m.InlineKeyboard)
	if before == 0 {
		t.Fatal("reminder markup has no buttons to preserve")
	}

	opts := b.withAppButton([]any{m})
	if len(opts) != 1 {
		t.Fatalf("opts grew to %d, want the markup merged in place", len(opts))
	}
	if len(m.InlineKeyboard) != before+1 {
		t.Fatalf("rows = %d, want %d", len(m.InlineKeyboard), before+1)
	}
	row := m.InlineKeyboard[len(m.InlineKeyboard)-1]
	if len(row) != 1 || row[0].URL == "" {
		t.Errorf("last row = %+v, want a single url button", row)
	}
}

// A message with no keyboard of its own gets one.
func TestWithAppButtonAddsMarkupWhenThereIsNone(t *testing.T) {
	b := testBot("family_core_hub_bot")

	opts := b.withAppButton([]any{tele.ModeHTML})
	if len(opts) != 2 {
		t.Fatalf("opts = %d, want the mode plus a markup", len(opts))
	}
	m, ok := opts[1].(*tele.ReplyMarkup)
	if !ok {
		t.Fatalf("appended %T, want *tele.ReplyMarkup", opts[1])
	}
	if len(m.InlineKeyboard) != 1 || len(m.InlineKeyboard[0]) != 1 {
		t.Fatalf("keyboard = %+v, want one button", m.InlineKeyboard)
	}
}

// Nothing to open: sends stay exactly as they were, and appMarkup still clears
// a keyboard the way the `&tele.ReplyMarkup{}` it replaced did.
func TestNoAppNoButton(t *testing.T) {
	b := testBot("")

	opts := b.withAppButton([]any{tele.ModeHTML})
	if len(opts) != 1 {
		t.Errorf("opts = %d, want untouched", len(opts))
	}
	if m := b.appMarkup(); len(m.InlineKeyboard) != 0 {
		t.Errorf("appMarkup = %+v, want empty", m.InlineKeyboard)
	}
}
