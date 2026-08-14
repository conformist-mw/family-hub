package appointments

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"familyhub/internal/db"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// recorder is the family group, as far as these tests are concerned.
type recorder struct {
	sent []string
	err  error
}

func (r *recorder) NotifyHTML(text string) error {
	r.sent = append(r.sent, text)
	return r.err
}

func (r *recorder) last(t *testing.T) string {
	t.Helper()
	if len(r.sent) == 0 {
		t.Fatal("nothing was posted to the group")
	}
	return r.sent[len(r.sent)-1]
}

func testService(t *testing.T, notify Notifier) (*Service, *store.Store) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(database)
	return NewService(st, time.UTC, notify, slog.New(slog.NewTextHandler(io.Discard, nil))), st
}

func TestCreateTellsTheGroup(t *testing.T) {
	group := &recorder{}
	svc, _ := testService(t, group)

	f := validForm()
	f.Cost = "800"
	if _, err := svc.Create(f, "Олег"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := group.last(t)
	for _, want := range []string{"🆕 Новий візит", "(Олег)", "Ортодонт", "пн 10 сер, 14:30", "Демид", "800 ₴"} {
		if !strings.Contains(got, want) {
			t.Errorf("group message %q is missing %q", got, want)
		}
	}
}

// A write that never happened must not be announced.
func TestRejectedFormTellsNobody(t *testing.T) {
	group := &recorder{}
	svc, _ := testService(t, group)

	f := validForm()
	f.Title = ""
	if _, err := svc.Create(f, "Олег"); err == nil {
		t.Fatal("Create accepted an empty title")
	}
	if len(group.sent) != 0 {
		t.Fatalf("group told about a rejected write: %q", group.sent)
	}
}

func TestUpdateTellsTheGroup(t *testing.T) {
	group := &recorder{}
	svc, _ := testService(t, group)

	created, err := svc.Create(validForm(), "Олег")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	f := validForm()
	f.Time = "16:00"
	if _, err := svc.Update(created.ID, f, "Ірина"); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got := group.last(t)
	if !strings.Contains(got, "🔄 Візит змінено (Ірина)") || !strings.Contains(got, "16:00") {
		t.Errorf("group message = %q", got)
	}
}

// Cancelling through the form is a cancellation, not an edit — burying it under
// "змінено" is how the group misses that the visit is off.
func TestUpdateToCancelledReadsAsACancellation(t *testing.T) {
	group := &recorder{}
	svc, _ := testService(t, group)

	created, err := svc.Create(validForm(), "Олег")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	f := validForm()
	f.Status = model.ApptStatusCancelled
	if _, err := svc.Update(created.ID, f, "Олег"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := group.last(t); !strings.Contains(got, "✗ Візит скасовано") {
		t.Errorf("group message = %q", got)
	}

	// Editing an already-cancelled visit is an edit again, not a second
	// cancellation.
	f.Note = "передзвонити"
	if _, err := svc.Update(created.ID, f, "Олег"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := group.last(t); !strings.Contains(got, "🔄 Візит змінено") {
		t.Errorf("second edit announced as = %q", got)
	}
}

func TestDeleteTellsTheGroup(t *testing.T) {
	group := &recorder{}
	svc, _ := testService(t, group)

	created, err := svc.Create(validForm(), "Олег")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete(created.ID, "Олег"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := group.last(t); !strings.Contains(got, "🗑 Візит видалено") || !strings.Contains(got, "Ортодонт") {
		t.Errorf("group message = %q", got)
	}
}

// Writing a visit down succeeded; Telegram being unreachable does not undo it.
// Reporting a failure here would invite a second attempt and a duplicate row.
func TestWriteSurvivesAFailedNotification(t *testing.T) {
	group := &recorder{err: errors.New("telegram is down")}
	svc, st := testService(t, group)

	created, err := svc.Create(validForm(), "Олег")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := st.GetAppointment(created.ID); err != nil {
		t.Fatalf("appointment not stored: %v", err)
	}
}

// No bot, or no group configured: the writes still work, silently.
func TestWritesWorkWithoutANotifier(t *testing.T) {
	svc, _ := testService(t, nil)

	created, err := svc.Create(validForm(), "Олег")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Update(created.ID, validForm(), "Олег"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := svc.Delete(created.ID, "Олег"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// Whoever typed it is a person, not a source of markup, and neither is the
// appointment. Telegram rejects a message whose HTML it cannot parse, which
// would mean the group silently missing a visit.
func TestGroupTextEscapesWhatPeopleTyped(t *testing.T) {
	a := model.Appointment{
		StartsAt: "2026-08-10T14:30",
		Title:    "Ремонт <крана> & фарба",
		Person:   "Demid & Co",
	}
	got := GroupAddText([]model.Appointment{a}, "<b>hacker</b>", time.UTC)

	for _, want := range []string{
		"Ремонт &lt;крана&gt; &amp; фарба",
		"Demid &amp; Co",
		"(&lt;b&gt;hacker&lt;/b&gt;)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message %q is missing %q", got, want)
		}
	}
	// The markup this layer owns stays markup.
	if !strings.Contains(got, "<b>Ремонт") {
		t.Errorf("the title lost its own formatting: %q", got)
	}
}

// A surface that cannot name the author says nothing rather than inventing one.
func TestNoBylineWhenTheAuthorIsUnknown(t *testing.T) {
	a := model.Appointment{StartsAt: "2026-08-10T14:30", Title: "Ортодонт"}
	if got := GroupAddText([]model.Appointment{a}, "", time.UTC); !strings.HasPrefix(got, "🆕 Новий візит:") {
		t.Errorf("message = %q", got)
	}
}
