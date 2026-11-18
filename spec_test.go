package sfntest_test

// Tests that follow the Amazon States Language specification
// (https://states-language.net/spec.html) and the AWS Step Functions
// developer guide, section by section.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/sfntest"
)

func mustNew(t *testing.T, def string, opts ...sfntest.Option) *sfntest.StateMachine {
	t.Helper()
	sm, err := sfntest.New([]byte(def), opts...)
	require.NoError(t, err)
	return sm
}

func assertInvalid(t *testing.T, def, want string, opts ...sfntest.Option) {
	t.Helper()
	_, err := sfntest.New([]byte(def), opts...)
	assert.ErrorContains(t, err, want, def)
}

// ---- Structure of a state machine ----

func TestSpecTopLevelFields(t *testing.T) {
	// Comment, Version and TimeoutSeconds are allowed at the top level,
	// and Comment is allowed in every state and in Choice rules.
	def := `{"Comment":"c","Version":"1.0","TimeoutSeconds":60,"StartAt":"C","States":{
	  "C":{"Type":"Choice","Comment":"c","Choices":[{"Comment":"r","Variable":"$.a","IsPresent":true,"Next":"S"}],"Default":"S"},
	  "S":{"Type":"Succeed","Comment":"c"}}}`
	res := run(t, def, `{}`, nil)
	assert.Equal(t, sfntest.Succeeded, res.Status)
}

func TestSpecTransitions(t *testing.T) {
	// State names are case-sensitive.
	assertInvalid(t, pathDef(`"S":{"Type":"Pass","Next":"s"},"E":{"Type":"Succeed"}`), `Next: state "s" does not exist`)
	// Choice has no Next or End of its own.
	assertInvalid(t, pathDef(`"S":{"Type":"Choice","Choices":[{"Variable":"$","IsNull":true,"Next":"S"}],"End":true}`), `field "End" is not allowed`)
	// Succeed and Fail are terminal and cannot have Next.
	assertInvalid(t, pathDef(`"S":{"Type":"Succeed","Next":"S"}`), `field "Next" is not allowed`)
	assertInvalid(t, pathDef(`"S":{"Type":"Fail","Next":"S"}`), `field "Next" is not allowed`)
	// States in a Parallel branch can only transition to each other.
	assertInvalid(t, pathDef(`"S":{"Type":"Parallel","Branches":[{"StartAt":"A","States":{"A":{"Type":"Pass","Next":"E"}}}],"Next":"E"},"E":{"Type":"Succeed"}`), `state "E" does not exist`)
	// ... and no state outside a branch can transition into it.
	assertInvalid(t, pathDef(`"S":{"Type":"Parallel","Branches":[{"StartAt":"A","States":{"A":{"Type":"Succeed"}}}],"Next":"A"}`), `state "A" does not exist`)
	// The same applies to Map iterations.
	assertInvalid(t, pathDef(`"S":{"Type":"Map","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Pass","Next":"S"}}},"End":true}`), `state "S" does not exist`)
}

// ---- Paths ----

func TestSpecPaths(t *testing.T) {
	// Paths that may select several nodes return an array; a reference
	// path returns the value itself.
	def := pathDef(`"S":{"Type":"Pass","Parameters":{"many.$":"$.a[0,1]","one.$":"$.a[0]","esc.$":"$.foo\\@bar.baz\\[\\[\\.\\?pretty"},"End":true}`)
	res := run(t, def, `{"a":[1,2,3,4],"foo@bar":{"baz[[.?pretty":5}}`, nil)
	assert.JSONEq(t, `{"many":[1,2],"one":1,"esc":5}`, string(res.Output))

	// ResultPath must be a reference path into the state input: no context
	// object, no variables, no wildcards.
	assertInvalid(t, pathDef(`"S":{"Type":"Pass","ResultPath":"$$.x","End":true}`), "reference path")
	assertInvalid(t, pathDef(`"S":{"Type":"Pass","ResultPath":"$v","End":true}`), "reference path")
	assertInvalid(t, pathDef(`"S":{"Type":"Pass","ResultPath":"$.a[0:1]","End":true}`), "reference path")
}

