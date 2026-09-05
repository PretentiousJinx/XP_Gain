package domain

import "errors"

// Sentinel errors. The HTTP layer maps these to status codes; nothing below
// the transport layer should know about status codes.
var (
	ErrNotFound        = errors.New("not found")
	ErrInvalidPayload  = errors.New("invalid payload")
	ErrRejectionClosed = errors.New("rejection already resolved")
)

// RejectedError is Path B: the Vision AI declined the photo. It is a normal,
// expected outcome of the workflow, not a server fault, so it carries the
// model's own reasoning back to the client for display.
type RejectedError struct {
	RejectionID         string
	ValidationReasoning string
	Confidence          float64
}

func (e *RejectedError) Error() string {
	return "photo rejected: " + e.ValidationReasoning
}
