package bot

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"lessons/internal/store"
)

type Config struct {
	Token         string
	WebhookURL    string
	WebhookSecret string
	AllowedChats  []int64
	NotifyChat       int64 // where attendance reminders are pushed; 0 disables
	ReminderDelayMin int   // minutes after a slot's time to ask about it; <0 disables
}

type Bot struct {
	b       *tele.Bot
	cfg     Config
	store   *store.Store
	logger  *slog.Logger
	allowed map[int64]bool
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

func New(cfg Config, st *store.Store, logger *slog.Logger) (*Bot, error) {
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

	bot := &Bot{
		b:       tb,
		cfg:     cfg,
		store:   st,
		logger:  logger,
		allowed: make(map[int64]bool, len(cfg.AllowedChats)),
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

	tb.Handle(&tele.Btn{Unique: "rem_visit"}, bot.onReminderTap)
	tb.Handle(&tele.Btn{Unique: "add_course"}, bot.onAddCourse)
	tb.Handle(&tele.Btn{Unique: "add_date"}, bot.onAddDate)
	tb.Handle(&tele.Btn{Unique: "add_status"}, bot.onAddStatus)
	tb.Handle(&tele.Btn{Unique: "add_cancel"}, bot.onAddCancel)

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
// cancelled or the bot is stopped.
func (b *Bot) RunPolling(ctx context.Context) {
	go func() {
		<-ctx.Done()
		b.b.Stop()
	}()
	b.logger.Info("bot: starting polling")
	b.b.Start()
}

func (b *Bot) Stop() {
	if b.b != nil {
		b.b.Stop()
	}
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
			return c.Send("Доступ запрещён.")
		}
		return next(c)
	}
}