func TestSpecInputOutputProcessing(t *testing.T) {
	t.Run("order of InputPath, Parameters, ResultSelector, ResultPath and OutputPath", func(t *testing.T) {
		var params string
		api := sfntest.APIFunc(func(_ context.Context, _ string, p []byte) ([]byte, error) {
			params = string(p)
			return []byte(`{"Payload":{"v":1,"noise":2}}`), nil
		})
		def := pathDef(`"S":{"Type":"Task","Resource":"r",
		  "InputPath":"$.in",
		  "Parameters":{"x.$":"$.x"},
		  "ResultSelector":{"v.$":"$.Payload.v"},
		  "ResultPath":"$.out.result",
		  "OutputPath":"$.out",
		  "End":true}`)
		res := run(t, def, `{"in":{"x":7},"out":{"keep":true}}`, &sfntest.Env{API: api})
		assert.JSONEq(t, `{"x":7}`, params)
		// ResultPath writes into the raw state input, not the effective input.
		assert.JSONEq(t, `{"keep":true,"result":{"v":1}}`, string(res.Output))
	})

	t.Run("ResultPath creates, overwrites and discards", func(t *testing.T) {
		def := pathDef(`"S":{"Type":"Pass","Result":6,"ResultPath":"$.master.result.sum","End":true}`)
		res := run(t, def, `{"master":{"detail":[1,2,3]}}`, nil)
		assert.JSONEq(t, `{"master":{"detail":[1,2,3],"result":{"sum":6}}}`, string(res.Output))

		def = pathDef(`"S":{"Type":"Pass","Result":"new","ResultPath":"$.comment","End":true}`)
		res = run(t, def, `{"comment":"old","x":1}`, nil)
		assert.JSONEq(t, `{"comment":"new","x":1}`, string(res.Output))

		def = pathDef(`"S":{"Type":"Pass","Result":"r","ResultPath":null,"End":true}`)
		res = run(t, def, `{"x":1}`, nil)
		assert.JSONEq(t, `{"x":1}`, string(res.Output))
	})

	t.Run("Pass Result takes precedence over Parameters", func(t *testing.T) {
		def := pathDef(`"S":{"Type":"Pass","Parameters":{"p":1},"Result":{"r":1},"End":true}`)
		res := run(t, def, `{}`, nil)
		assert.JSONEq(t, `{"r":1}`, string(res.Output))
	})

	t.Run("ResultSelector can flatten arrays", func(t *testing.T) {
		def := pathDef(`"S":{"Type":"Parallel","Branches":[
		  {"StartAt":"A","States":{"A":{"Type":"Pass","Result":[1,2],"End":true}}},
		  {"StartAt":"B","States":{"B":{"Type":"Pass","Result":[3],"End":true}}}],
		  "ResultSelector":{"all.$":"$[*][*]"},"End":true}`)
		res := run(t, def, `{}`, nil)
		assert.JSONEq(t, `{"all":[1,2,3]}`, string(res.Output))
	})

	t.Run("payload templates", func(t *testing.T) {
		// .$ keys at any depth, static arrays, context object and intrinsic functions
		def := pathDef(`"S":{"Type":"Pass","Parameters":{
		  "static":{"a":[1,{"b":2}]},
		  "nested":{"deep":{"v.$":"$.v"}},
		  "list":[{"v.$":"$.v"}],
		  "ctx.$":"$$.State.Name",
		  "fn.$":"States.Format('v={}', $.v)"},"End":true}`)
		res := run(t, def, `{"v":3}`, nil)
		assert.JSONEq(t, `{"static":{"a":[1,{"b":2}]},"nested":{"deep":{"v":3}},"list":[{"v":3}],"ctx":"S","fn":"v=3"}`, string(res.Output))

		// Duplicate field names after removing ".$" are invalid.
		assertInvalid(t, pathDef(`"S":{"Type":"Pass","Parameters":{"a.$":"$","a":1},"End":true}`), "duplicate field")
	})

	t.Run("intrinsic functions in ResultSelector and Assign", func(t *testing.T) {
		api := sfntest.APIFunc(func(context.Context, string, []byte) ([]byte, error) { return []byte(`{"n":1}`), nil })
		def := pathDef(`"S":{"Type":"Task","Resource":"r",
		  "ResultSelector":{"s.$":"States.Format('n{}', $.n)"},
		  "Assign":{"len.$":"States.ArrayLength(States.Array($.n, $.n))"},"Next":"E"},
		  "E":{"Type":"Pass","Parameters":{"len.$":"$len","s.$":"$.s"},"End":true}`)
		res := run(t, def, `{}`, &sfntest.Env{API: api})
		assert.JSONEq(t, `{"len":2,"s":"n1"}`, string(res.Output))
	})
}

// ---- Errors ----

