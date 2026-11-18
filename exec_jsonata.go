package sfntest

import (
	"strings"
	"time"
)

func (r *runner) runStateJSONata(s *state, input any, sc *scope, ev *Event) (any, string, error) {
	stc := stateCtx{name: s.name, entered: ev.EnteredTime}

	switch s.Type {
	case "Pass":
		vars, err := r.jsonataVars(sc, jsonataStates{input: input, context: r.contextObject(stc)})
		if err != nil {
			return nil, "", err
		}
		out, err := r.finishJSONata(s, vars, input, sc, ev)
		return out, s.Next, err
	case "Task":
		return r.withRetry(s, input, sc, ev, func(retryCount int) (any, error) {
			stc.retryCount = retryCount
			return r.runTaskJSONata(s, input, sc, stc, ev)
		})
	case "Choice":
		return r.runChoiceJSONata(s, input, sc, stc, ev)
	case "Wait":
		out, err := r.runWaitJSONata(s, input, sc, stc, ev)
		return out, s.Next, err
	case "Succeed":
		vars, err := r.jsonataVars(sc, jsonataStates{input: input, context: r.contextObject(stc)})
		if err != nil {
			return nil, "", err
		}
		out, err := r.finishJSONata(s, vars, input, sc, ev)
		return out, "", err
	case "Fail":
		return nil, "", r.runFailJSONata(s, input, sc, stc)
	case "Parallel":
		return r.withRetry(s, input, sc, ev, func(retryCount int) (any, error) {
			stc.retryCount = retryCount
			return r.runParallelJSONata(s, input, sc, stc, ev)
		})
	case "Map":
		return r.withRetry(s, input, sc, ev, func(retryCount int) (any, error) {
			stc.retryCount = retryCount
			return r.runMapJSONata(s, input, sc, stc, ev)
		})
	}
	return nil, "", fatal(errUnsupportedState(s.Type))
}

func (r *runner) evalJSONataAssign(a map[string]any, vars map[string]any) (map[string]any, error) {
	if len(a) == 0 {
		return nil, nil
	}
	v, err := r.evalJSONata(a, vars)
	if err != nil {
		return nil, err
	}
	return r.checkVariables(v.(map[string]any))
}

// finishJSONata evaluates Assign and Output (dflt when Output is omitted)
// and commits the variables.
func (r *runner) finishJSONata(s *state, vars map[string]any, dflt any, sc *scope, ev *Event) (any, error) {
	assigned, err := r.evalJSONataAssign(s.Assign, vars)
	if err != nil {
		return nil, err
	}
	out := dflt
	if s.Output != nil {
		if out, err = r.evalJSONata(s.Output, vars); err != nil {
			return nil, err
		}
	}
	if err := r.checkPayload(out); err != nil {
		return nil, err
	}
	return out, r.commitAssign(sc, assigned, ev)
}

// finishResultJSONata evaluates Assign and Output with $states.result.
func (r *runner) finishResultJSONata(s *state, input, result any, sc *scope, stc stateCtx, ev *Event) (any, error) {
	vars, err := r.jsonataVars(sc, jsonataStates{input: input, result: result, hasResult: true, context: r.contextObject(stc)})
	if err != nil {
		return nil, err
	}
	return r.finishJSONata(s, vars, result, sc, ev)
}

// positiveIntField checks a field such as TimeoutSeconds that may be a JSONata expression.
func (r *runner) positiveIntField(field string, v any, vars map[string]any) error {
	if v == nil {
		return nil
	}
	val, err := r.evalJSONata(v, vars)
	if err != nil {
		return err
	}
	if n, ok := toNonNegInt(val); !ok || n == 0 {
		return queryError("%s must be a positive integer, got %s", field, mustEncodeJSON(val))
	}
	return nil
}

func (r *runner) runTaskJSONata(s *state, input any, sc *scope, stc stateCtx, ev *Event) (any, error) {
	if strings.HasSuffix(s.Resource, ".waitForTaskToken") {
		stc.token = r.env.taskToken.New(s.name)
	}

	vars, err := r.jsonataVars(sc, jsonataStates{input: input, context: r.contextObject(stc)})
	if err != nil {
		return nil, err
	}
	if err := r.positiveIntField("TimeoutSeconds", s.TimeoutSeconds, vars); err != nil {
		return nil, err
	}
	if err := r.positiveIntField("HeartbeatSeconds", s.HeartbeatSeconds, vars); err != nil {
		return nil, err
	}

	args := input
	if s.Arguments != nil {
		if args, err = r.evalJSONata(s.Arguments, vars); err != nil {
			return nil, err
		}
	}

	result, err := r.call(s.Resource, args, ev)
	if err != nil {
		return nil, err
	}
	return r.finishResultJSONata(s, input, result, sc, stc, ev)
}

