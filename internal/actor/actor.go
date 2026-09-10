// Package actor resolves the way a person refers to themselves in a form
// field to the name of whoever is making the write.
//
// It exists because the identity was already there and being thrown away.
// Both surfaces authenticate — the Mini App from verified initData, the web
// from the proxy's forwarded headers — and both pass the author down to the
// write layer for the group notification's byline. Meanwhile `person` is
// free text, and the shortest thing to type in it is "Я", which is
// unreadable a month later and cannot be filtered, counted or reported on.
//
// One package rather than a helper in either domain: appointments and
// reminders write the same free-text `person`, so the rule about what "Я"
// means has to be the same on both or the two halves of the calendar
// disagree about who did what.
package actor

import (
	"strconv"
	"strings"
)

// Unknown is what a surface passes as the author when the write is
// authenticated but the author cannot be named — the forward-auth proxy sent
// no identity headers, say. It is fine in a group byline, where it says where
// the change came from, and must never reach a `person` field: writing it
// there swaps one unreadable value for another.
const Unknown = "веб"

// selfNames are the ways somebody writes "me" in a person field. The list
// comes from the bot, which has been resolving these against the message
// sender since before there was a Mini App — the two surfaces sharing it is
// the point of this package. Ukrainian and Russian forms both appear because
// the family types both.
var selfNames = map[string]bool{
	"я": true, "мене": true, "мне": true, "себе": true, "собі": true,
}

// IsSelf reports whether person is somebody referring to themselves. Exposed
// for the bot, which additionally treats an empty person as the sender: a
// parsed message that names nobody means whoever sent it, whereas an empty
// form field just means it was not filled in.
func IsSelf(person string) bool {
	return selfNames[strings.ToLower(strings.TrimSpace(person))]
}

// Resolve returns the person a row should record. person is what was typed;
// by is the authenticated author, or "" when the surface cannot name them.
//
// Anything that is not a self-reference comes back untouched — naming somebody
// else is the common case and must survive verbatim. A self-reference with no
// usable author also comes back untouched: "Я" is bad, but silently attributing
// the row to the wrong person is worse.
func Resolve(person, by string) string {
	person = strings.TrimSpace(person)
	if !selfNames[strings.ToLower(person)] {
		return person
	}
	if by = strings.TrimSpace(by); by == "" || by == Unknown {
		return person
	}
	return by
}

// Roster maps a Telegram user id to the name this family calls that person.
//
// The id is the identity, not the display name: a display name is the
// person's to change, and when they do, every "Я" they write starts resolving
// to a different string while the rows already written keep the old one — one
// human, two names, and no way to tell they are the same. Ids never change.
type Roster map[int64]string

// ParseRoster reads "<id>:<name>,<id>:<name>". Malformed entries are skipped
// rather than refused: the roster is a convenience over the Telegram display
// name, and a typo in it must not stop the app from starting.
func ParseRoster(raw string) Roster {
	r := Roster{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, name, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
		if err != nil {
			continue
		}
		if name = strings.TrimSpace(name); name != "" {
			r[n] = name
		}
	}
	return r
}

// Name is what to call the person behind a Telegram id. fallback is their
// current display name, used for anyone the roster does not list — a guest in
// the group is still better named badly than not at all.
func (r Roster) Name(id int64, fallback string) string {
	if name, ok := r[id]; ok {
		return name
	}
	return fallback
}