func TestSpecErrorHandling(t *testing.T) {
	t.Run("MaxAttempts 0 never retries", func(t *testing.T) {
		api := &mockAPI{handler: func(int, string, map[string]any) (any, error) { return nil, sfntest.Error("E", "") }}
		def := pathDef(`"S":{"Type":"Task","Resource":"r","Retry":[{"ErrorEquals":["E"],"MaxAttempts":0}],"End":true}`)
		res := run(t, def, `{}`, &sfntest.Env{API: api})
		assert.Equal(t, "E", res.Error)
		assert.Len(t, api.calls, 1)
	})

	t.Run("the first matching retrier is used even when it is exhausted", func(t *testing.T) {
		// From the spec: the error matches the first retrier, which is
		// exhausted, so the second retrier is not used and Catch applies.
		api := &mockAPI{handler: func(int, string, map[string]any) (any, error) { return nil, sfntest.Error("E", "") }}
		def := pathDef(`"S":{"Type":"Task","Resource":"r",
		  "Retry":[{"ErrorEquals":["E"],"MaxAttempts":1},{"ErrorEquals":["States.ALL"],"MaxAttempts":5}],
		  "Catch":[{"ErrorEquals":["States.ALL"],"Next":"C"}],"End":true},"C":{"Type":"Succeed"}`)
		res := run(t, def, `{}`, &sfntest.Env{API: api})
		assert.Equal(t, sfntest.Succeeded, res.Status)
		assert.Len(t, api.calls, 2)
	})

	t.Run("retrier counters are kept per retrier across errors", func(t *testing.T) {
		// Errors alternate between A and B. Each retrier allows 2 retries,
		// so the 5th error (the third A) is not retried.
		api := &mockAPI{handler: func(n int, _ string, _ map[string]any) (any, error) {
			if n%2 == 1 {
				return nil, sfntest.Error("A", "")
			}
			return nil, sfntest.Error("B", "")
		}}
		def := pathDef(`"S":{"Type":"Task","Resource":"r",
		  "Retry":[{"ErrorEquals":["A"],"MaxAttempts":2},{"ErrorEquals":["B"],"MaxAttempts":2}],"End":true}`)
		res := run(t, def, `{}`, &sfntest.Env{API: api})
		assert.Equal(t, "A", res.Error)
		assert.Len(t, api.calls, 5)
	})

	t.Run("RetryCount in the context object", func(t *testing.T) {
		var counts []any
		api := &mockAPI{handler: func(n int, _ string, p map[string]any) (any, error) {
			counts = append(counts, p["count"])
			if n < 3 {
				return nil, sfntest.Error("E", "")
			}
			return 1, nil
		}}
		def := pathDef(`"S":{"Type":"Task","Resource":"r","Parameters":{"count.$":"$$.State.RetryCount"},
		  "Retry":[{"ErrorEquals":["E"]}],"End":true}`)
		run(t, def, `{}`, &sfntest.Env{API: api})
		assert.Equal(t, []any{float64(0), float64(1), float64(2)}, counts)
	})

	t.Run("catchers are matched in order", func(t *testing.T) {
		def := pathDef(`"S":{"Type":"Task","Resource":"r","Catch":[
		  {"ErrorEquals":["Other"],"Next":"A"},
		  {"ErrorEquals":["Task.Failed"],"Next":"B"},
		  {"ErrorEquals":["States.ALL"],"Next":"A"}],"End":true},
		  "A":{"Type":"Succeed"},"B":{"Type":"Succeed"}`)
		res := run(t, def, `{}`, &sfntest.Env{API: failAPI})
		assert.Equal(t, []string{"S", "B"}, path(res))
	})

	t.Run("States.TaskFailed does not match States.Timeout", func(t *testing.T) {
		timeout := sfntest.APIFunc(func(context.Context, string, []byte) ([]byte, error) {
			return nil, sfntest.Error("States.Timeout", "")
		})
		def := pathDef(`"S":{"Type":"Task","Resource":"r","Catch":[
		  {"ErrorEquals":["States.TaskFailed"],"Next":"A"},
		  {"ErrorEquals":["States.Timeout"],"Next":"B"}],"End":true},
		  "A":{"Type":"Succeed"},"B":{"Type":"Succeed"}`)
		res := run(t, def, `{}`, &sfntest.Env{API: timeout})
		assert.Equal(t, []string{"S", "B"}, path(res))

		res = run(t, def, `{}`, &sfntest.Env{API: failAPI})
		assert.Equal(t, []string{"S", "A"}, path(res))
	})

	t.Run("States.DataLimitExceeded is caught only when named", func(t *testing.T) {
		big := sfntest.APIFunc(func(context.Context, string, []byte) ([]byte, error) {
			return []byte(`"0123456789012345678901234567890123456789"`), nil
		})
		def := pathDef(`"S":{"Type":"Task","Resource":"r","Catch":[{"ErrorEquals":["States.DataLimitExceeded"],"ResultPath":null,"Next":"C"}],"End":true},"C":{"Type":"Succeed"}`)
		res := run(t, def, `{}`, &sfntest.Env{API: big}, sfntest.WithPayloadLimit(40))
		assert.Equal(t, sfntest.Succeeded, res.Status)
		assert.Equal(t, "States.DataLimitExceeded", res.History[0].Error)
	})

	t.Run("States.Runtime is neither retried nor caught", func(t *testing.T) {
		def := pathDef(`"S":{"Type":"Task","Resource":"r","InputPath":"$.missing",
		  "Retry":[{"ErrorEquals":["States.ALL"]}],"Catch":[{"ErrorEquals":["States.ALL"],"Next":"C"}],"End":true},"C":{"Type":"Succeed"}`)
		res := run(t, def, `{}`, &sfntest.Env{API: okAPI})
		assert.Equal(t, "States.Runtime", res.Error)
		assert.Len(t, path(res), 1)
	})

	t.Run("other States errors are caught by States.ALL", func(t *testing.T) {
		for _, def := range []string{
			pathDef(`"S":{"Type":"Task","Resource":"r","ResultPath":"$.x","Catch":[{"ErrorEquals":["States.ALL"],"Next":"C"}],"End":true},"C":{"Type":"Succeed"}`),
			pathDef(`"S":{"Type":"Task","Resource":"r","Parameters":{"a.$":"$.x"},"Catch":[{"ErrorEquals":["States.ALL"],"Next":"C"}],"End":true},"C":{"Type":"Succeed"}`),
			pathDef(`"S":{"Type":"Task","Resource":"r","Parameters":{"a.$":"States.ArrayGetItem($.a, 9)"},"Catch":[{"ErrorEquals":["States.ALL"],"Next":"C"}],"End":true},"C":{"Type":"Succeed"}`),
		} {
			res := run(t, def, `"str"`, &sfntest.Env{API: okAPI})
			assert.Equal(t, sfntest.Succeeded, res.Status, def)
		}
	})

	t.Run("Retry and Catch on Map retry all iterations", func(t *testing.T) {
		api := &mockAPI{handler: func(n int, _ string, _ map[string]any) (any, error) {
			if n == 2 {
				return nil, sfntest.Error("E", "")
			}
			return n, nil
		}}
		def := pathDef(`"S":{"Type":"Map","ItemProcessor":{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"r","End":true}}},
		  "Retry":[{"ErrorEquals":["E"]}],"End":true}`)
		res := run(t, def, `[1,2]`, &sfntest.Env{API: api})
		assert.Equal(t, sfntest.Succeeded, res.Status)
		// the first attempt called 1 (ok) and 2 (error); the retry ran both again
		assert.JSONEq(t, `[3,4]`, string(res.Output))
	})

	t.Run("Parallel fails with the error of the failed branch", func(t *testing.T) {
		def := pathDef(`"S":{"Type":"Parallel","Branches":[
		  {"StartAt":"A","States":{"A":{"Type":"Succeed"}}},
		  {"StartAt":"B","States":{"B":{"Type":"Fail","Error":"Bad","Cause":"b"}}}],"End":true}`)
		res := run(t, def, `{}`, nil)
		assert.Equal(t, "Bad", res.Error)
		assert.Equal(t, "b", res.Cause)
	})
}

// ---- Choice ----

