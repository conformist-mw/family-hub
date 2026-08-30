package store_test

import (
	"testing"
	"time"

	"familyhub/internal/model"
)

// A slot and its first version have to arrive together: a slot with no version
// is invisible to every read, so a half-written pair would be a course that
// silently stopped existing.
func TestANewSlotIsReadableAsItStandsToday(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingPerLesson})
	if err := st.CreateSlot(id, int(time.Tuesday), "13:35", 45, "2000-01-01T00:00"); err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}

	slots, err := st.ListSlots(id)
	if err != nil {
		t.Fatalf("ListSlots: %v", err)
	}
	if len(slots) != 1 {
		t.Fatalf("got %d slots, want 1", len(slots))
	}
	if slots[0].Weekday != int(time.Tuesday) || slots[0].Time != "13:35" || slots[0].DurationMin != 45 {
		t.Fatalf("slot = %+v", slots[0])
	}
	got, err := st.GetSlot(slots[0].ID)
	if err != nil {
		t.Fatalf("GetSlot: %v", err)
	}
	if got.Time != "13:35" {
		t.Fatalf("GetSlot = %+v", got)
	}
}

// The whole point of #53's second half: the editor shows the new schedule, and
// the old one is still on the record.
func TestMovingASlotKeepsWhatItUsedToBe(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingPerLesson})
	if err := st.CreateSlot(id, int(time.Tuesday), "13:35", 60, "2000-01-01T00:00"); err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	slots, _ := st.ListSlots(id)
	slotID := slots[0].ID

	// Effective in the past, so "as it stands today" picks it up.
	if err := st.AddSlotVersion(slotID, "2026-01-01T00:00", int(time.Thursday), "17:00", 45); err != nil {
		t.Fatalf("AddSlotVersion: %v", err)
	}

	now, err := st.ListSlots(id)
	if err != nil {
		t.Fatalf("ListSlots: %v", err)
	}
	if len(now) != 1 {
		t.Fatalf("a moved slot became %d slots — the id is the identity", len(now))
	}
	if now[0].ID != slotID {
		t.Fatalf("the slot id changed from %d to %d", slotID, now[0].ID)
	}
	if now[0].Weekday != int(time.Thursday) || now[0].Time != "17:00" {
		t.Fatalf("today's schedule = %+v", now[0])
	}

	versions, err := st.VersionsFor(slotID)
	if err != nil {
		t.Fatalf("VersionsFor: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("got %d versions, want the old one and the new", len(versions))
	}
	if versions[0].Time != "13:35" || versions[0].Weekday != int(time.Tuesday) {
		t.Fatalf("the original schedule was rewritten: %+v", versions[0])
	}
	if versions[1].Time != "17:00" {
		t.Fatalf("newest version = %+v", versions[1])
	}
}

// A version stamped in the future is not yet in force, so today's reads must
// not show it.
func TestAScheduledChangeIsNotVisibleYet(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingPerLesson})
	if err := st.CreateSlot(id, int(time.Tuesday), "13:35", 60, "2000-01-01T00:00"); err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	slots, _ := st.ListSlots(id)
	future := time.Now().AddDate(1, 0, 0).Format(model.LocalDatetime)
	if err := st.AddSlotVersion(slots[0].ID, future, int(time.Friday), "20:00", 60); err != nil {
		t.Fatalf("AddSlotVersion: %v", err)
	}

	now, _ := st.ListSlots(id)
	if now[0].Time != "13:35" {
		t.Fatalf("a change that has not taken effect is already showing: %+v", now[0])
	}
	versions, _ := st.VersionsFor(slots[0].ID)
	if len(versions) != 2 {
		t.Fatalf("the scheduled change was not recorded: %d versions", len(versions))
	}
}

