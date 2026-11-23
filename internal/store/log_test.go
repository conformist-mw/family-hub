package store_test

import (
	"database/sql"
	"errors"
	"slices"
	"testing"

	"familyhub/internal/model"
)

func TestLogEntriesFilterByTagNewestFirst(t *testing.T) {
	st := testStore(t)
	for _, e := range []model.LogEntry{
		{Date: "2025-12-13", Text: "Генератор: 30 л газу, 151,7 мч", Tags: []string{"генератор", "газ"}},
		{Date: "2026-01-09", Text: "Генератор: 35 л газу, 161,6 мч", Tags: []string{"генератор", "газ"}},
		{Date: "2026-01-09", Text: "Поміняв фільтр у колонці", Tags: []string{"вода"}},
		{Date: "2026-01-20", Text: "Без тегів"},
	} {
		if _, err := st.CreateLogEntry(e); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	all, err := st.ListLogEntries("")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var texts []string
	for _, e := range all {
		texts = append(texts, e.Text)
	}
	// Same day: the later-written entry first.
	want := []string{"Без тегів", "Поміняв фільтр у колонці", "Генератор: 35 л газу, 161,6 мч", "Генератор: 30 л газу, 151,7 мч"}
	if !slices.Equal(texts, want) {
		t.Fatalf("order = %q, want %q", texts, want)
	}
	if got := all[2].Tags; !slices.Equal(got, []string{"газ", "генератор"}) {
		t.Fatalf("tags read back = %q, want sorted", got)
	}
	if len(all[0].Tags) != 0 {
		t.Fatalf("untagged entry has tags %q", all[0].Tags)
	}

	gen, err := st.ListLogEntries("генератор")
	if err != nil {
		t.Fatalf("list by tag: %v", err)
	}
	if len(gen) != 2 || gen[0].Date != "2026-01-09" {
		t.Fatalf("by tag = %+v", gen)
	}

	tags, err := st.LogTags()
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	// Most used first; equal counts by tag.
	wantTags := []model.TagCount{{Tag: "газ", Count: 2}, {Tag: "генератор", Count: 2}, {Tag: "вода", Count: 1}}
	if !slices.Equal(tags, wantTags) {
		t.Fatalf("tags = %+v, want %+v", tags, wantTags)
	}
}

func TestLogEntryUpdateReplacesTagsAndDeleteCascades(t *testing.T) {
	st := testStore(t)
	e, err := st.CreateLogEntry(model.LogEntry{Date: "2026-02-09", Text: "газ", Tags: []string{"генератор", "газ"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	e.Text, e.Tags = "Генератор: 30 л газу", []string{"генератор"}
	if err := st.UpdateLogEntry(e); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := st.GetLogEntry(e.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Text != "Генератор: 30 л газу" || !slices.Equal(got.Tags, []string{"генератор"}) {
		t.Fatalf("after update = %+v", got)
	}
	if tags, _ := st.LogTags(); len(tags) != 1 {
		t.Fatalf("a dropped tag is still counted: %+v", tags)
	}

	if err := st.DeleteLogEntry(e.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if tags, _ := st.LogTags(); len(tags) != 0 {
		t.Fatalf("tags outlived their entry: %+v", tags)
	}
	if err := st.DeleteLogEntry(e.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second delete = %v, want ErrNoRows", err)
	}
	if err := st.UpdateLogEntry(e); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("update of a gone entry = %v, want ErrNoRows", err)
	}
}
