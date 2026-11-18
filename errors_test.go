package sfntest_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/sfntest"
)

func pathDef(states string) string {
	return `{"StartAt":"S","States":{` + states + `}}`
}

func jsonataDef(states string) string {
	return `{"QueryLanguage":"JSONata","StartAt":"S","States":{` + states + `}}`
}

var okAPI = sfntest.APIFunc(func(context.Context, string, []byte) ([]byte, error) {
	return []byte(`{"r":1}`), nil
})

var failAPI = sfntest.APIFunc(func(context.Context, string, []byte) ([]byte, error) {
	return nil, sfntest.Error("Task.Failed", "boom")
})

// TestStateErrors checks that errors raised while processing each field
// fail the execution with the right error name.
func TestStateErrors(t *testing.T) {
	cases := []struct {
		name  string
		def   string
		input string
		api   sfntest.API
		want  string
	}{
		// JSONPath: Pass
		{"pass inputpath", pathDef(`"S":{"Type":"Pass","InputPath":"$.x","End":true}`), `{}`, nil, "States.Runtime"},
		{"pass parameters", pathDef(`"S":{"Type":"Pass","Parameters":{"a.$":"$.x"},"End":true}`), `{}`, nil, "States.ParameterPathFailure"},
		{"pass parameters array", pathDef(`"S":{"Type":"Pass","Parameters":{"a":[{"b.$":"$.x"}]},"End":true}`), `{}`, nil, "States.ParameterPathFailure"},
		{"pass assign", pathDef(`"S":{"Type":"Pass","Assign":{"a.$":"$.x"},"End":true}`), `{}`, nil, "States.ParameterPathFailure"},
		{"pass outputpath", pathDef(`"S":{"Type":"Pass","OutputPath":"$.x","End":true}`), `{}`, nil, "States.Runtime"},
		// JSONPath: Task
		{"task inputpath", pathDef(`"S":{"Type":"Task","Resource":"r","InputPath":"$.x","End":true}`), `{}`, okAPI, "States.Runtime"},
		{"task parameters", pathDef(`"S":{"Type":"Task","Resource":"r","Parameters":{"a.$":"$.x"},"End":true}`), `{}`, okAPI, "States.ParameterPathFailure"},
		{"task resultselector", pathDef(`"S":{"Type":"Task","Resource":"r","ResultSelector":{"a.$":"$.x"},"End":true}`), `{}`, okAPI, "States.ParameterPathFailure"},
		{"task assign", pathDef(`"S":{"Type":"Task","Resource":"r","Assign":{"a.$":"$.x"},"End":true}`), `{}`, okAPI, "States.ParameterPathFailure"},
		{"task resultpath", pathDef(`"S":{"Type":"Task","Resource":"r","ResultPath":"$.x","End":true}`), `"str"`, okAPI, "States.ResultPathMatchFailure"},
		{"task outputpath", pathDef(`"S":{"Type":"Task","Resource":"r","OutputPath":"$.x","End":true}`), `{}`, okAPI, "States.Runtime"},
		{"task error", pathDef(`"S":{"Type":"Task","Resource":"r","End":true}`), `{}`, failAPI, "Task.Failed"},
		// JSONPath: Choice
		{"choice inputpath", pathDef(`"S":{"Type":"Choice","InputPath":"$.x","Choices":[{"Variable":"$.a","IsPresent":true,"Next":"E"}]},"E":{"Type":"Succeed"}`), `{}`, nil, "States.Runtime"},
		{"choice undefined variable", pathDef(`"S":{"Type":"Choice","Choices":[{"Variable":"$v","IsNull":true,"Next":"E"}]},"E":{"Type":"Succeed"}`), `{}`, nil, "States.Runtime"},
		{"choice operand path", pathDef(`"S":{"Type":"Choice","Choices":[{"Variable":"$.a","StringEqualsPath":"$.x","Next":"E"}]},"E":{"Type":"Succeed"}`), `{"a":"s"}`, nil, "States.Runtime"},
		{"choice assign", pathDef(`"S":{"Type":"Choice","Choices":[{"Variable":"$.a","IsPresent":true,"Next":"E","Assign":{"v.$":"$.x"}}]},"E":{"Type":"Succeed"}`), `{"a":1}`, nil, "States.ParameterPathFailure"},
		{"choice outputpath", pathDef(`"S":{"Type":"Choice","OutputPath":"$.x","Choices":[{"Variable":"$.a","IsPresent":true,"Next":"E"}]},"E":{"Type":"Succeed"}`), `{"a":1}`, nil, "States.Runtime"},
		// JSONPath: Wait
		{"wait inputpath", pathDef(`"S":{"Type":"Wait","Seconds":1,"InputPath":"$.x","End":true}`), `{}`, nil, "States.Runtime"},
		{"wait secondspath missing", pathDef(`"S":{"Type":"Wait","SecondsPath":"$.x","End":true}`), `{}`, nil, "States.Runtime"},
		{"wait secondspath negative", pathDef(`"S":{"Type":"Wait","SecondsPath":"$.x","End":true}`), `{"x":-1}`, nil, "States.Runtime"},
		{"wait timestamppath missing", pathDef(`"S":{"Type":"Wait","TimestampPath":"$.x","End":true}`), `{}`, nil, "States.Runtime"},
		{"wait timestamppath invalid", pathDef(`"S":{"Type":"Wait","TimestampPath":"$.x","End":true}`), `{"x":"now"}`, nil, "States.Runtime"},
		{"wait assign", pathDef(`"S":{"Type":"Wait","Seconds":1,"Assign":{"a.$":"$.x"},"End":true}`), `{}`, nil, "States.ParameterPathFailure"},
		{"wait outputpath", pathDef(`"S":{"Type":"Wait","Seconds":1,"OutputPath":"$.x","End":true}`), `{}`, nil, "States.Runtime"},
		// JSONPath: Succeed / Fail
		{"succeed inputpath", pathDef(`"S":{"Type":"Succeed","InputPath":"$.x"}`), `{}`, nil, "States.Runtime"},
		{"succeed outputpath", pathDef(`"S":{"Type":"Succeed","OutputPath":"$.x"}`), `{}`, nil, "States.Runtime"},
		{"fail errorpath missing", pathDef(`"S":{"Type":"Fail","ErrorPath":"$.x"}`), `{}`, nil, "States.Runtime"},
		{"fail errorpath not string", pathDef(`"S":{"Type":"Fail","ErrorPath":"$.x"}`), `{"x":1}`, nil, "States.Runtime"},
		{"fail causepath missing", pathDef(`"S":{"Type":"Fail","Error":"E","CausePath":"$.x"}`), `{}`, nil, "States.Runtime"},
		{"fail empty", pathDef(`"S":{"Type":"Fail"}`), `{}`, nil, ""},
		// JSONPath: Parallel
		{"parallel inputpath", pathDef(`"S":{"Type":"Parallel","InputPath":"$.x","Branches":[{"StartAt":"A","States":{"A":{"Type":"Succeed"}}}],"End":true}`), `{}`, nil, "States.Runtime"},
		{"parallel parameters", pathDef(`"S":{"Type":"Parallel","Parameters":{"a.$":"$.x"},"Branches":[{"StartAt":"A","States":{"A":{"Type":"Succeed"}}}],"End":true}`), `{}`, nil, "States.ParameterPathFailure"},
		{"parallel resultselector", pathDef(`"S":{"Type":"Parallel","ResultSelector":{"a.$":"$.x"},"Branches":[{"StartAt":"A","States":{"A":{"Type":"Succeed"}}}],"End":true}`), `{}`, nil, "States.ParameterPathFailure"},
		// JSONPath: Map
		{"map inputpath", pathDef(`"S":{"Type":"Map","InputPath":"$.x","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`), `{}`, nil, "States.Runtime"},
		{"map itemspath missing", pathDef(`"S":{"Type":"Map","ItemsPath":"$.x","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`), `{}`, nil, "States.Runtime"},
		{"map itemspath not array", pathDef(`"S":{"Type":"Map","ItemsPath":"$.x","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`), `{"x":1}`, nil, "States.Runtime"},
		{"map itemselector", pathDef(`"S":{"Type":"Map","ItemSelector":{"a.$":"$.x"},"ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`), `[1]`, nil, "States.ParameterPathFailure"},
		{"map iteration", pathDef(`"S":{"Type":"Map","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Fail","Error":"Iter"}}},"End":true}`), `[1]`, nil, "Iter"},
		// JSONPath: Catch
		{"catch assign", pathDef(`"S":{"Type":"Task","Resource":"r","Catch":[{"ErrorEquals":["States.ALL"],"Assign":{"a.$":"$.x"},"Next":"E"}],"End":true},"E":{"Type":"Succeed"}`), `{}`, failAPI, "States.ParameterPathFailure"},
		{"catch resultpath", pathDef(`"S":{"Type":"Task","Resource":"r","Catch":[{"ErrorEquals":["States.ALL"],"ResultPath":"$.x","Next":"E"}],"End":true},"E":{"Type":"Succeed"}`), `"str"`, failAPI, "States.ResultPathMatchFailure"},
		// intrinsic functions
		{"intrinsic path arg", pathDef(`"S":{"Type":"Pass","Parameters":{"a.$":"States.Array($.x)"},"End":true}`), `{}`, nil, "States.IntrinsicFailure"},
		{"intrinsic nested", pathDef(`"S":{"Type":"Pass","Parameters":{"a.$":"States.Array(States.ArrayLength(1))"},"End":true}`), `{}`, nil, "States.IntrinsicFailure"},
		// JSONata: Pass / Succeed
		{"jsonata pass assign", jsonataDef(`"S":{"Type":"Pass","Assign":{"a":"{% $states.input.x %}"},"End":true}`), `{}`, nil, "States.QueryEvaluationError"},
		{"jsonata pass output", jsonataDef(`"S":{"Type":"Pass","Output":["{% 1 %}","{% $states.input.x %}"],"End":true}`), `{}`, nil, "States.QueryEvaluationError"},
		{"jsonata succeed output", jsonataDef(`"S":{"Type":"Succeed","Output":"{% $states.input.x %}"}`), `{}`, nil, "States.QueryEvaluationError"},
		// JSONata: Task
		{"jsonata task timeout eval", jsonataDef(`"S":{"Type":"Task","Resource":"r","TimeoutSeconds":"{% $states.input.x %}","End":true}`), `{}`, okAPI, "States.QueryEvaluationError"},
		{"jsonata task timeout zero", jsonataDef(`"S":{"Type":"Task","Resource":"r","TimeoutSeconds":"{% 0 %}","End":true}`), `{}`, okAPI, "States.QueryEvaluationError"},
		{"jsonata task heartbeat", jsonataDef(`"S":{"Type":"Task","Resource":"r","TimeoutSeconds":10,"HeartbeatSeconds":"{% 'x' %}","End":true}`), `{}`, okAPI, "States.QueryEvaluationError"},
		{"jsonata task arguments", jsonataDef(`"S":{"Type":"Task","Resource":"r","Arguments":{"a":"{% $states.input.x %}"},"End":true}`), `{}`, okAPI, "States.QueryEvaluationError"},
		{"jsonata task error", jsonataDef(`"S":{"Type":"Task","Resource":"r","End":true}`), `{}`, failAPI, "Task.Failed"},
		{"jsonata task output", jsonataDef(`"S":{"Type":"Task","Resource":"r","Output":"{% $states.result.x %}","End":true}`), `{}`, okAPI, "States.QueryEvaluationError"},
		// JSONata: Choice
		{"jsonata choice condition", jsonataDef(`"S":{"Type":"Choice","Choices":[{"Condition":"{% $states.input.x %}","Next":"E"}]},"E":{"Type":"Succeed"}`), `{}`, nil, "States.QueryEvaluationError"},
		{"jsonata choice no match", jsonataDef(`"S":{"Type":"Choice","Choices":[{"Condition":false,"Next":"E"}]},"E":{"Type":"Succeed"}`), `{}`, nil, "States.NoChoiceMatched"},
		// JSONata: Wait
		{"jsonata wait seconds eval", jsonataDef(`"S":{"Type":"Wait","Seconds":"{% $states.input.x %}","End":true}`), `{}`, nil, "States.QueryEvaluationError"},
		{"jsonata wait seconds negative", jsonataDef(`"S":{"Type":"Wait","Seconds":"{% -1 %}","End":true}`), `{}`, nil, "States.QueryEvaluationError"},
		{"jsonata wait timestamp eval", jsonataDef(`"S":{"Type":"Wait","Timestamp":"{% $states.input.x %}","End":true}`), `{}`, nil, "States.QueryEvaluationError"},
		{"jsonata wait timestamp invalid", jsonataDef(`"S":{"Type":"Wait","Timestamp":"{% 'now' %}","End":true}`), `{}`, nil, "States.QueryEvaluationError"},
		// JSONata: Fail
		{"jsonata fail error eval", jsonataDef(`"S":{"Type":"Fail","Error":"{% $states.input.x %}"}`), `{}`, nil, "States.QueryEvaluationError"},
		{"jsonata fail error not string", jsonataDef(`"S":{"Type":"Fail","Error":"{% 1 %}"}`), `{}`, nil, "States.QueryEvaluationError"},
		{"jsonata fail cause eval", jsonataDef(`"S":{"Type":"Fail","Error":"E","Cause":"{% $states.input.x %}"}`), `{}`, nil, "States.QueryEvaluationError"},
		{"jsonata fail empty", jsonataDef(`"S":{"Type":"Fail"}`), `{}`, nil, ""},
		// JSONata: Parallel / Map
		{"jsonata parallel arguments", jsonataDef(`"S":{"Type":"Parallel","Arguments":"{% $states.input.x %}","Branches":[{"StartAt":"A","States":{"A":{"Type":"Succeed"}}}],"End":true}`), `{}`, nil, "States.QueryEvaluationError"},
		{"jsonata parallel branch", jsonataDef(`"S":{"Type":"Parallel","Branches":[{"StartAt":"A","States":{"A":{"Type":"Fail","Error":"B"}}}],"End":true}`), `{}`, nil, "B"},
		{"jsonata parallel output", jsonataDef(`"S":{"Type":"Parallel","Output":"{% $states.result.x %}","Branches":[{"StartAt":"A","States":{"A":{"Type":"Succeed"}}}],"End":true}`), `{}`, nil, "States.QueryEvaluationError"},
		{"jsonata map items eval", jsonataDef(`"S":{"Type":"Map","Items":"{% $states.input.x %}","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`), `{}`, nil, "States.QueryEvaluationError"},
		{"jsonata map items not array", jsonataDef(`"S":{"Type":"Map","Items":"{% 1 %}","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`), `{}`, nil, "States.QueryEvaluationError"},
		{"jsonata map itemselector", jsonataDef(`"S":{"Type":"Map","ItemSelector":"{% $states.input.x %}","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`), `[1]`, nil, "States.QueryEvaluationError"},
		{"jsonata map iteration", jsonataDef(`"S":{"Type":"Map","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Fail","Error":"I"}}},"End":true}`), `[1]`, nil, "I"},
		{"jsonata map output", jsonataDef(`"S":{"Type":"Map","Output":"{% $states.result.x %}","ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`), `[1]`, nil, "States.QueryEvaluationError"},
		// JSONata: Catch
		{"jsonata catch assign", jsonataDef(`"S":{"Type":"Task","Resource":"r","Catch":[{"ErrorEquals":["States.ALL"],"Assign":{"a":"{% $states.input.x %}"},"Next":"E"}],"End":true},"E":{"Type":"Succeed"}`), `{}`, failAPI, "States.QueryEvaluationError"},
		{"jsonata catch output", jsonataDef(`"S":{"Type":"Task","Resource":"r","Catch":[{"ErrorEquals":["States.ALL"],"Output":"{% $states.input.x %}","Next":"E"}],"End":true},"E":{"Type":"Succeed"}`), `{}`, failAPI, "States.QueryEvaluationError"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := run(t, c.def, c.input, &sfntest.Env{API: c.api})
			assert.Equal(t, sfntest.Failed, res.Status)
			assert.Equal(t, c.want, res.Error)
		})
	}
}

