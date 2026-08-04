package parse

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestParseSample hits the real Gemini API; it is skipped unless
// GEMINI_API_KEY is set (so `go test ./...` stays offline by default).
func TestParseSample(t *testing.T) {
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		t.Skip("GEMINI_API_KEY not set")
	}
	loc, _ := time.LoadLocation("Europe/Kyiv")
	modelName := os.Getenv("GEMINI_MODEL")
	if modelName == "" {
		modelName = "gemini-flash-lite-latest"
	}

	ctx := context.Background()
	p, err := New(ctx, key, modelName, loc, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const sample = `8.07
11:30
Педикюр Олєжа

14.07
10:00
Педикюр я

16.07
11:45
Ортодонт я

22.07
13:30
Манікюр в обох

30.07
11:30
Ортодонт дьома`

	now := time.Date(2026, 7, 6, 12, 0, 0, 0, loc)
	got, err := p.Parse(ctx, sample, now)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, g := range got {
		a := g.Appointment
		t.Logf("%-18s %-10s → %-8s [%s]", a.StartsAt, a.Title, a.Person, g.Confidence)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 appointments, got %d", len(got))
	}
}
