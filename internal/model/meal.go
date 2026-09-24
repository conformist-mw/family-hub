package model

// The two meals the menu covers. Breakfast is not planned: nobody decides it
// in the morning chat.
const (
	MealLunch  = "lunch"
	MealDinner = "dinner"
)

// Meal entry statuses. A morning tap writes planned; the evening answer or a
// photo of the plate turns it into eaten. History is only eaten, but rotation
// counts both — an unanswered plan was still most likely cooked.
const (
	MealPlanned = "planned"
	MealEaten   = "eaten"
)

// MealEntry is one dish at one meal of one day. Dish and DishStatus are read
// through a join so the menu and the evening check can print and mark a row
// without a second lookup per dish.
type MealEntry struct {
	ID         int64
	DishID     int64
	Dish       string
	DishStatus string
	Date       string // YYYY-MM-DD, local
	Meal       string // MealLunch | MealDinner
	Who        string
	Status     string // MealPlanned | MealEaten
	// Leftover marks a meal that finishes yesterday's pot rather than a fresh
	// one; it is a property of the meal, not of the dish.
	Leftover  bool
	CreatedAt string
}

// ValidMeal guards meal names coming from callback data.
func ValidMeal(m string) bool {
	return m == MealLunch || m == MealDinner
}

// MenuMessage is the morning menu of one day: where it was posted, and every
// dish it has offered for each meal, the shuffles included. Shown outlives
// the message on purpose — what was offered yesterday is kept out of today's
// menu, so a dish nobody picks does not sit in the window for ever.
type MenuMessage struct {
	Date      string // YYYY-MM-DD, local
	ChatID    int64
	MessageID int64
	// Shown maps MealLunch/MealDinner to dish ids in the order they were
	// first offered, each once.
	Shown map[string][]int64
}
