// Package ctl is the CLI-to-daemon control protocol: newline-delimited JSON
// over a unix socket, one request line and one response line per
// connection, then close.
//
// Every request carries the client's identity (Peer) and every response the
// daemon's. The daemon refuses every method except hello and shutdown when
// the client's version or protocol differs from its own (DMN-07). The hello
// and shutdown schemas are frozen forever, so any CLI can identify and stop
// any daemon.
package ctl

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Protocol is the control protocol revision. Bump it on any incompatible
// schema change.
const Protocol = 1

// Limits applied by the server to one request.
const (
	// MaxLine caps one request or response line (T-04-13).
	MaxLine = 1 << 20
	// ReadTimeout bounds how long the server waits for the request line.
	ReadTimeout = 5 * time.Second
)

// Error codes carried in Response.Error.
const (
	CodeBadRequest       = "bad_request"
	CodeVersionMismatch  = "version_mismatch"
	CodeNotFound         = "not_found"
	CodeForbidden        = "forbidden"
	CodeNoToken          = "no_token"
	CodeUnauthorized     = "unauthorized"
	CodeRateLimited      = "rate_limited"
	CodeSecondaryLimit   = "secondary_limit"
	CodeUnavailable      = "unavailable"
	CodeTimeout          = "timeout"
	CodePRClosed         = "pr_closed"
	CodePaneNotFound     = "pane_not_found"
	CodeHerdrUnreachable = "herdr_unreachable"
	CodeNotWatched       = "not_watched"
	CodeEnded            = "ended"
	CodeInternal         = "internal"
)

// Peer identifies one end of a control connection.
type Peer struct {
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
	PID      int    `json:"pid,omitempty"`
}

// Request is one request line.
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	Client Peer            `json:"client"`
}

// Response is one response line. Exactly one of Result and Error is set.
type Response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
	Daemon Peer            `json:"daemon"`
}

// Error is a protocol-level error reply. Handlers return it to choose the
// code the client sees; any other handler error becomes CodeInternal.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e.Message == "" {
		return "ctl: " + e.Code
	}
	return fmt.Sprintf("ctl: %s: %s", e.Code, e.Message)
}

// UnreachableError means the daemon could not be reached or went away before
// it answered: the socket is missing, refused, not a socket or not ours, or
// the connection failed mid-request.
type UnreachableError struct {
	Socket string
	Err    error
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("ctl: unreachable %s: %v", e.Socket, e.Err)
}

func (e *UnreachableError) Unwrap() error { return e.Err }

// VersionMismatchError is returned by Client.Call when the daemon refused the
// request because its version or protocol differs from the client's.
type VersionMismatchError struct {
	Client Peer
	Daemon Peer
}

func (e *VersionMismatchError) Error() string {
	return fmt.Sprintf("ctl: %s: cli %s/%d, daemon %s/%d", CodeVersionMismatch,
		e.Client.Version, e.Client.Protocol, e.Daemon.Version, e.Daemon.Protocol)
}

// Code returns the protocol error code carried by err, or "" when there is
// none. A *VersionMismatchError reports CodeVersionMismatch.
func Code(err error) string {
	var vm *VersionMismatchError
	if errors.As(err, &vm) {
		return CodeVersionMismatch
	}
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

// IsUnreachable reports whether err is (or wraps) an *UnreachableError.
func IsUnreachable(err error) bool {
	var ue *UnreachableError
	return errors.As(err, &ue)
}