func TestChoiceOperators(t *testing.T) {
	type rule struct {
		rule  string
		input string
		want  bool
	}
	cases := []rule{
		{`"Variable":"$.v","IsNumeric":true`, `{"v":1}`, true},
		{`"Variable":"$.v","IsNumeric":true`, `{"v":"1"}`, false},
		{`"Variable":"$.v","IsString":true`, `{"v":"1"}`, true},
		{`"Variable":"$.v","IsBoolean":false`, `{"v":"1"}`, true},
		{`"Variable":"$.v","IsTimestamp":true`, `{"v":"2026-01-01T00:00:00Z"}`, true},
		{`"Variable":"$.v","IsTimestamp":true`, `{"v":1}`, false},
		{`"Variable":"$.v","StringEquals":"a"`, `{"v":1}`, false},
		{`"Variable":"$.v","StringLessThan":"b"`, `{"v":"a"}`, true},
		{`"Variable":"$.v","StringGreaterThan":"b"`, `{"v":"c"}`, true},
		{`"Variable":"$.v","StringLessThanEquals":"a"`, `{"v":"a"}`, true},
		{`"Variable":"$.v","StringGreaterThanEquals":"b"`, `{"v":"a"}`, false},
		{`"Variable":"$.v","StringEqualsPath":"$.w"`, `{"v":"a","w":"a"}`, true},
		{`"Variable":"$.v","StringMatches":"a*"`, `{"v":1}`, false},
		{`"Variable":"$.v","NumericEquals":1`, `{"v":"1"}`, false},
		{`"Variable":"$.v","NumericLessThan":2`, `{"v":1}`, true},
		{`"Variable":"$.v","NumericGreaterThan":2`, `{"v":3}`, true},
		{`"Variable":"$.v","NumericLessThanEquals":1`, `{"v":1}`, true},
		{`"Variable":"$.v","NumericGreaterThanEqualsPath":"$.w"`, `{"v":1,"w":2}`, false},
		{`"Variable":"$.v","BooleanEquals":true`, `{"v":true}`, true},
		{`"Variable":"$.v","BooleanEqualsPath":"$.w"`, `{"v":true,"w":"true"}`, false},
		{`"Variable":"$.v","TimestampEquals":"2026-01-01T00:00:00Z"`, `{"v":"2026-01-01T09:00:00+09:00"}`, true},
		{`"Variable":"$.v","TimestampGreaterThan":"2026-01-01T00:00:00Z"`, `{"v":"2027-01-01T00:00:00Z"}`, true},
		{`"Variable":"$.v","TimestampGreaterThanEquals":"2026-01-01T00:00:00Z"`, `{"v":1}`, false},
		{`"Variable":"$$.State.Name","StringEquals":"S"`, `{}`, true},
	}
	for _, c := range cases {
		def := pathDef(`"S":{"Type":"Choice","Choices":[{` + c.rule + `,"Next":"Y"}],"Default":"N"},"Y":{"Type":"Succeed"},"N":{"Type":"Succeed"}`)
		res := run(t, def, c.input, nil)
		require.Equal(t, sfntest.Succeeded, res.Status, "%s %s: %s", c.rule, c.input, res.Cause)
		want := "N"
		if c.want {
			want = "Y"
		}
		assert.Equal(t, want, res.History[1].State, "%s %s", c.rule, c.input)
	}
}

