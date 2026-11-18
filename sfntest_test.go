package sfntest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/sfntest"
)

func run(t *testing.T, def, input string, env *sfntest.Env, opts ...sfntest.Option) *sfntest.Result {
	t.Helper()
	sm, err := sfntest.New([]byte(def), opts...)
	require.NoError(t, err)
	res, err := sm.Run(context.Background(), []byte(input), env)
	require.NoError(t, err)
	return res
}

func path(res *sfntest.Result) []string {
	var p []string
	for _, e := range res.History {
		p = append(p, e.State)
	}
	return p
}

// mockAPI answers calls by resource and records them.
type mockAPI struct {
	calls   []string
	handler func(n int, resource string, params map[string]any) (any, error)
}

func (m *mockAPI) Call(_ context.Context, resource string, params []byte) ([]byte, error) {
	var p map[string]any
	_ = json.Unmarshal(params, &p)
	m.calls = append(m.calls, resource)
	v, err := m.handler(len(m.calls), resource, p)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

const orderDef = `{
  "StartAt": "ProcessOrder",
  "States": {
    "ProcessOrder": {
      "Type": "Task",
      "Resource": "arn:aws:states:::lambda:invoke",
      "Parameters": {
        "FunctionName": "process-order",
        "Payload": { "orderId.$": "$.orderId" }
      },
      "ResultSelector": { "status.$": "$.Payload.status" },
      "Retry": [{ "ErrorEquals": ["Lambda.ServiceException"], "MaxAttempts": 2 }],
      "Catch": [{ "ErrorEquals": ["States.ALL"], "ResultPath": "$.error", "Next": "HandleFailure" }],
      "Next": "CheckStatus"
    },
    "CheckStatus": {
      "Type": "Choice",
      "Choices": [{ "Variable": "$.status", "StringEquals": "OK", "Next": "Done" }],
      "Default": "HandleFailure"
    },
    "Done": {
      "Type": "Pass",
      "Parameters": { "receiptId.$": "States.UUID()" },
      "End": true
    },
    "HandleFailure": { "Type": "Fail", "Error": "OrderFailed", "CausePath": "$.error.Cause" }
  }
}`

func TestOrder_SucceedsAfterRetry(t *testing.T) {
	api := &mockAPI{handler: func(n int, resource string, p map[string]any) (any, error) {
		if n == 1 {
			return nil, sfntest.Error("Lambda.ServiceException", "temporary")
		}
		assert.Equal(t, "process-order", p["FunctionName"])
		assert.Equal(t, map[string]any{"orderId": "123"}, p["Payload"])
		return map[string]any{"Payload": map[string]any{"status": "OK"}}, nil
	}}

	res := run(t, orderDef, `{"orderId":"123"}`, &sfntest.Env{API: api, Intrinsic: sfntest.IntrinsicFuncs{
		"States.UUID": func(...any) (any, error) { return "uuid-1", nil },
	}})

	assert.Equal(t, sfntest.Succeeded, res.Status)
	assert.JSONEq(t, `{"receiptId":"uuid-1"}`, string(res.Output))
	assert.Equal(t, []string{"ProcessOrder", "CheckStatus", "Done"}, path(res))
	require.Len(t, res.History[0].Attempts, 2)
	assert.Equal(t, "Lambda.ServiceException", res.History[0].Attempts[0].Error)
	assert.JSONEq(t, `{"status":"OK"}`, string(res.History[0].Output))
}

func TestOrder_CatchAfterRetriesExhausted(t *testing.T) {
	api := &mockAPI{handler: func(int, string, map[string]any) (any, error) {
		return nil, sfntest.Error("Lambda.ServiceException", "down")
	}}

	res := run(t, orderDef, `{"orderId":"123"}`, &sfntest.Env{API: api})

	assert.Equal(t, sfntest.Failed, res.Status)
	assert.Equal(t, "OrderFailed", res.Error)
	assert.Equal(t, "down", res.Cause)
	assert.Len(t, api.calls, 3)
	assert.Equal(t, []string{"ProcessOrder", "HandleFailure"}, path(res))
	assert.Equal(t, "Lambda.ServiceException", res.History[0].Error)
	assert.Equal(t, "HandleFailure", res.History[0].Next)
}

func TestRetryDelayUsesClock(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &sfntest.FakeClock{T: start}
	api := &mockAPI{handler: func(n int, _ string, _ map[string]any) (any, error) {
		if n < 4 {
			return nil, sfntest.Error("E", "")
		}
		return map[string]any{}, nil
	}}
	def := `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"r","End":true,
	  "Retry":[{"ErrorEquals":["E"],"IntervalSeconds":2,"BackoffRate":3,"MaxDelaySeconds":10}]}}}`

	res := run(t, def, `{}`, &sfntest.Env{API: api, Clock: clock})
	assert.Equal(t, sfntest.Succeeded, res.Status)
	// 2 + 6 + min(18, 10)
	assert.Equal(t, start.Add(18*time.Second), clock.T)
}

func TestFatalErrorFromMock(t *testing.T) {
	api := sfntest.APIFunc(func(context.Context, string, []byte) ([]byte, error) {
		return nil, errors.New("unexpected call")
	})
	sm, err := sfntest.New([]byte(orderDef))
	require.NoError(t, err)

	res, err := sm.Run(context.Background(), []byte(`{"orderId":"1"}`), &sfntest.Env{API: api})
	require.ErrorContains(t, err, "unexpected call")
	assert.Equal(t, sfntest.Incomplete, res.Status)
	assert.Equal(t, []string{"ProcessOrder"}, path(res))
}

func TestNilAPI(t *testing.T) {
	sm, err := sfntest.New([]byte(orderDef))
	require.NoError(t, err)
	_, err = sm.Run(context.Background(), []byte(`{"orderId":"1"}`), nil)
	require.ErrorContains(t, err, "Env.API is not set")
}

func TestTimeout(t *testing.T) {
	def := `{"StartAt":"A","States":{"A":{"Type":"Pass","Next":"B"},"B":{"Type":"Pass","Next":"A"}}}`
	sm, err := sfntest.New([]byte(def), sfntest.WithTimeout(50*time.Millisecond))
	require.NoError(t, err)

	res, err := sm.Run(context.Background(), []byte(`{}`), nil)
	require.ErrorContains(t, err, "timeout")
	assert.Equal(t, sfntest.Incomplete, res.Status)
	assert.NotEmpty(t, res.History)
}

func TestPassProcessing(t *testing.T) {
	def := `{"StartAt":"P","States":{"P":{"Type":"Pass",
	  "InputPath":"$.in",
	  "Result":{"x":1},
	  "ResultPath":"$.result.inner",
	  "OutputPath":"$.result",
	  "End":true}}}`
	res := run(t, def, `{"in":{"a":"b"}}`, nil)
	assert.JSONEq(t, `{"inner":{"x":1}}`, string(res.Output))

	def = `{"StartAt":"P","States":{"P":{"Type":"Pass","ResultPath":null,"Result":1,"End":true}}}`
	res = run(t, def, `{"a":1}`, nil)
	assert.JSONEq(t, `{"a":1}`, string(res.Output))
}

func TestResultPathMatchFailure(t *testing.T) {
	def := `{"StartAt":"P","States":{"P":{"Type":"Pass","Result":1,"ResultPath":"$.x","End":true}}}`
	res := run(t, def, `"foo"`, nil)
	assert.Equal(t, sfntest.Failed, res.Status)
	assert.Equal(t, "States.ResultPathMatchFailure", res.Error)
}

func TestParameterPathFailure(t *testing.T) {
	def := `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{"a.$":"$.missing"},"End":true}}}`
	res := run(t, def, `{}`, nil)
	assert.Equal(t, "States.ParameterPathFailure", res.Error)
}

func TestContextObject(t *testing.T) {
	def := `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{
	  "id.$":"$$.Execution.Id","name.$":"$$.Execution.Name","start.$":"$$.Execution.StartTime",
	  "smId.$":"$$.StateMachine.Id","smName.$":"$$.StateMachine.Name",
	  "state.$":"$$.State.Name","entered.$":"$$.State.EnteredTime","input.$":"$$.Execution.Input"},"End":true}}}`
	env := &sfntest.Env{Execution: &sfntest.Execution{
		StateMachineArn: "arn:aws:states:ap-northeast-1:111122223333:stateMachine:orders",
		Name:            "exec-1",
		StartTime:       time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.UTC),
	}}

	res := run(t, def, `{"k":"v"}`, env)
	assert.JSONEq(t, `{
	  "id":"arn:aws:states:ap-northeast-1:111122223333:execution:orders:exec-1",
	  "name":"exec-1","start":"2026-01-02T03:04:05.006Z",
	  "smId":"arn:aws:states:ap-northeast-1:111122223333:stateMachine:orders","smName":"orders",
	  "state":"P","entered":"2026-01-02T03:04:05.006Z","input":{"k":"v"}}`, string(res.Output))
}

