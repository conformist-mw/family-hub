package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/audit"
	"familyhub/internal/parse"
	"familyhub/internal/reminders"
	"familyhub/internal/schooltoday"
	"familyhub/internal/store"
)

type Config struct {
	Token            string
	WebhookURL       string
	WebhookSecret    string
	AllowedChats     []int64
	NotifyChat       int64 // where attendance reminders are pushed; 0 disables
	ReminderDelayMin int   // minutes after a slot's time to ask about it; <0 disables

	// CostPromptDelayMin is how long after an appointment starts the bot asks
	// what it cost; <0 disables the prompt entirely.
	CostPromptDelayMin int

	// Loc places stored wall-clock times: slot "HH:MM", appointment
	// starts_at, and the digest times below. It is time.Local in practice
	// (the deploy sets TZ in the container); the appointment code takes it
	// explicitly because it parses naive datetimes.
	Loc *time.Location

	// NotificationsEnabled gates the appointment digests (daily/weekly).
	// Default false: opt-in, so the app stays quiet unless explicitly turned
	// on. Home Assistant owns the morning/weekly summaries (its Remote
	// Calendar reads this app's ICS feed), so leaving this off avoids sending
	// the family group a duplicate message. Lesson reminders are unaffected.
	NotificationsEnabled bool
	DailyDigestTime      string // "HH:MM" in Loc; "" disables
	WeeklyDigestDOW      int    // 0=Sun..6=Sat; <0 disables
	WeeklyDigestTime     string // "HH:MM" in Loc

	// ReminderNagTime is when the bot lists the recurring chores nobody closed
	// today. "" disables it. Deliberately NOT behind NotificationsEnabled:
	// that flag is off in prod because Home Assistant already sends the
	// appointment summaries, and HA has nothing to say about what went
	// unfinished — it reads a calendar, not the record of what was closed.
	ReminderNagTime string

	// SchoolDigestTime is when the bot sends tomorrow's school timetable.
	// "" disables it. Not behind NotificationsEnabled either, and for a
	// sharper reason than the nag: HA's calendar API hands a template only
	// summary/start/end/description, so it cannot tell a lesson from the
	// after-school block and cannot say when the child is actually free.
	SchoolDigestTime string

	// SchoolWeekReviewDOW/Time is when the bot posts the review of the school
	// week just gone: the topics, the teacher's notes, the homework and the
	// marks, per subject. DOW is 0=Sun..6=Sat and <0 disables, matching
	// WeeklyDigestDOW; "" for the time disables it too.
	//
	// Exempt from NotificationsEnabled for the same reason as SchoolDigestTime,
	// and set fifteen minutes after it so the two school messages do not land
	// in the group at the same moment.
	SchoolWeekReviewDOW  int
	SchoolWeekReviewTime string

	// Reminders answers what came due and was left open. nil disables the nag
	// regardless of ReminderNagTime.
	Reminders *reminders.Service

	// School collects the week from the portal for the review above. nil —
	// which is what a deploy without portal credentials gets — disables the
	// review regardless of the times, because there would be nothing to read.
	School *schooltoday.Service
}

type Bot struct {
	b       *tele.Bot
	cfg     Config
	store   *store.Store
	logger  *slog.Logger
	allowed map[int64]bool

	// parser is nil when GEMINI_API_KEY is unset: the lessons half of the bot
	// works fine without an LLM, so a missing key disables free-text capture
	// instead of refusing to start.
	parser   *parse.Parser
	pending  *pendingStore
	awaiting *awaitingStore

	// reviewRunning guards the Friday school review, which is the only message
	// that leaves the ticker's goroutine. Without it a portal crawling badly
	// enough to outlast a minute would have a second collect started on top of
	// the first, and the group would get the week twice.
	reviewRunning atomic.Bool
}

// ParseChatIDs parses a comma-separated list of int64 chat ids.
// Empty entries are skipped; invalid entries are logged at warn level.
func ParseChatIDs(raw string, logger *slog.Logger) []int64 {
	var out []int64
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			if logger != nil {
				logger.Warn("bot: bad chat id in TELEGRAM_ALLOWED_CHATS", "value", p)
			}
			continue
		}
		out = append(out, id)
	}
	return out
}