func TestMiscBehaviors(t *testing.T) {
	t.Run("null paths", func(t *testing.T) {
		res := run(t, pathDef(`"S":{"Type":"Pass","InputPath":null,"Result":1,"ResultPath":"$.r","OutputPath":"$","End":true}`), `{"a":1}`, nil)
		assert.JSONEq(t, `{"a":1,"r":1}`, string(res.Output))
		res = run(t, pathDef(`"S":{"Type":"Pass","InputPath":null,"End":true}`), `{"a":1}`, nil)
		assert.JSONEq(t, `{}`, string(res.Output))
		res = run(t, pathDef(`"S":{"Type":"Pass","OutputPath":null,"End":true}`), `{"a":1}`, nil)
		assert.JSONEq(t, `{}`, string(res.Output))
	})

	t.Run("parameters arrays and parallel parameters", func(t *testing.T) {
		res := run(t, pathDef(`"S":{"Type":"Parallel","Parameters":{"a":[{"b.$":"$.x"},2]},
		  "Branches":[{"StartAt":"A","States":{"A":{"Type":"Pass","End":true}}}],"End":true}`), `{"x":1}`, nil)
		assert.JSONEq(t, `[{"a":[{"b":1},2]}]`, string(res.Output))
	})

	t.Run("wait timestamp and iterator", func(t *testing.T) {
		start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		clock := &sfntest.FakeClock{T: start}
		res := run(t, pathDef(`"S":{"Type":"Wait","Timestamp":"2026-01-01T00:00:30Z","Next":"M"},
		  "M":{"Type":"Map","Iterator":{"StartAt":"A","States":{"A":{"Type":"Pass","End":true}}},"End":true}`), `[1]`, &sfntest.Env{Clock: clock})
		assert.Equal(t, start.Add(30*time.Second), clock.T)
		assert.JSONEq(t, `[1]`, string(res.Output))
	})

	t.Run("jitter", func(t *testing.T) {
		clock := &sfntest.FakeClock{T: time.Unix(0, 0)}
		api := &mockAPI{handler: func(n int, _ string, _ map[string]any) (any, error) {
			if n == 1 {
				return nil, sfntest.Error("E", "")
			}
			return 1, nil
		}}
		res := run(t, pathDef(`"S":{"Type":"Task","Resource":"r","Retry":[{"ErrorEquals":["E"],"IntervalSeconds":10,"JitterStrategy":"FULL"}],"End":true}`), `{}`, &sfntest.Env{API: api, Clock: clock})
		assert.Equal(t, sfntest.Succeeded, res.Status)
		assert.LessOrEqual(t, clock.T.Unix(), int64(10))
	})

	t.Run("redrive time and nanoseconds", func(t *testing.T) {
		env := &sfntest.Env{Execution: &sfntest.Execution{
			StartTime:    time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC),
			RedriveCount: 2,
			RedriveTime:  time.Date(2026, 1, 1, 0, 0, 0, 123456000, time.UTC),
		}}
		res := run(t, pathDef(`"S":{"Type":"Pass","Parameters":{"s.$":"$$.Execution.StartTime","r.$":"$$.Execution.RedriveTime","c.$":"$$.Execution.RedriveCount"},"End":true}`), `{}`, env)
		assert.JSONEq(t, `{"s":"2026-01-01T00:00:00.123456789Z","r":"2026-01-01T00:00:00.123456Z","c":2}`, string(res.Output))
	})

	t.Run("intrinsic defaults", func(t *testing.T) {
		res := run(t, pathDef(`"S":{"Type":"Pass","Parameters":{"u.$":"States.UUID()","r.$":"States.MathRandom(1, 2)","c.$":"States.ArrayContains($.a, 9)","n.$":"States.Array(null)"},"End":true}`), `{"a":[1]}`, nil)
		assert.Regexp(t, `"u":"[0-9a-f-]{36}"`, string(res.Output))
		assert.Contains(t, string(res.Output), `"r":1`)
		assert.Contains(t, string(res.Output), `"c":false`)
		assert.Contains(t, string(res.Output), `"n":[null]`)
	})

	t.Run("jsonata task token and timeouts", func(t *testing.T) {
		var got string
		api := sfntest.APIFunc(func(_ context.Context, _ string, params []byte) ([]byte, error) {
			got = string(params)
			return []byte(`1`), nil
		})
		res := run(t, jsonataDef(`"S":{"Type":"Task","Resource":"arn:aws:states:::sqs:sendMessage.waitForTaskToken",
		  "TimeoutSeconds":"{% 10 %}","HeartbeatSeconds":5,"Arguments":{"t":"{% $states.context.Task.Token %}"},"End":true}`), `{}`,
			&sfntest.Env{API: api, TaskToken: tokenFunc(func(string) string { return "tok" })})
		assert.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
		assert.JSONEq(t, `{"t":"tok"}`, got)
	})

	t.Run("jsonata succeed output", func(t *testing.T) {
		res := run(t, jsonataDef(`"S":{"Type":"Succeed","Output":{"a":["{% 1 %}",2]}}`), `{}`, nil)
		assert.JSONEq(t, `{"a":[1,2]}`, string(res.Output))
	})
}

