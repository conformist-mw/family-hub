package store_test

import (
	"slices"
	"strings"
	"testing"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

func appointment(title, person, startsAt string) model.Appointment {
	return model.Appointment{Title: title, Person: person, StartsAt: startsAt}
}

// The chips exist so the visit booked again and again is picked rather than
// retyped, so what matters is that the repeated one leads and that nothing
// which would not fit on a chip can reach the row.
func TestFrequentAppointmentValuesRankByUse(t *testing.T) {
	st := testStore(t)
	// The orthodontist every six weeks, against a single haircut booked after
	// all of them: frequency, not recency, is what decides the order.
	for _, d := range []string{"2026-07-06T14:00", "2026-08-17T14:00", "2026-09-28T14:00"} {
		mustCreateAppointment(t, st, appointment("Ортодонт", "Демид", d))
	}
	mustCreateAppointment(t, st, appointment("Перукарня", "Єгор", "2026-10-05T11:00"))
	// Nobody was named — it must not become an empty chip.
	mustCreateAppointment(t, st, appointment("Нотаріус", "", "2026-10-06T09:00"))
	// Too long to be a chip.
	mustCreateAppointment(t, st, appointment(strings.Repeat("я", 41), strings.Repeat("е", 41), "2026-10-07T09:00"))

	titles, err := st.FrequentAppointmentTitles(6)
	if err != nil {
		t.Fatalf("frequent titles: %v", err)
	}
	if want := []string{"Ортодонт", "Нотаріус", "Перукарня"}; !slices.Equal(titles, want) {
		t.Errorf("titles = %q, want %q", titles, want)
	}

	persons, err := st.FrequentAppointmentPersons(6)
	if err != nil {
		t.Fatalf("frequent persons: %v", err)
	}
	if want := []string{"Демид", "Єгор"}; !slices.Equal(persons, want) {
		t.Errorf("persons = %q, want %q", persons, want)
	}
}

// A visit typed by mistake and deleted must stop suggesting itself; one that
// was called off must not, because a cancelled dentist is still the dentist
// this family books.
func TestFrequentAppointmentValuesIgnoreDeletedButNotCancelled(t *testing.T) {
	st := testStore(t)
	typo := mustCreateAppointment(t, st, appointment("Ортадонт", "Демид", "2026-09-01T14:00"))
	called := mustCreateAppointment(t, st, appointment("Масаж", "Демид", "2026-09-02T14:00"))

	if err := st.SoftDeleteAppointment(typo.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := st.SetAppointmentStatus(called.ID, model.ApptStatusCancelled); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	titles, err := st.FrequentAppointmentTitles(6)
	if err != nil {
		t.Fatalf("frequent titles: %v", err)
	}
	if want := []string{"Масаж"}; !slices.Equal(titles, want) {
		t.Errorf("titles = %q, want %q", titles, want)
	}
}

func mustCreateAppointment(t *testing.T, st *store.Store, a model.Appointment) model.Appointment {
	t.Helper()
	got, err := st.CreateAppointment(a)
	if err != nil {
		t.Fatalf("create appointment %q: %v", a.Title, err)
	}
	return got
}
