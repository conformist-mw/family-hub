package reminders

import (
	"fmt"
	"strconv"
	"strings"
)

// Describe turns a recurrence rule into Ukrainian.
//
// The stored form is a full RRULE because that is what expands correctly, but
// it is machinery: nobody reading a list of chores should have to parse
// FREQ=WEEKLY;INTERVAL=2;BYDAY=SA.
//
// It lives in Go, and is served to both surfaces, because it used to live in
// the Mini App's JavaScript and the web needed the same words. Two
// implementations of "раз на 2 тижні, сб" would drift, and the one that drifts
// is always the one nobody is looking at.
//
// It describes the parts it knows and refuses to guess at the rest: a rule
// built from parts it does not understand is called what it is rather than
// described wrongly. The raw text still lives in the form, which is where a
// rule is meant to be edited.
func Describe(rrule string) string {
	p := parseRule(rrule)
	every := 1
	if n, err := strconv.Atoi(p["INTERVAL"]); err == nil && n > 0 {
		every = n
	}

	var tokens, days []string
	positional := false
	if raw := p["BYDAY"]; raw != "" {
		for _, d := range strings.Split(raw, ",") {
			d = strings.TrimSpace(d)
			// A positional day ("2SU" — the second Sunday) is a shape this does
			// not describe; note it and fall through rather than call it plain
			// Sunday.
			if strings.ContainsAny(d, "0123456789") {
				positional = true
				continue
			}
			if short, ok := dayShort[d]; ok {
				tokens = append(tokens, d)
				days = append(days, short)
			}
		}
	}
	dayList := strings.Join(days, ", ")
	hasByDay := p["BYDAY"] != ""

	switch p["FREQ"] {
	case "DAILY":
		if hasByDay {
			break
		}
		switch every {
		case 1:
			return "щодня"
		case 2:
			return "через день"
		}
		return fmt.Sprintf("кожні %d %s", every, plural(every, "день", "дні", "днів"))

	case "WEEKLY":
		if positional {
			break
		}
		if every == 1 {
			if len(tokens) == 1 {
				// "щовівторка" reads better than "щотижня: вт", and Ukrainian
				// builds it per weekday rather than from a suffix.
				return everyDayOfWeek[tokens[0]]
			}
			if len(days) > 1 {
				return "щотижня: " + dayList
			}
			return "щотижня"
		}
		base := fmt.Sprintf("кожні %d %s", every, plural(every, "тиждень", "тижні", "тижнів"))
		if every == 2 {
			base = "раз на 2 тижні"
		}
		if len(days) > 0 {
			return base + ", " + dayList
		}
		return base

	case "MONTHLY":
		if hasByDay {
			break
		}
		day, hasDay := 0, false
		if n, err := strconv.Atoi(p["BYMONTHDAY"]); err == nil {
			day, hasDay = n, true
		}
		if hasDay && day == -1 {
			if every == 1 {
				return "останній день місяця"
			}
			return fmt.Sprintf("останній день кожні %d місяці", every)
		}
		on := ""
		if hasDay && day != 0 {
			on = fmt.Sprintf(", %d-го", day)
		}
		switch every {
		case 1:
			return "щомісяця" + on
		case 2:
			return "раз на 2 місяці" + on
		}
		return fmt.Sprintf("кожні %d %s%s", every,
			plural(every, "місяць", "місяці", "місяців"), on)

	case "YEARLY":
		if hasByDay || p["BYMONTH"] != "" {
			break
		}
		if every == 1 {
			return "щороку"
		}
		return fmt.Sprintf("кожні %d %s", every, plural(every, "рік", "роки", "років"))
	}

	return "за власним правилом"
}

var dayShort = map[string]string{
	"SU": "нд", "MO": "пн", "TU": "вт", "WE": "ср",
	"TH": "чт", "FR": "пт", "SA": "сб",
}

var everyDayOfWeek = map[string]string{
	"SU": "щонеділі", "MO": "щопонеділка", "TU": "щовівторка", "WE": "щосереди",
	"TH": "щочетверга", "FR": "щоп'ятниці", "SA": "щосуботи",
}

// plural picks the Ukrainian form: 1 день, 2 дні, 5 днів.
func plural(n int, one, few, many string) string {
	if n < 0 {
		n = -n
	}
	mod10, mod100 := n%10, n%100
	switch {
	case mod10 == 1 && mod100 != 11:
		return one
	case mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14):
		return few
	}
	return many
}

// parseRule splits a rule body into its parts, tolerating the "RRULE:" prefix
// for the same reason recur.parse does: that is how rules appear in every ICS
// file and in every example on the web.
func parseRule(rrule string) map[string]string {
	parts := map[string]string{}
	body := strings.TrimPrefix(strings.TrimSpace(rrule), "RRULE:")
	for _, chunk := range strings.Split(body, ";") {
		k, v, _ := strings.Cut(chunk, "=")
		if k = strings.ToUpper(strings.TrimSpace(k)); k != "" {
			parts[k] = strings.ToUpper(strings.TrimSpace(v))
		}
	}
	return parts
}
