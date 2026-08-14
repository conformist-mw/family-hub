package mini

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"familyhub/internal/model"
)

func jsonRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

const validBody = `{"title":"Ортодонт","person":"Демид","location":"Хрещатик 1",
	"date":"2026-08-10","time":"14:30","endTime":"15:30","status":"planned",
	"note":"взяти картку","cost":"800"}`

func TestCreateAppointment(t *testing.T) {
	st := testStore(t)
	h := testRouter(t, st, []int64{42}, 42)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPost, "/mini/api/appointments", validBody))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == 0 {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}

	got, err := st.GetAppointment(created.ID)
	if err != nil {
		t.Fatalf("stored row not found: %v", err)
	}
	if got.Title != "Ортодонт" || got.StartsAt != "2026-08-10T14:30" || got.EndsAt != "2026-08-10T15:30" {
		t.Errorf("stored = %+v", got)
	}
	if got.Cost == nil || *got.Cost != 800 {
		t.Errorf("cost = %v, want 800", got.Cost)
	}
}

func TestUpdateAppointment(t *testing.T) {
	st := testStore(t)
	seed, err := st.CreateAppointment(model.Appointment{
		StartsAt: "2026-08-10T09:00", Title: "Було", Person: "Єгор",
		Status: model.ApptStatusPlanned, Raw: "з вільного тексту",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := testRouter(t, st, []int64{42}, 42)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPut, "/mini/api/appointments/"+itoa(seed.ID), validBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	got, err := st.GetAppointment(seed.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Ортодонт" || got.StartsAt != "2026-08-10T14:30" {
		t.Errorf("not updated: %+v", got)
	}
	// The captured source text is not the form's to erase.
	if got.Raw != "з вільного тексту" {
		t.Errorf("raw = %q, want the capture preserved", got.Raw)
	}
}

func TestDeleteAppointmentIsSoft(t *testing.T) {
	st := testStore(t)
	seed, err := st.CreateAppointment(model.Appointment{
		StartsAt: "2026-08-10T09:00", Title: "Зайве", Status: model.ApptStatusPlanned,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := testRouter(t, st, []int64{42}, 42)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/mini/api/appointments/"+itoa(seed.ID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	// Gone from the list, still on disk for the Home Assistant outbox.
	items, err := st.UpcomingAppointments("2026-08-01T00:00", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, it := range items {
		if it.ID == seed.ID {
			t.Fatal("deleted appointment still listed")
		}
	}
	if _, err := st.GetAppointment(seed.ID); err != nil {
		t.Fatalf("row hard-deleted: %v", err)
	}
}

func TestWriteValidationIsReported(t *testing.T) {
	st := testStore(t)
	h := testRouter(t, st, []int64{42}, 42)

	cases := []struct {
		name  string
		body  string
		field string
	}{
		{"no title", `{"title":"","date":"2026-08-10","time":"14:30","status":"planned"}`, "title"},
		{"bad date", `{"title":"X","date":"10.08.2026","time":"14:30","status":"planned"}`, "date"},
		{"end before start", `{"title":"X","date":"2026-08-10","time":"14:30","endTime":"13:00","status":"planned"}`, "endTime"},
		{"bad status", `{"title":"X","date":"2026-08-10","time":"14:30","status":"maybe"}`, "status"},
		{"bad cost", `{"title":"X","date":"2026-08-10","time":"14:30","status":"planned","cost":"багато"}`, "cost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, jsonRequest(http.MethodPost, "/mini/api/appointments", tc.body))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body)
			}
			var body struct {
				Error struct {
					Code, Message, Field string
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Error.Code != "validation" || body.Error.Field != tc.field || body.Error.Message == "" {
				t.Fatalf("error = %+v, want field %q", body.Error, tc.field)
			}
		})
	}
}

// A client typo must fail loudly rather than save a half-filled appointment.
func TestWriteRejectsUnknownFields(t *testing.T) {
	h := testRouter(t, testStore(t), []int64{42}, 42)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPost, "/mini/api/appointments",
		`{"title":"X","date":"2026-08-10","time":"14:30","status":"planned","titel":"typo"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
}

func TestWriteOnMissingAppointment(t *testing.T) {
	h := testRouter(t, testStore(t), []int64{42}, 42)

	for _, r := range []*http.Request{
		jsonRequest(http.MethodPut, "/mini/api/appointments/999", validBody),
		httptest.NewRequest(http.MethodDelete, "/mini/api/appointments/999", nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s -> %d, want 404", r.Method, rec.Code)
		}
	}
}

// Every write is behind the same allowlist as the read.
func TestWritesRequireAuthentication(t *testing.T) {
	st := testStore(t)
	h := testRouter(t, st, []int64{42}, 0) // fixture off

	for _, r := range []*http.Request{
		jsonRequest(http.MethodPost, "/mini/api/appointments", validBody),
		jsonRequest(http.MethodPut, "/mini/api/appointments/1", validBody),
		httptest.NewRequest(http.MethodDelete, "/mini/api/appointments/1", nil),
		httptest.NewRequest(http.MethodGet, "/mini/api/persons", nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		assertAPIError(t, rec, http.StatusBadRequest, "bad_init_data")
	}

	// Nothing reached the database.
	items, err := st.UpcomingAppointments("2000-01-01T00:00", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("unauthenticated write stored %d rows", len(items))
	}
}

// group stands in for the family chat.
type group struct{ sent []string }

func (g *group) NotifyHTML(text string) error {
	g.sent = append(g.sent, text)
	return nil
}

// signedRequest launches the app as a real user would, so the byline has a name
// to carry — the dev fixture has no initData and therefore no name.
func signedRequest(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	r := jsonRequest(method, path, body)
	r.Header.Set("Authorization",
		"tma "+signInitData(t, testToken, launchData(t, 42, testNow.Add(-time.Minute))))
	return r
}

// A visit added on a phone is invisible to the rest of the family until the
// group hears about it.
func TestWritesReachTheFamilyGroup(t *testing.T) {
	st := testStore(t)
	fam := &group{}
	h := testRouterWithNotifier(t, st, []int64{42}, 0, fam)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, http.MethodPost, "/mini/api/appointments", validBody))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body)
	}
	if len(fam.sent) != 1 {
		t.Fatalf("group got %d messages, want 1", len(fam.sent))
	}
	// Attributed to whoever opened the Mini App, the way the bot attributes a
	// capture in a private chat.
	if !strings.Contains(fam.sent[0], "🆕 Новий візит (Тест)") || !strings.Contains(fam.sent[0], "Ортодонт") {
		t.Errorf("group message = %q", fam.sent[0])
	}

	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, http.MethodDelete, "/mini/api/appointments/"+itoa(created.ID), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: status = %d, body = %s", rec.Code, rec.Body)
	}
	if len(fam.sent) != 2 || !strings.Contains(fam.sent[1], "🗑 Візит видалено (Тест)") {
		t.Errorf("group messages = %q", fam.sent)
	}
}

func TestPersonsSuggestions(t *testing.T) {
	st := testStore(t)
	h := testRouter(t, st, []int64{42}, 42)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mini/api/persons", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var body struct {
		Persons []string `json:"persons"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The seed migration ships the family, so this is never empty in practice;
	// the contract under test is the shape, not the contents.
	if body.Persons == nil {
		t.Fatal("persons key missing from the response")
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
