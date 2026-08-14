package mini

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testToken = "123456:test-bot-token"

var testNow = time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// signInitData builds a payload Telegram would have signed with testToken.
// It mirrors the documented algorithm rather than calling the code under test.
func signInitData(t *testing.T, token string, v url.Values) string {
	t.Helper()

	keys := make([]string, 0, len(v))
	for k := range v {
		if k == "hash" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + v.Get(k)
	}

	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(parts, "\n")))

	signed := url.Values{}
	for k, vals := range v {
		signed[k] = vals
	}
	signed.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return signed.Encode()
}

func launchData(t *testing.T, userID int64, authDate time.Time) url.Values {
	t.Helper()
	return url.Values{
		"auth_date": {strconv.FormatInt(authDate.Unix(), 10)},
		"query_id":  {"AAF_test"},
		"user":      {`{"id":` + strconv.FormatInt(userID, 10) + `,"first_name":"Тест","username":"tester"}`},
	}
}

func testVerifier(allowed []int64, devUser int64, webhookURL string) *verifier {
	cfg := Config{
		AllowedUsers: allowed,
		MaxAge:       DefaultMaxAge,
		DevUser:      devUser,
		WebhookURL:   webhookURL,
	}
	return newVerifier(testToken, cfg, discardLogger(), func() time.Time { return testNow })
}

func request(initData string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/mini/api/appointments", nil)
	if initData != "" {
		r.Header.Set("Authorization", "tma "+initData)
	}
	return r
}

func TestAuthenticateValidInitData(t *testing.T) {
	v := testVerifier([]int64{42}, 0, "")
	raw := signInitData(t, testToken, launchData(t, 42, testNow.Add(-time.Minute)))

	got, err := v.authenticate(request(raw))
	if err != nil {
		t.Fatalf("authenticate: %+v", err)
	}
	if got.ID != 42 {
		t.Fatalf("user id = %d, want 42", got.ID)
	}
	// The name rides along so a Mini App write can be attributed in the group.
	if got.Name != "Тест" {
		t.Fatalf("name = %q, want the first_name from initData", got.Name)
	}
}

// Telegram omits first_name for an account that has none; the username is the
// next best byline, and neither missing is still a valid launch.
func TestAuthenticateNameFallsBackToUsername(t *testing.T) {
	v := testVerifier([]int64{42}, 0, "")
	data := launchData(t, 42, testNow.Add(-time.Minute))
	data.Set("user", `{"id":42,"username":"tester"}`)

	got, err := v.authenticate(request(signInitData(t, testToken, data)))
	if err != nil {
		t.Fatalf("authenticate: %+v", err)
	}
	if got.Name != "tester" {
		t.Fatalf("name = %q, want the username", got.Name)
	}
}

func TestAuthenticateRejects(t *testing.T) {
	fresh := func() url.Values { return launchData(t, 42, testNow.Add(-time.Minute)) }

	cases := []struct {
		name     string
		initData func() string
		want     *apiError
	}{
		{
			name:     "no header at all",
			initData: func() string { return "" },
			want:     errBadInitData,
		},
		{
			name: "tampered payload keeps old hash",
			initData: func() string {
				raw := signInitData(t, testToken, fresh())
				return strings.Replace(raw, "tester", "hacker", 1)
			},
			want: errBadInitData,
		},
		{
			name:     "signed with a different bot token",
			initData: func() string { return signInitData(t, "999:other-token", fresh()) },
			want:     errBadInitData,
		},
		{
			name: "hash missing",
			initData: func() string {
				v := fresh()
				return v.Encode()
			},
			want: errBadInitData,
		},
		{
			name: "hash is not hex",
			initData: func() string {
				v := fresh()
				v.Set("hash", "zzzz")
				return v.Encode()
			},
			want: errBadInitData,
		},
		{
			name: "auth_date older than the window",
			initData: func() string {
				return signInitData(t, testToken, launchData(t, 42, testNow.Add(-DefaultMaxAge-time.Minute)))
			},
			want: errBadInitData,
		},
		{
			name: "auth_date far in the future",
			initData: func() string {
				return signInitData(t, testToken, launchData(t, 42, testNow.Add(time.Hour)))
			},
			want: errBadInitData,
		},
		{
			name: "no user object",
			initData: func() string {
				v := fresh()
				v.Del("user")
				return signInitData(t, testToken, v)
			},
			want: errBadInitData,
		},
		{
			name: "valid signature, user not on the allowlist",
			initData: func() string {
				return signInitData(t, testToken, launchData(t, 777, testNow.Add(-time.Minute)))
			},
			want: errForbidden,
		},
	}

	v := testVerifier([]int64{42}, 0, "")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := v.authenticate(request(tc.initData()))
			if err != tc.want {
				t.Fatalf("error = %+v, want %+v", err, tc.want)
			}
		})
	}
}

