package artifactory

import (
	"context"
	"errors"
	"net"
)

type Error struct {
	Category string `json:"category"`
	Message  string `json:"message"`
}

func (e *Error) Error() string                 { return e.Category + ": " + e.Message }
func NewError(category, message string) *Error { return &Error{category, message} }
func Classify(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return NewError("timed_out", "request deadline exceeded or request canceled")
	}
	var n net.Error
	if errors.As(err, &n) && n.Timeout() {
		return NewError("timed_out", "upstream request timed out")
	}
	return NewError("unavailable", "upstream request failed")
}
func StatusError(code int) *Error {
	switch code {
	case 400, 422:
		return NewError("invalid_input", "upstream rejected the request")
	case 401:
		return NewError("unauthorized", "upstream authentication failed")
	case 403:
		return NewError("forbidden", "token lacks permission for this operation")
	case 404:
		return NewError("not_found", "requested item was not found")
	case 429:
		return NewError("rate_limited", "upstream rate limit exceeded")
	case 408, 504:
		return NewError("timed_out", "upstream request timed out")
	default:
		return NewError("unavailable", "upstream returned an unexpected status")
	}
}