func New(cfg Config, st *store.Store, parser *parse.Parser, logger *slog.Logger) (*Bot, error) {
	// In webhook mode the secret header is the auth layer in front of
	// ProcessUpdate: authMiddleware trusts the chat id from the update body,
	// so without the secret anyone who learns the (secret) path could forge
	// updates from an allowlisted chat. Refuse to start rather than run
	// silently unprotected — prod always has it via SOPS.
	if cfg.WebhookURL != "" && cfg.WebhookSecret == "" {
		return nil, fmt.Errorf("webhook mode requires TELEGRAM_WEBHOOK_SECRET")
	}
	pref := tele.Settings{Token: cfg.Token}
	// Use long polling only when no webhook URL is configured — otherwise we
	// process updates via the HTTP handler and do not need a poller.
	if cfg.WebhookURL == "" {
		pref.Poller = &tele.LongPoller{Timeout: 10 * time.Second}
	}

	tb, err := tele.NewBot(pref)
	if err != nil {
		return nil, err
	}

	if cfg.Loc == nil {
		cfg.Loc = time.Local
	}
	bot := &Bot{
		b:        tb,
		cfg:      cfg,
		store:    st,
		parser:   parser,
		logger:   logger,
		allowed:  make(map[int64]bool, len(cfg.AllowedChats)),
		pending:  newPendingStore(),
		awaiting: newAwaitingStore(),
	}
	for _, id := range cfg.AllowedChats {
		bot.allowed[id] = true
	}

	tb.Use(bot.authMiddleware)

	tb.Handle("/start", bot.cmdStart)
	tb.Handle("/help", bot.cmdHelp)
	tb.Handle("/balance", bot.cmdBalance)
	tb.Handle("/stats", bot.cmdStats)
	tb.Handle("/add", bot.cmdAdd)
	tb.Handle("/app", bot.cmdApp)

	tb.Handle(&tele.Btn{Unique: "rem_visit"}, bot.onReminderTap)
	tb.Handle(&tele.Btn{Unique: "add_course"}, bot.onAddCourse)
	tb.Handle(&tele.Btn{Unique: "add_date"}, bot.onAddDate)
	tb.Handle(&tele.Btn{Unique: "add_status"}, bot.onAddStatus)
	tb.Handle(&tele.Btn{Unique: "add_cancel"}, bot.onAddCancel)
	tb.Handle(&tele.Btn{Unique: "vis_reason"}, bot.onReasonTap)
	tb.Handle(&tele.Btn{Unique: "rem_chore"}, bot.onChoreTap)

	// Appointments. /list, /week and their callbacks only read and edit stored
	// rows, so they work with or without a parser. OnText is registered
	// unconditionally too: it also delivers the reply to a field-edit prompt
	// (title/who), which needs no LLM — free-text capture inside it is what
	// the parser gates.
	tb.Handle("/week", bot.cmdWeek)
	tb.Handle("/list", bot.cmdList)
	// Reads stored rows like the two above, so it needs neither a parser nor a
	// configured portal — the records outlive the credentials that collected them.
	tb.Handle("/schoolweek", bot.cmdSchoolWeek)
	tb.Handle(&tele.Btn{Unique: "lst_nav"}, bot.onNav)
	tb.Handle(&tele.Btn{Unique: "lst_arm"}, bot.onArm)
	tb.Handle(&tele.Btn{Unique: "lst_del"}, bot.onDel)
	tb.Handle(&tele.Btn{Unique: "appt_cost"}, bot.onCostSkip)
	tb.Handle(tele.OnText, bot.onText)

	if parser != nil {
		tb.Handle("/visit", bot.cmdVisit)
		tb.Handle(&tele.Btn{Unique: "appt_save"}, bot.onSave)
		tb.Handle(&tele.Btn{Unique: "appt_update"}, bot.onUpdate)
		tb.Handle(&tele.Btn{Unique: "appt_cancel"}, bot.onCancel)
	} else {
		logger.Info("bot: free-text capture disabled (GEMINI_API_KEY not set)")
	}

	// Populate the "/" menu — how the group discovers the appointment commands
	// (best-effort; a network hiccup here must not block startup).
	cmds := []tele.Command{
		{Text: "app", Description: "Відкрити застосунок"},
		{Text: "add", Description: "Відмітити заняття"},
		{Text: "balance", Description: "Баланс по курсах"},
		{Text: "stats", Description: "Скільки витрачено"},
		{Text: "schoolweek", Description: "Огляд шкільного тижня: /schoolweek 1"},
	}
	if parser != nil {
		cmds = append(cmds, tele.Command{Text: "visit", Description: "Записати візит: /visit завтра 15:00 педикюр"})
	}
	cmds = append(cmds,
		tele.Command{Text: "week", Description: "Записи на найближчий тиждень"},
		tele.Command{Text: "list", Description: "Записи по тижнях: перенести, виправити, скасувати"},
		tele.Command{Text: "help", Description: "Довідка"},
	)
	if err := tb.SetCommands(cmds); err != nil {
		logger.Warn("bot: set commands", "err", err)
	}

	return bot, nil
}

