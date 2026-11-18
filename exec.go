package sfntest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"
)

type runner struct {
	ctx      context.Context
	sm       *StateMachine
	env      *resolvedEnv
	input    any
	execID   string
	varBytes int
}

// ---- variables ----

type scope struct {
	parent *scope
	vars   map[string]any
	sizes  map[string]int
}

func newScope(parent *scope) *scope {
	return &scope{parent: parent, vars: map[string]any{}, sizes: map[string]int{}}
}

func (s *scope) get(name string) (any, bool) {
	for sc := s; sc != nil; sc = sc.parent {
		if v, ok := sc.vars[name]; ok {
			return v, true
		}
	}
	return nil, false
}

func (r *runner) setVar(sc *scope, name string, v any, size int) error {
	r.varBytes += size - sc.sizes[name]
	sc.vars[name] = v
	sc.sizes[name] = size
	if r.varBytes > r.sm.opts.variableTotal {
		return statesErrorf(ErrorDataLimitExceeded, "the total size of variables exceeds %d bytes", r.sm.opts.variableTotal)
	}
	return nil
}

func (r *runner) releaseScope(sc *scope) {
	for _, n := range sc.sizes {
		r.varBytes -= n
	}
}

// ---- context object ----

type stateCtx struct {
	name       string
	entered    time.Time
	retryCount int
	token      string
	mapItem    *mapItem
}

type mapItem struct {
	index int
	value any
}

func (r *runner) executionID() string {
	if r.execID != "" {
		return r.execID
	}
	arn := r.env.exec.StateMachineArn
	if r.sm.opts.typ == Express {
		r.execID = strings.Replace(arn, ":stateMachine:", ":express:", 1) + ":" + r.env.exec.Name + ":" + newUUID()
	} else {
		r.execID = strings.Replace(arn, ":stateMachine:", ":execution:", 1) + ":" + r.env.exec.Name
	}
	return r.execID
}

func (r *runner) contextObject(sc stateCtx) map[string]any {
	e := r.env.exec
	arn := e.StateMachineArn

	execution := map[string]any{
		"Id":           r.executionID(),
		"Input":        r.input,
		"Name":         e.Name,
		"RoleArn":      e.RoleArn,
		"StartTime":    formatTimestamp(e.StartTime),
		"RedriveCount": mustNormalize(e.RedriveCount),
	}
	if !e.RedriveTime.IsZero() {
		execution["RedriveTime"] = formatTimestamp(e.RedriveTime)
	}

	c := map[string]any{
		"Execution": execution,
		"State": map[string]any{
			"EnteredTime": formatTimestamp(sc.entered),
			"Name":        sc.name,
			"RetryCount":  mustNormalize(sc.retryCount),
		},
		"StateMachine": map[string]any{
			"Id":   arn,
			"Name": arn[strings.LastIndex(arn, ":")+1:],
		},
	}
	if sc.token != "" {
		c["Task"] = map[string]any{"Token": sc.token}
	}
	if sc.mapItem != nil {
		c["Map"] = map[string]any{
			"Item": map[string]any{
				"Index": mustNormalize(sc.mapItem.index),
				"Value": sc.mapItem.value,
			},
		}
	}
	return c
}

func mustNormalize(v any) any {
	n, err := normalize(v)
	if err != nil {
		panic(err)
	}
	return n
}

func (r *runner) penv(input any, vars *scope, sc stateCtx) pathEnv {
	return pathEnv{input: input, context: r.contextObject(sc), vars: vars}
}

// ---- machine ----

func (r *runner) checkCtx() error {
	if r.ctx.Err() != nil {
		return fatal(context.Cause(r.ctx))
	}
	return nil
}

