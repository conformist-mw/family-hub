package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"familyhub/internal/bot"
	"familyhub/internal/db"
	"familyhub/internal/mini"
	"familyhub/internal/parse"
	"familyhub/internal/store"
	"familyhub/internal/web"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	dbPath := flag.String("db", "data/family-hub.db", "SQLite database path")
	flag.Parse()

	// Load .env for local development; absence is not an error (prod uses
	// real env vars from the Ansible role).
	_ = godotenv.Load()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	database, err := db.Open(*dbPath)
	if err != nil {
		logger.Error("open db", "err", err)
		os.Exit(1)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		logger.Error("migrate", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var lessonsBot *bot.Bot
	var webhookHandler http.Handler
	var webhookPath string
	// Typed as the interface and assigned only when the bot can actually
	// send: a nil *bot.Bot stuffed into an interface would be non-nil and
	// the send button would render against a dead bot.
	var notifier web.Notifier
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token != "" {
		notifyChat, _ := strconv.ParseInt(os.Getenv("TELEGRAM_NOTIFY_CHAT"), 10, 64)
		reminderDelay := 60
		if v, err := strconv.Atoi(os.Getenv("TELEGRAM_REMINDER_DELAY_MIN")); err == nil {
			reminderDelay = v
		}
		preLessonLead := 120
		if v, err := strconv.Atoi(os.Getenv("TELEGRAM_PRELESSON_LEAD_MIN")); err == nil {
			preLessonLead = v
		}
		costPromptDelay := 60
		if v, err := strconv.Atoi(os.Getenv("APPOINTMENT_COST_PROMPT_MIN")); err == nil {
			costPromptDelay = v
		}
		billingLead := 1
		if v, err := strconv.Atoi(os.Getenv("BILLING_LEAD_DAYS")); err == nil {
			billingLead = v
		}
		// Free-text capture is optional: without a Gemini key the bot runs with
		// a nil parser and everything except capture keeps working.
		var parser *parse.Parser
		if apiKey := os.Getenv("GEMINI_API_KEY"); apiKey != "" {
			modelName := os.Getenv("GEMINI_MODEL")
			if modelName == "" {
				modelName = "gemini-flash-lite-latest"
			}
			// A broken parser must not take the whole app down: lessons
			// reminders, the web UI and the ICS feed HA polls do not need it.
			// Capture degrades to "not registered", exactly as with no key.
			parser, err = parse.New(ctx, apiKey, modelName, time.Local, splitCSV(os.Getenv("VISIT_PEOPLE")))
			if err != nil {
				logger.Error("parser init failed, continuing without free-text capture", "err", err)
				parser = nil
			}
		}

		cfg := bot.Config{
			Token:                token,
			WebhookURL:           os.Getenv("TELEGRAM_WEBHOOK_URL"),
			WebhookSecret:        os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
			AllowedChats:         bot.ParseChatIDs(os.Getenv("TELEGRAM_ALLOWED_CHATS"), logger),
			NotifyChat:           notifyChat,
			ReminderDelayMin:     reminderDelay,
			PreLessonLeadMin:     preLessonLead,
			CostPromptDelayMin:   costPromptDelay,
			BillingLeadDays:      billingLead,
			Loc:                  time.Local, // TZ comes from the container env
			NotificationsEnabled: parseBool(os.Getenv("NOTIFICATIONS_ENABLED")),
			DailyDigestTime:      os.Getenv("DAILY_DIGEST_TIME"),
			WeeklyDigestDOW:      parseDOW(os.Getenv("WEEKLY_DIGEST_DOW")),
			WeeklyDigestTime:     os.Getenv("WEEKLY_DIGEST_TIME"),
		}
		// No deferred Stop(): telebot's Stop() handshakes with the Start()
		// loop, which webhook mode never runs and polling mode has already
		// stopped via ctx by the time defers fire — either way it deadlocks
		// and Docker escalates to SIGKILL. RunPolling owns its own stop.
		lessonsBot, err = bot.New(cfg, store.New(database), parser, logger)
		if err != nil {
			logger.Error("bot init", "err", err)
			os.Exit(1)
		}

		if lessonsBot.WebhookMode() {
			webhookHandler = lessonsBot.WebhookHandler()
			webhookPath = lessonsBot.WebhookPath()
			if err := lessonsBot.RegisterWebhook(); err != nil {
				logger.Error("bot: register webhook", "err", err)
			} else {
				logger.Info("bot: webhook registered")
			}
		} else {
			go lessonsBot.RunPolling(ctx)
		}
		// Two independent tickers: lesson reminders (always on) and appointment
		// digests (gated by NOTIFICATIONS_ENABLED — HA owns those summaries in
		// prod, so it stays off there).
		go lessonsBot.RunScheduler(ctx)
		go lessonsBot.RunDigests(ctx)
		go lessonsBot.RunCostPrompts(ctx)
		go lessonsBot.RunBillingReminders(ctx)
		if notifyChat != 0 {
			notifier = lessonsBot
		}
	} else {
		logger.Info("bot: disabled (TELEGRAM_BOT_TOKEN not set)")
	}

	webHandler := web.NewRouter(database, logger, webhookPath, webhookHandler, notifier)

	// initData verification is an HMAC keyed by the bot token, so with no token
	// there is nothing to mount — the same shape as free-text capture without
	// a Gemini key.
	var miniHandler http.Handler
	if token == "" {
		logger.Info("mini: disabled (TELEGRAM_BOT_TOKEN not set)")
	} else {
		devUser, _ := strconv.ParseInt(os.Getenv("MINI_DEV_USER"), 10, 64)
		miniRouter, err := mini.NewRouter(store.New(database), logger, mini.Config{
			BotToken:     token,
			AllowedUsers: mini.ParseUserIDs(os.Getenv("TELEGRAM_MINI_USERS"), logger),
			DevUser:      devUser,
			WebhookURL:   os.Getenv("TELEGRAM_WEBHOOK_URL"),
			Loc:          time.Local,
		})
		if err != nil {
			logger.Error("mini: init", "err", err)
			os.Exit(1)
		}
		miniHandler = miniRouter
		logger.Info("mini: mounted at /mini")
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           buildHandler(webHandler, miniHandler),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("listening", "addr", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown", "err", err)
	}
}

// buildHandler composes the two HTTP surfaces. They are deliberately separate
// handlers rather than one shared mux: /mini/* must not pass through web's
// csrfGuard — it authenticates each request with a header and so has no
// ambient credential to protect — and must not inherit the web layout or its
// OAuth-proxy assumptions. A nil miniHandler means the Mini App is off.
func buildHandler(webHandler, miniHandler http.Handler) http.Handler {
	root := http.NewServeMux()
	root.Handle("/", webHandler)
	if miniHandler != nil {
		root.Handle("/mini/", miniHandler)
	}
	return root
}

// parseDOW returns 0..6 for a valid day-of-week, or -1 (disabled) otherwise.
func parseDOW(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return -1
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 6 {
		return -1
	}
	return n
}

// parseBool reports whether s is a truthy value (1/t/true/yes/on, any case).
// Anything else — including empty/unset — is false, so the appointment digests
// stay opt-in.
func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "t", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

// splitCSV parses a comma-separated list, dropping empty entries — the people
// hint list the parser uses to normalize "who".
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