func TestSpecChoice(t *testing.T) {
	choice := func(rule string) string {
		return pathDef(`"S":{"Type":"Choice","Choices":[{` + rule + `,"Next":"Y"}],"Default":"N"},"Y":{"Type":"Succeed"},"N":{"Type":"Succeed"}`)
	}
	matches := func(rule, input string) bool {
		t.Helper()
		res := run(t, choice(rule), input, nil)
		require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
		return res.History[1].State == "Y"
	}

	// Numbers compare by value.
	assert.True(t, matches(`"Variable":"$.n","NumericEquals":1`, `{"n":1.0}`))
	assert.True(t, matches(`"Variable":"$.n","NumericLessThanPath":"$.m"`, `{"n":1.5,"m":2}`))
	// Strings compare character by character without case folding.
	assert.False(t, matches(`"Variable":"$.s","StringEquals":"abc"`, `{"s":"ABC"}`))
	assert.True(t, matches(`"Variable":"$.s","StringLessThan":"b"`, `{"s":"B"}`))
	// A timestamp-looking string can be compared as a string.
	assert.True(t, matches(`"Variable":"$.t","StringEquals":"2026-01-01T00:00:00Z"`, `{"t":"2026-01-01T00:00:00Z"}`))
	// Timestamps must use an uppercase T and Z.
	assert.False(t, matches(`"Variable":"$.t","IsTimestamp":true`, `{"t":"2026-01-01t00:00:00z"}`))
	// Variables and the context object can be tested.
	res := run(t, `{"StartAt":"A","States":{"A":{"Type":"Pass","Assign":{"x":5},"Next":"S"},`+
		`"S":{"Type":"Choice","Choices":[{"And":[{"Variable":"$x","NumericEquals":5},{"Variable":"$$.State.Name","StringEquals":"S"}],"Next":"Y"}],"Default":"N"},`+
		`"Y":{"Type":"Succeed"},"N":{"Type":"Succeed"}}}`, `{}`, nil)
	assert.Equal(t, []string{"A", "S", "Y"}, path(res))
	// Or stops at the first true rule; a later rule would raise an error.
	assert.True(t, matches(`"Or":[{"Variable":"$.a","IsPresent":true},{"Variable":"$.missing","IsNull":true}]`, `{"a":1}`))
	// And stops at the first false rule.
	assert.False(t, matches(`"And":[{"Variable":"$.a","IsPresent":false},{"Variable":"$.missing","IsNull":true}]`, `{"a":1}`))
	// Rules are evaluated in order and the first match wins.
	res = run(t, pathDef(`"S":{"Type":"Choice","Choices":[
	  {"Variable":"$.n","NumericGreaterThan":0,"Next":"A"},{"Variable":"$.n","NumericGreaterThan":1,"Next":"B"}]},
	  "A":{"Type":"Succeed"},"B":{"Type":"Succeed"}`), `{"n":5}`, nil)
	assert.Equal(t, []string{"S", "A"}, path(res))
	// The output of a Choice state is its effective input after OutputPath.
	res = run(t, pathDef(`"S":{"Type":"Choice","InputPath":"$.in","OutputPath":"$.x","Choices":[{"Variable":"$.x","IsPresent":true,"Next":"E"}]},"E":{"Type":"Succeed"}`), `{"in":{"x":1}}`, nil)
	assert.JSONEq(t, `1`, string(res.Output))
	// "$" in a rule's Assign is the effective input.
	res = run(t, pathDef(`"S":{"Type":"Choice","InputPath":"$.in","Choices":[{"Variable":"$.x","IsPresent":true,"Assign":{"v.$":"$.x"},"Next":"E"}]},"E":{"Type":"Pass","Parameters":{"v.$":"$v"},"End":true}`), `{"in":{"x":1}}`, nil)
	assert.JSONEq(t, `{"v":1}`, string(res.Output))
}

// ---- Wait ----

func TestSpecWait(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &sfntest.FakeClock{T: start}
	// A timestamp in the past does not wait.
	res := run(t, pathDef(`"S":{"Type":"Wait","Timestamp":"2020-01-01T00:00:00Z","Next":"W"},"W":{"Type":"Wait","Seconds":0,"End":true}`), `{}`, &sfntest.Env{Clock: clock})
	assert.Equal(t, sfntest.Succeeded, res.Status)
	assert.Equal(t, start, clock.T)
	// Seconds must be an integer.
	assertInvalid(t, pathDef(`"S":{"Type":"Wait","Seconds":1.5,"End":true}`), "Seconds: must be a non-negative integer")
}

// ---- Task ----

func TestSpecTaskTimeouts(t *testing.T) {
	// TimeoutSeconds and HeartbeatSeconds are positive integers, and
	// HeartbeatSeconds must be smaller than TimeoutSeconds.
	mustNew(t, pathDef(`"S":{"Type":"Task","Resource":"r","TimeoutSeconds":10,"HeartbeatSeconds":5,"End":true}`))
	mustNew(t, pathDef(`"S":{"Type":"Task","Resource":"r","TimeoutSecondsPath":"$.t","HeartbeatSecondsPath":"$.h","End":true}`))
	assertInvalid(t, pathDef(`"S":{"Type":"Task","Resource":"r","TimeoutSeconds":0,"End":true}`), "TimeoutSeconds: must be a positive integer")
	assertInvalid(t, pathDef(`"S":{"Type":"Task","Resource":"r","HeartbeatSeconds":"5","End":true}`), "HeartbeatSeconds: must be a positive integer")
	assertInvalid(t, pathDef(`"S":{"Type":"Task","Resource":"r","TimeoutSeconds":5,"HeartbeatSeconds":5,"End":true}`), "HeartbeatSeconds must be smaller than TimeoutSeconds")
	assertInvalid(t, pathDef(`"S":{"Type":"Task","Resource":"r","TimeoutSeconds":5,"TimeoutSecondsPath":"$.t","End":true}`), "TimeoutSeconds and TimeoutSecondsPath cannot be used together")
	assertInvalid(t, pathDef(`"S":{"Type":"Task","Resource":"r","HeartbeatSeconds":5,"HeartbeatSecondsPath":"$.t","End":true}`), "HeartbeatSeconds and HeartbeatSecondsPath cannot be used together")
	assertInvalid(t, pathDef(`"S":{"Type":"Task","Resource":"r","TimeoutSecondsPath":"x","End":true}`), "TimeoutSecondsPath")

	// The same rules apply to JSONata states, where the fields may also be expressions.
	mustNew(t, jsonataDef(`"S":{"Type":"Task","Resource":"r","TimeoutSeconds":"{% 10 %}","HeartbeatSeconds":"{% 5 %}","End":true}`))
	assertInvalid(t, jsonataDef(`"S":{"Type":"Task","Resource":"r","TimeoutSeconds":10,"HeartbeatSeconds":20,"End":true}`), "HeartbeatSeconds must be smaller than TimeoutSeconds")

	// The values are evaluated at run time (a timeout itself is reported by the mock).
	res := run(t, pathDef(`"S":{"Type":"Task","Resource":"r","TimeoutSecondsPath":"$.t","End":true}`), `{}`, &sfntest.Env{API: okAPI})
	assert.Equal(t, "States.Runtime", res.Error)
	res = run(t, pathDef(`"S":{"Type":"Task","Resource":"r","HeartbeatSecondsPath":"$.h","End":true}`), `{"h":0}`, &sfntest.Env{API: okAPI})
	assert.Equal(t, "States.Runtime", res.Error)
	res = run(t, pathDef(`"S":{"Type":"Task","Resource":"r","TimeoutSecondsPath":"$.t","End":true}`), `{"t":3}`, &sfntest.Env{API: okAPI})
	assert.Equal(t, sfntest.Succeeded, res.Status)
}