func TestExpressExecutionID(t *testing.T) {
	def := `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{"id.$":"$$.Execution.Id"},"End":true}}}`
	env := &sfntest.Env{Execution: &sfntest.Execution{Name: "e"}}
	res := run(t, def, `{}`, env, sfntest.WithType(sfntest.Express))

	var out map[string]string
	require.NoError(t, json.Unmarshal(res.Output, &out))
	assert.Regexp(t, `^arn:aws:states:us-east-1:123456789012:express:test:e:[0-9a-f-]{36}$`, out["id"])
}

func TestWaitTaskToken(t *testing.T) {
	def := `{"StartAt":"T","States":{"T":{"Type":"Task",
	  "Resource":"arn:aws:states:::sqs:sendMessage.waitForTaskToken",
	  "Parameters":{"MessageBody":{"token.$":"$$.Task.Token"}},"End":true}}}`
	var got string
	api := sfntest.APIFunc(func(_ context.Context, resource string, params []byte) ([]byte, error) {
		assert.Equal(t, "arn:aws:states:::sqs:sendMessage.waitForTaskToken", resource)
		var p struct{ MessageBody struct{ Token string } }
		_ = json.Unmarshal(params, &p)
		got = p.MessageBody.Token
		return []byte(`"done"`), nil
	})
	token := tokenFunc(func(state string) string { return "tok-" + state })

	res := run(t, def, `{}`, &sfntest.Env{API: api, TaskToken: token})
	assert.Equal(t, "tok-T", got)
	assert.JSONEq(t, `"done"`, string(res.Output))
}

