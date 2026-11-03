package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/priyanshu-shubham/jenkinsctl/internal/jenkins"
)

// Exit codes stay coarse on purpose: error.kind in the JSON output carries the
// detail, the exit code only keeps `&&` chaining and harness failure flags working.
const (
	ExitOK    = 0
	ExitError = 1
)

// Error kinds are the machine-readable contract agents branch on.
const (
	kindUsage          = "usage"
	kindInvalidParam   = "invalid_parameter"
	kindAuth           = "auth"
	kindNotFound       = "not_found"
	kindTimeout        = "timeout"
	kindRunFailed      = "run_failed"
	kindQueueCancelled = "queue_cancelled"
	kindServer         = "server_error"
	kindNetwork        = "network"
	kindError          = "error"
)

type cliError struct {
	kind   string
	err    error
	resume string
	valid  []string
	// data is a partial result reported alongside the error, e.g. the run
	// state when a wait timed out; text renders it for humans.
	data any
	text func(io.Writer)
}

func (e *cliError) Error() string { return e.err.Error() }
func (e *cliError) Unwrap() error { return e.err }

type errInfo struct {
	Kind       string   `json:"kind"`
	Message    string   `json:"message"`
	Resume     string   `json:"resume,omitempty"`
	Valid      []string `json:"valid,omitempty"`
	HTTPStatus int      `json:"http_status,omitempty"`
}

func (e *cliError) info() errInfo {
	return errInfo{Kind: e.kind, Message: e.err.Error(), Resume: e.resume, Valid: e.valid, HTTPStatus: httpStatus(e.err)}
}

func usageErr(format string, a ...any) error {
	return &cliError{kind: kindUsage, err: fmt.Errorf(format, a...)}
}

func classify(err error) *cliError {
	var ce *cliError
	if errors.As(err, &ce) {
		return ce
	}
	var ae *jenkins.APIError
	if errors.As(err, &ae) {
		switch ae.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return &cliError{kind: kindAuth, err: err}
		case http.StatusNotFound:
			return &cliError{kind: kindNotFound, err: err}
		}
		return &cliError{kind: kindServer, err: err}
	}
	if errors.Is(err, jenkins.ErrInvalidInput) {
		return &cliError{kind: kindUsage, err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &cliError{kind: kindTimeout, err: err}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return &cliError{kind: kindNetwork, err: err}
	}
	// cobra reports arg/flag problems as plain errors.
	msg := err.Error()
	for _, p := range []string{"unknown command", "unknown flag", "unknown shorthand", "invalid argument", "flag needs an argument"} {
		if strings.Contains(msg, p) {
			return &cliError{kind: kindUsage, err: err}
		}
	}
	return &cliError{kind: kindError, err: err}
}

func httpStatus(err error) int {
	var ae *jenkins.APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}
