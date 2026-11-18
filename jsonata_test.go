package sfntest_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/sfntest"
)

func TestJSONataTask(t *testing.T) {
	def := `{"QueryLanguage":"JSONata","StartAt":"T","States":{
	  "T":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke",
	    "Arguments":{"FunctionName":"f","Payload":{"id":"{% $states.input.id %}","lit":"$not.a.path"}},
	    "Assign":{"price":"{% $states.result.Payload.price %}"},
	    "Output":{"total":"{% $states.result.Payload.price * $states.input.qty %}"},
	    "Next":"P"},
	  "P":{"Type":"Pass","Output":{"total":"{% $states.input.total %}","price":"{% $price %}"},"End":true}}}`
	var got map[string]any
	api := sfntest.APIFunc(func(_ context.Context, _ string, params []byte) ([]byte, error) {
		_ = json.Unmarshal(params, &got)
		return []byte(`{"Payload":{"price":2.5}}`), nil
	})

	res := run(t, def, `{"id":"x","qty":4}`, &sfntest.Env{API: api})
	require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
	assert.Equal(t, map[string]any{"FunctionName": "f", "Payload": map[string]any{"id": "x", "lit": "$not.a.path"}}, got)
	assert.JSONEq(t, `{"total":10,"price":2.5}`, string(res.Output))
	assert.JSONEq(t, `2.5`, string(res.History[0].Variables["price"]))
}

func TestJSONataDefaults(t *testing.T) {
	// Without Arguments the input is sent; without Output the result is the output.
	def := `{"QueryLanguage":"JSONata","StartAt":"T","States":{"T":{"Type":"Task","Resource":"r","End":true}}}`
	api := sfntest.APIFunc(func(_ context.Context, _ string, params []byte) ([]byte, error) {
		assert.JSONEq(t, `{"a":1}`, string(params))
		return []byte(`[1,2]`), nil
	})
	res := run(t, def, `{"a":1}`, &sfntest.Env{API: api})
	assert.JSONEq(t, `[1,2]`, string(res.Output))
}

func TestJSONataCatch(t *testing.T) {
	def := `{"QueryLanguage":"JSONata","StartAt":"T","States":{
	  "T":{"Type":"Task","Resource":"r",
	    "Retry":[{"ErrorEquals":["States.QueryEvaluationError"],"MaxAttempts":0}],
	    "Catch":[{"ErrorEquals":["States.ALL"],"Next":"C",
	      "Assign":{"err":"{% $states.errorOutput.Error %}"},
	      "Output":{"in":"{% $states.input %}","cause":"{% $states.errorOutput.Cause %}"}}],
	    "End":true},
	  "C":{"Type":"Pass","Output":{"err":"{% $err %}","prev":"{% $states.input %}"},"End":true}}}`
	api := sfntest.APIFunc(func(context.Context, string, []byte) ([]byte, error) {
		return nil, sfntest.Error("MyError", "boom")
	})
	res := run(t, def, `{"a":1}`, &sfntest.Env{API: api})
	assert.JSONEq(t, `{"err":"MyError","prev":{"in":{"a":1},"cause":"boom"}}`, string(res.Output))

	// Without Output, the error output is the state output.
	def = `{"QueryLanguage":"JSONata","StartAt":"T","States":{
	  "T":{"Type":"Task","Resource":"r","Catch":[{"ErrorEquals":["States.ALL"],"Next":"C"}],"End":true},
	  "C":{"Type":"Succeed"}}}`
	res = run(t, def, `{}`, &sfntest.Env{API: api})
	assert.JSONEq(t, `{"Error":"MyError","Cause":"boom"}`, string(res.Output))
}