// ---- Map ----

func TestSpecMap(t *testing.T) {
	t.Run("legacy Parameters works as ItemSelector", func(t *testing.T) {
		def := pathDef(`"S":{"Type":"Map","ItemsPath":"$.items","Parameters":{"i.$":"$$.Map.Item.Index","v.$":"$$.Map.Item.Value","k.$":"$.k"},
		  "Iterator":{"StartAt":"A","States":{"A":{"Type":"Pass","End":true}}},"End":true}`)
		res := run(t, def, `{"k":"K","items":["a"]}`, nil)
		assert.JSONEq(t, `[{"i":0,"v":"a","k":"K"}]`, string(res.Output))
	})

	t.Run("ItemsPath defaults to the effective input", func(t *testing.T) {
		def := pathDef(`"S":{"Type":"Map","InputPath":"$.items","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Pass","End":true}}},"End":true}`)
		res := run(t, def, `{"items":[1,2]}`, nil)
		assert.JSONEq(t, `[1,2]`, string(res.Output))
	})

	t.Run("Map.Item is not available inside the iteration", func(t *testing.T) {
		def := pathDef(`"S":{"Type":"Map","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Pass","Parameters":{"i.$":"$$.Map.Item.Index"},"End":true}}},"End":true}`)
		res := run(t, def, `[1]`, nil)
		assert.Equal(t, "States.ParameterPathFailure", res.Error)
	})

	t.Run("MaxConcurrency", func(t *testing.T) {
		mustNew(t, pathDef(`"S":{"Type":"Map","MaxConcurrency":1,"ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`))
		mustNew(t, jsonataDef(`"S":{"Type":"Map","MaxConcurrency":"{% 2 %}","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`))
		assertInvalid(t, pathDef(`"S":{"Type":"Map","MaxConcurrency":1,"MaxConcurrencyPath":"$.m","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`), "MaxConcurrency and MaxConcurrencyPath cannot be used together")
		assertInvalid(t, pathDef(`"S":{"Type":"Map","MaxConcurrency":-1,"ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`), "MaxConcurrency: must be a non-negative integer")
		assertInvalid(t, pathDef(`"S":{"Type":"Map","MaxConcurrencyPath":"x","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`), "MaxConcurrencyPath")
	})
}

// ---- Variables ----

