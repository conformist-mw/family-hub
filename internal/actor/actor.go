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

import "strings"

// Unknown is what a surface passes as the author when the write is
// authenticated but the author cannot be named — oauth2-proxy forwarded no
// username, say. It is fine in a group byline, where it says where the change
// came from, and must never reach a `person` field: writing it there swaps one
// unreadable value for another.
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
