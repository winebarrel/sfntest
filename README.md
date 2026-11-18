# sfntest

[![CI](https://github.com/winebarrel/sfntest/actions/workflows/ci.yml/badge.svg)](https://github.com/winebarrel/sfntest/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/winebarrel/sfntest.svg)](https://pkg.go.dev/github.com/winebarrel/sfntest)
[![codecov](https://codecov.io/gh/winebarrel/sfntest/graph/badge.svg)](https://codecov.io/gh/winebarrel/sfntest)
[![AI Generated](https://img.shields.io/badge/AI%20Generated-Claude-orange?logo=anthropic)](https://claude.com/product/claude-code)

sfntest is a library for unit testing AWS Step Functions state machines locally.

It runs a state machine definition (Amazon States Language) in process. Calls to AWS services go to a mock that you write. The states themselves are evaluated as Step Functions evaluates them, so a test checks the definition: its paths, transformations, choices, retries and error handling.

No AWS account, credentials or network access is needed.

Supported:

- The JSONPath and JSONata query languages, also mixed in one state machine
- All state types: Pass, Task, Choice, Wait, Succeed, Fail, Parallel and Map
- Distributed Map, with ItemReader, ItemBatcher, ResultWriter and tolerated failures
- Retry and Catch, variables, the context object, and all intrinsic functions
- Standard and Express workflows

## Installation

```sh
go get github.com/winebarrel/sfntest
```

## Usage

Take this state machine, saved as `order.asl.json`:

```json
{
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
      "Next": "CheckStatus"
    },
    "CheckStatus": {
      "Type": "Choice",
      "Choices": [{ "Variable": "$.status", "StringEquals": "OK", "Next": "Done" }],
      "Default": "Failed"
    },
    "Done": { "Type": "Succeed" },
    "Failed": { "Type": "Fail", "Error": "OrderFailed" }
  }
}
```

A test creates the state machine with `sfntest.New`, runs it with a mock, and checks the result:

```go
package order_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/winebarrel/sfntest"
)

func TestOrderSucceedsAfterRetry(t *testing.T) {
	def, err := os.ReadFile("order.asl.json")
	if err != nil {
		t.Fatal(err)
	}
	sm, err := sfntest.New(def)
	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	api := sfntest.APIFunc(func(ctx context.Context, resource string, params []byte) ([]byte, error) {
		calls++
		if calls == 1 {
			// A States error fails the task, so Retry and Catch apply.
			return nil, sfntest.Error("Lambda.ServiceException", "temporary failure")
		}
		return []byte(`{"Payload":{"status":"OK"}}`), nil
	})

	res, err := sm.Run(context.Background(), []byte(`{"orderId":"123"}`), &sfntest.Env{API: api})
	if err != nil {
		t.Fatal(err)
	}

	if res.Status != sfntest.Succeeded {
		t.Fatalf("status = %s, error = %s, cause = %s", res.Status, res.Error, res.Cause)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}

	// The parameters of each call are recorded in the history.
	var params struct {
		FunctionName string
		Payload      struct {
			OrderID string `json:"orderId"`
		}
	}
	if err := json.Unmarshal(res.History[0].Attempts[0].Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Payload.OrderID != "123" {
		t.Errorf("orderId = %q, want %q", params.Payload.OrderID, "123")
	}

	var path []string
	for _, e := range res.History {
		path = append(path, e.State)
	}
	if len(path) != 3 || path[2] != "Done" {
		t.Errorf("path = %v", path)
	}
}
```

`New` validates the definition as `CreateStateMachine` does, so a broken definition fails there. Create the state machine once and call `Run` for each test case.

## Mocking service calls

Every call the state machine makes to a service goes to `Env.API`:

```go
type API interface {
	Call(ctx context.Context, resource string, params []byte) ([]byte, error)
}
```

- `resource` is the `Resource` of the state as written, such as `arn:aws:states:::lambda:invoke`, `arn:aws:states:::ecs:runTask.sync`, `arn:aws:states:::sqs:sendMessage.waitForTaskToken` or an activity ARN.
- `params` is the JSON the state sends, after `Parameters` or `Arguments` are evaluated.
- The returned bytes are the result of the task. For a `.sync` task, return the final result. For a `.waitForTaskToken` task, return what `SendTaskSuccess` would send. The task token is in `params` and in the context object.
- Return an error made by `sfntest.Error(name, cause)` to make the task fail with that error, as `SendTaskFailure` or a failed Lambda function would. Return `sfntest.Error("States.Timeout", "")` for a timeout.
- Any other error aborts `Run`, which returns that error. Use it for calls a test does not expect.

Nested executions (`states:startExecution`) are mocked the same way.

### Distributed Map

The S3 calls of a distributed Map also go to `Env.API`:

| Resource | params | Return |
|---|---|---|
| `arn:aws:states:::s3:getObject` | `Bucket`, `Key`, ... | The object body (JSON, JSONL or CSV, optionally gzipped) |
| `arn:aws:states:::s3:listObjectsV2` | `Bucket`, `Prefix`, ..., `ContinuationToken` | A ListObjectsV2 response: `{"Contents": [...], "IsTruncated": ..., "NextContinuationToken": ...}` |
| `arn:aws:states:::s3:putObject` | `Bucket`, `Key`, `Body` | Anything (ignored) |

S3 Inventory and Athena manifests are followed by more `getObject` calls for their data files. `ResultWriter` writes `SUCCEEDED_0.json`, `FAILED_0.json` and `manifest.json` with one `putObject` call each.

## Controlling non-deterministic values

`Env` provides everything a state machine reads from outside its input. Unset fields use defaults.

```go
env := &sfntest.Env{
	API: api,
	// Context object values. Zero-value fields use defaults.
	Execution: &sfntest.Execution{
		StateMachineArn: "arn:aws:states:us-east-1:123456789012:stateMachine:orders",
		Name:            "test-execution",
		StartTime:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	},
	// The clock for EnteredTime, Wait states and retry intervals.
	// A FakeClock (the default) advances instantly, so waits take no time.
	Clock: &sfntest.FakeClock{T: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	// Task tokens for .waitForTaskToken tasks (any TaskToken implementation).
	TaskToken: myTaskTokens{},
	// Replace intrinsic functions (JSONPath) by name.
	Intrinsic: sfntest.IntrinsicFuncs{
		"States.UUID": func(args ...any) (any, error) { return "uuid-1", nil },
	},
	// Replace or add JSONata functions by name.
	JSONata: sfntest.JSONataFuncs{
		"$uuid": func(args ...any) (any, error) { return "uuid-1", nil },
	},
}
```

The default functions follow the AWS documentation. `States.UUID`, `States.MathRandom`, `$uuid` and `$random` return random values, so replace them when a test depends on the value. JSONata `$now` and `$millis` read `Env.Clock`.

`$$.Execution.Id`, `$$.StateMachine.Id` and `$$.StateMachine.Name` are built from `StateMachineArn` and `Name`, in the Standard or Express format.

## Results

`Run` returns a `Result`:

- `Status`: `Succeeded`, `Failed`, or `Incomplete` when `Run` returned an error.
- `Output`: the execution output.
- `Error` and `Cause`: the error that failed the execution.
- `History`: one `Event` per state visit. An event holds the state's input and output, its error, the next state, the service calls of each attempt (`Attempts`), the variables it assigned, and the events of each Parallel branch or Map iteration (`Branches`).

A failed execution is not an error: `Run` returns a nil error and `Status` is `Failed`. `Run` returns an error only when the execution cannot continue: the mock returned a non-States error, the timeout expired, or `ctx` was canceled. The partial result is returned too, so `History` shows where it stopped.

## Options

```go
sm, err := sfntest.New(def,
	sfntest.WithType(sfntest.Express),           // default: Standard
	sfntest.WithTimeout(30*time.Second),         // real time per Run, default: 10s
	sfntest.WithPayloadLimit(256*1024),          // default: 256 KiB
	sfntest.WithVariableLimit(256*1024, 10<<20), // per variable and per execution
)
```

`WithType(sfntest.Express)` rejects what Express workflows do not support: `.sync`, `.waitForTaskToken`, activities and distributed Map. `WithTimeout` stops runaway loops. It is not the state machine's `TimeoutSeconds`.

## Differences from AWS Step Functions

- Parallel branches, Map iterations and distributed Map child executions run one at a time, in order. `MaxConcurrency` is ignored. When a branch or iteration fails, the later ones do not run.
- Timeouts are not measured. `TimeoutSeconds` and `HeartbeatSeconds` are only validated. A mock returns `States.Timeout` to simulate a timeout.
- The execution `TimeoutSeconds` and the history event limit are not enforced.
- Redrive is not supported.
- The items of a JSON object read by a distributed Map are visited in key order.
- Parquet and ZSTD input for distributed Map is not supported.
- Where AWS does not document a behavior, sfntest follows the ASL specification or the closest documented behavior. For example, the schema of the files written by `ResultWriter` is inferred.
