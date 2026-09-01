package model

import "strings"

// The household bills, carried over from the home-meters app. A Utility is one
// billed service at one property — "Електрика в Домі" — which is why it is not
// called a service: in an app that also has trainers and courses, the bare word
// names nothing.
//
// The old app's Service.Category came with a catalogue of icons and colours per
// category. It does not move: the two fields it drove are stored on the utility
// itself, and a lookup table whose only job is to fill them in is a second
// place for them to disagree.

// Tariff kinds. A tariff carries how it is calculated, not just its price.
const (
	KindMeter      = "meter"       // consumption × rate1
	KindMeterZoned = "meter_zoned" // two zones: zone1 × rate1 + zone2 × rate2
	KindFlat       = "flat"        // a fixed monthly sum, no meter
)

// CurrencySymbol maps a stored currency code to the glyph it is shown as. The
// code is what is kept (UAH); the symbol is what is read (₴). An unknown code
// falls back to itself rather than to a wrong symbol, and empty means the
// app default.
func CurrencySymbol(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "", "UAH":
		return "₴"
	case "USD":
		return "$"
	case "EUR":
		return "€"
	default:
		return code
	}
}

// Address is a property bills are paid for — "Дім", "Тьоща". Unrelated to
// Appointment.Location, which is where you go to see a dentist.
type Address struct {
	ID        int64
	Name      string
	Comment   string
	Area      *float64 // m², for tariffs charged per area
	Currency  string
	Active    bool
	SortOrder int
}

// Tariff is a price list, shared between properties: the same gas tariff
// serves both, so a tariff belongs to no one utility.
type Tariff struct {
	ID            int64
	Name          string
	Kind          string
	Unit          *string // "м3", "кВт"; nil for a flat tariff
	Rate1         float64
	Rate2         *float64 // zone 2, meter_zoned only
	EffectiveFrom *string  // informational; the reading's tariff_id is what binds
	EffectiveTo   *string
	Active        bool
	Comment       string
}

// Utility is one billed service at one property.
type Utility struct {
	ID        int64
	AddressID int64
	Name      string
	// CurrentTariffID is the tariff the NEXT reading will use. A stored
	// reading keeps the one it was written with.
	CurrentTariffID *int64
	Icon            string
	Color           string
	URL             string // the provider's site, for paying
	Active          bool
	SortOrder       int
	Comment         string
}

// Reading is one month of one utility. TariffID is the tariff that applied
// then, never re-read from the utility: change the price today and every past
// month must keep the number it was actually billed at.
type Reading struct {
	ID          int64
	UtilityID   int64
	TariffID    int64
	Period      string // "YYYY-MM"
	ReadingDate *string
	Prev1       *float64
	Curr1       *float64
	Prev2       *float64
	Curr2       *float64
	Consumed1   *float64
	Consumed2   *float64
	Amount      float64
	PaidDate    *string // nil means unpaid
	Comment     string
}

// ComputeAmount fills in Consumed1/Consumed2/Amount from the raw prev/curr
// values already on r, according to how t is calculated. Every field the kind
// does not use is cleared, so a reading switched from a zoned tariff to a flat
// one does not keep half of its old arithmetic.
func (r *Reading) ComputeAmount(t Tariff) {
	// Reset first. The zoned branch accumulates across two zones, and starting
	// from whatever Amount already held would add this month's zone 2 to last
	// month's total when zone 1 is left blank.
	r.Amount = 0
	switch t.Kind {
	case KindMeter:
		r.Prev2, r.Curr2, r.Consumed2 = nil, nil, nil
		r.Consumed1 = nil
		if r.Prev1 != nil && r.Curr1 != nil {
			c := *r.Curr1 - *r.Prev1
			r.Consumed1 = &c
			r.Amount = c * t.Rate1
		}
	case KindMeterZoned:
		r.Consumed1, r.Consumed2 = nil, nil
		if r.Prev1 != nil && r.Curr1 != nil {
			c := *r.Curr1 - *r.Prev1
			r.Consumed1 = &c
			r.Amount += c * t.Rate1
		}
		if r.Prev2 != nil && r.Curr2 != nil && t.Rate2 != nil {
			c := *r.Curr2 - *r.Prev2
			r.Consumed2 = &c
			r.Amount += c * *t.Rate2
		}
	case KindFlat:
		r.Prev1, r.Curr1, r.Prev2, r.Curr2 = nil, nil, nil, nil
		r.Consumed1, r.Consumed2 = nil, nil
		r.Amount = t.Rate1
	}
}
