package mini

import (
	"net/http"

	"familyhub/internal/payments"
)

// Recording a payment was the last thing that still meant opening a browser:
// the balance line on the Заняття tab said a course was running out and the
// only way to answer it was the web form behind oauth. A payment belongs to a
// course, so it is written under one — the course is picked by tapping its
// card, never by choosing from a list.

// paymentForm is the JSON body of a payment write, mirroring payments.Form.
// The client always sends every field; which of lessons/month is required
// follows from how the course is billed, and the server decides that from the
// enrollment rather than trusting what arrived.
type paymentForm struct {
	Date    string `json:"date"`
	Amount  string `json:"amount"`
	Lessons string `json:"lessons"`
	Month   string `json:"month"`
	Comment string `json:"comment"`
}

func (f paymentForm) form() payments.Form {
	return payments.Form{
		Date: f.Date, Amount: f.Amount, Lessons: f.Lessons,
		CoversMonth: f.Month, Comment: f.Comment,
	}
}

func (rt *Router) handlePaymentCreate(w http.ResponseWriter, r *http.Request) {
	if _, err := rt.v.authenticate(r); err != nil {
		rt.fail(w, err)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	var body paymentForm
	if err := decodeJSON(r, &body); err != nil {
		rt.fail(w, errBadRequest)
		return
	}
	// A missing course is a 404, not a validation failure: the client picked it
	// from a card, so there is no input for the person to correct.
	if _, err := rt.store.GetEnrollment(id); err != nil {
		rt.fail(w, errNotFound)
		return
	}
	p, err := rt.payments.Create(id, body.form())
	if err != nil {
		rt.writeError(w, err, "create payment")
		return
	}
	rt.writeJSON(w, http.StatusCreated, map[string]int64{"id": p.ID})
}
