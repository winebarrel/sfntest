// Package sfntest runs AWS Step Functions state machines locally for unit tests.
//
// Service calls are answered by a mock ([API]), and intrinsic functions,
// clocks and other non-deterministic values can be replaced through [Env].
package sfntest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/recolabs/gnata"
)

// WorkflowType is the type of a state machine.
type WorkflowType int

const (
	Standard WorkflowType = iota
	Express
)

func (t WorkflowType) String() string {
	if t == Express {
		return "EXPRESS"
	}
	return "STANDARD"
}

const (
	defaultTimeout       = 10 * time.Second
	defaultPayloadLimit  = 256 * 1024
	defaultVariableLimit = 256 * 1024
	defaultVariableTotal = 10 * 1024 * 1024
)

type options struct {
	typ           WorkflowType
	timeout       time.Duration
	payloadLimit  int
	variableLimit int
	variableTotal int
}

// Option configures a StateMachine.
type Option func(*options)

// WithType sets the workflow type. The default is Standard.
func WithType(t WorkflowType) Option {
	return func(o *options) { o.typ = t }
}

// WithTimeout limits the real time a single Run may take, to stop runaway
// loops. The default is 10 seconds.
func WithTimeout(d time.Duration) Option {
	return func(o *options) { o.timeout = d }
}

// WithPayloadLimit sets the maximum size in bytes of state input/output and
// service call parameters/results. The default is 256 KiB.
func WithPayloadLimit(n int) Option {
	return func(o *options) { o.payloadLimit = n }
}

// WithVariableLimit sets the maximum size in bytes of a single variable (and
// of all variables in a single Assign), and of all variables of an
// execution. The defaults are 256 KiB and 10 MiB.
func WithVariableLimit(perVariable, total int) Option {
	return func(o *options) {
		o.variableLimit = perVariable
		o.variableTotal = total
	}
}

// StateMachine is a parsed and validated state machine definition.
type StateMachine struct {
	def     *machine
	opts    options
	jsonata map[string]*gnata.Expression
}

// New parses and validates a state machine definition (Amazon States
// Language, JSON).
func New(definition []byte, opts ...Option) (*StateMachine, error) {
	o := options{
		typ:           Standard,
		timeout:       defaultTimeout,
		payloadLimit:  defaultPayloadLimit,
		variableLimit: defaultVariableLimit,
		variableTotal: defaultVariableTotal,
	}
	for _, opt := range opts {
		opt(&o)
	}

	m, exprs, err := parseMachine(definition, o.typ)
	if err != nil {
		return nil, err
	}

	return &StateMachine{def: m, opts: o, jsonata: exprs}, nil
}

// Status is the status of an execution.
type Status string

const (
	Succeeded Status = "SUCCEEDED"
	Failed    Status = "FAILED"
	// Incomplete means Run stopped with an error before the execution finished.
	Incomplete Status = "INCOMPLETE"
)

// Result is the result of an execution.
type Result struct {
	Status Status
	// Output is the execution output when Status is Succeeded.
	Output json.RawMessage
	// Error and Cause describe the failure when Status is Failed.
	Error string
	Cause string
	// History holds one event per state visit.
	History []Event
}

// Event records a visit to a state.
type Event struct {
	State       string
	Type        string
	EnteredTime time.Time
	Input       json.RawMessage
	// Output is nil when the state failed.
	Output json.RawMessage
	Error  string
	Cause  string
	// Next is the next state, including a state reached by Catch.
	// It is empty when the state ended its branch or the execution.
	Next string
	// Attempts records each service call of a Task state.
	Attempts []Attempt
	// Variables holds the variables assigned by this state.
	Variables map[string]json.RawMessage
	// Branches holds the events of each Parallel branch or Map iteration.
	Branches [][]Event
}

// Attempt records a service call.
type Attempt struct {
	Resource string
	Params   json.RawMessage
	Result   json.RawMessage
	Error    string
	Cause    string
}

// Run executes the state machine with the given input (JSON).
//
// A failed execution is not an error: it is reported by Result.Status.
// Run returns an error when the execution cannot continue, e.g. a mock
// returned a non-States error, the timeout expired or ctx was canceled.
// In that case the partial Result (Status Incomplete) is returned too.
func (sm *StateMachine) Run(ctx context.Context, input []byte, env *Env) (*Result, error) {
	renv, err := resolveEnv(env)
	if err != nil {
		return nil, err
	}

	in, err := decodeJSON(input)
	if err != nil {
		return nil, fmt.Errorf("invalid execution input: %w", err)
	}
	if len(input) > sm.opts.payloadLimit {
		return nil, fmt.Errorf("execution input exceeds %d bytes", sm.opts.payloadLimit)
	}

	ctx, cancel := context.WithTimeoutCause(ctx, sm.opts.timeout, fmt.Errorf("execution exceeded the timeout of %s", sm.opts.timeout))
	defer cancel()

	r := &runner{
		ctx:   ctx,
		sm:    sm,
		env:   renv,
		input: in,
	}
	if r.jsonata, err = r.newJSONataEnv(renv.jsonata); err != nil {
		return nil, err
	}

	res := &Result{}
	out, err := r.runMachine(sm.def, in, newScope(nil), &res.History)

	var fe *fatalError
	if errors.As(err, &fe) {
		res.Status = Incomplete
		return res, fe.err
	}
	if se, ok := asStatesError(err); ok {
		res.Status = Failed
		res.Error = se.Name
		res.Cause = se.Cause
		return res, nil
	}
	if err != nil {
		res.Status = Incomplete
		return res, err
	}

	res.Status = Succeeded
	res.Output = mustEncodeJSON(out)
	return res, nil
}
