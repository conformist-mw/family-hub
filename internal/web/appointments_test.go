package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The byline on a group notification. tinyauth may or may not have a display
// name for the user, so the fallback has to be honest about where the change
// came from rather than guess at a name.
func TestActorName(t *testing.T) {
	cases := []struct {
		name   string
		header map[string]string
		want   string
	}{
		{"nothing forwarded", nil, "веб"},
		{"display name wins", map[string]string{
			"Remote-Name":  "Олег",
			"Remote-User":  "oleh",
			"Remote-Email": "oleh@example.com",
		}, "Олег"},
		{"username when there is no display name", map[string]string{
			"Remote-User":  "oleh",
			"Remote-Email": "oleh@example.com",
		}, "oleh"},
		{"email is cut to its local part", map[string]string{
			"Remote-Email": "oleh@example.com",
		}, "oleh"},
		{"blank header is not an identity", map[string]string{
			"Remote-User": "   ",
		}, "веб"},
		// tinyauth publishes Remote-Sub for OAuth logins, but the forward-auth
		// middleware does not overwrite it, so it reaches this handler exactly
		// as the browser sent it. Reading it would let a signed-in family
		// member sign somebody else's name to a row.
		{"unsanitised headers are ignored", map[string]string{
			"Remote-Sub":                     "abc123",
			"X-Forwarded-Preferred-Username": "someone-else",
		}, "веб"},
	}
	t.Setenv("WEB_IDENTITY_HEADERS", "")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/appointments", nil)
			for k, v := range tc.header {
				r.Header.Set(k, v)
			}
			if got := actorName(r); got != tc.want {
				t.Errorf("actorName = %q, want %q", got, tc.want)
			}
		})
	}
}

// WEB_IDENTITY_HEADERS is what a different proxy in front of this app is
// configured through, so the names it lists have to win over tinyauth's and a
// blank one has to fall back rather than leave every write anonymous.
func TestActorNameIdentityHeaders(t *testing.T) {
	cases := []struct {
		name   string
		env    string
		header map[string]string
		want   string
	}{
		{"configured names win", "X-Auth-Request-Preferred-Username,X-Auth-Request-Email", map[string]string{
			"X-Auth-Request-Preferred-Username": "oleh",
			"Remote-Name":                       "Олег",
		}, "oleh"},
		{"tinyauth names are not read once another proxy is configured",
			"X-Auth-Request-Preferred-Username", map[string]string{
				"Remote-Name": "Олег",
			}, "веб"},
		{"order is the configured order", "X-Auth-Request-Email,X-Auth-Request-User", map[string]string{
			"X-Auth-Request-User":  "oleh",
			"X-Auth-Request-Email": "oleh@example.com",
		}, "oleh"},
		{"blank falls back to tinyauth", "  ,  ", map[string]string{
			"Remote-User": "oleh",
		}, "oleh"},
		{"unset falls back to tinyauth", "", map[string]string{
			"Remote-User": "oleh",
		}, "oleh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WEB_IDENTITY_HEADERS", tc.env)
			r := httptest.NewRequest(http.MethodPost, "/appointments", nil)
			for k, v := range tc.header {
				r.Header.Set(k, v)
			}
			if got := actorName(r); got != tc.want {
				t.Errorf("actorName = %q, want %q", got, tc.want)
			}
		})
	}
}