type tokenFunc func(string) string

func (f tokenFunc) New(s string) string { return f(s) }

func TestWait(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &sfntest.FakeClock{T: start}
	def := `{"StartAt":"W1","States":{
	  "W1":{"Type":"Wait","Seconds":10,"Next":"W2"},
	  "W2":{"Type":"Wait","TimestampPath":"$.until","Next":"W3"},
	  "W3":{"Type":"Wait","SecondsPath":"$.sec","End":true}}}`

	res := run(t, def, `{"until":"2026-01-01T00:01:00Z","sec":5}`, &sfntest.Env{Clock: clock})
	assert.Equal(t, sfntest.Succeeded, res.Status)
	assert.Equal(t, start.Add(65*time.Second), clock.T)
	assert.Equal(t, start.Add(10*time.Second), res.History[1].EnteredTime)
}

func TestChoice(t *testing.T) {
	def := `{"StartAt":"C","States":{
	  "C":{"Type":"Choice","Choices":[
	    {"And":[{"Variable":"$.n","NumericGreaterThanEquals":10},{"Not":{"Variable":"$.s","StringMatches":"skip*"}}],"Next":"Big"},
	    {"Variable":"$.t","TimestampLessThanPath":"$.limit","Next":"Early"},
	    {"Or":[{"Variable":"$.x","IsPresent":false},{"Variable":"$.x","IsNull":true}],"Next":"NoX"}
	  ],"Default":"Other"},
	  "Big":{"Type":"Succeed"},"Early":{"Type":"Succeed"},"NoX":{"Type":"Succeed"},"Other":{"Type":"Succeed"}}}`

	cases := map[string]string{
		`{"n":10,"s":"go","x":1}`: "Big",
		`{"n":10,"s":"skipme","t":"2026-07-01T00:00:00Z","limit":"2026-06-01T00:00:00Z","x":1}`: "Other",
		`{"n":1,"t":"2026-01-01T00:00:00Z","limit":"2026-06-01T00:00:00Z","x":1}`:               "Early",
		`{"n":1,"t":"2026-07-01T00:00:00Z","limit":"2026-06-01T00:00:00Z"}`:                     "NoX",
		`{"n":1,"t":"bad","limit":"2026-06-01T00:00:00Z","x":null}`:                             "NoX",
	}
	for in, want := range cases {
		res := run(t, def, in, nil)
		assert.Equal(t, []string{"C", want}, path(res), in)
	}
}