func (r *runner) runMachine(m *machine, input any, sc *scope, events *[]Event) (any, error) {
	name := m.StartAt
	for {
		if err := r.checkCtx(); err != nil {
			return nil, err
		}

		s := m.states[name]
		ev := Event{
			State:       name,
			Type:        s.Type,
			EnteredTime: r.env.clock.Now(),
			Input:       mustEncodeJSON(input),
		}

		out, next, err := r.runState(s, input, sc, &ev)
		if err != nil {
			if se, ok := asStatesError(err); ok && !isFatal(err) {
				ev.Error, ev.Cause = se.Name, se.Cause
			}
			*events = append(*events, ev)
			return nil, err
		}

		ev.Output = mustEncodeJSON(out)
		ev.Next = next
		*events = append(*events, ev)

		if next == "" {
			return out, nil
		}
		name, input = next, out
	}
}

func (r *runner) runState(s *state, input any, sc *scope, ev *Event) (any, string, error) {
	stc := stateCtx{name: s.name, entered: ev.EnteredTime}

	switch s.Type {
	case "Pass":
		out, err := r.runPass(s, input, sc, stc, ev)
		return out, s.Next, err
	case "Task":
		return r.withRetry(s, input, sc, ev, func(retryCount int) (any, error) {
			stc.retryCount = retryCount
			return r.runTask(s, input, sc, stc, ev)
		})
	case "Choice":
		return r.runChoice(s, input, sc, stc, ev)
	case "Wait":
		out, err := r.runWait(s, input, sc, stc, ev)
		return out, s.Next, err
	case "Succeed":
		eff, err := r.inputPath(s, input, sc, stc)
		if err != nil {
			return nil, "", err
		}
		out, err := r.outputPath(s, eff, sc, stc)
		return out, "", err
	case "Fail":
		return nil, "", r.runFail(s, input, sc, stc)
	case "Parallel":
		return r.withRetry(s, input, sc, ev, func(retryCount int) (any, error) {
			stc.retryCount = retryCount
			return r.runParallel(s, input, sc, stc, ev)
		})
	case "Map":
		return r.withRetry(s, input, sc, ev, func(retryCount int) (any, error) {
			stc.retryCount = retryCount
			return r.runMap(s, input, sc, stc, ev)
		})
	}
	return nil, "", fatal(fmt.Errorf("unsupported state type: %s", s.Type))
}

// ---- retry / catch ----

func (r *runner) withRetry(s *state, input any, sc *scope, ev *Event, attempt func(retryCount int) (any, error)) (any, string, error) {
	counts := make([]int64, len(s.Retry))
	retryCount := 0

	for {
		out, err := attempt(retryCount)
		if err == nil {
			return out, s.Next, nil
		}
		se, ok := asStatesError(err)
		if !ok || isFatal(err) {
			return nil, "", err
		}

		if i := findRetrier(s.Retry, se.Name); i >= 0 {
			rt := s.Retry[i]
			maxAttempts := int64(3)
			if rt.MaxAttempts != nil {
				maxAttempts = *rt.MaxAttempts
			}
			if counts[i] < maxAttempts {
				r.env.clock.Sleep(retryDelay(rt, counts[i]))
				counts[i]++
				retryCount++
				if err := r.checkCtx(); err != nil {
					return nil, "", err
				}
				continue
			}
		}

		for _, c := range s.Catch {
			if !errorMatches(c.ErrorEquals, se.Name) {
				continue
			}
			errOut := map[string]any{"Error": se.Name, "Cause": se.Cause}
			stc := stateCtx{name: s.name, entered: ev.EnteredTime, retryCount: retryCount}

			vars, err := r.evalAssign(c.Assign, r.penv(errOut, sc, stc))
			if err != nil {
				return nil, "", err
			}
			out, err := applyResultPath(input, c.ResultPath, errOut)
			if err != nil {
				return nil, "", err
			}
			if err := r.checkPayload(out); err != nil {
				return nil, "", err
			}
			if err := r.commitAssign(sc, vars, ev); err != nil {
				return nil, "", err
			}
			ev.Error, ev.Cause = se.Name, se.Cause
			return out, c.Next, nil
		}

		return nil, "", se
	}
}