// Saving twice inside the same minute is the person correcting the first save,
// not a collision to fail at them.
func TestSavingTwiceInTheSameMinuteAmendsRatherThanFails(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingPerLesson})
	if err := st.CreateSlot(id, int(time.Tuesday), "13:35", 60, "2000-01-01T00:00"); err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	slots, _ := st.ListSlots(id)
	slotID := slots[0].ID

	if err := st.AddSlotVersion(slotID, "2026-01-01T10:00", int(time.Thursday), "17:00", 60); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := st.AddSlotVersion(slotID, "2026-01-01T10:00", int(time.Thursday), "17:30", 60); err != nil {
		t.Fatalf("second save in the same minute: %v", err)
	}

	versions, _ := st.VersionsFor(slotID)
	if len(versions) != 2 {
		t.Fatalf("got %d versions, want the original plus one corrected", len(versions))
	}
	if versions[1].Time != "17:30" {
		t.Fatalf("the correction did not stick: %+v", versions[1])
	}
}

// Amend rewrites the past on purpose — the schedule was entered wrong.
func TestAmendCorrectsAVersionInPlace(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingPerLesson})
	if err := st.CreateSlot(id, int(time.Tuesday), "13:53", 60, "2000-01-01T00:00"); err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	slots, _ := st.ListSlots(id)
	versions, _ := st.VersionsFor(slots[0].ID)

	if err := st.AmendSlotVersion(versions[0].ID, int(time.Tuesday), "13:35", 60); err != nil {
		t.Fatalf("AmendSlotVersion: %v", err)
	}
	after, _ := st.VersionsFor(slots[0].ID)
	if len(after) != 1 {
		t.Fatalf("an amend created a version: %d", len(after))
	}
	if after[0].Time != "13:35" {
		t.Fatalf("the typo survives: %+v", after[0])
	}
}

// The feed reads histories, and every active slot has to arrive with its
// versions attached — a slot whose versions went missing renders as a course
// that never happens.
func TestSlotHistoriesCarryEveryVersion(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingPerLesson})
	if err := st.CreateSlot(id, int(time.Tuesday), "13:35", 60, "2000-01-01T00:00"); err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	slots, _ := st.ListSlots(id)
	if err := st.AddSlotVersion(slots[0].ID, "2026-01-01T00:00", int(time.Thursday), "17:00", 45); err != nil {
		t.Fatalf("AddSlotVersion: %v", err)
	}

	histories, err := st.SlotHistories()
	if err != nil {
		t.Fatalf("SlotHistories: %v", err)
	}
	if len(histories) != 1 {
		t.Fatalf("got %d histories, want 1", len(histories))
	}
	h := histories[0]
	if h.SlotID != slots[0].ID {
		t.Fatalf("history is for slot %d, want %d", h.SlotID, slots[0].ID)
	}
	if len(h.Versions) != 2 {
		t.Fatalf("got %d versions, want 2", len(h.Versions))
	}
	// Oldest first, which is what the expansion relies on.
	if h.Versions[0].ValidFromAt > h.Versions[1].ValidFromAt {
		t.Fatalf("versions are not oldest first: %+v", h.Versions)
	}
	if h.Enrollment.ID != id {
		t.Fatalf("history lost its enrollment: %+v", h.Enrollment)
	}
}

// Deleting a slot takes its versions with it rather than orphaning them.
func TestDeletingASlotTakesItsVersions(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingPerLesson})
	if err := st.CreateSlot(id, int(time.Tuesday), "13:35", 60, "2000-01-01T00:00"); err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	slots, _ := st.ListSlots(id)
	if err := st.DeleteSlot(slots[0].ID); err != nil {
		t.Fatalf("DeleteSlot: %v", err)
	}
	versions, err := st.VersionsFor(slots[0].ID)
	if err != nil {
		t.Fatalf("VersionsFor: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("%d versions outlived their slot", len(versions))
	}
	if h, _ := st.SlotHistories(); len(h) != 0 {
		t.Fatalf("the deleted slot still reaches the feed")
	}
}