func TestJSONataQueryEvaluationError(t *testing.T) {
	def := `{"QueryLanguage":"JSONata","StartAt":"P","States":{"P":{"Type":"Pass","Output":"{% $states.input.missing %}","End":true}}}`
	res := run(t, def, `{}`, nil)
	assert.Equal(t, "States.QueryEvaluationError", res.Error)
	assert.Contains(t, res.Cause, "undefined")

	def = `{"QueryLanguage":"JSONata","StartAt":"P","States":{"P":{"Type":"Pass","Output":"{% 'a' + 1 %}","End":true}}}`
	res = run(t, def, `{}`, nil)
	assert.Equal(t, "States.QueryEvaluationError", res.Error)

	def = `{"QueryLanguage":"JSONata","StartAt":"P","States":{"P":{"Type":"Pass","Output":"{% $eval('1') %}","End":true}}}`
	res = run(t, def, `{}`, nil)
	assert.Equal(t, "States.QueryEvaluationError", res.Error)
	assert.Contains(t, res.Cause, "$parse")
}

func TestJSONataChoice(t *testing.T) {
	def := `{"QueryLanguage":"JSONata","StartAt":"C","States":{
	  "C":{"Type":"Choice","Choices":[
	    {"Condition":"{% $states.input.n > 10 %}","Next":"Big","Assign":{"kind":"big"}},
	    {"Condition":false,"Next":"Never"}
	  ],"Default":"Small","Assign":{"kind":"small"},"Output":{"n":"{% $states.input.n %}"}},
	  "Big":{"Type":"Pass","Output":"{% $kind %}","End":true},
	  "Small":{"Type":"Pass","Output":"{% $kind %}","End":true},
	  "Never":{"Type":"Succeed"}}}`
	res := run(t, def, `{"n":20}`, nil)
	assert.Equal(t, []string{"C", "Big"}, path(res))
	assert.JSONEq(t, `"big"`, string(res.Output))
	assert.JSONEq(t, `{"n":20}`, string(res.History[0].Output))

	res = run(t, def, `{"n":1}`, nil)
	assert.JSONEq(t, `"small"`, string(res.Output))

	def = `{"QueryLanguage":"JSONata","StartAt":"C","States":{
	  "C":{"Type":"Choice","Choices":[{"Condition":"{% 1 %}","Next":"S"}]},"S":{"Type":"Succeed"}}}`
	res = run(t, def, `{}`, nil)
	assert.Equal(t, "States.QueryEvaluationError", res.Error)
}

func TestJSONataWaitAndFail(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &sfntest.FakeClock{T: start}
	def := `{"QueryLanguage":"JSONata","StartAt":"W","States":{
	  "W":{"Type":"Wait","Seconds":"{% $states.input.sec %}","Next":"W2"},
	  "W2":{"Type":"Wait","Timestamp":"{% $states.input.until %}","Next":"F"},
	  "F":{"Type":"Fail","Error":"{% 'E' & $states.input.sec %}","Cause":"static"}}}`
	res := run(t, def, `{"sec":3,"until":"2026-01-01T00:00:10Z"}`, &sfntest.Env{Clock: clock})
	assert.Equal(t, start.Add(10*time.Second), clock.T)
	assert.Equal(t, "E3", res.Error)
	assert.Equal(t, "static", res.Cause)
}

func TestJSONataParallelAndMap(t *testing.T) {
	def := `{"QueryLanguage":"JSONata","StartAt":"M","States":{
	  "M":{"Type":"Map","Items":"{% $states.input.items %}",
	    "ItemSelector":{"i":"{% $states.context.Map.Item.Index %}","v":"{% $states.context.Map.Item.Value %}","k":"{% $states.input.key %}"},
	    "ItemProcessor":{"StartAt":"P","States":{"P":{"Type":"Parallel","Arguments":{"x":"{% $states.input.k & $states.input.v %}"},
	      "Branches":[
	        {"StartAt":"A","States":{"A":{"Type":"Pass","Output":"{% $states.input.x %}","End":true}}},
	        {"StartAt":"B","States":{"B":{"Type":"Pass","Output":"{% $states.context.State.Name %}","End":true}}}
	      ],
	      "Output":"{% $join($states.result, '-') %}","End":true}}},
	    "Output":{"r":"{% $states.result %}"},"End":true}}}`
	res := run(t, def, `{"key":"K","items":["a","b"]}`, nil)
	require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
	assert.JSONEq(t, `{"r":["Ka-B","Kb-B"]}`, string(res.Output))
}