func TestSpecVariables(t *testing.T) {
	t.Run("expressions see the values at state entry", func(t *testing.T) {
		// From the docs: with $x=3 and $a=6, {"x": $a, "nextX": $x} gives $x=6 and $nextX=3.
		def := pathDef(`"S":{"Type":"Pass","Assign":{"x":3,"a":6},"Next":"T"},
		  "T":{"Type":"Pass","Assign":{"x.$":"$a","nextX.$":"$x"},"Next":"U"},
		  "U":{"Type":"Pass","Parameters":{"x.$":"$x","nextX.$":"$nextX"},"End":true}`)
		res := run(t, def, `{}`, nil)
		assert.JSONEq(t, `{"x":6,"nextX":3}`, string(res.Output))
	})

	t.Run("variables are not visible in the state that assigns them", func(t *testing.T) {
		def := pathDef(`"S":{"Type":"Pass","Assign":{"x":1},"Parameters":{"x.$":"$x"},"End":true}`)
		res := run(t, def, `{}`, nil)
		assert.Equal(t, "States.ParameterPathFailure", res.Error)
	})

	t.Run("inner scopes read outer variables but do not leak", func(t *testing.T) {
		def := pathDef(`"S":{"Type":"Pass","Assign":{"outer":1},"Next":"P"},
		  "P":{"Type":"Parallel","Branches":[{"StartAt":"A","States":{
		    "A":{"Type":"Pass","Parameters":{"o.$":"$outer"},"Assign":{"inner":2},"End":true}}}],"Next":"E"},
		  "E":{"Type":"Pass","Parameters":{"i.$":"$inner"},"End":true}`)
		res := run(t, def, `{}`, nil)
		assert.Equal(t, "States.ParameterPathFailure", res.Error)
		assert.JSONEq(t, `[{"o":1}]`, string(res.History[1].Output))
	})

	t.Run("branches do not see each other's variables", func(t *testing.T) {
		def := pathDef(`"S":{"Type":"Parallel","Branches":[
		  {"StartAt":"A","States":{"A":{"Type":"Pass","Assign":{"a":1},"End":true}}},
		  {"StartAt":"B","States":{"B":{"Type":"Pass","Parameters":{"a.$":"$a"},"End":true}}}],"End":true}`)
		res := run(t, def, `{}`, nil)
		assert.Equal(t, "States.ParameterPathFailure", res.Error)
	})

	t.Run("a Catch in a Map writes to the outer scope", func(t *testing.T) {
		def := pathDef(`"S":{"Type":"Map","ItemProcessor":{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"E"}}},
		  "Catch":[{"ErrorEquals":["States.ALL"],"Assign":{"err.$":"$.Error"},"Next":"T"}],"End":true},
		  "T":{"Type":"Pass","Parameters":{"err.$":"$err"},"End":true}`)
		res := run(t, def, `[1]`, nil)
		assert.JSONEq(t, `{"err":"E"}`, string(res.Output))
	})

	t.Run("an inner scope cannot reuse an outer variable name", func(t *testing.T) {
		assertInvalid(t, pathDef(`"S":{"Type":"Pass","Assign":{"x":1},"Next":"M"},
		  "M":{"Type":"Map","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Pass","Assign":{"x":2},"End":true}}},"End":true}`),
			`variable "x" is already assigned in an outer scope`)
		assertInvalid(t, pathDef(`"S":{"Type":"Parallel","Assign":{"y":1},"Branches":[{"StartAt":"A","States":{"A":{"Type":"Pass","Assign":{"y":2},"End":true}}}],"End":true}`),
			`variable "y" is already assigned in an outer scope`)
		assertInvalid(t, jsonataDef(`"S":{"Type":"Pass","Assign":{"z":1},"Next":"P"},
		  "P":{"Type":"Parallel","Branches":[{"StartAt":"A","States":{"A":{"Type":"Choice","Choices":[{"Condition":true,"Next":"B","Assign":{"z":2}}]},"B":{"Type":"Succeed"}}}],"End":true}`),
			`variable "z" is already assigned in an outer scope`)
		// A Catch of the Map itself assigns in the outer scope, so it does not clash.
		mustNew(t, pathDef(`"S":{"Type":"Map","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},
		  "Catch":[{"ErrorEquals":["States.ALL"],"Assign":{"x":1},"Next":"T"}],"Assign":{"x":2},"End":true},"T":{"Type":"Succeed"}`))
		// Sibling branches may use the same name.
		mustNew(t, pathDef(`"S":{"Type":"Parallel","Branches":[
		  {"StartAt":"A","States":{"A":{"Type":"Pass","Assign":{"w":1},"End":true}}},
		  {"StartAt":"B","States":{"B":{"Type":"Pass","Assign":{"w":2},"End":true}}}],"End":true}`))
		// A distributed Map does not see outer variables, so its children may reuse names.
		mustNew(t, pathDef(`"S":{"Type":"Pass","Assign":{"v":1},"Next":"M"},
		  "M":{"Type":"Map","ItemProcessor":{"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"STANDARD"},
		    "StartAt":"A","States":{"A":{"Type":"Pass","Assign":{"v":2},"End":true}}},"End":true}`))
	})
}

// ---- JSONata ----