func TestVariableLimits(t *testing.T) {
	def := pathDef(`"S":{"Type":"Pass","Assign":{"a":"0123456789"},"End":true}`)
	res := run(t, def, `{}`, nil, sfntest.WithVariableLimit(5, 100))
	assert.Equal(t, "States.DataLimitExceeded", res.Error)

	def = pathDef(`"S":{"Type":"Pass","Assign":{"a":"012","b":"345"},"End":true}`)
	res = run(t, def, `{}`, nil, sfntest.WithVariableLimit(8, 100))
	assert.Equal(t, "States.DataLimitExceeded", res.Error)

	def = pathDef(`"S":{"Type":"Pass","Assign":{"a":"012"},"Next":"T"},"T":{"Type":"Pass","Assign":{"b":"345"},"End":true}`)
	res = run(t, def, `{}`, nil, sfntest.WithVariableLimit(8, 8))
	assert.Equal(t, "States.DataLimitExceeded", res.Error)

	// commitAssign in a Catch
	def = pathDef(`"S":{"Type":"Pass","Assign":{"a":"012"},"Next":"T"},
	  "T":{"Type":"Task","Resource":"r","Catch":[{"ErrorEquals":["States.ALL"],"Assign":{"b":"345"},"Next":"E"}],"End":true},"E":{"Type":"Succeed"}`)
	res = run(t, def, `{}`, &sfntest.Env{API: failAPI}, sfntest.WithVariableLimit(8, 8))
	assert.Equal(t, "States.DataLimitExceeded", res.Error)
}