func TestChoiceNoMatch(t *testing.T) {
	def := `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[
	  {"Variable":"$.a","BooleanEquals":true,"Next":"S"}]},"S":{"Type":"Succeed"}}}`
	res := run(t, def, `{"a":false}`, nil)
	assert.Equal(t, "States.NoChoiceMatched", res.Error)

	res = run(t, def, `{}`, nil)
	assert.Equal(t, "States.Runtime", res.Error)
}

func TestParallel(t *testing.T) {
	def := `{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[
	  {"StartAt":"A","States":{"A":{"Type":"Pass","Result":"a","End":true}}},
	  {"StartAt":"B","States":{"B":{"Type":"Pass","InputPath":"$.v","End":true}}}
	],"ResultPath":"$.out","End":true}}}`
	res := run(t, def, `{"v":2}`, nil)
	assert.JSONEq(t, `{"v":2,"out":["a",2]}`, string(res.Output))
	require.Len(t, res.History[0].Branches, 2)
	assert.Equal(t, "B", res.History[0].Branches[1][0].State)
}

func TestParallelBranchFailure(t *testing.T) {
	def := `{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[
	  {"StartAt":"A","States":{"A":{"Type":"Fail","Error":"Boom","Cause":"c"}}},
	  {"StartAt":"B","States":{"B":{"Type":"Pass","End":true}}}
	],"Catch":[{"ErrorEquals":["Boom"],"Next":"Caught"}],"End":true},
	"Caught":{"Type":"Pass","End":true}}}`
	res := run(t, def, `{}`, nil)
	assert.JSONEq(t, `{"Error":"Boom","Cause":"c"}`, string(res.Output))
	assert.Len(t, res.History[0].Branches, 1)
}

func TestMap(t *testing.T) {
	def := `{"StartAt":"M","States":{"M":{"Type":"Map","ItemsPath":"$.items",
	  "ItemSelector":{"i.$":"$$.Map.Item.Index","v.$":"$$.Map.Item.Value","k.$":"$.key"},
	  "ItemProcessor":{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"r","End":true}}},
	  "End":true}}}`
	api := &mockAPI{handler: func(_ int, _ string, p map[string]any) (any, error) {
		return fmt.Sprintf("%v-%v-%v", p["k"], p["i"], p["v"]), nil
	}}
	res := run(t, def, `{"key":"K","items":["a","b"]}`, &sfntest.Env{API: api})
	assert.JSONEq(t, `["K-0-a","K-1-b"]`, string(res.Output))
	assert.Len(t, res.History[0].Branches, 2)
}

