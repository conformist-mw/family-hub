// Package valid holds the one error type the write layers share.
//
// Both internal/appointments and internal/schedule reject input a person can
// fix, and both the web form and the Mini App's JSON API have to turn that
// rejection into something shown next to the offending input. One type means
// one mapping in each HTTP surface instead of one per domain.
package valid

// FieldError is a validation failure a person can act on. Field names the
// input to point at; Message is shown as written, in Ukrainian.
type FieldError struct {
	Field   string
	Message string
}

func (e FieldError) Error() string { return e.Message }
