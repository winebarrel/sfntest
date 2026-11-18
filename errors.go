package sfntest

import (
	"errors"
	"fmt"
)

// Predefined error names.
const (
	ErrorAll                             = "States.ALL"
	ErrorHeartbeatTimeout                = "States.HeartbeatTimeout"
	ErrorTimeout                         = "States.Timeout"
	ErrorTaskFailed                      = "States.TaskFailed"
	ErrorPermissions                     = "States.Permissions"
	ErrorResultPathMatchFailure          = "States.ResultPathMatchFailure"
	ErrorParameterPathFailure            = "States.ParameterPathFailure"
	ErrorQueryEvaluationError            = "States.QueryEvaluationError"
	ErrorBranchFailed                    = "States.BranchFailed"
	ErrorNoChoiceMatched                 = "States.NoChoiceMatched"
	ErrorIntrinsicFailure                = "States.IntrinsicFailure"
	ErrorExceedToleratedFailureThreshold = "States.ExceedToleratedFailureThreshold"
	ErrorItemReaderFailed                = "States.ItemReaderFailed"
	ErrorResultWriterFailed              = "States.ResultWriterFailed"
	ErrorRuntime                         = "States.Runtime"
	ErrorDataLimitExceeded               = "States.DataLimitExceeded"
)

// StatesError is an error of the state machine. It is handled by Retry and
// Catch, and fails the execution when it is not caught.
//
// An API mock returns one (see [Error]) to make a task fail.
// Any other error returned by a mock aborts [StateMachine.Run].
type StatesError struct {
	Name  string
	Cause string
}

// Error returns a state machine error with the given error name and cause.
func Error(name, cause string) error {
	return &StatesError{Name: name, Cause: cause}
}

func (e *StatesError) Error() string {
	if e.Cause == "" {
		return e.Name
	}
	return fmt.Sprintf("%s: %s", e.Name, e.Cause)
}

func statesErrorf(name, format string, args ...any) *StatesError {
	return &StatesError{Name: name, Cause: fmt.Sprintf(format, args...)}
}

func asStatesError(err error) (*StatesError, bool) {
	var se *StatesError
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}

// errorMatches reports whether an error name is matched by ErrorEquals.
func errorMatches(errorEquals []string, name string) bool {
	if name == ErrorRuntime {
		return false
	}
	for _, e := range errorEquals {
		switch e {
		case name:
			return true
		case ErrorAll:
			if name != ErrorDataLimitExceeded {
				return true
			}
		case ErrorTaskFailed:
			if name != ErrorTimeout && name != ErrorDataLimitExceeded {
				return true
			}
		}
	}
	return false
}

// fatalError aborts Run. It is never handled by Retry or Catch.
type fatalError struct {
	err error
}

func (e *fatalError) Error() string { return e.err.Error() }
func (e *fatalError) Unwrap() error { return e.err }

func fatal(err error) error {
	var fe *fatalError
	if errors.As(err, &fe) {
		return err
	}
	return &fatalError{err: err}
}

func isFatal(err error) bool {
	var fe *fatalError
	return errors.As(err, &fe)
}

func errUnsupportedState(typ string) error {
	return fmt.Errorf("unsupported state type: %s", typ)
}
