package mini

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Telegram authentication for the Mini App.
//
// initData is Telegram's signed launch payload. It travels as
// "Authorization: tma <initData>" on every API request and is verified per
// request. There is no cookie and no server-side session, deliberately: on
// Telegram Web and Desktop the Mini App runs inside a cross-site iframe, where
// a SameSite=Lax cookie is never sent and SameSite=None rests on
// third-party-cookie policy. A header has neither problem, and with no ambient
// credential there is no CSRF surface either.

// apiError is the JSON error contract. The two authentication failures stay
// distinct on purpose: "your signature is broken" and "you are not family" are
// different situations and the shell reacts differently to each.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	status  int
}

var (
	errBadInitData = &apiError{Code: "bad_init_data", Message: "невірні дані запуску", status: http.StatusBadRequest}
	errForbidden   = &apiError{Code: "forbidden", Message: "доступ заборонено", status: http.StatusForbidden}
	errInternal    = &apiError{Code: "internal", Message: "внутрішня помилка", status: http.StatusInternalServerError}
)

// maxClockSkew tolerates an auth_date slightly in the future rather than
// rejecting a launch because two machines disagree by seconds.
const maxClockSkew = 5 * time.Minute

type verifier struct {
	secret  []byte
	allowed map[int64]bool
	maxAge  time.Duration
	devUser int64 // 0 = fixture off
	log     *slog.Logger
	now     func() time.Time
}

// newVerifier derives the signing secret once. Per request only one HMAC over
// a few hundred bytes remains, which disappears next to the SQLite query that
// follows — there is deliberately no cache of verified payloads.
func newVerifier(botToken string, cfg Config, logger *slog.Logger, now func() time.Time) *verifier {
	mac := hmac.New(sha256.New, []byte("WebAppData"))
	mac.Write([]byte(botToken))

	allowed := make(map[int64]bool, len(cfg.AllowedUsers))
	for _, id := range cfg.AllowedUsers {
		allowed[id] = true
	}
	return &verifier{
		secret:  mac.Sum(nil),
		allowed: allowed,
		maxAge:  cfg.MaxAge,
		devUser: cfg.devFixtureUser(),
		log:     logger,
		now:     now,
	}
}

// authenticate resolves the Telegram user behind a request. It never logs the
// payload itself: initData carries the user's name and username.
func (v *verifier) authenticate(r *http.Request) (int64, *apiError) {
	raw := initDataFromHeader(r.Header.Get("Authorization"))

	if raw == "" && v.devUser != 0 {
		// The fixture skips signature verification only — the injected id still
		// has to be on the allowlist, so the worst it can grant is access
		// somebody already has.
		if !v.allowed[v.devUser] {
			return 0, errForbidden
		}
		return v.devUser, nil
	}
	if raw == "" {
		return 0, errBadInitData
	}

	userID, err := v.parse(raw)
	if err != nil {
		v.log.Warn("mini: init data rejected", "reason", err.Error(), "path", r.URL.Path)
		return 0, errBadInitData
	}
	if !v.allowed[userID] {
		v.log.Warn("mini: user not allowed", "user_id", userID, "path", r.URL.Path)
		return 0, errForbidden
	}
	return userID, nil
}

// initDataFromHeader extracts the payload from "Authorization: tma <initData>".
// The scheme match is case-insensitive; the payload is returned verbatim
// because its percent-encoding is part of the signed material.
func initDataFromHeader(h string) string {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(h), " ")
	if !ok || !strings.EqualFold(scheme, "tma") {
		return ""
	}
	return strings.TrimSpace(rest)
}

type initDataError string

func (e initDataError) Error() string { return string(e) }

func (v *verifier) parse(raw string) (int64, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return 0, initDataError("malformed query string")
	}

	gotHash := values.Get("hash")
	if gotHash == "" {
		return 0, initDataError("missing hash")
	}

	// Every received field except the two signature fields, sorted, joined by
	// newlines. The published algorithm names only "hash" because it predates
	// "signature" — that one is the Ed25519 third-party signature and is
	// likewise not part of its own check string.
	keys := make([]string, 0, len(values))
	for k := range values {
		if k == "hash" || k == "signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var check strings.Builder
	for i, k := range keys {
		if i > 0 {
			check.WriteByte('\n')
		}
		check.WriteString(k)
		check.WriteByte('=')
		check.WriteString(values.Get(k))
	}

	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(check.String()))
	want, err := hex.DecodeString(gotHash)
	if err != nil || subtle.ConstantTimeCompare(mac.Sum(nil), want) != 1 {
		return 0, initDataError("signature mismatch")
	}

	// Telegram does not refresh initData while the Mini App stays open, so the
	// freshness window doubles as the session lifetime.
	unix, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil {
		return 0, initDataError("bad auth_date")
	}
	switch age := v.now().Sub(time.Unix(unix, 0)); {
	case age > v.maxAge:
		return 0, initDataError("stale auth_date")
	case age < -maxClockSkew:
		return 0, initDataError("auth_date in the future")
	}

	var user struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID == 0 {
		return 0, initDataError("no user in init data")
	}
	return user.ID, nil
}

// ParseUserIDs reads a comma-separated allowlist of Telegram user ids,
// skipping and logging anything unparseable rather than failing the boot.
func ParseUserIDs(s string, logger *slog.Logger) []int64 {
	var out []int64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			logger.Warn("mini: bad user id in allowlist", "value", part)
			continue
		}
		out = append(out, id)
	}
	return out
}