func findRetrier(retriers []*retrier, name string) int {
	for i, rt := range retriers {
		if errorMatches(rt.ErrorEquals, name) {
			return i
		}
	}
	return -1
}

func retryDelay(rt *retrier, n int64) time.Duration {
	interval := 1.0
	if rt.IntervalSeconds != nil {
		interval = float64(*rt.IntervalSeconds)
	}
	backoff := 2.0
	if rt.BackoffRate != nil {
		backoff = *rt.BackoffRate
	}

	d := interval * math.Pow(backoff, float64(n))
	if rt.MaxDelaySeconds != nil {
		d = min(d, float64(*rt.MaxDelaySeconds))
	}
	if rt.JitterStrategy == "FULL" {
		d = rand.Float64() * d
	}
	return time.Duration(d * float64(time.Second))
}

// ---- input / output processing ----

func (r *runner) inputPath(s *state, input any, sc *scope, stc stateCtx) (any, error) {
	return r.applyPath("InputPath", s.InputPath, input, sc, stc)
}

func (r *runner) outputPath(s *state, v any, sc *scope, stc stateCtx) (any, error) {
	out, err := r.applyPath("OutputPath", s.OutputPath, v, sc, stc)
	if err != nil {
		return nil, err
	}
	return out, r.checkPayload(out)
}

func (r *runner) applyPath(field string, o optString, v any, sc *scope, stc stateCtx) (any, error) {
	if !o.set {
		return v, nil
	}
	if o.null {
		return map[string]any{}, nil
	}
	p, err := parsePath(o.v)
	if err != nil {
		return nil, fatal(err)
	}
	out, err := p.eval(r.penv(v, sc, stc))
	if err != nil {
		return nil, statesErrorf(ErrorRuntime, "%s %s: %s", field, o.v, err)
	}
	return out, nil
}

func applyResultPath(input any, rp optString, result any) (any, error) {
	if rp.set && rp.null {
		return input, nil
	}
	src := "$"
	if rp.set {
		src = rp.v
	}
	p, err := parsePath(src)
	if err != nil {
		return nil, fatal(err)
	}
	out, err := setPath(input, p, result)
	if err != nil {
		return nil, statesErrorf(ErrorResultPathMatchFailure, "ResultPath %s: %s", src, err)
	}
	return out, nil
}

func (r *runner) checkPayload(v any) error {
	if n := len(mustEncodeJSON(v)); n > r.sm.opts.payloadLimit {
		return statesErrorf(ErrorDataLimitExceeded, "the payload size %d exceeds %d bytes", n, r.sm.opts.payloadLimit)
	}
	return nil
}

// evalTemplate evaluates a payload template (Parameters, ResultSelector,
// ItemSelector, Assign).
func (r *runner) evalTemplate(t any, penv pathEnv) (any, error) {
	switch t := t.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, v := range t {
			if name, ok := strings.CutSuffix(k, ".$"); ok {
				expr, _ := v.(string)
				val, err := r.evalExpr(expr, penv, ErrorParameterPathFailure)
				if err != nil {
					return nil, err
				}
				out[name] = val
				continue
			}
			val, err := r.evalTemplate(v, penv)
			if err != nil {
				return nil, err
			}
			out[k] = val
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, v := range t {
			val, err := r.evalTemplate(v, penv)
			if err != nil {
				return nil, err
			}
			out[i] = val
		}
		return out, nil
	}
	return t, nil
}

// evalExpr evaluates a path or an intrinsic function call.
func (r *runner) evalExpr(expr string, penv pathEnv, pathErr string) (any, error) {
	if isPath(expr) {
		p, err := parsePath(expr)
		if err != nil {
			return nil, fatal(err)
		}
		v, err := p.eval(penv)
		if err != nil {
			return nil, statesErrorf(pathErr, "%s: %s", expr, err)
		}
		return deepCopy(v), nil
	}

	c, err := parseIntrinsic(expr)
	if err != nil {
		return nil, fatal(err)
	}
	return r.evalIntrinsic(c, penv)
}

