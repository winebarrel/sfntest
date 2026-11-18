package sfntest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// API receives every call a state machine makes to an external service:
// Task states (any integration pattern, activities and nested executions)
// and the ItemReader / ResultWriter of distributed Map states.
//
// resource is the Resource of the state, e.g. "arn:aws:states:::lambda:invoke"
// or "arn:aws:states:::lambda:invoke.waitForTaskToken".
// params is the JSON the state sends. The returned bytes are the result.
//
// Return an error made by [Error] to make the task fail. Any other error
// aborts [StateMachine.Run].
type API interface {
	Call(ctx context.Context, resource string, params []byte) ([]byte, error)
}

// APIFunc adapts a function to the API interface.
type APIFunc func(ctx context.Context, resource string, params []byte) ([]byte, error)

func (f APIFunc) Call(ctx context.Context, resource string, params []byte) ([]byte, error) {
	return f(ctx, resource, params)
}

// Clock is the clock of an execution. Wait states and retry intervals call
// Sleep; EnteredTime and other timestamps come from Now.
type Clock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

// FakeClock is a clock that advances instantly on Sleep.
type FakeClock struct {
	T time.Time
}

func (c *FakeClock) Now() time.Time        { return c.T }
func (c *FakeClock) Sleep(d time.Duration) { c.T = c.T.Add(d) }

// TaskToken generates task tokens for .waitForTaskToken tasks.
type TaskToken interface {
	New(state string) string
}

type randomTaskToken struct{}

func (randomTaskToken) New(string) string {
	b := make([]byte, 64)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Execution holds the values of the context object that stay the same for
// the whole execution. Zero-value fields are filled with defaults.
type Execution struct {
	// Default: arn:aws:states:us-east-1:123456789012:stateMachine:test
	StateMachineArn string
	// Default: a random UUID.
	Name string
	// Default: arn:aws:iam::123456789012:role/test
	RoleArn string
	// Default: the time Run is called.
	StartTime    time.Time
	RedriveCount int
	RedriveTime  time.Time
}

const (
	defaultStateMachineArn = "arn:aws:states:us-east-1:123456789012:stateMachine:test"
	defaultRoleArn         = "arn:aws:iam::123456789012:role/test"
)

// Env is the environment an execution runs in.
type Env struct {
	// API receives service calls. If nil, any service call aborts Run.
	API API
	// Intrinsic implements the States.* intrinsic functions.
	// If nil, DefaultIntrinsic is used.
	Intrinsic Intrinsic
	// Execution holds context object values. If nil, defaults are used.
	Execution *Execution
	// Clock is the execution clock. If nil, a FakeClock starting at
	// Execution.StartTime is used.
	Clock Clock
	// TaskToken generates task tokens. If nil, random tokens are used.
	TaskToken TaskToken
}

type resolvedEnv struct {
	api       API
	intrinsic Intrinsic
	exec      Execution
	clock     Clock
	taskToken TaskToken
}

func resolveEnv(env *Env) (*resolvedEnv, error) {
	if env == nil {
		env = &Env{}
	}

	r := &resolvedEnv{
		api:       env.API,
		intrinsic: env.Intrinsic,
		clock:     env.Clock,
		taskToken: env.TaskToken,
	}

	if env.Execution != nil {
		r.exec = *env.Execution
	}
	if r.exec.StateMachineArn == "" {
		r.exec.StateMachineArn = defaultStateMachineArn
	}
	if !strings.Contains(r.exec.StateMachineArn, ":stateMachine:") {
		return nil, fmt.Errorf("invalid state machine ARN: %s", r.exec.StateMachineArn)
	}
	if r.exec.Name == "" {
		r.exec.Name = newUUID()
	}
	if r.exec.RoleArn == "" {
		r.exec.RoleArn = defaultRoleArn
	}
	if r.exec.StartTime.IsZero() {
		r.exec.StartTime = time.Now()
	}
	if r.intrinsic == nil {
		r.intrinsic = DefaultIntrinsic{}
	}
	if r.clock == nil {
		r.clock = &FakeClock{T: r.exec.StartTime}
	}
	if r.taskToken == nil {
		r.taskToken = randomTaskToken{}
	}

	return r, nil
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// formatTimestamp formats t like Step Functions: ISO 8601 in UTC with zero,
// three, six or nine fractional digits.
func formatTimestamp(t time.Time) string {
	t = t.UTC()
	ns := t.Nanosecond()
	switch {
	case ns == 0:
		return t.Format("2006-01-02T15:04:05Z")
	case ns%1_000_000 == 0:
		return t.Format("2006-01-02T15:04:05.000Z")
	case ns%1_000 == 0:
		return t.Format("2006-01-02T15:04:05.000000Z")
	default:
		return t.Format("2006-01-02T15:04:05.000000000Z")
	}
}
