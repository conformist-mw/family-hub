package model

// Dish statuses. A dish the model suggests lives in the same table as the
// family's own, so the menu, the cooking log and the suggestions all read one
// catalogue; status is what tells them apart. Prefixed like the appointment
// and occurrence ones — the bare Status* names belong to lesson visits.
const (
	DishActive = "active"
	// DishProposed is a suggestion nobody has decided on yet. It is what the
	// menu marks 🆕, and it becomes active the first time it is eaten.
	DishProposed = "proposed"
	// DishRejected is kept rather than deleted: the list is handed to the
	// model so the same dish is not suggested again next week.
	DishRejected = "rejected"
)

// Which meal a dish is for, and which days. "any" is the default for both: a
// dish imported without a meal tag is offered at lunch and at dinner, and one
// without the weekend tag on any day.
const (
	DishMealAny     = "any"
	DishDaysAny     = "any"
	DishDaysWeekend = "weekend"
)

// Dish is something that is put on the table, as the family names it — "Пюре
// зі скумбрією" is one dish, not a main and a side.
type Dish struct {
	ID   int64
	Name string
	Meal string // lunch | dinner | any
	Days string // any | weekend
	// Status is one of the Dish* constants above.
	Status string
	// Note is the model's one-line pitch for a proposed dish; empty for the
	// family's own.
	Note      string
	CreatedAt string
}

// ValidDishStatus guards writes coming from the bot's callback data, which is
// not trusted to stay in step with the CHECK constraint.
func ValidDishStatus(s string) bool {
	switch s {
	case DishActive, DishProposed, DishRejected:
		return true
	}
	return false
}