func (r *runner) evalAssign(a map[string]any, penv pathEnv) (map[string]any, error) {
	if len(a) == 0 {
		return nil, nil
	}
	v, err := r.evalTemplate(a, penv)
	if err != nil {
		return nil, err
	}
	vars := v.(map[string]any)

	total := 0
	for name, val := range vars {
		n := len(mustEncodeJSON(val))
		if n > r.sm.opts.variableLimit {
			return nil, statesErrorf(ErrorDataLimitExceeded, "variable $%s exceeds %d bytes", name, r.sm.opts.variableLimit)
		}
		total += n
	}
	if total > r.sm.opts.variableLimit {
		return nil, statesErrorf(ErrorDataLimitExceeded, "the variables of an Assign exceed %d bytes", r.sm.opts.variableLimit)
	}
	return vars, nil
}

func (r *runner) commitAssign(sc *scope, vars map[string]any, ev *Event) error {
	if len(vars) == 0 {
		return nil
	}
	if ev.Variables == nil {
		ev.Variables = map[string]json.RawMessage{}
	}
	for _, name := range sortedKeys(vars) {
		b := mustEncodeJSON(vars[name])
		if err := r.setVar(sc, name, vars[name], len(b)); err != nil {
			return err
		}
		ev.Variables[name] = b
	}
	return nil
}

// ---- states ----

func (r *runner) runPass(s *state, input any, sc *scope, stc stateCtx, ev *Event) (any, error) {
	eff, err := r.inputPath(s, input, sc, stc)
	if err != nil {
		return nil, err
	}
	if s.Parameters != nil {
		if eff, err = r.evalTemplate(s.Parameters, r.penv(eff, sc, stc)); err != nil {
			return nil, err
		}
	}

	result := eff
	if s.has("Result") {
		result = deepCopy(s.Result)
	}

	vars, err := r.evalAssign(s.Assign, r.penv(result, sc, stc))
	if err != nil {
		return nil, err
	}
	out, err := applyResultPath(input, s.ResultPath, result)
	if err != nil {
		return nil, err
	}
	if out, err = r.outputPath(s, out, sc, stc); err != nil {
		return nil, err
	}
	return out, r.commitAssign(sc, vars, ev)
}

func (r *runner) runTask(s *state, input any, sc *scope, stc stateCtx, ev *Event) (any, error) {
	if strings.HasSuffix(s.Resource, ".waitForTaskToken") {
		stc.token = r.env.taskToken.New(s.name)
	}

	eff, err := r.inputPath(s, input, sc, stc)
	if err != nil {
		return nil, err
	}
	params := eff
	if s.Parameters != nil {
		if params, err = r.evalTemplate(s.Parameters, r.penv(eff, sc, stc)); err != nil {
			return nil, err
		}
	}

	result, err := r.call(s.Resource, params, ev)
	if err != nil {
		return nil, err
	}

	return r.finishResult(s, input, result, sc, stc, ev)
}

// finishResult applies ResultSelector, Assign, ResultPath and OutputPath.
func (r *runner) finishResult(s *state, input, result any, sc *scope, stc stateCtx, ev *Event) (any, error) {
	selected := result
	if s.ResultSelector != nil {
		var err error
		if selected, err = r.evalTemplate(s.ResultSelector, r.penv(result, sc, stc)); err != nil {
			return nil, err
		}
	}

	vars, err := r.evalAssign(s.Assign, r.penv(result, sc, stc))
	if err != nil {
		return nil, err
	}
	out, err := applyResultPath(input, s.ResultPath, selected)
	if err != nil {
		return nil, err
	}
	if out, err = r.outputPath(s, out, sc, stc); err != nil {
		return nil, err
	}
	return out, r.commitAssign(sc, vars, ev)
}

