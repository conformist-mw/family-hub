package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"familyhub/internal/db"
	"familyhub/internal/store"
)

func logApp(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewRouter(database, slog.New(slog.NewTextHandler(io.Discard, nil)), "", nil, nil, nil, nil),
		store.New(database)
}

func TestAnEntryIsWrittenAndFoundByItsTag(t *testing.T) {
	h, st := logApp(t)

	rec := post(t, h, "/log", url.Values{
		"date": {"2026-02-09"}, "text": {"Генератор: 30 л газу\n190,2 мч, 1150 ₴"},
		"tags": {"#Генератор, газ"}, "filter": {"генератор"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /log = %d\n%s", rec.Code, rec.Body.String())
	}
	// Written from a filtered list, it goes back to that list.
	if loc := rec.Header().Get("Location"); loc != "/log?tag="+url.QueryEscape("генератор") {
		t.Fatalf("Location = %q", loc)
	}

	entries, _ := st.ListLogEntries("генератор")
	if len(entries) != 1 || entries[0].Text != "Генератор: 30 л газу\n190,2 мч, 1150 ₴" {
		t.Fatalf("stored = %+v", entries)
	}

	body := get(t, h, "/log?tag="+url.QueryEscape("газ")).Body.String()
	if !strings.Contains(body, "190,2 мч") {
		t.Fatal("the entry is missing from its tag's list")
	}
	// The form under a filter starts with that tag filled in.
	if !strings.Contains(body, `name="tags" id="log-tags" value="газ"`) {
		t.Fatal("the filtered page's form does not carry the tag")
	}
	if body := get(t, h, "/log?tag=вода").Body.String(); strings.Contains(body, "190,2 мч") {
		t.Fatal("the entry shows under a tag it does not have")
	}
}

// A rejected form keeps what was typed: an entry that vanishes on a missing
// date has to be written again from memory, which is the thing this page
// exists to avoid.
func TestARejectedEntryKeepsItsText(t *testing.T) {
	h, st := logApp(t)
	rec := post(t, h, "/log", url.Values{"date": {""}, "text": {"Поміняв масло"}, "tags": {"авто"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST without a date = %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Поміняв масло") || !strings.Contains(body, `value="авто"`) {
		t.Fatalf("the typed values are lost:\n%s", body)
	}
	if rec := post(t, h, "/log", url.Values{"date": {"2026-02-09"}, "text": {"   "}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST with blank text = %d", rec.Code)
	}
	if entries, _ := st.ListLogEntries(""); len(entries) != 0 {
		t.Fatalf("a rejected entry was stored: %+v", entries)
	}
}

func TestAnEntryCanBeEditedAndDeleted(t *testing.T) {
	h, st := logApp(t)
	post(t, h, "/log", url.Values{"date": {"2026-01-09"}, "text": {"газ"}, "tags": {"генератор"}})
	entries, _ := st.ListLogEntries("")
	id := strconv.FormatInt(entries[0].ID, 10)

	if body := get(t, h, "/log/"+id+"/edit").Body.String(); !strings.Contains(body, `value="генератор"`) {
		t.Fatal("the edit form does not show the entry's tags")
	}
	rec := post(t, h, "/log/"+id, url.Values{"date": {"2026-01-29"}, "text": {"Генератор: 30 л"}, "tags": {"генератор газ"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update = %d\n%s", rec.Code, rec.Body.String())
	}
	got, _ := st.GetLogEntry(entries[0].ID)
	if got.Date != "2026-01-29" || got.Text != "Генератор: 30 л" || len(got.Tags) != 2 {
		t.Fatalf("after update = %+v", got)
	}

	if rec := post(t, h, "/log/"+id+"/delete", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d", rec.Code)
	}
	if rec := get(t, h, "/log/"+id+"/edit"); rec.Code != http.StatusNotFound {
		t.Fatalf("edit of a deleted entry = %d", rec.Code)
	}
	if rec := post(t, h, "/log/"+id, url.Values{"date": {"2026-01-29"}, "text": {"x"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("update of a deleted entry = %d", rec.Code)
	}
}
