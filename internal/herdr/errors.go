package herdr

import (
	"errors"
	"fmt"
)

// herdr error codes seen on protocol 22 (verified by live probe, except
// events_lost and agent_prompt_stalled, which come from the docs).
const (
	CodeAgentNotFound      = "agent_not_found"
	CodeAgentNotRunning    = "agent_not_running"
	CodeAgentBlocked       = "agent_blocked"
	CodeAgentPromptStalled = "agent_prompt_stalled"
	CodePaneNotFound       = "pane_not_found"
	CodeTabNotFound        = "tab_not_found"
	CodeTimeout            = "timeout"
	CodeInvalidRequest     = "invalid_request"
	CodeEventsLost         = "events_lost"
)

// Error is an error response from herdr: the decoded "error" object.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e.Message == "" {
		return "herdr: " + e.Code
	}
	return fmt.Sprintf("herdr: %s: %s", e.Code, e.Message)
}

// UnreachableError means herdr could not be reached or went away before it
// answered: a dial failure, a socket that is missing or not owned by the
// current user, or a write or read failure (including EOF before a full
// response line). Delivery treats it as "keep queued" (DLV-08).
type UnreachableError struct {
	Socket string
	Err    error
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("herdr: unreachable %s: %v", e.Socket, e.Err)
}

func (e *UnreachableError) Unwrap() error { return e.Err }

// Code returns the herdr error code carried by err, or "" when err is not
// (or does not wrap) an *Error.
func Code(err error) string {
	var he *Error
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

// IsUnreachable reports whether err is (or wraps) an *UnreachableError.
func IsUnreachable(err error) bool {
	var ue *UnreachableError
	return errors.As(err, &ue)
}