func TestPayloadLimits(t *testing.T) {
	def := pathDef(`"S":{"Type":"Task","Resource":"r","Parameters":{"a":"0123456789012345678901234567890123456789"},"End":true}`)
	res := run(t, def, `{}`, &sfntest.Env{API: okAPI}, sfntest.WithPayloadLimit(30))
	assert.Equal(t, "States.DataLimitExceeded", res.Error)

	def = pathDef(`"S":{"Type":"Task","Resource":"r","Catch":[{"ErrorEquals":["Task.Failed"],"ResultPath":"$.e","Next":"E"}],"End":true},"E":{"Type":"Succeed"}`)
	res = run(t, def, `{}`, &sfntest.Env{API: failAPI}, sfntest.WithPayloadLimit(30))
	assert.Equal(t, "States.DataLimitExceeded", res.Error)

	sm, err := sfntest.New([]byte(pathDef(`"S":{"Type":"Succeed"}`)), sfntest.WithPayloadLimit(5))
	require.NoError(t, err)
	_, err = sm.Run(context.Background(), []byte(`{"a":"long"}`), nil)
	assert.ErrorContains(t, err, "execution input exceeds")
	_, err = sm.Run(context.Background(), []byte(`{`), nil)
	assert.ErrorContains(t, err, "invalid execution input")
}

