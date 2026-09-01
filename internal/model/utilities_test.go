package model_test

import (
	"testing"

	"familyhub/internal/model"
)

func f(v float64) *float64 { return &v }

func tariff(kind string, rate1 float64, rate2 *float64) model.Tariff {
	return model.Tariff{Kind: kind, Rate1: rate1, Rate2: rate2}
}

func TestAMeterIsConsumptionTimesRate(t *testing.T) {
	r := model.Reading{Prev1: f(100), Curr1: f(150)}
	r.ComputeAmount(tariff(model.KindMeter, 4.32, nil))

	if r.Consumed1 == nil || *r.Consumed1 != 50 {
		t.Fatalf("consumed1 = %v, want 50", r.Consumed1)
	}
	if r.Amount != 216 {
		t.Fatalf("amount = %v, want 216", r.Amount)
	}
	// A single-zone tariff must not leave zone 2 behind.
	if r.Prev2 != nil || r.Curr2 != nil || r.Consumed2 != nil {
		t.Fatalf("zone 2 survived a single-zone tariff: %v %v %v", r.Prev2, r.Curr2, r.Consumed2)
	}
}

// A month whose meter was not read yet is not a month that cost nothing, but
// it is the only honest number until someone reads it.
func TestAMeterWithoutBothEndsCostsNothing(t *testing.T) {
	r := model.Reading{Prev1: f(100)}
	r.ComputeAmount(tariff(model.KindMeter, 4.32, nil))

	if r.Consumed1 != nil {
		t.Fatalf("consumed1 = %v, want nil", *r.Consumed1)
	}
	if r.Amount != 0 {
		t.Fatalf("amount = %v, want 0", r.Amount)
	}
}

func TestAZonedMeterAddsBothZones(t *testing.T) {
	r := model.Reading{Prev1: f(10), Curr1: f(20), Prev2: f(5), Curr2: f(8)}
	r.ComputeAmount(tariff(model.KindMeterZoned, 2, f(0.5)))

	if *r.Consumed1 != 10 || *r.Consumed2 != 3 {
		t.Fatalf("consumed = %v / %v, want 10 / 3", *r.Consumed1, *r.Consumed2)
	}
	if r.Amount != 21.5 { // 10×2 + 3×0.5
		t.Fatalf("amount = %v, want 21.5", r.Amount)
	}
}

// The zoned branch accumulates across two zones. Recomputing a reading that
// already carries an amount must start from zero, or a night-zone-only edit
// adds this month's figure to the one already stored.
func TestRecomputingAZonedReadingDoesNotAccumulate(t *testing.T) {
	r := model.Reading{Amount: 999, Prev2: f(5), Curr2: f(8)}
	r.ComputeAmount(tariff(model.KindMeterZoned, 2, f(0.5)))

	if r.Amount != 1.5 { // 3×0.5, and nothing of the 999
		t.Fatalf("amount = %v, want 1.5 — the previous amount was carried over", r.Amount)
	}
	if r.Consumed1 != nil {
		t.Fatalf("consumed1 = %v, want nil for an unread zone", *r.Consumed1)
	}
}

func TestAFlatTariffIsItsRateAndForgetsTheMeter(t *testing.T) {
	r := model.Reading{Amount: 999, Prev1: f(10), Curr1: f(20), Prev2: f(1), Curr2: f(2)}
	r.ComputeAmount(tariff(model.KindFlat, 500, nil))

	if r.Amount != 500 {
		t.Fatalf("amount = %v, want 500", r.Amount)
	}
	for name, got := range map[string]*float64{
		"prev1": r.Prev1, "curr1": r.Curr1, "prev2": r.Prev2, "curr2": r.Curr2,
		"consumed1": r.Consumed1, "consumed2": r.Consumed2,
	} {
		if got != nil {
			t.Errorf("%s = %v, want nil — a flat tariff has no meter", name, *got)
		}
	}
}

// The code is stored, the symbol is shown, and an unknown code is shown as
// itself rather than as somebody else's currency.
func TestCurrencySymbol(t *testing.T) {
	for code, want := range map[string]string{
		"": "₴", "UAH": "₴", "uah": "₴", " UAH ": "₴",
		"USD": "$", "EUR": "€", "PLN": "PLN",
	} {
		if got := model.CurrencySymbol(code); got != want {
			t.Errorf("CurrencySymbol(%q) = %q, want %q", code, got, want)
		}
	}
}