func (r *runner) call(resource string, params any, ev *Event) (any, error) {
	pb := mustEncodeJSON(params)
	att := Attempt{Resource: resource, Params: pb}
	record := func(se *StatesError) error {
		att.Error, att.Cause = se.Name, se.Cause
		ev.Attempts = append(ev.Attempts, att)
		return se
	}

	if len(pb) > r.sm.opts.payloadLimit {
		return nil, record(statesErrorf(ErrorDataLimitExceeded, "the parameters size %d exceeds %d bytes", len(pb), r.sm.opts.payloadLimit))
	}
	if r.env.api == nil {
		ev.Attempts = append(ev.Attempts, att)
		return nil, fatal(fmt.Errorf("state %s called %s but Env.API is not set", ev.State, resource))
	}

	rb, err := r.env.api.Call(r.ctx, resource, pb)
	if err != nil {
		if se, ok := asStatesError(err); ok {
			return nil, record(&StatesError{Name: se.Name, Cause: se.Cause})
		}
		ev.Attempts = append(ev.Attempts, att)
		return nil, fatal(fmt.Errorf("state %s: API call %s: %w", ev.State, resource, err))
	}

	att.Result = rb
	if len(rb) > r.sm.opts.payloadLimit {
		return nil, record(statesErrorf(ErrorDataLimitExceeded, "the result size %d exceeds %d bytes", len(rb), r.sm.opts.payloadLimit))
	}
	v, err := decodeJSON(rb)
	if err != nil {
		ev.Attempts = append(ev.Attempts, att)
		return nil, fatal(fmt.Errorf("state %s: API call %s returned invalid JSON: %w", ev.State, resource, err))
	}
	ev.Attempts = append(ev.Attempts, att)
	return v, nil
}

func (r *runner) runChoice(s *state, input any, sc *scope, stc stateCtx, ev *Event) (any, string, error) {
	eff, err := r.inputPath(s, input, sc, stc)
	if err != nil {
		return nil, "", err
	}
	penv := r.penv(eff, sc, stc)

	next := ""
	var assign map[string]any
	for _, rule := range s.Choices {
		ok, err := r.evalRule(rule, penv)
		if err != nil {
			return nil, "", err
		}
		if ok {
			next, _ = rule["Next"].(string)
			assign, _ = rule["Assign"].(map[string]any)
			break
		}
	}
	if next == "" {
		if s.Default == "" {
			return nil, "", statesErrorf(ErrorNoChoiceMatched, "no Choice rule matched and no Default is specified")
		}
		next, assign = s.Default, s.Assign
	}

	vars, err := r.evalAssign(assign, penv)
	if err != nil {
		return nil, "", err
	}
	out, err := r.outputPath(s, eff, sc, stc)
	if err != nil {
		return nil, "", err
	}
	return out, next, r.commitAssign(sc, vars, ev)
}

func (r *runner) runWait(s *state, input any, sc *scope, stc stateCtx, ev *Event) (any, error) {
	eff, err := r.inputPath(s, input, sc, stc)
	if err != nil {
		return nil, err
	}
	penv := r.penv(eff, sc, stc)

	var d time.Duration
	switch {
	case s.Seconds != nil:
		n, _ := toNonNegInt(s.Seconds)
		d = time.Duration(n) * time.Second
	case s.Timestamp != nil:
		t, _ := parseTimestamp(s.Timestamp.(string))
		d = t.Sub(r.env.clock.Now())
	case s.SecondsPath.set:
		v, err := r.evalExpr(s.SecondsPath.v, penv, ErrorRuntime)
		if err != nil {
			return nil, err
		}
		n, ok := toNonNegInt(v)
		if !ok {
			return nil, statesErrorf(ErrorRuntime, "SecondsPath %s: must be a non-negative integer", s.SecondsPath.v)
		}
		d = time.Duration(n) * time.Second
	case s.TimestampPath.set:
		v, err := r.evalExpr(s.TimestampPath.v, penv, ErrorRuntime)
		if err != nil {
			return nil, err
		}
		str, _ := v.(string)
		t, ok := parseTimestamp(str)
		if !ok {
			return nil, statesErrorf(ErrorRuntime, "TimestampPath %s: must be a timestamp", s.TimestampPath.v)
		}
		d = t.Sub(r.env.clock.Now())
	}
	if d > 0 {
		r.env.clock.Sleep(d)
	}

	vars, err := r.evalAssign(s.Assign, penv)
	if err != nil {
		return nil, err
	}
	out, err := r.outputPath(s, eff, sc, stc)
	if err != nil {
		return nil, err
	}
	return out, r.commitAssign(sc, vars, ev)
}

