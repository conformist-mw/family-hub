package mini

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"familyhub/internal/appointments"
	"familyhub/internal/payments"
	"familyhub/internal/schedule"
	"familyhub/internal/store"
)

// The Mini App is a JSON API plus a small client-rendered frontend, both inside
// this binary and both under a single /mini prefix:
//
//	/mini            bootstrap shell, no family data
//	/mini/assets/*   JS, CSS, vendored Preact+htm
//	/mini/api/*      JSON, authenticated per request
//
// One prefix, so the Traefik oauth bypass needs exactly one rule. It shares the
// store and the database with the web UI and the bot; nothing here talks to
// internal/bot, the token arrives as a string.

//go:embed static
var staticFS embed.FS

// DefaultMaxAge is how long a launch payload stays acceptable. Telegram never
// refreshes initData while the app is open, so a short window would kill the
// app in the user's hand. The allowlist is the real barrier here, not
// signature freshness.
const DefaultMaxAge = 24 * time.Hour

// maxAppointments caps the upcoming list. The realistic horizon is tens of
// rows; this is a fuse, not pagination.
const maxAppointments = 100

type Config struct {
	// BotToken keys the initData HMAC. Without it there is no Mini App.
	BotToken string
	// AllowedUsers are Telegram *user* ids. Deliberately not the bot's
	// TELEGRAM_ALLOWED_CHATS: that lists chats, including the family group,
	// while a Mini App authenticates the individual who opened it.
	AllowedUsers []int64
	// MaxAge overrides DefaultMaxAge when non-zero.
	MaxAge time.Duration
	// DevUser makes the API accept unsigned requests as this Telegram id, so
	// screens can be opened in a normal browser. It is honoured only while
	// WebhookURL is empty — see devFixtureUser.
	DevUser int64
	// WebhookURL is the production signal. Production always sets it, so
	// gating the fixture on its absence is structural rather than a flag
	// somebody can forget to unset.
	WebhookURL string
	// Loc is the wall-clock zone appointments are stored in.
	Loc *time.Location
	// Notifier posts a write to the family group. The bot implements it and
	// nothing here imports it; nil means no group message, the same as a
	// disabled bot.
	Notifier appointments.Notifier
	// Now is injectable for tests; nil means time.Now.
	Now func() time.Time
}

func (c Config) devFixtureUser() int64 {
	if c.WebhookURL != "" {
		return 0
	}
	return c.DevUser
}

type Router struct {
	store *store.Store
	// appointments holds the write rules shared with the web UI, so the two
	// surfaces cannot drift on what a valid appointment is.
	appointments *appointments.Service
	payments     *payments.Service
	schedule     *schedule.Service
	log          *slog.Logger
	v            *verifier
	loc          *time.Location
	now          func() time.Time
	// assetVersion busts caches on restart; see handleShell.
	assetVersion string
}

// NewRouter builds the Mini App surface. It fails when there is no bot token:
// initData verification is an HMAC keyed by it, so there is nothing to mount.
func NewRouter(st *store.Store, logger *slog.Logger, cfg Config) (http.Handler, error) {
	if cfg.BotToken == "" {
		return nil, errors.New("mini: bot token required")
	}
	if cfg.MaxAge == 0 {
		cfg.MaxAge = DefaultMaxAge
	}
	if cfg.Loc == nil {
		cfg.Loc = time.Local
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	rt := &Router{
		store:        st,
		appointments: appointments.NewService(st, cfg.Loc, cfg.Notifier, logger),
		payments:     payments.NewService(st),
		schedule:     schedule.NewService(st),
		log:          logger,
		v:            newVerifier(cfg.BotToken, cfg, logger, cfg.Now),
		loc:          cfg.Loc,
		now:          cfg.Now,
		assetVersion: strconv.FormatInt(cfg.Now().Unix(), 10),
	}
	if u := cfg.devFixtureUser(); u != 0 {
		logger.Warn("mini: DEV AUTH FIXTURE ACTIVE — unsigned requests accepted", "user_id", u)
	}

	assets, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	// Patterns carry the full path: the outer mux dispatches on the /mini/
	// subtree without stripping it.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /mini/{$}", rt.handleShell)
	mux.Handle("GET /mini/assets/", noStore(http.StripPrefix("/mini/assets/", http.FileServerFS(assets))))
	mux.HandleFunc("GET /mini/api/home", rt.handleHome)
	mux.HandleFunc("GET /mini/api/appointments", rt.handleAppointments)
	mux.HandleFunc("POST /mini/api/appointments", rt.handleAppointmentCreate)
	mux.HandleFunc("PUT /mini/api/appointments/{id}", rt.handleAppointmentUpdate)
	mux.HandleFunc("DELETE /mini/api/appointments/{id}", rt.handleAppointmentDelete)
	mux.HandleFunc("GET /mini/api/persons", rt.handlePersons)
	mux.HandleFunc("GET /mini/api/courses", rt.handleCourses)
	mux.HandleFunc("POST /mini/api/courses/{id}/slots", rt.handleSlotCreate)
	mux.HandleFunc("POST /mini/api/courses/{id}/payments", rt.handlePaymentCreate)
	mux.HandleFunc("PUT /mini/api/slots/{id}", rt.handleSlotUpdate)
	mux.HandleFunc("DELETE /mini/api/slots/{id}", rt.handleSlotDelete)
	return mux, nil
}

// noStore keeps the assets out of every cache between here and the phone.
//
// Without an explicit header Cloudflare applies its own default to .js and
// .css — four hours, in the browser and at its edge — so a deployed change is
// simply not visible, and neither a reload nor reopening the app helps. There
// is no build step here and therefore no content-hashed filenames to rely on
// instead. Re-fetching some twenty kilobytes per launch costs this family
// nothing; serving them a stale app costs an evening of confusion.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		next.ServeHTTP(w, r)
	})
}

// handleShell serves the bootstrap page. It is unauthenticated on purpose:
// there is no family data in it, only the markup that then authenticates.
//
// The asset URLs carry ?v=<this process's start time>. no-store above stops
// caches filling up again, but anything cached *before* that header existed
// still has four hours to live, and a changed URL is the only way past it.
// A restart is also exactly when the assets can have changed.
func (rt *Router) handleShell(w http.ResponseWriter, r *http.Request) {
	page, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		rt.log.Error("mini: read shell", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(strings.ReplaceAll(string(page), "__V__", rt.assetVersion)))
}

func (rt *Router) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		rt.log.Error("mini: write json", "err", err)
	}
}

func (rt *Router) fail(w http.ResponseWriter, e *apiError) {
	rt.writeJSON(w, e.status, map[string]*apiError{"error": e})
}
