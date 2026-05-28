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
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"lessons/internal/bot"
	"lessons/internal/db"
	"lessons/internal/store"
	"lessons/internal/web"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	dbPath := flag.String("db", "data/lessons.db", "SQLite database path")
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
	if token := os.Getenv("TELEGRAM_BOT_TOKEN"); token != "" {
		notifyChat, _ := strconv.ParseInt(os.Getenv("TELEGRAM_NOTIFY_CHAT"), 10, 64)
		reminderHour := 20
		if v, err := strconv.Atoi(os.Getenv("TELEGRAM_REMINDER_HOUR")); err == nil {
			reminderHour = v
		}
		cfg := bot.Config{
			Token:         token,
			WebhookURL:    os.Getenv("TELEGRAM_WEBHOOK_URL"),
			WebhookSecret: os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
			AllowedChats:  bot.ParseChatIDs(os.Getenv("TELEGRAM_ALLOWED_CHATS"), logger),
			NotifyChat:    notifyChat,
			ReminderHour:  reminderHour,
		}
		lessonsBot, err = bot.New(cfg, store.New(database), logger)
		if err != nil {
			logger.Error("bot init", "err", err)
			os.Exit(1)
		}
		defer lessonsBot.Stop()

		if lessonsBot.WebhookMode() {
			webhookHandler = lessonsBot.WebhookHandler()
			webhookPath = lessonsBot.WebhookPath()
			if err := lessonsBot.RegisterWebhook(); err != nil {
				logger.Error("bot: register webhook", "err", err)
			} else {
				logger.Info("bot: webhook registered", "url", cfg.WebhookURL, "path", webhookPath)
			}
		} else {
			go lessonsBot.RunPolling(ctx)
		}
		go lessonsBot.RunScheduler(ctx)
	} else {
		logger.Info("bot: disabled (TELEGRAM_BOT_TOKEN not set)")
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           web.NewRouter(database, logger, webhookPath, webhookHandler),
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