func (b *Bot) WebhookMode() bool { return b.cfg.WebhookURL != "" }

// WebhookPath returns the path component of the configured webhook URL.
// It is used to mount the HTTP handler on the right route. Falls back to
// /tgwebhook if the URL is malformed or has no path.
func (b *Bot) WebhookPath() string {
	u, err := url.Parse(b.cfg.WebhookURL)
	if err != nil || u.Path == "" || u.Path == "/" {
		return "/tgwebhook"
	}
	return u.Path
}

// RunPolling starts long-polling in the foreground and returns when ctx is
// cancelled. It is the only place telebot's Stop() may be called: Stop()
// handshakes over an unbuffered channel that only the Start() loop drains,
// so calling it in webhook mode (no Start loop) or a second time after the
// loop exited blocks forever.
func (b *Bot) RunPolling(ctx context.Context) {
	go func() {
		<-ctx.Done()
		b.b.Stop()
	}()
	b.logger.Info("bot: starting polling")
	b.b.Start()
}

// NotifyText posts plain text to the notify chat. Implements web.Notifier for
// the audit page's "send to group" button.
func (b *Bot) NotifyText(text string) error { return b.notify(text) }

// NotifyHTML posts with Telegram's HTML parse mode, for the group notifications
// internal/appointments builds — they carry <b> markup and escape the rest.
// Implements appointments.Notifier, so a visit written down on the web or in the
// Mini App reaches the group the same way one captured by the bot does.
func (b *Bot) NotifyHTML(text string) error { return b.notify(text, tele.ModeHTML) }

// notify sends to the notify chat, split into ≤4000-char chunks (Telegram caps
// messages at 4096). The split falls on line boundaries and every line closes
// its own tags, so chunking cannot cut HTML in half. No notify chat means
// nowhere to post, which is not an error.
func (b *Bot) notify(text string, opts ...any) error {
	if b.cfg.NotifyChat == 0 {
		return nil
	}
	chunks := audit.SplitMessage(text, 4000)
	for i, chunk := range chunks {
		// Only the last chunk carries the app button: one per chunk would put
		// three of them under a long reconciliation.
		var err error
		if i == len(chunks)-1 {
			_, err = b.sendToGroup(chunk, opts...)
		} else {
			_, err = b.b.Send(tele.ChatID(b.cfg.NotifyChat), chunk, opts...)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// sendToGroup is the only way this bot posts to the family group on its own
// initiative, so the "open the app" button (see miniapp.go) cannot be forgotten
// on a new kind of message. It returns a nil message when no notify chat is
// configured — there is nowhere to post, which is not an error.
func (b *Bot) sendToGroup(what any, opts ...any) (*tele.Message, error) {
	if b.cfg.NotifyChat == 0 {
		return nil, nil
	}
	return b.b.Send(tele.ChatID(b.cfg.NotifyChat), what, b.withAppButton(opts)...)
}

// RegisterWebhook tells Telegram to deliver updates to our public URL.
func (b *Bot) RegisterWebhook() error {
	w := &tele.Webhook{
		Endpoint:    &tele.WebhookEndpoint{PublicURL: b.cfg.WebhookURL},
		SecretToken: b.cfg.WebhookSecret,
	}
	return b.b.SetWebhook(w)
}

// WebhookHandler serves incoming Telegram updates from our HTTP mux.
func (b *Bot) WebhookHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b.cfg.WebhookSecret != "" {
			if r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != b.cfg.WebhookSecret {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		var u tele.Update
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
			b.logger.Warn("bot: bad webhook payload", "err", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		b.b.ProcessUpdate(u)
		w.WriteHeader(http.StatusOK)
	})
}

func (b *Bot) authMiddleware(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		// If no whitelist is configured, allow everyone but warn — the user
		// has the only knowledge of the bot's username, but it's still loose.
		if len(b.allowed) == 0 {
			return next(c)
		}
		chat := c.Chat()
		if chat == nil || !b.allowed[chat.ID] {
			id := int64(0)
			username := ""
			if chat != nil {
				id = chat.ID
			}
			if c.Sender() != nil {
				username = c.Sender().Username
			}
			b.logger.Warn("bot: unauthorized chat", "chat_id", id, "user", username)
			return c.Send("Доступ заборонено.")
		}
		return next(c)
	}
}