func TestRunAborts(t *testing.T) {
	sm, err := sfntest.New([]byte(pathDef(`"S":{"Type":"Task","Resource":"r","End":true}`)))
	require.NoError(t, err)

	res, err := sm.Run(context.Background(), []byte(`{}`), &sfntest.Env{API: sfntest.APIFunc(func(context.Context, string, []byte) ([]byte, error) {
		return []byte(`{`), nil
	})})
	require.ErrorContains(t, err, "returned invalid JSON")
	assert.Equal(t, sfntest.Incomplete, res.Status)

	// ctx canceled while waiting for a retry
	ctx, cancel := context.WithCancel(context.Background())
	sm, err = sfntest.New([]byte(pathDef(`"S":{"Type":"Task","Resource":"r","Retry":[{"ErrorEquals":["States.ALL"]}],"End":true}`)))
	require.NoError(t, err)
	_, err = sm.Run(ctx, []byte(`{}`), &sfntest.Env{API: failAPI, Clock: cancelClock{cancel}})
	assert.ErrorIs(t, err, context.Canceled)

	// a plain error from an intrinsic function aborts Run
	sm, err = sfntest.New([]byte(pathDef(`"S":{"Type":"Pass","Parameters":{"a.$":"States.UUID()"},"End":true}`)))
	require.NoError(t, err)
	_, err = sm.Run(context.Background(), []byte(`{}`), &sfntest.Env{Intrinsic: sfntest.IntrinsicFuncs{"States.UUID": func(...any) (any, error) {
		return nil, errors.New("bug")
	}}})
	assert.ErrorContains(t, err, "bug")

	// an intrinsic function returning a non-JSON value
	res, err = sm.Run(context.Background(), []byte(`{}`), &sfntest.Env{Intrinsic: sfntest.IntrinsicFuncs{"States.UUID": func(...any) (any, error) {
		return math.Inf(1), nil
	}}})
	require.NoError(t, err)
	assert.Equal(t, "States.IntrinsicFailure", res.Error)
}

type cancelClock struct{ cancel context.CancelFunc }

func (cancelClock) Now() time.Time        { return time.Unix(0, 0) }
func (c cancelClock) Sleep(time.Duration) { c.cancel() }

func TestJSONataFunctionErrors(t *testing.T) {
	cases := map[string]string{
		`$partition([1])`:          "expected 2 arguments",
		`$partition(1, 1)`:         "expected an array",
		`$partition([1], 'x')`:     "expected a number",
		`$partition([1], 0)`:       "positive integer",
		`$range(1, 2)`:             "expected 3 arguments",
		`$range(1, 'x', 1)`:        "expected a number",
		`$range(1, 2, 0)`:          "delta must not be zero",
		`$range(0, 100000000, 1)`:  "too large",
		`$hash('a')`:               "expected 2 arguments",
		`$hash('a', 1)`:            "expected a string algorithm",
		`$hash('a', 'SHA-3')`:      "unsupported algorithm",
		`$random('x')`:             "expected a number",
		`$random(1, 2)`:            "at most 1 argument",
		`$uuid(1)`:                 "expected no arguments",
		`$parse()`:                 "expected 1 argument",
		`$parse(1)`:                "expected a string",
		`$parse('{')`:              "$parse",
		`$millis(1)`:               "expected no arguments",
		`$now('[Y0001]', 'bogus')`: "$now",
	}
	for expr, want := range cases {
		def := jsonataDef(`"S":{"Type":"Pass","Output":"{% ` + strings.ReplaceAll(expr, `"`, `\"`) + ` %}","End":true}`)
		res := run(t, def, `{}`, nil)
		assert.Equal(t, "States.QueryEvaluationError", res.Error, expr)
		assert.Contains(t, res.Cause, want, expr)
	}

	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	def := jsonataDef(`"S":{"Type":"Pass","Output":{
	  "now":"{% $now('[Y0001]-[M01]-[D01]') %}",
	  "tz":"{% $now('[H01]', '+0900') %}",
	  "r1":"{% $random(7) = $random(7) %}",
	  "range":"{% $range(5, 1, -2) %}",
	  "hash":"{% $length($hash({'a': 1}, 'MD5')) %}",
	  "uuid":"{% $length($uuid()) %}"
	},"End":true}`)
	res := run(t, def, `{}`, &sfntest.Env{Clock: &sfntest.FakeClock{T: start}})
	require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
	assert.JSONEq(t, `{"now":"2026-01-02","tz":"12","r1":true,"range":[5,3,1],"hash":32,"uuid":36}`, string(res.Output))
}

