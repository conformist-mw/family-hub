package web

import (
	"testing"

	"familyhub/internal/model"
)

func TestNoticeRoundTripsThroughTheForm(t *testing.T) {
	cases := []struct {
		value int
		unit  string
		min   int
	}{
		{2, "hour", 120},
		{5, "day", 7200},
		{30, "min", 30},
		{1, "day", 1440},
		{0, "min", 0},
	}
	for _, c := range cases {
		if got := noticeMinutes(c.value, c.unit); got != c.min {
			t.Errorf("%d %s: got %d minutes, want %d", c.value, c.unit, got, c.min)
		}
	}
}

// The form should show the unit a person would have typed, not raw minutes.
func TestSplitNoticePicksTheLargestWholeUnit(t *testing.T) {
	cases := []struct {
		min   int
		value int
		unit  string
	}{
		{7200, 5, "day"}, // not 120 hours
		{1440, 1, "day"}, // not 24 hours
		{120, 2, "hour"}, // not 120 min
		{90, 90, "min"},  // an hour and a half is neither whole hours nor days
		{45, 45, "min"},
		{0, 0, "min"},
	}
	for _, c := range cases {
		value, unit := splitNotice(c.min)
		if value != c.value || unit != c.unit {
			t.Errorf("%d min: got %d %s, want %d %s", c.min, value, unit, c.value, c.unit)
		}
	}
}

// Whatever a person picks must survive a save and come back unchanged.
func TestNoticeSurvivesTheRoundTrip(t *testing.T) {
	for _, min := range []int{0, 15, 120, 1440, 7200, 90} {
		value, unit := splitNotice(min)
		if got := noticeMinutes(value, unit); got != min {
			t.Errorf("%d min rendered as %d %s and read back as %d", min, value, unit, got)
		}
	}
}

func TestNoticeDueMatchesTheStoredMinutes(t *testing.T) {
	bal := func(daysLeft, noticeMin int) model.Balance {
		return model.Balance{
			Enrollment: model.Enrollment{
				BillingType: model.BillingMonthly, PaymentNoticeMin: noticeMin,
			},
			CoveredNow: true, DaysLeft: daysLeft,
		}
	}
	if !bal(5, 7200).NoticeDue() {
		t.Error("five days left with a five-day notice should be due")
	}
	if bal(6, 7200).NoticeDue() {
		t.Error("six days left with a five-day notice should not be due")
	}
	if bal(0, 0).NoticeDue() {
		t.Error("a zero notice should never be due")
	}
}
