// Package parse turns free-text family notes ("22.07 13:30 Манікюр в обох")
// into structured appointments via Gemini with a forced JSON schema.
package parse

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genai"

	"familyhub/internal/model"
)

type Parser struct {
	client *genai.Client
	model  string
	loc    *time.Location
	people []string
}

// New builds a Parser. loc is the timezone relative dates ("завтра") resolve
// against; people is an optional roster used to normalize the "who" field.
func New(ctx context.Context, apiKey, modelName string, loc *time.Location, people []string) (*Parser, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, err
	}
	return &Parser{client: client, model: modelName, loc: loc, people: people}, nil
}

type parsedItem struct {
	Title      string `json:"title"`
	Person     string `json:"person"`
	Start      string `json:"start"`
	Cost       string `json:"cost"` // string, not number: "" carries "no price in the text"
	Confidence string `json:"confidence"`
	Raw        string `json:"raw"`
}

var responseSchema = &genai.Schema{
	Type: genai.TypeArray,
	Items: &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"title":      {Type: genai.TypeString},
			"person":     {Type: genai.TypeString},
			"start":      {Type: genai.TypeString, Description: "ISO 8601 local datetime, e.g. 2026-07-08T11:30"},
			"cost":       {Type: genai.TypeString, Description: "price in UAH if the text states one, digits only (e.g. 800); empty string if it does not"},
			"confidence": {Type: genai.TypeString, Enum: []string{"high", "low"}},
			"raw":        {Type: genai.TypeString},
		},
		// cost is required so the model always answers about it — with "" when
		// there is no price. A number type would have to invent a 0 instead,
		// and 0 means "free" in the schema.
		Required: []string{"title", "person", "start", "cost", "confidence", "raw"},
	},
}

// Parsed pairs an appointment with the parser's confidence, so the caller can
// flag low-confidence rows in the confirmation card.
type Parsed struct {
	Appointment model.Appointment
	Confidence  string
}

// Parse extracts zero or more appointments from text. now anchors relative
// dates; it should be time.Now().In(loc).
func (p *Parser) Parse(ctx context.Context, text string, now time.Time) ([]Parsed, error) {
	sys := p.systemPrompt(now)
	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(sys, genai.RoleUser),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    responseSchema,
		Temperature:       genai.Ptr[float32](0),
	}
	res, err := p.client.Models.GenerateContent(ctx, p.model, genai.Text(text), cfg)
	if err != nil {
		return nil, fmt.Errorf("gemini generate: %w", err)
	}

	var items []parsedItem
	if err := json.Unmarshal([]byte(res.Text()), &items); err != nil {
		return nil, fmt.Errorf("decode parse result: %w", err)
	}

	out := make([]Parsed, 0, len(items))
	for _, it := range items {
		start, err := normalizeStart(it.Start, p.loc)
		if err != nil {
			// Skip a row we can't place on the calendar rather than fail the
			// whole message; the user sees fewer parsed items and can retry.
			continue
		}
		appt := model.Appointment{
			Title:    strings.TrimSpace(it.Title),
			Person:   strings.TrimSpace(it.Person),
			StartsAt: start.Format(model.LocalDatetime),
			Status:   model.ApptStatusPlanned,
			Raw:      it.Raw,
		}
		// A price only lands when the model actually read one. Leaving Cost nil
		// is not a loss: the bot asks about it an hour after the visit.
		if cost, ok := parseCost(it.Cost); ok {
			appt.Cost = &cost
		}
		out = append(out, Parsed{Appointment: appt, Confidence: it.Confidence})
	}
	return out, nil
}

type whenItem struct {
	Start      string `json:"start"`
	Confidence string `json:"confidence"`
}

var whenSchema = &genai.Schema{
	Type: genai.TypeObject,
	Properties: map[string]*genai.Schema{
		"start":      {Type: genai.TypeString, Description: "ISO 8601 local datetime, e.g. 2026-07-08T11:30"},
		"confidence": {Type: genai.TypeString, Enum: []string{"high", "low"}},
	},
	Required: []string{"start", "confidence"},
}

