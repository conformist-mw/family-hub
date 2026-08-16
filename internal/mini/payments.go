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
//
// Editing and deleting live here too, keyed by the payment rather than the
// course: a wrong amount typed on a phone with one hand is exactly the thing
// that should not need a laptop to fix, and the course a payment belongs to is
// not the phone's to change.

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

func decodePayment(r *http.Request) (payments.Form, *apiError) {
	var body paymentForm
	if err := decodeJSON(r, &body); err != nil {
		return payments.Form{}, errBadRequest
	}
	return body.form(), nil
}

func (rt *Router) handlePaymentCreate(w http.ResponseWriter, r *http.Request) {
	who, bad := rt.v.authenticate(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	form, bad := decodePayment(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	// A missing course is a 404, not a validation failure: the client picked it
	// from a card, so there is no input for the person to correct.
	if _, err := rt.store.GetEnrollment(id); err != nil {
		rt.fail(w, errNotFound)
		return
	}
	p, err := rt.payments.Create(id, form, who.Name)
	if err != nil {
		rt.writeError(w, err, "create payment")
		return
	}
	rt.writeJSON(w, http.StatusCreated, map[string]int64{"id": p.ID})
}

// handlePaymentUpdate rewrites an existing payment. The course comes from the
// stored row, not from the request: moving a payment to a different course is a
// correction of a different order, and the web form is where it belongs.
func (rt *Router) handlePaymentUpdate(w http.ResponseWriter, r *http.Request) {
	who, bad := rt.v.authenticate(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	form, bad := decodePayment(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	// Editing a row that is gone must not report success: the store's UPDATE
	// would match nothing and return no error.
	existing, err := rt.payments.Get(id)
	if err != nil {
		rt.fail(w, errNotFound)
		return
	}
	if _, err := rt.payments.Update(id, existing.EnrollmentID, form, who.Name); err != nil {
		rt.writeError(w, err, "update payment")
		return
	}
	rt.writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (rt *Router) handlePaymentDelete(w http.ResponseWriter, r *http.Request) {
	who, bad := rt.v.authenticate(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	if _, err := rt.payments.Get(id); err != nil {
		rt.fail(w, errNotFound)
		return
	}
	// Unlike an appointment, this is a hard delete — the payments table has no
	// deleted_at, and a row that is gone from the balance has to be gone from
	// the table for the balance to be right.
	if err := rt.payments.Delete(id, who.Name); err != nil {
		rt.log.Error("mini: delete payment", "err", err)
		rt.fail(w, errInternal)
		return
	}
	rt.writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}
