package bot

import (
	tele "gopkg.in/telebot.v3"
)

// Getting into the Mini App from the family group.
//
// Telegram has three launch affordances and two of them are private-chat only:
// the menu button beside the input field, and an inline web_app button. What is
// left for a group is a t.me deep link, which works as a plain url button
// anywhere. So the group gets a url button, and it goes on every message the
// bot posts there — the point is that whatever arrived last is the way in.
// Otherwise the app is only reachable through a pinned message somebody has to
// remember to pin.
//
// The link needs the Main Mini App configured in BotFather (Bot Settings →
// Configure Mini App). Until it is, `?startapp` opens the chat with the bot
// rather than the app.

const appButtonLabel = "📱 Відкрити застосунок"

// miniAppURL is derived from the bot's own username rather than configured:
// telebot resolves it at startup, so there is no env var to keep in step with
// whatever BotFather calls the bot. Empty when that resolve failed, which hides
// the button instead of sending a dead link.
func (b *Bot) miniAppURL() string {
	if b.b.Me == nil || b.b.Me.Username == "" {
		return ""
	}
	return "https://t.me/" + b.b.Me.Username + "?startapp"
}

// appMarkup is a keyboard carrying just the app button — and an empty one when
// there is no app to open, so it can stand in for the `&tele.ReplyMarkup{}`
// that ends an interaction without changing what that meant.
func (b *Bot) appMarkup() *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	if url := b.miniAppURL(); url != "" {
		m.Inline(m.Row(m.URL(appButtonLabel, url)))
	}
	return m
}

// withAppButton adds the app row to whatever is being sent: under the markup
// already in opts when there is one (the reminder's answer buttons, the cost
// prompt), otherwise as the message's only keyboard.
//
// The caller's markup is extended in place, which is safe because every group
// send builds its markup fresh for that one message.
func (b *Bot) withAppButton(opts []any) []any {
	url := b.miniAppURL()
	if url == "" {
		return opts
	}
	btn := tele.Btn{Text: appButtonLabel, URL: url}
	for _, o := range opts {
		if m, ok := o.(*tele.ReplyMarkup); ok {
			m.InlineKeyboard = append(m.InlineKeyboard, []tele.InlineButton{*btn.Inline()})
			return opts
		}
	}
	return append(opts, b.appMarkup())
}

// cmdApp is the launch affordance for a group, where the menu button does not
// exist. In a private chat it duplicates that button, which costs nothing and
// saves explaining which chat has which.
func (b *Bot) cmdApp(c tele.Context) error {
	if b.miniAppURL() == "" {
		return c.Send("Застосунок недоступний 😕")
	}
	return c.Send("Записи, заняття й баланси — у застосунку:", b.appMarkup())
}