func TestJSONataCustomFuncs(t *testing.T) {
	env := &sfntest.Env{JSONata: sfntest.JSONataFuncs{
		"$null": func(...any) (any, error) { return nil, nil },
		"$inf":  func(...any) (any, error) { return math.Inf(1), nil },
		"$apply": func(args ...any) (any, error) {
			return len(args), nil
		},
	}}

	res := run(t, jsonataDef(`"S":{"Type":"Pass","Output":{"n":"{% $null() %}","a":"{% $apply(function($x){$x}, 1) %}"},"End":true}`), `{}`, env)
	require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
	assert.JSONEq(t, `{"n":null,"a":2}`, string(res.Output))

	res = run(t, jsonataDef(`"S":{"Type":"Pass","Output":"{% $inf() %}","End":true}`), `{}`, env)
	assert.Equal(t, "States.QueryEvaluationError", res.Error)

	res = run(t, jsonataDef(`"S":{"Type":"Pass","Output":"{% 1/0 %}","End":true}`), `{}`, nil)
	assert.Equal(t, "States.QueryEvaluationError", res.Error)
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		`{`: "invalid state machine definition",
		`{"StartAt":"S","States":{"S":{"Type":"Succeed"}},"QueryLanguage":"XPath"}`: "unknown query language",
		`{"States":{"S":{"Type":"Succeed"}}}`:                                       "StartAt: required",
		`{"StartAt":"S"}`:                                                           "States: required",
		`{"StartAt":"S","States":{"S":1}}`:                                          "States.S:",
		`{"StartAt":"S","States":{"S":{"Type":1}}}`:                                 "States.S:",
		`{"StartAt":"S","States":{"S":{"Type":"Bogus"}}}`:                           "unknown state type",
		pathDef(`"S":{"Type":"Pass","Next":"S","End":true}`):                        "Next and End cannot be used together",
		pathDef(`"S":{"Type":"Task","End":true}`):                                   "Resource: required",
		pathDef(`"S":{"Type":"Choice"}`):                                            "Choices: required",
		pathDef(`"S":{"Type":"Parallel","End":true}`):                               "Branches: required",
		pathDef(`"S":{"Type":"Map","End":true}`):                                    "ItemProcessor: required",
		pathDef(`"S":{"Type":"Map","Iterator":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`): "cannot be used together",
		pathDef(`"S":{"Type":"Map","ItemSelector":{},"Parameters":{},"ItemProcessor":{"StartAt":"A","States":{"A":{"Type":"Succeed"}}},"End":true}`):                            "ItemSelector and Parameters",
		pathDef(`"S":{"Type":"Wait","Seconds":-1,"End":true}`):                                                                   "Seconds: must be a non-negative integer",
		pathDef(`"S":{"Type":"Wait","Timestamp":"x","End":true}`):                                                                "Timestamp: must be a timestamp",
		pathDef(`"S":{"Type":"Wait","Seconds":1,"SecondsPath":"$.x","End":true}`):                                                "exactly one of",
		pathDef(`"S":{"Type":"Wait","SecondsPath":null,"End":true}`):                                                             "must not be null",
		pathDef(`"S":{"Type":"Wait","TimestampPath":"x","End":true}`):                                                            "TimestampPath",
		pathDef(`"S":{"Type":"Fail","Error":"E","ErrorPath":"$.e"}`):                                                             "Error and ErrorPath",
		pathDef(`"S":{"Type":"Fail","Cause":"C","CausePath":"$.c"}`):                                                             "Cause and CausePath",
		pathDef(`"S":{"Type":"Fail","ErrorPath":null}`):                                                                          "must not be null",
		pathDef(`"S":{"Type":"Fail","ErrorPath":"$["}`):                                                                          "ErrorPath",
		pathDef(`"S":{"Type":"Pass","InputPath":"x","End":true}`):                                                                "InputPath",
		pathDef(`"S":{"Type":"Pass","ResultPath":"x","End":true}`):                                                               "ResultPath",
		pathDef(`"S":{"Type":"Pass","Parameters":{"a.$":"$.a","a":1},"End":true}`):                                               "duplicate field",
		pathDef(`"S":{"Type":"Pass","Parameters":{"a.$":1},"End":true}`):                                                         "must be a string",
		pathDef(`"S":{"Type":"Pass","Parameters":{"a":[{"b.$":"$["}]},"End":true}`):                                              "Parameters.a[0]",
		pathDef(`"S":{"Type":"Pass","Assign":{"1x":1},"End":true}`):                                                              "invalid variable name",
		pathDef(`"S":{"Type":"Task","Resource":"r","Retry":[{}],"End":true}`):                                                    "ErrorEquals: required",
		pathDef(`"S":{"Type":"Task","Resource":"r","Retry":[{"ErrorEquals":["States.ALL","E"]}],"End":true}`):                    "must appear alone",
		pathDef(`"S":{"Type":"Task","Resource":"r","Retry":[{"ErrorEquals":["E"],"IntervalSeconds":0}],"End":true}`):             "IntervalSeconds",
		pathDef(`"S":{"Type":"Task","Resource":"r","Retry":[{"ErrorEquals":["E"],"MaxAttempts":-1}],"End":true}`):                "MaxAttempts",
		pathDef(`"S":{"Type":"Task","Resource":"r","Retry":[{"ErrorEquals":["E"],"BackoffRate":0.5}],"End":true}`):               "BackoffRate",
		pathDef(`"S":{"Type":"Task","Resource":"r","Retry":[{"ErrorEquals":["E"],"MaxDelaySeconds":0}],"End":true}`):             "MaxDelaySeconds",
		pathDef(`"S":{"Type":"Task","Resource":"r","Retry":[{"ErrorEquals":["E"],"JitterStrategy":"X"}],"End":true}`):            "JitterStrategy",
		pathDef(`"S":{"Type":"Task","Resource":"r","Catch":[{"ErrorEquals":["E"]}],"End":true}`):                                 "Next: required",
		pathDef(`"S":{"Type":"Task","Resource":"r","Catch":[{"ErrorEquals":["E"],"Next":"S","Output":1}],"End":true}`):           `"Output" is not allowed in a JSONPath state`,
		jsonataDef(`"S":{"Type":"Task","Resource":"r","Catch":[{"ErrorEquals":["E"],"Next":"S","ResultPath":"$"}],"End":true}`):  `"ResultPath" is not allowed in a JSONata state`,
		jsonataDef(`"S":{"Type":"Task","Resource":"r","Catch":[{"ErrorEquals":["E"],"Next":"S","Assign":{"1x":1}}],"End":true}`): "invalid variable name",
		pathDef(`"S":{"Type":"Choice","Choices":[{"Variable":"$.a","IsNull":true}]}`):                                            "Next: required",
		pathDef(`"S":{"Type":"Choice","Choices":[{"Not":{"Variable":"$.a","IsNull":true,"Next":"S"},"Next":"S"}]}`):              "not allowed in a nested rule",
		pathDef(`"S":{"Type":"Choice","Choices":[{"Not":{"Variable":"$.a","IsNull":true,"Assign":{}},"Next":"S"}]}`):             "Assign: not allowed in a nested rule",
		pathDef(`"S":{"Type":"Choice","Choices":[{"Variable":"$.a","IsNull":true,"Assign":{"1x":1},"Next":"S"}]}`):               "invalid variable name",
		pathDef(`"S":{"Type":"Choice","Choices":[{"Variable":"$.a","Bogus":true,"Next":"S"}]}`):                                  "unknown field",
		pathDef(`"S":{"Type":"Choice","Choices":[{"Variable":"$.a","Next":"S"}]}`):                                               "exactly one comparison operator",
		pathDef(`"S":{"Type":"Choice","Choices":[{"And":[],"Next":"S"}]}`):                                                       "must be a non-empty array",
		pathDef(`"S":{"Type":"Choice","Choices":[{"Or":[1],"Next":"S"}]}`):                                                       "must be an object",
		pathDef(`"S":{"Type":"Choice","Choices":[{"Not":1,"Next":"S"}]}`):                                                        "Not: must be an object",
		pathDef(`"S":{"Type":"Choice","Choices":[{"IsNull":true,"Next":"S"}]}`):                                                  "Variable: required",
		pathDef(`"S":{"Type":"Choice","Choices":[{"Variable":"x","IsNull":true,"Next":"S"}]}`):                                   "Variable:",
		pathDef(`"S":{"Type":"Choice","Choices":[{"Variable":"$.a","StringEqualsPath":1,"Next":"S"}]}`):                          "must be a path",
		pathDef(`"S":{"Type":"Choice","Choices":[{"Variable":"$.a","StringEqualsPath":"x","Next":"S"}]}`):                        "StringEqualsPath:",
		pathDef(`"S":{"Type":"Choice","Choices":[{"Variable":"$.a","NumericEquals":"1","Next":"S"}]}`):                           "must be a number",
		pathDef(`"S":{"Type":"Choice","Choices":[{"Variable":"$.a","TimestampEquals":"x","Next":"S"}]}`):                         "must be a timestamp",
		pathDef(`"S":{"Type":"Choice","Choices":[{"Variable":"$.a","IsNull":"yes","Next":"S"}]}`):                                "must be a boolean",
		pathDef(`"S":{"Type":"Task","Resource":"arn:aws:states:us-east-1:123456789012:activity:a","End":true}`):                  "activities are not supported in Express",
		jsonataDef(`"S":{"Type":"Pass","Assign":{"1x":1},"End":true}`):                                                           "invalid variable name",
		jsonataDef(`"S":{"Type":"Pass","Output":["{% 1 +"],"End":true}`):                                                         "Output[0]",
		jsonataDef(`"S":{"Type":"Wait","End":true}`):                                                                             "exactly one of Seconds and Timestamp",
		jsonataDef(`"S":{"Type":"Wait","Seconds":-1,"End":true}`):                                                                "Seconds: must be a non-negative integer",
		jsonataDef(`"S":{"Type":"Wait","Timestamp":"x","End":true}`):                                                             "Timestamp: must be a timestamp",
		jsonataDef(`"S":{"Type":"Wait","Timestamp":1,"End":true}`):                                                               "Timestamp: must be a timestamp",
		jsonataDef(`"S":{"Type":"Choice","Choices":[{"Condition":true,"Next":"S","Variable":"$"}]}`):                             "is not allowed in a JSONata Choice rule",
		jsonataDef(`"S":{"Type":"Choice","Choices":[{"Condition":true}]}`):                                                       "Next: required",
		jsonataDef(`"S":{"Type":"Choice","Choices":[{"Condition":"true","Next":"S"}]}`):                                          "Condition: must be",
		jsonataDef(`"S":{"Type":"Choice","Choices":[{"Condition":1,"Next":"S"}]}`):                                               "Condition: must be",
		jsonataDef(`"S":{"Type":"Choice","Choices":[{"Condition":true,"Next":"S","Assign":{"1x":1}}]}`):                          "invalid variable name",
		`{"StartAt":"S","States":{"` + strings.Repeat("a", 81) + `":{"Type":"Succeed"},"S":{"Type":"Succeed"}}}`:                 "exceeds 80 characters",
	}
	for def, want := range cases {
		opts := []sfntest.Option{}
		if strings.Contains(want, "Express") {
			opts = append(opts, sfntest.WithType(sfntest.Express))
		}
		_, err := sfntest.New([]byte(def), opts...)
		assert.ErrorContains(t, err, want, def)
	}
}

const slowJSONata = `$count($filter([1..10000000], function($v){$v % 7 = 0}))`

func TestJSONataTimeout(t *testing.T) {
	def := jsonataDef(`"S":{"Type":"Pass","Output":"{% ` + slowJSONata + ` %}","End":true}`)

	// an expression running longer than 1 second fails the state
	res := run(t, def, `{}`, nil)
	assert.Equal(t, "States.QueryEvaluationError", res.Error)
	assert.Contains(t, res.Cause, "Expression evaluation timeout")

	// the timeout of Run stops a running expression
	sm, err := sfntest.New([]byte(def), sfntest.WithTimeout(50*time.Millisecond))
	require.NoError(t, err)
	res, err = sm.Run(context.Background(), []byte(`{}`), nil)
	assert.ErrorContains(t, err, "timeout")
	assert.Equal(t, sfntest.Incomplete, res.Status)
}
