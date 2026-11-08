package redact

import "errors"

// Error is an error whose message is safe to show anywhere (status page,
// /healthz, email) and whose Detail is for the journal only. Detail has
// already been scrubbed by the producer.
type Error struct {
	Summary string
	Detail  string
}

func (e *Error) Error() string { return e.Summary }

// GoString keeps Detail out of %#v output; it is journal-only.
func (e *Error) GoString() string { return e.Summary }

// Detail returns the Detail of the first *Error in err's chain, or "" if
// there is none. It is safe for a nil err.
func Detail(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Detail
	}
	return ""
}