func TestVariables(t *testing.T) {
	def := `{"StartAt":"A","States":{
	  "A":{"Type":"Pass","Result":{"n":1},"ResultPath":"$.a","Assign":{"x.$":"$.n","y":"lit"},"Next":"B"},
	  "B":{"Type":"Pass","Assign":{"x":"new","old.$":"$x"},"Next":"M"},
	  "M":{"Type":"Map","ItemsPath":"$.items","ItemProcessor":{"StartAt":"I","States":{
	    "I":{"Type":"Pass","Parameters":{"v.$":"$","x.$":"$x"},"Assign":{"inner":1},"End":true}}},
	    "ResultPath":"$.res","Next":"C"},
	  "C":{"Type":"Pass","Parameters":{"x.$":"$x","old.$":"$old","y.$":"$y","res.$":"$.res"},"End":true}}}`
	res := run(t, def, `{"items":[1]}`, nil)
	assert.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
	assert.JSONEq(t, `{"x":"new","old":1,"y":"lit","res":[{"v":1,"x":"new"}]}`, string(res.Output))
	assert.JSONEq(t, `"lit"`, string(res.History[0].Variables["y"]))
}

func TestIntrinsics(t *testing.T) {
	def := `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{
	  "format.$":"States.Format('Hello, {} {}', $.name, $.n)",
	  "partition.$":"States.ArrayPartition($.arr, 2)",
	  "contains.$":"States.ArrayContains($.arr, 3)",
	  "range.$":"States.ArrayRange(1, 9, 2)",
	  "item.$":"States.ArrayGetItem($.arr, 1)",
	  "len.$":"States.ArrayLength($.arr)",
	  "uniq.$":"States.ArrayUnique(States.Array(1, 1, 'a', 'a'))",
	  "b64.$":"States.Base64Encode('Data to encode')",
	  "dec.$":"States.Base64Decode('RGF0YSB0byBlbmNvZGU=')",
	  "hash.$":"States.Hash('input data', 'SHA-1')",
	  "merge.$":"States.JsonMerge($.a, $.b, false)",
	  "json.$":"States.StringToJson('{\"foo\": \"bar\"}')",
	  "str.$":"States.JsonToString($.a)",
	  "add.$":"States.MathAdd(111, -1)",
	  "split.$":"States.StringSplit('This.is+a,test=string', '.+,=')",
	  "esc.$":"States.Format('it\\'s \\{\\}')"
	},"End":true}}}`
	res := run(t, def, `{"name":"Arnav","n":1,"arr":[1,2,3],"a":{"x":1},"b":{"y":2}}`, nil)
	require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
	assert.JSONEq(t, `{
	  "format":"Hello, Arnav 1",
	  "partition":[[1,2],[3]],
	  "contains":true,
	  "range":[1,3,5,7,9],
	  "item":2,
	  "len":3,
	  "uniq":[1,"a"],
	  "b64":"RGF0YSB0byBlbmNvZGU=",
	  "dec":"Data to encode",
	  "hash":"aaff4a450a104cd177d28d18d74485e8cae074b7",
	  "merge":{"x":1,"y":2},
	  "json":{"foo":"bar"},
	  "str":"{\"x\":1}",
	  "add":110,
	  "split":["This","is","a","test","string"],
	  "esc":"it's {}"
	}`, string(res.Output))
}

func TestIntrinsicFailure(t *testing.T) {
	def := `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{"x.$":"States.ArrayGetItem($.a, 5)"},"End":true}}}`
	res := run(t, def, `{"a":[1]}`, nil)
	assert.Equal(t, "States.IntrinsicFailure", res.Error)
}

func TestJSONPathQueries(t *testing.T) {
	def := `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{
	  "names.$":"$.books[*].title",
	  "cheap.$":"$.books[?(@.price < 10)].title",
	  "last.$":"$.books[-1:].title",
	  "deep.$":"$..price",
	  "first.$":"$['books'][0]['title']"
	},"End":true}}}`
	in := `{"books":[{"title":"A","price":5},{"title":"B","price":15}]}`
	res := run(t, def, in, nil)
	assert.JSONEq(t, `{"names":["A","B"],"cheap":["A"],"last":["B"],"deep":[5,15],"first":"A"}`, string(res.Output))
}

