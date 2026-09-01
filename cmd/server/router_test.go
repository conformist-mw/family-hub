package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"familyhub/internal/db"
	"familyhub/internal/mini"
	"familyhub/internal/store"
	"familyhub/internal/web"
)

// The Mini App adds a second HTTP surface next to the web UI. These tests pin
// the boundary between them: what each prefix answers, and that mounting the
// Mini App did not loosen the web side.

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	miniHandler, err := mini.NewRouter(store.New(database), logger, mini.Config{
		BotToken:     "123456:test-bot-token",
		AllowedUsers: []int64{42},
		DevUser:      42,
		Loc:          time.UTC,
	})
	if err != nil {
		t.Fatalf("mini.NewRouter: %v", err)
	}

	return buildHandler(web.NewRouter(database, logger, "", nil, nil, nil), miniHandler)
}

func TestRoutesReachTheirOwnSurface(t *testing.T) {
	h := testHandler(t)

	cases := []struct {
		path string
		want int
	}{
		{"/mini/", http.StatusOK},                 // shell, public by design
		{"/mini/assets/app.js", http.StatusOK},    // asset, public by design
		{"/mini/api/appointments", http.StatusOK}, // dev fixture is on here
		{"/", http.StatusOK},                      // web dashboard
		{"/healthz", http.StatusOK},
		{"/mini/api/nope", http.StatusNotFound},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("GET %s = %d, want %d", tc.path, rec.Code, tc.want)
		}
	}
}

// The Mini App prefix must not become a hole in the web UI's CSRF defence:
// web keeps rejecting cross-site POSTs exactly as before.
func TestWebStillRejectsCrossSitePost(t *testing.T) {
	h := testHandler(t)

	r := httptest.NewRequest(http.MethodPost, "/lessons/visits", strings.NewReader(""))
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST /visits = %d, want 403", rec.Code)
	}
}

// With no bot token there is no Mini App, and /mini must not resolve to the
// web UI's catch-all either.
func TestMiniAbsentWhenDisabled(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	h := buildHandler(web.NewRouter(database, logger, "", nil, nil, nil), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mini/api/appointments", nil))

	if rec.Code == http.StatusOK {
		t.Fatalf("GET /mini/api/appointments = 200 with the Mini App disabled")
	}
}