func TestSpecJSONata(t *testing.T) {
	t.Run("Output and Arguments accept any JSON value", func(t *testing.T) {
		res := run(t, jsonataDef(`"S":{"Type":"Pass","Output":42,"End":true}`), `{}`, nil)
		assert.JSONEq(t, `42`, string(res.Output))

		var got string
		api := sfntest.APIFunc(func(_ context.Context, _ string, p []byte) ([]byte, error) {
			got = string(p)
			return []byte(`null`), nil
		})
		run(t, jsonataDef(`"S":{"Type":"Task","Resource":"r","Arguments":[1,"{% 1 + 1 %}",{"a":"{% 'x' %}"}],"End":true}`), `{}`, &sfntest.Env{API: api})
		assert.JSONEq(t, `[1,2,{"a":"x"}]`, got)
	})

	t.Run("strings without {% %} are literals", func(t *testing.T) {
		res := run(t, jsonataDef(`"S":{"Type":"Pass","Output":{"a":"$states.input","b":" {% 1 %}x","c":"$.x"},"End":true}`), `{}`, nil)
		assert.JSONEq(t, `{"a":"$states.input","b":" {% 1 %}x","c":"$.x"}`, string(res.Output))
	})

	t.Run("Map Items may be an array with expressions", func(t *testing.T) {
		res := run(t, jsonataDef(`"S":{"Type":"Map","Items":[1,"{% 1 + 1 %}",3],
		  "ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Pass","End":true}}},"End":true}`), `{}`, nil)
		assert.JSONEq(t, `[1,2,3]`, string(res.Output))
	})

	t.Run("Assign and Output are evaluated in parallel", func(t *testing.T) {
		res := run(t, jsonataDef(`"S":{"Type":"Pass","Assign":{"x":1},"Next":"T"},
		  "T":{"Type":"Pass","Assign":{"x":2},"Output":{"x":"{% $x %}"},"Next":"U"},
		  "U":{"Type":"Pass","Output":{"x":"{% $x %}","prev":"{% $states.input.x %}"},"End":true}`), `{}`, nil)
		assert.JSONEq(t, `{"x":2,"prev":1}`, string(res.Output))
	})

	t.Run("$states.input is the state input in every field", func(t *testing.T) {
		var got string
		api := sfntest.APIFunc(func(_ context.Context, _ string, p []byte) ([]byte, error) {
			got = string(p)
			return []byte(`{"r":1}`), nil
		})
		res := run(t, jsonataDef(`"S":{"Type":"Task","Resource":"r","Arguments":{"a":"{% $states.input.a %}"},
		  "Output":{"in":"{% $states.input %}","res":"{% $states.result %}"},"End":true}`), `{"a":1}`, &sfntest.Env{API: api})
		assert.JSONEq(t, `{"a":1}`, got)
		assert.JSONEq(t, `{"in":{"a":1},"res":{"r":1}}`, string(res.Output))
	})

	t.Run("$states.context in JSONata", func(t *testing.T) {
		res := run(t, jsonataDef(`"S":{"Type":"Pass","Output":{"name":"{% $states.context.State.Name %}","input":"{% $states.context.Execution.Input %}"},"End":true}`), `{"k":1}`, nil)
		assert.JSONEq(t, `{"name":"S","input":{"k":1}}`, string(res.Output))
	})

	t.Run("$states.result and $states.errorOutput are rejected where unavailable", func(t *testing.T) {
		// $states.result is only for Task, Parallel and Map, and not in Arguments.
		assertInvalid(t, jsonataDef(`"S":{"Type":"Pass","Output":"{% $states.result %}","End":true}`), "$states.result is not available")
		assertInvalid(t, jsonataDef(`"S":{"Type":"Task","Resource":"r","Arguments":"{% $states.result %}","End":true}`), "$states.result is not available")
		assertInvalid(t, jsonataDef(`"S":{"Type":"Choice","Choices":[{"Condition":"{% $states.result %}","Next":"S"}]}`), "$states.result is not available")
		assertInvalid(t, jsonataDef(`"S":{"Type":"Map","Items":"{% $states.result %}","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`), "$states.result is not available")
		// $states.errorOutput is only for a Catch of Task, Parallel and Map.
		assertInvalid(t, jsonataDef(`"S":{"Type":"Task","Resource":"r","Output":"{% $states.errorOutput %}","End":true}`), "$states.errorOutput is not available")
		assertInvalid(t, jsonataDef(`"S":{"Type":"Fail","Error":"{% $states.errorOutput.Error %}"}`), "$states.errorOutput is not available")
		// $states.result is not available in a Catch either.
		assertInvalid(t, jsonataDef(`"S":{"Type":"Task","Resource":"r","Catch":[{"ErrorEquals":["States.ALL"],"Output":"{% $states.result %}","Next":"S"}],"End":true}`), "$states.result is not available")

		mustNew(t, jsonataDef(`"S":{"Type":"Task","Resource":"r","Output":"{% $states.result %}","Assign":{"r":"{% $states.result %}"},
		  "Catch":[{"ErrorEquals":["States.ALL"],"Output":"{% $states.errorOutput %}","Assign":{"e":"{% $states.errorOutput %}"},"Next":"E"}],"End":true},"E":{"Type":"Succeed"}`))
		mustNew(t, jsonataDef(`"S":{"Type":"Map","Output":"{% $states.result %}","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`))
		mustNew(t, jsonataDef(`"S":{"Type":"Parallel","Output":"{% $states.result %}","Branches":[{"StartAt":"A","States":{"A":{"Type":"Succeed"}}}],"End":true}`))
	})

	t.Run("JSONPath-only fields are rejected", func(t *testing.T) {
		for _, f := range []string{`"InputPath":"$"`, `"Parameters":{}`, `"ResultSelector":{}`, `"ResultPath":"$"`, `"OutputPath":"$"`} {
			assertInvalid(t, jsonataDef(`"S":{"Type":"Task","Resource":"r",`+f+`,"End":true}`), "is not allowed in a JSONata Task state")
		}
		assertInvalid(t, jsonataDef(`"S":{"Type":"Pass","Result":1,"End":true}`), `field "Result" is not allowed`)
		assertInvalid(t, jsonataDef(`"S":{"Type":"Map","ItemsPath":"$","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`), `field "ItemsPath" is not allowed`)
		assertInvalid(t, jsonataDef(`"S":{"Type":"Wait","SecondsPath":"$.s","End":true}`), `field "SecondsPath" is not allowed`)
		assertInvalid(t, jsonataDef(`"S":{"Type":"Fail","ErrorPath":"$.e"}`), `field "ErrorPath" is not allowed`)
	})

	t.Run("JSONata fields are rejected in JSONPath states", func(t *testing.T) {
		assertInvalid(t, pathDef(`"S":{"Type":"Task","Resource":"r","Arguments":{},"End":true}`), `field "Arguments" is not allowed`)
		assertInvalid(t, pathDef(`"S":{"Type":"Pass","Output":{},"End":true}`), `field "Output" is not allowed`)
		assertInvalid(t, pathDef(`"S":{"Type":"Choice","Choices":[{"Condition":true,"Next":"S"}]}`), "unknown field")
	})

	t.Run("integer arguments of Step Functions functions are rounded down", func(t *testing.T) {
		res := run(t, jsonataDef(`"S":{"Type":"Pass","Output":{"p":"{% $partition([1,2,3], 2.9) %}","r":"{% $range(0.9, 3.9, 1.9) %}"},"End":true}`), `{}`, nil)
		assert.JSONEq(t, `{"p":[[1,2],[3]],"r":[0,1,2,3]}`, string(res.Output))
	})

	t.Run("an unassigned variable is undefined", func(t *testing.T) {
		res := run(t, jsonataDef(`"S":{"Type":"Pass","Output":"{% $nope %}","End":true}`), `{}`, nil)
		assert.Equal(t, "States.QueryEvaluationError", res.Error)
	})
}

// ---- Intrinsic functions: documented examples ----