// An auth_date at the very edge of the window is still good: the boundary is
// "older than", not "as old as".
func TestAuthenticateAcceptsEdgeOfWindow(t *testing.T) {
	v := testVerifier([]int64{42}, 0, "")
	raw := signInitData(t, testToken, launchData(t, 42, testNow.Add(-DefaultMaxAge)))
	if _, err := v.authenticate(request(raw)); err != nil {
		t.Fatalf("edge of window rejected: %+v", err)
	}
}

// Telegram's newer "signature" field (Ed25519, for third-party validation) IS
// part of the HMAC check string — only "hash" is excluded. Widely repeated
// advice says to strip both; that describes the Ed25519 flow, and following it
// rejects every real payload. Confirmed against a live bot.
func TestSignatureFieldIsPartOfCheckString(t *testing.T) {
	v := testVerifier([]int64{42}, 0, "")

	data := launchData(t, 42, testNow.Add(-time.Minute))
	data.Set("signature", "bXktZWQyNTUxOS1zaWc")

	if _, err := v.authenticate(request(signInitData(t, testToken, data))); err != nil {
		t.Fatalf("real-shaped payload with a signature field rejected: %+v", err)
	}

	// Pin the direction: a hash computed without the signature field must not
	// be accepted for a payload that carries one. Without this, excluding
	// "signature" again would leave every test green.
	unsigned := launchData(t, 42, testNow.Add(-time.Minute))
	raw := signInitData(t, testToken, unsigned) + "&signature=" + url.QueryEscape("bXktZWQyNTUxOS1zaWc")
	if _, err := v.authenticate(request(raw)); err != errBadInitData {
		t.Fatalf("hash that ignored the signature field was accepted: %+v", err)
	}
}

func TestAuthorizationSchemeIsRequired(t *testing.T) {
	v := testVerifier([]int64{42}, 0, "")
	raw := signInitData(t, testToken, launchData(t, 42, testNow.Add(-time.Minute)))

	for _, header := range []string{raw, "Bearer " + raw, "tmaX " + raw} {
		r := httptest.NewRequest(http.MethodGet, "/mini/api/appointments", nil)
		r.Header.Set("Authorization", header)
		if _, err := v.authenticate(r); err != errBadInitData {
			t.Fatalf("header %q accepted, want rejection", header)
		}
	}

	// The scheme itself is case-insensitive, as HTTP auth schemes are.
	r := httptest.NewRequest(http.MethodGet, "/mini/api/appointments", nil)
	r.Header.Set("Authorization", "TMA "+raw)
	if _, err := v.authenticate(r); err != nil {
		t.Fatalf("uppercase scheme rejected: %+v", err)
	}
}

func TestDevFixture(t *testing.T) {
	t.Run("accepts unsigned requests locally", func(t *testing.T) {
		v := testVerifier([]int64{42}, 42, "")
		got, err := v.authenticate(request(""))
		if err != nil {
			t.Fatalf("fixture rejected: %+v", err)
		}
		if got.ID != 42 {
			t.Fatalf("user id = %d, want 42", got.ID)
		}
	})

	t.Run("inert once a webhook is configured", func(t *testing.T) {
		v := testVerifier([]int64{42}, 42, "https://example.com/hook")
		if _, err := v.authenticate(request("")); err != errBadInitData {
			t.Fatalf("fixture active in production: err = %+v", err)
		}
	})

	t.Run("still subject to the allowlist", func(t *testing.T) {
		v := testVerifier([]int64{42}, 777, "")
		if _, err := v.authenticate(request("")); err != errForbidden {
			t.Fatalf("fixture bypassed the allowlist: err = %+v", err)
		}
	})

	t.Run("does not weaken a real payload", func(t *testing.T) {
		v := testVerifier([]int64{42}, 42, "")
		raw := signInitData(t, "999:other-token", launchData(t, 42, testNow.Add(-time.Minute)))
		if _, err := v.authenticate(request(raw)); err != errBadInitData {
			t.Fatalf("bad signature accepted while fixture on: err = %+v", err)
		}
	})
}

func TestParseUserIDs(t *testing.T) {
	got := ParseUserIDs(" 42, 777 ,,oops, -5 ", discardLogger())
	want := []int64{42, 777, -5}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