// ParseWhen extracts a single datetime from free text ("в пятницу 17:00",
// "завтра 10", "22.08 14:30") — used when rescheduling an existing visit.
func (p *Parser) ParseWhen(ctx context.Context, text string, now time.Time) (time.Time, string, error) {
	var sb strings.Builder
	sb.WriteString("Извлеки одну дату и время из текста. ")
	fmt.Fprintf(&sb, "Сейчас: %s (%s), таймзона %s. ",
		now.Format("2006-01-02 15:04"), weekdayRU(now.Weekday()), p.loc.String())
	sb.WriteString("Год обычно не указан — резолви в ближайшее будущее. ")
	sb.WriteString("Верни start как локальное ISO 8601 без смещения: 2006-01-02T15:04. ")
	sb.WriteString("confidence=low, если дата/время неоднозначны.")

	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(sb.String(), genai.RoleUser),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    whenSchema,
		Temperature:       genai.Ptr[float32](0),
	}
	res, err := p.client.Models.GenerateContent(ctx, p.model, genai.Text(text), cfg)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("gemini generate: %w", err)
	}
	var it whenItem
	if err := json.Unmarshal([]byte(res.Text()), &it); err != nil {
		return time.Time{}, "", fmt.Errorf("decode when result: %w", err)
	}
	t, err := normalizeStart(it.Start, p.loc)
	if err != nil {
		return time.Time{}, "", err
	}
	return t, it.Confidence, nil
}

func (p *Parser) systemPrompt(now time.Time) string {
	var b strings.Builder
	b.WriteString("Ты парсишь неформальные заметки семьи о будущих визитах ")
	b.WriteString("(маникюр, педикюр, ортодонт, врачи и т.п.). ")
	b.WriteString("Каждая запись = дата, время и строка \"что + кто\". ")
	b.WriteString("Текст на смеси украинского и русского.\n")
	fmt.Fprintf(&b, "Сейчас: %s (%s), таймзона %s. ",
		now.Format("2006-01-02 15:04"), weekdayRU(now.Weekday()), p.loc.String())
	b.WriteString("Год в датах обычно не указан — резолви в ближайшее будущее. ")
	b.WriteString("Поле start верни как локальное время ISO 8601 без смещения: 2006-01-02T15:04.\n")
	if len(p.people) > 0 {
		fmt.Fprintf(&b, "Известные люди: %s. ", strings.Join(p.people, ", "))
		b.WriteString("Приводи person к одному из них, где очевидно; ")
		b.WriteString("\"обоє/оба/в обох\" оставляй как \"обоє\".\n")
	} else {
		b.WriteString("person оставляй как в тексте (например: я, Олєжа, обоє, дьома).\n")
	}
	b.WriteString("cost — цена в гривнах, ТОЛЬКО если она явно есть в тексте ")
	b.WriteString("(«педикюр 800», «1200 грн»); если цены нет — пустая строка, не выдумывай. ")
	b.WriteString("confidence=low, если дата/время/суть неоднозначны. ")
	b.WriteString("Верни массив в порядке появления. Если визитов нет — пустой массив.")
	return b.String()
}

var weekdaysRU = [7]string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}

func weekdayRU(w time.Weekday) string { return weekdaysRU[int(w)] }

// parseCost reads the model's cost field. It is deliberately strict: anything
// that is not a plain non-negative number is treated as "no price", because a
// wrong amount is worse than an absent one — the bot will ask later anyway.
func parseCost(s string) (float64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, " ", ""))
	s = strings.ReplaceAll(s, ",", ".")
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// normalizeStart accepts the few shapes the model may emit and reduces them to
// a local time in loc.
func normalizeStart(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04",
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t.In(loc), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized datetime %q", s)
}