func (r *runner) runChoiceJSONata(s *state, input any, sc *scope, stc stateCtx, ev *Event) (any, string, error) {
	vars, err := r.jsonataVars(sc, jsonataStates{input: input, context: r.contextObject(stc)})
	if err != nil {
		return nil, "", err
	}

	next := ""
	assign := s.Assign
	for _, rule := range s.Choices {
		cond, err := r.evalJSONata(rule["Condition"], vars)
		if err != nil {
			return nil, "", err
		}
		b, ok := cond.(bool)
		if !ok {
			return nil, "", queryError("Condition must evaluate to a boolean, got %s", typeName(cond))
		}
		if b {
			next, _ = rule["Next"].(string)
			assign, _ = rule["Assign"].(map[string]any)
			break
		}
	}
	if next == "" {
		if s.Default == "" {
			return nil, "", statesErrorf(ErrorNoChoiceMatched, "no Choice rule matched and no Default is specified")
		}
		next = s.Default
	}

	// Only the Assign of the matched rule (or the top-level one for Default) is evaluated.
	sub := *s
	sub.Assign = assign
	out, err := r.finishJSONata(&sub, vars, input, sc, ev)
	return out, next, err
}

func (r *runner) runWaitJSONata(s *state, input any, sc *scope, stc stateCtx, ev *Event) (any, error) {
	vars, err := r.jsonataVars(sc, jsonataStates{input: input, context: r.contextObject(stc)})
	if err != nil {
		return nil, err
	}

	var d time.Duration
	if s.Seconds != nil {
		v, err := r.evalJSONata(s.Seconds, vars)
		if err != nil {
			return nil, err
		}
		n, ok := toNonNegInt(v)
		if !ok || n > 99_999_999 {
			return nil, queryError("Seconds must be an integer from 0 to 99999999, got %s", mustEncodeJSON(v))
		}
		d = time.Duration(n) * time.Second
	} else {
		v, err := r.evalJSONata(s.Timestamp, vars)
		if err != nil {
			return nil, err
		}
		str, _ := v.(string)
		t, ok := parseTimestamp(str)
		if !ok {
			return nil, queryError("Timestamp must be a timestamp, got %s", mustEncodeJSON(v))
		}
		d = t.Sub(r.env.clock.Now())
	}
	if d > 0 {
		r.env.clock.Sleep(d)
	}

	return r.finishJSONata(s, vars, input, sc, ev)
}

func (r *runner) runFailJSONata(s *state, input any, sc *scope, stc stateCtx) error {
	vars, err := r.jsonataVars(sc, jsonataStates{input: input, context: r.contextObject(stc)})
	if err != nil {
		return err
	}

	str := func(field string, o optString) (string, error) {
		if !o.set || o.null {
			return "", nil
		}
		v, err := r.evalJSONata(o.v, vars)
		if err != nil {
			return "", err
		}
		s, ok := v.(string)
		if !ok {
			return "", queryError("%s must evaluate to a string, got %s", field, typeName(v))
		}
		return s, nil
	}

	name, err := str("Error", s.Error)
	if err != nil {
		return err
	}
	cause, err := str("Cause", s.Cause)
	if err != nil {
		return err
	}
	return &StatesError{Name: name, Cause: cause}
}

func (r *runner) runParallelJSONata(s *state, input any, sc *scope, stc stateCtx, ev *Event) (any, error) {
	vars, err := r.jsonataVars(sc, jsonataStates{input: input, context: r.contextObject(stc)})
	if err != nil {
		return nil, err
	}

	in := input
	if s.Arguments != nil {
		if in, err = r.evalJSONata(s.Arguments, vars); err != nil {
			return nil, err
		}
	}

	ev.Branches = nil
	results := make([]any, 0, len(s.Branches))
	for _, b := range s.Branches {
		out, err := r.runBranch(b, deepCopy(in), sc, ev)
		if err != nil {
			return nil, err
		}
		results = append(results, out)
	}

	return r.finishResultJSONata(s, input, results, sc, stc, ev)
}

func (r *runner) runMapJSONata(s *state, input any, sc *scope, stc stateCtx, ev *Event) (any, error) {
	vars, err := r.jsonataVars(sc, jsonataStates{input: input, context: r.contextObject(stc)})
	if err != nil {
		return nil, err
	}

	items := input
	if s.Items != nil {
		if items, err = r.evalJSONata(s.Items, vars); err != nil {
			return nil, err
		}
	}
	arr, ok := items.([]any)
	if !ok {
		return nil, queryError("Items must be an array, got %s", typeName(items))
	}

	ev.Branches = nil
	results := make([]any, 0, len(arr))
	for i, item := range arr {
		in := deepCopy(item)
		if s.ItemSelector != nil {
			ictx := stc
			ictx.mapItem = &mapItem{index: i, value: item}
			ivars, err := r.jsonataVars(sc, jsonataStates{input: input, context: r.contextObject(ictx)})
			if err != nil {
				return nil, err
			}
			if in, err = r.evalJSONata(s.ItemSelector, ivars); err != nil {
				return nil, err
			}
		}

		out, err := r.runBranch(s.processor(), in, sc, ev)
		if err != nil {
			return nil, err
		}
		results = append(results, out)
	}

	return r.finishResultJSONata(s, input, results, sc, stc, ev)
}
