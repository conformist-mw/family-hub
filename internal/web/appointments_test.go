package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The byline on a group notification. oauth2-proxy may or may not be configured
// to forward the identity it verified, so the fallback has to be honest about
// where the change came from rather than guess at a name.
func TestActorName(t *testing.T) {
	cases := []struct {
		name   string
		header map[string]string
		want   string
	}{
		{"nothing forwarded", nil, "веб"},
		{"preferred username wins", map[string]string{
			"X-Forwarded-Preferred-Username": "oleh",
			"X-Forwarded-Email":              "oleh@example.com",
		}, "oleh"},
		{"email is cut to its local part", map[string]string{
			"X-Forwarded-Email": "oleh@example.com",
		}, "oleh"},
		{"blank header is not an identity", map[string]string{
			"X-Forwarded-User": "   ",
		}, "веб"},
	}
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