func TestJSONataFunctions(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	def := `{"QueryLanguage":"JSONata","StartAt":"P","States":{"P":{"Type":"Pass","Output":{
	  "partition":"{% $partition([1,2,3], 2) %}",
	  "range":"{% $range(0, 10, 5) %}",
	  "hash":"{% $hash('input data', 'SHA-1') %}",
	  "parse":"{% $parse('{\"a\":1}') %}",
	  "uuid":"{% $uuid() %}",
	  "now":"{% $now() %}",
	  "millis":"{% $millis() %}",
	  "random":"{% $random() < 1 %}",
	  "sum":"{% $sum([1,2]) %}"
	},"End":true}}}`
	env := &sfntest.Env{
		Clock:   &sfntest.FakeClock{T: start},
		JSONata: sfntest.JSONataFuncs{"$uuid": func(...any) (any, error) { return "uuid-1", nil }},
	}
	res := run(t, def, `{}`, env)
	require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
	assert.JSONEq(t, `{
	  "partition":[[1,2],[3]],
	  "range":[0,5,10],
	  "hash":"aaff4a450a104cd177d28d18d74485e8cae074b7",
	  "parse":{"a":1},
	  "uuid":"uuid-1",
	  "now":"2026-01-02T03:04:05.000Z",
	  "millis":1767323045000,
	  "random":true,
	  "sum":3
	}`, string(res.Output))
}

func TestJSONataFuncErrors(t *testing.T) {
	def := `{"QueryLanguage":"JSONata","StartAt":"P","States":{"P":{"Type":"Pass","Output":"{% $check() %}","End":true}}}`

	env := &sfntest.Env{JSONata: sfntest.JSONataFuncs{"$check": func(...any) (any, error) {
		return nil, sfntest.Error("Custom.Error", "bad")
	}}}
	res := run(t, def, `{}`, env)
	assert.Equal(t, "Custom.Error", res.Error)

	env = &sfntest.Env{JSONata: sfntest.JSONataFuncs{"$check": func(...any) (any, error) {
		return nil, errors.New("mock bug")
	}}}
	sm, err := sfntest.New([]byte(def))
	require.NoError(t, err)
	res, err = sm.Run(context.Background(), []byte(`{}`), env)
	require.ErrorContains(t, err, "mock bug")
	assert.Equal(t, sfntest.Incomplete, res.Status)

	_, err = sm.Run(context.Background(), []byte(`{}`), &sfntest.Env{JSONata: sfntest.JSONataFuncs{"check": nil}})
	require.ErrorContains(t, err, "must start with '$'")
}

func TestIntrinsicOverrideErrors(t *testing.T) {
	sm, err := sfntest.New([]byte(`{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`))
	require.NoError(t, err)
	_, err = sm.Run(context.Background(), []byte(`{}`), &sfntest.Env{Intrinsic: sfntest.IntrinsicFuncs{"States.Nope": nil}})
	require.ErrorContains(t, err, "unknown intrinsic function")
}

func TestMixedQueryLanguages(t *testing.T) {
	def := `{"StartAt":"A","States":{
	  "A":{"Type":"Pass","Result":{"n":2},"Assign":{"x.$":"$.n"},"Next":"B"},
	  "B":{"Type":"Pass","QueryLanguage":"JSONata","Output":{"y":"{% $x * $states.input.n %}"},"End":true}}}`
	res := run(t, def, `{}`, nil)
	assert.JSONEq(t, `{"y":4}`, string(res.Output))
}
