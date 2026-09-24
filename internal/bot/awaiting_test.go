package bot

import (
	"testing"
	"time"

	"familyhub/internal/menu"
)

var awaitNow = time.Date(2026, 9, 24, 20, 0, 0, 0, time.UTC)

// The evening check's "Інше" and an appointment edit share one store; what
// keeps a meal answer out of applyEdit is the kind, which onText switches on.
func TestAwaitingKeepsTheKind(t *testing.T) {
	a := newAwaitingStore()
	a.setMealOther(1, plateFor{date: awaitNow.Truncate(24 * time.Hour), meal: menu.Dinner}, awaitNow)
	a.setEdit(2, 42, "title", awaitNow)

	e, ok := a.take(1, awaitNow)
	if !ok || e.kind != awaitMealOther || e.meal != menu.Dinner || e.apptID != 0 {
		t.Fatalf("meal entry = %+v ok=%v", e, ok)
	}
	e, ok = a.take(2, awaitNow)
	if !ok || e.kind != awaitApptEdit || e.apptID != 42 || e.field != "title" {
		t.Fatalf("edit entry = %+v ok=%v", e, ok)
	}
	if _, ok := a.take(1, awaitNow); ok {
		t.Fatal("take did not clear the entry")
	}
}

// A photo answers only the evening check: a pending edit stays armed for the
// text that follows it.
func TestAwaitingPhotoLeavesAnEditAlone(t *testing.T) {
	a := newAwaitingStore()
	a.setEdit(1, 42, "who", awaitNow)
	if _, ok := a.takeMealOther(1, awaitNow); ok {
		t.Fatal("a photo took an appointment edit")
	}
	if e, ok := a.take(1, awaitNow); !ok || e.kind != awaitApptEdit {
		t.Fatalf("edit lost after a photo: %+v ok=%v", e, ok)
	}

	a.setMealOther(1, plateFor{date: awaitNow, meal: menu.Lunch}, awaitNow)
	if e, ok := a.takeMealOther(1, awaitNow); !ok || e.meal != menu.Lunch {
		t.Fatalf("photo answer = %+v ok=%v", e, ok)
	}
}

func TestAwaitingExpires(t *testing.T) {
	a := newAwaitingStore()
	a.setMealOther(1, plateFor{date: awaitNow, meal: menu.Lunch}, awaitNow)
	if _, ok := a.take(1, awaitNow.Add(awaitTTL+time.Second)); ok {
		t.Fatal("a stale question was still answered")
	}
	// A later question replaces the earlier one.
	a.setEdit(1, 7, "time", awaitNow)
	a.setMealOther(1, plateFor{date: awaitNow, meal: menu.Dinner}, awaitNow)
	if e, _ := a.take(1, awaitNow); e.kind != awaitMealOther {
		t.Fatalf("kind = %q, want the latest question", e.kind)
	}
}

// A question asked again after an unreadable answer is asked only once more:
// the retried mark goes through the store with it, and a plate that already
// was a retry has nothing to arm.
func TestPlateForAgainOnlyOnce(t *testing.T) {
	var none *plateFor
	if none.again() != nil {
		t.Fatal("the cooking log's own entry points have no question to re-arm")
	}
	first := &plateFor{date: awaitNow, meal: menu.Dinner}
	again := first.again()
	if again == nil || !again.retried || again.meal != menu.Dinner || !again.date.Equal(awaitNow) {
		t.Fatalf("again = %+v, want the same meal marked retried", again)
	}

	a := newAwaitingStore()
	a.setMealOther(1, *again, awaitNow)
	e, ok := a.take(1, awaitNow)
	if !ok || !e.retried {
		t.Fatalf("entry = %+v ok=%v, want the retried mark kept", e, ok)
	}
	if e.again() != nil {
		t.Fatal("a retried question was armed a third time")
	}
}
