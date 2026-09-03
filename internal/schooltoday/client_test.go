package schooltoday

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// loggedInClient logs into a fake portal and hands back the authenticated
// client, so each test starts where the real collector does.
func loggedInClient(t *testing.T, portal *fakePortal) *Client {
	t.Helper()
	srv := httptest.NewServer(portal.handler())
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL)
	if err := c.Login(context.Background(), validEmail, validPassword); err != nil {
		t.Fatalf("login: %v", err)
	}
	return c
}

// The event's own type has to reach the endpoint: the portal keys the detail
// page on (lessonID, lessonType) and answers 404 for the wrong pair, so a
// hardcoded type would silently lose every lesson of another kind.
func TestLessonViewSendsTheEventTypeItWasGiven(t *testing.T) {
	portal := &fakePortal{dataset: weekOneDataset}
	c := loggedInClient(t, portal)

	body, err := c.LessonView(context.Background(), 11714619, 1)
	if err != nil {
		t.Fatalf("lesson view: %v", err)
	}
	if !bytes.Contains(body, []byte("Нотатки")) {
		t.Fatal("fixture did not come back")
	}
	if len(portal.lessonCalls) != 1 {
		t.Fatalf("got %d calls, want 1", len(portal.lessonCalls))
	}
	if got := portal.lessonCalls[0]; got.id != 11714619 || got.kind != 1 {
		t.Fatalf("called with %+v, want {11714619 1}", got)
	}
}

// A 404 is the portal's ordinary answer for a meal or after-school block, not
// a failure. It gets its own error so the collector can skip it quietly rather
// than reporting a fortnight of phantom outages.
func TestLessonViewReportsANonLessonSeparately(t *testing.T) {
	portal := &fakePortal{
		dataset:      weekOneDataset,
		lessonStatus: map[int64]int{11714608: http.StatusNotFound},
	}
	c := loggedInClient(t, portal)

	_, err := c.LessonView(context.Background(), 11714608, 0)
	if !errors.Is(err, ErrNotALesson) {
		t.Fatalf("err = %v, want ErrNotALesson", err)
	}
}

// A real outage must stay distinguishable from the above, or a portal that is
// down reads as a week with no lessons in it.
func TestLessonViewReportsAServerErrorAsAFailure(t *testing.T) {
	portal := &fakePortal{
		dataset:      weekOneDataset,
		lessonStatus: map[int64]int{11714596: http.StatusInternalServerError},
	}
	c := loggedInClient(t, portal)

	_, err := c.LessonView(context.Background(), 11714596, 1)
	if err == nil {
		t.Fatal("a 500 came back as success")
	}
	if errors.Is(err, ErrNotALesson) {
		t.Fatal("a 500 was mistaken for a non-lesson")
	}
}

// The portal never answers 401: the auth filter redirects, the client follows,
// and the login form arrives as a 200. Without the check that page would be
// parsed as a lesson with no topic, no homework and no marks — an empty review
// that looks exactly like a quiet week.
func TestLessonViewDetectsTheLoginFormServedAsSuccess(t *testing.T) {
	portal := &fakePortal{
		dataset: weekOneDataset,
		lessonBody: []byte(
			`<form><input name="__RequestVerificationToken" type="hidden" value="TOK123" /></form>`),
	}
	c := loggedInClient(t, portal)

	_, err := c.LessonView(context.Background(), 11714596, 1)
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("err = %v, want ErrSessionExpired", err)
	}
}

// A cancelled context has to reach the request: the Friday collect makes ~29 of
// these in a row and shutdown must not wait for all of them.
func TestLessonViewHonoursContextCancellation(t *testing.T) {
	portal := &fakePortal{dataset: weekOneDataset}
	c := loggedInClient(t, portal)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.LessonView(ctx, 11714596, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