func (r *runner) runFail(s *state, input any, sc *scope, stc stateCtx) error {
	penv := r.penv(input, sc, stc)
	str := func(field string, lit, path optString) (string, error) {
		if lit.set {
			return lit.v, nil
		}
		if !path.set {
			return "", nil
		}
		v, err := r.evalExpr(path.v, penv, ErrorRuntime)
		if err != nil {
			return "", err
		}
		s, ok := v.(string)
		if !ok {
			return "", statesErrorf(ErrorRuntime, "%s %s: must be a string, got %s", field, path.v, typeName(v))
		}
		return s, nil
	}

	name, err := str("ErrorPath", s.Error, s.ErrorPath)
	if err != nil {
		return err
	}
	cause, err := str("CausePath", s.Cause, s.CausePath)
	if err != nil {
		return err
	}
	return &StatesError{Name: name, Cause: cause}
}

func (r *runner) runParallel(s *state, input any, sc *scope, stc stateCtx, ev *Event) (any, error) {
	eff, err := r.inputPath(s, input, sc, stc)
	if err != nil {
		return nil, err
	}
	if s.Parameters != nil {
		if eff, err = r.evalTemplate(s.Parameters, r.penv(eff, sc, stc)); err != nil {
			return nil, err
		}
	}

	ev.Branches = nil
	results := make([]any, 0, len(s.Branches))
	for _, b := range s.Branches {
		out, err := r.runBranch(b, deepCopy(eff), sc, ev)
		if err != nil {
			return nil, err
		}
		results = append(results, out)
	}

	return r.finishResult(s, input, results, sc, stc, ev)
}

func (r *runner) runMap(s *state, input any, sc *scope, stc stateCtx, ev *Event) (any, error) {
	eff, err := r.inputPath(s, input, sc, stc)
	if err != nil {
		return nil, err
	}

	items := eff
	if s.ItemsPath.set && !s.ItemsPath.null {
		if items, err = r.evalExpr(s.ItemsPath.v, r.penv(eff, sc, stc), ErrorRuntime); err != nil {
			return nil, err
		}
	}
	arr, ok := items.([]any)
	if !ok {
		return nil, statesErrorf(ErrorRuntime, "ItemsPath must select an array, got %s", typeName(items))
	}

	selector := s.ItemSelector
	if selector == nil {
		selector = s.Parameters
	}

	ev.Branches = nil
	results := make([]any, 0, len(arr))
	for i, item := range arr {
		in := deepCopy(item)
		if selector != nil {
			ictx := stc
			ictx.mapItem = &mapItem{index: i, value: item}
			if in, err = r.evalTemplate(selector, r.penv(eff, sc, ictx)); err != nil {
				return nil, err
			}
		}

		out, err := r.runBranch(s.processor(), in, sc, ev)
		if err != nil {
			return nil, err
		}
		results = append(results, out)
	}

	return r.finishResult(s, input, results, sc, stc, ev)
}

func (r *runner) runBranch(m *machine, input any, parent *scope, ev *Event) (any, error) {
	child := newScope(parent)
	defer r.releaseScope(child)

	var events []Event
	out, err := r.runMachine(m, input, child, &events)
	ev.Branches = append(ev.Branches, events)
	return out, err
}
