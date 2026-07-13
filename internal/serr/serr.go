// Package serr defines the stable, machine-readable error codes that stigmergy
// returns to agents. The codes are part of the protocol contract: an agent may
// branch on them, so they must never be renamed or repurposed.
package serr

import (
	"errors"
	"fmt"
)

// Code is a stable error code. The set is closed — see the spec's pinned
// protocol decisions.
type Code string

const (
	WrongState        Code = "wrong_state"
	InvalidInput      Code = "invalid_input"
	NotARepo          Code = "not_a_repo"
	CASConflict       Code = "cas_conflict"
	ClaimConflict     Code = "claim_conflict"
	NotOwner          Code = "not_owner"
	RecipientInactive Code = "recipient_inactive"
	UnsupportedSearch Code = "unsupported_search"
	Internal          Code = "internal"
)

// Error carries a code, an actionable message, and arbitrary context that the
// MCP layer merges into the error payload (e.g. the current entry on a CAS
// conflict, so the agent can re-read and retry without another round trip).
type Error struct {
	Code    Code           `json:"code"`
	Message string         `json:"message"`
	Context map[string]any `json:"-"`
	wrapped error
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }
func (e *Error) Unwrap() error { return e.wrapped }

// E builds an error with the given code and formatted message.
func E(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// With attaches context to an error, returning it for chaining.
func (e *Error) With(key string, val any) *Error {
	if e.Context == nil {
		e.Context = map[string]any{}
	}
	e.Context[key] = val
	return e
}

// Wrap attaches an underlying cause without exposing it to the agent.
func (e *Error) Wrap(err error) *Error {
	e.wrapped = err
	return e
}

// Internalf wraps an unexpected failure. The message reaches the agent, so it
// stays generic; the cause is preserved for logs and errors.Is/As.
func Internalf(err error, format string, args ...any) *Error {
	return E(Internal, format, args...).Wrap(err)
}

// As is errors.As specialized to *Error, so callers need not import both.
func As(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// CodeOf reports the code of err, or Internal when err is not a *serr.Error.
func CodeOf(err error) Code {
	if e, ok := As(err); ok {
		return e.Code
	}
	return Internal
}