func TestDataLimitExceeded(t *testing.T) {
	def := `{"StartAt":"P","States":{"P":{"Type":"Pass","Result":"0123456789012345678901234567890123456789","End":true}}}`
	res := run(t, def, `{}`, nil, sfntest.WithPayloadLimit(20))
	assert.Equal(t, "States.DataLimitExceeded", res.Error)

	// States.ALL does not catch it.
	def = `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"r",
	  "Catch":[{"ErrorEquals":["States.ALL"],"Next":"C"}],"End":true},"C":{"Type":"Succeed"}}}`
	api := sfntest.APIFunc(func(context.Context, string, []byte) ([]byte, error) {
		return []byte(`"0123456789012345678901234567890123456789"`), nil
	})
	res = run(t, def, `{}`, &sfntest.Env{API: api}, sfntest.WithPayloadLimit(20))
	assert.Equal(t, sfntest.Failed, res.Status)
	assert.Equal(t, "States.DataLimitExceeded", res.Error)
}

func TestFailPaths(t *testing.T) {
	def := `{"StartAt":"F","States":{"F":{"Type":"Fail","ErrorPath":"$.e","CausePath":"States.Format('bad {}', $.id)"}}}`
	res := run(t, def, `{"e":"MyError","id":7}`, nil)
	assert.Equal(t, "MyError", res.Error)
	assert.Equal(t, "bad 7", res.Cause)
}

func TestValidation(t *testing.T) {
	cases := map[string]string{
		`{"StartAt":"X","States":{"A":{"Type":"Succeed"}}}`:                                                                                       `StartAt: state "X" does not exist`,
		`{"StartAt":"A","States":{"A":{"Type":"Pass"}}}`:                                                                                          "either Next or End is required",
		`{"StartAt":"A","States":{"A":{"Type":"Pass","Next":"B"}}}`:                                                                               `Next: state "B" does not exist`,
		`{"StartAt":"A","States":{"A":{"Type":"Pass","End":true,"Resource":"x"}}}`:                                                                `field "Resource" is not allowed`,
		`{"StartAt":"A","States":{"A":{"Type":"Pass","End":true,"Parameters":{"a.$":"States.Nope()"}}}}`:                                          `unknown function`,
		`{"StartAt":"A","States":{"A":{"Type":"Task","Resource":"r","End":true,"Retry":[{"ErrorEquals":["States.ALL"]},{"ErrorEquals":["E"]}]}}}`: "States.ALL must be in the last element",
		`{"StartAt":"A","States":{"A":{"Type":"Pass","End":true,"ResultPath":"$.a[*]"}}}`:                                                         "reference path",
		`{"StartAt":"A","States":{"A":{"Type":"Choice","Choices":[{"Variable":"$.a","StringEquals":1,"Next":"A"}]}}}`:                             "must be a string",
		`{"StartAt":"A","States":{"A":{"Type":"Pass","End":true,"InputPath":"$"}},"QueryLanguage":"JSONata"}`:                                     `field "InputPath" is not allowed in a JSONata Pass state`,
		`{"StartAt":"A","States":{"A":{"Type":"Pass","End":true,"Output":"{% $x + %}"}},"QueryLanguage":"JSONata"}`:                               "States.A.Output",
		`{"StartAt":"A","States":{"A":{"Type":"Pass","End":true,"Output":"{% 1 %} "}},"QueryLanguage":"JSONata"}`:                                 "must start with '{%' and end with '%}'",
		`{"StartAt":"A","States":{"A":{"Type":"Pass","End":true,"QueryLanguage":"JSONPath"}},"QueryLanguage":"JSONata"}`:                          "cannot be used in a JSONata state machine",
	}
	for def, want := range cases {
		_, err := sfntest.New([]byte(def))
		assert.ErrorContains(t, err, want, def)
	}
}

func TestValidationExpress(t *testing.T) {
	def := `{"StartAt":"A","States":{"A":{"Type":"Task","Resource":"arn:aws:states:::ecs:runTask.sync","End":true}}}`
	_, err := sfntest.New([]byte(def))
	require.NoError(t, err)
	_, err = sfntest.New([]byte(def), sfntest.WithType(sfntest.Express))
	assert.ErrorContains(t, err, "not supported in Express workflows")
}