func TestSpecIntrinsicExamples(t *testing.T) {
	cases := []struct{ input, expr, want string }{
		{`{"Id":123456}`, `States.Array($.Id)`, `[123456]`},
		{`{"inputArray":[1,2,3,4,5,6,7,8,9]}`, `States.ArrayPartition($.inputArray,4)`, `[[1,2,3,4],[5,6,7,8],[9]]`},
		{`{"inputArray":[1,2,3,4,5,6,7,8,9],"lookingFor":5}`, `States.ArrayContains($.inputArray, $.lookingFor)`, `true`},
		{`{}`, `States.ArrayRange(1, 9, 2)`, `[1,3,5,7,9]`},
		{`{"inputArray":[1,2,3,4,5,6,7,8,9],"index":5}`, `States.ArrayGetItem($.inputArray, $.index)`, `6`},
		{`{"inputArray":[1,2,3,4,5,6,7,8,9]}`, `States.ArrayLength($.inputArray)`, `9`},
		{`{"inputArray":[1,2,3,3,3,3,3,3,4]}`, `States.ArrayUnique($.inputArray)`, `[1,2,3,4]`},
		{`{"input":"Data to encode"}`, `States.Base64Encode($.input)`, `"RGF0YSB0byBlbmNvZGU="`},
		{`{"base64":"RGF0YSB0byBlbmNvZGU="}`, `States.Base64Decode($.base64)`, `"Data to encode"`},
		{`{"json1":{"a":{"a1":1,"a2":2},"b":2},"json2":{"a":{"a3":1,"a4":2},"c":3}}`, `States.JsonMerge($.json1, $.json2, false)`, `{"a":{"a3":1,"a4":2},"b":2,"c":3}`},
		{`{"escapedJsonString":"{\"foo\": \"bar\"}"}`, `States.StringToJson($.escapedJsonString)`, `{"foo":"bar"}`},
		{`{"unescapedJson":{"foo":"bar"}}`, `States.JsonToString($.unescapedJson)`, `"{\"foo\":\"bar\"}"`},
		{`{"value1":111,"step":-1}`, `States.MathAdd($.value1, $.step)`, `110`},
		{`{"inputString":"1,2,3,4,5","splitter":","}`, `States.StringSplit($.inputString, $.splitter)`, `["1","2","3","4","5"]`},
		{`{"inputString":"This.is+a,test=string","splitter":".+,="}`, `States.StringSplit($.inputString, $.splitter)`, `["This","is","a","test","string"]`},
		{`{"name":"Arnav"}`, `States.Format('Hello, my name is {}.', $.name)`, `"Hello, my name is Arnav."`},
		{`{"name":"Arnav","template":"Hello, my name is {}."}`, `States.Format($.template, $.name)`, `"Hello, my name is Arnav."`},
		// non-integer arguments are rounded
		{`{}`, `States.ArrayRange(1.4, 2.6, 1)`, `[1,2,3]`},
		{`{}`, `States.MathAdd(1.5, 1)`, `3`},
	}
	for _, c := range cases {
		b, _ := json.Marshal(c.expr)
		def := pathDef(`"S":{"Type":"Pass","Parameters":{"v.$":` + string(b) + `},"End":true}`)
		res := run(t, def, c.input, nil)
		require.Equal(t, sfntest.Succeeded, res.Status, "%s: %s", c.expr, res.Cause)
		assert.JSONEq(t, `{"v":`+c.want+`}`, string(res.Output), c.expr)
	}

	// MathRandom returns an integer in [start, end).
	for i := 0; i < 20; i++ {
		res := run(t, pathDef(`"S":{"Type":"Pass","Parameters":{"v.$":"States.MathRandom(1, 3)"},"End":true}`), `{}`, nil)
		assert.Contains(t, []string{`{"v":1}`, `{"v":2}`}, string(res.Output))
	}
	// Intrinsic functions are not available in JSONata states.
	res := run(t, jsonataDef(`"S":{"Type":"Pass","Output":"States.UUID()","End":true}`), `{}`, nil)
	assert.JSONEq(t, `"States.UUID()"`, string(res.Output))
}

// ---- Context object ----

func TestSpecContextObject(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &sfntest.FakeClock{T: start}
	env := &sfntest.Env{Clock: clock, Execution: &sfntest.Execution{StartTime: start, Name: "e", RoleArn: "arn:aws:iam::1:role/r"}}
	def := pathDef(`"S":{"Type":"Wait","Seconds":5,"Next":"T"},
	  "T":{"Type":"Pass","Parameters":{
	    "entered.$":"$$.State.EnteredTime","start.$":"$$.Execution.StartTime","role.$":"$$.Execution.RoleArn",
	    "retry.$":"$$.State.RetryCount","redrive.$":"$$.Execution.RedriveCount"},"End":true}`)
	res := run(t, def, `{}`, env)
	// Timestamps with zero fractional seconds have no fraction.
	assert.JSONEq(t, `{"entered":"2026-01-01T00:00:05Z","start":"2026-01-01T00:00:00Z","role":"arn:aws:iam::1:role/r","retry":0,"redrive":0}`, string(res.Output))

	// RedriveTime appears only after a redrive.
	res = run(t, pathDef(`"S":{"Type":"Pass","Parameters":{"r.$":"$$.Execution.RedriveTime"},"End":true}`), `{}`, nil)
	assert.Equal(t, "States.ParameterPathFailure", res.Error)
}

// ---- Service integrations ----

func TestSpecResources(t *testing.T) {
	// Every integration pattern reaches the API mock with its Resource as is;
	// the mock returns what the state receives as the result.
	for _, resource := range []string{
		"arn:aws:lambda:us-east-1:123456789012:function:f",
		"arn:aws:states:::lambda:invoke",
		"arn:aws:states:us-east-1:123456789012:activity:a",
		"arn:aws:states:::ecs:runTask.sync",
		"arn:aws:states:::glue:startJobRun.sync:2",
		"arn:aws:states:::sqs:sendMessage.waitForTaskToken",
		"arn:aws:states:::aws-sdk:s3:listBuckets",
		"arn:aws:states:::http:invoke",
		"arn:aws:states:::states:startExecution.sync:2",
	} {
		var got string
		api := sfntest.APIFunc(func(_ context.Context, r string, _ []byte) ([]byte, error) {
			got = r
			return []byte(`"ok"`), nil
		})
		res := run(t, pathDef(`"S":{"Type":"Task","Resource":"`+resource+`","End":true}`), `[1,"a"]`, &sfntest.Env{API: api})
		assert.Equal(t, resource, got)
		assert.JSONEq(t, `"ok"`, string(res.Output), resource)
	}

	// Without Parameters, the effective input is sent as is, whatever its type.
	var params string
	api := sfntest.APIFunc(func(_ context.Context, _ string, p []byte) ([]byte, error) {
		params = string(p)
		return []byte(`null`), nil
	})
	run(t, pathDef(`"S":{"Type":"Task","Resource":"r","InputPath":"$.list","End":true}`), `{"list":[1,"a"]}`, &sfntest.Env{API: api})
	assert.JSONEq(t, `[1,"a"]`, params)
}
