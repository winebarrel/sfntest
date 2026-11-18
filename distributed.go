package sfntest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// callRaw calls the API for an S3 access of a distributed Map and returns
// the raw result. States errors returned by the mock are reported as failName.
func (r *runner) callRaw(resource string, params any, ev *Event, failName string) ([]byte, error) {
	pb := mustEncodeJSON(params)
	att := Attempt{Resource: resource, Params: pb}

	if r.env.api == nil {
		ev.Attempts = append(ev.Attempts, att)
		return nil, fatal(fmt.Errorf("state %s called %s but Env.API is not set", ev.State, resource))
	}

	rb, err := r.env.api.Call(r.ctx, resource, pb)
	if err != nil {
		if se, ok := asStatesError(err); ok {
			fe := statesErrorf(failName, "%s: %s", se.Name, se.Cause)
			att.Error, att.Cause = fe.Name, fe.Cause
			ev.Attempts = append(ev.Attempts, att)
			return nil, fe
		}
		ev.Attempts = append(ev.Attempts, att)
		return nil, fatal(fmt.Errorf("state %s: API call %s: %w", ev.State, resource, err))
	}

	if json.Valid(rb) {
		att.Result = rb
	}
	ev.Attempts = append(ev.Attempts, att)
	return rb, nil
}

// dmap evaluates the fields of a distributed Map state in either query language.
type dmap struct {
	r     *runner
	s     *state
	input any // state input
	eff   any // effective input (JSONPath: after InputPath)
	sc    *scope
	stc   stateCtx
}

func (d *dmap) jsonata() bool { return d.s.query == queryJSONata }

func (d *dmap) jsonataVars(stc stateCtx) (map[string]any, error) {
	return d.r.jsonataVars(d.sc, jsonataStates{input: d.input, context: d.r.contextObject(stc)})
}

// template evaluates a payload template (JSONPath) or a JSONata template.
func (d *dmap) template(t any, stc stateCtx) (any, error) {
	if t == nil {
		return nil, nil
	}
	if d.jsonata() {
		vars, err := d.jsonataVars(stc)
		if err != nil {
			return nil, err
		}
		return d.r.evalJSONata(t, vars)
	}
	return d.r.evalTemplate(t, d.r.penv(d.eff, d.sc, d.stc.with(stc)))
}

// integer evaluates a number field that may be given as a JSONPath path
// (fieldPath) or a JSONata expression. set is false when neither is given.
func (d *dmap) integer(field string, v any, path optString) (n int64, set bool, err error) {
	switch {
	case path.set:
		val, err := d.r.evalExpr(path.v, d.r.penv(d.eff, d.sc, d.stc), ErrorRuntime)
		if err != nil {
			return 0, false, err
		}
		n, ok := toNonNegInt(val)
		if !ok {
			return 0, false, statesErrorf(ErrorRuntime, "%sPath %s: must be a non-negative integer", field, path.v)
		}
		return n, true, nil
	case v == nil:
		return 0, false, nil
	}

	val, err := d.template(v, d.stc)
	if err != nil {
		return 0, false, err
	}
	n, ok := toNonNegInt(val)
	if !ok {
		f, isNum := toFloat(val)
		if !isNum || f < 0 {
			return 0, false, queryError("%s must be a non-negative integer", field)
		}
		n = int64(math.Floor(f))
	}
	return n, true, nil
}

func (stc stateCtx) with(o stateCtx) stateCtx {
	if o.mapItem != nil {
		stc.mapItem = o.mapItem
	}
	return stc
}

func (r *runner) runDistributedMap(s *state, input any, sc *scope, stc stateCtx, ev *Event) (any, error) {
	d := &dmap{r: r, s: s, input: input, eff: input, sc: sc, stc: stc}
	if !d.jsonata() {
		var err error
		if d.eff, err = r.inputPath(s, input, sc, stc); err != nil {
			return nil, err
		}
	}

	items, err := d.readItems(ev)
	if err != nil {
		return nil, err
	}
	inputs, err := d.childInputs(items)
	if err != nil {
		return nil, err
	}

	// Child workflow executions. They cannot see the variables of the parent.
	label := s.Label
	if label == "" {
		label = s.name
	}
	mapRunID := newUUID()
	childType := Standard
	if s.processor().ProcessorConfig.ExecutionType == "EXPRESS" {
		childType = Express
	}

	ev.Branches = nil
	children := make([]childResult, len(inputs))
	failed := 0
	for i, in := range inputs {
		c, err := r.runChild(s.processor(), in, label, childType, ev)
		if err != nil {
			return nil, err
		}
		children[i] = c
		if c.err != nil {
			failed++
		}
	}

	if err := d.checkTolerance(failed, len(children)); err != nil {
		return nil, err
	}

	result, err := d.results(children, label, mapRunID, ev)
	if err != nil {
		return nil, err
	}

	if d.jsonata() {
		return r.finishResultJSONata(s, input, result, sc, stc, ev)
	}
	return r.finishResult(s, input, result, sc, stc, ev)
}

func (d *dmap) readItems(ev *Event) ([]readItem, error) {
	s := d.s
	var items []readItem

	if s.ItemReader != nil {
		pt := s.ItemReader.Parameters
		if d.jsonata() {
			pt = s.ItemReader.Arguments
		}
		params, err := d.template(pt, d.stc)
		if err != nil {
			return nil, err
		}
		if items, err = d.r.readItems(s.ItemReader, params, ev); err != nil {
			return nil, err
		}
	} else {
		src := d.eff
		if d.jsonata() && s.Items != nil {
			var err error
			if src, err = d.template(s.Items, d.stc); err != nil {
				return nil, err
			}
		} else if s.ItemsPath.set && !s.ItemsPath.null {
			var err error
			if src, err = d.r.evalExpr(s.ItemsPath.v, d.r.penv(d.eff, d.sc, d.stc), ErrorRuntime); err != nil {
				return nil, err
			}
		}
		switch t := src.(type) {
		case []any:
			for _, v := range t {
				items = append(items, readItem{value: v, source: "STATE_DATA"})
			}
		case map[string]any:
			for _, k := range sortedKeys(t) {
				items = append(items, readItem{value: t[k], key: k, hasKey: true, source: "STATE_DATA"})
			}
		default:
			if d.jsonata() {
				return nil, queryError("Items must be an array or an object, got %s", typeName(src))
			}
			return nil, statesErrorf(ErrorRuntime, "ItemsPath must select an array or an object, got %s", typeName(src))
		}
	}

	if c := s.ItemReader; c != nil && c.ReaderConfig != nil {
		maxItems, set, err := d.integer("MaxItems", c.ReaderConfig.MaxItems, c.ReaderConfig.MaxItemsPath)
		if err != nil {
			return nil, err
		}
		if set && maxItems > 0 && int64(len(items)) > maxItems {
			items = items[:maxItems]
		}
	}
	return items, nil
}

// childInputs applies ItemSelector and ItemBatcher.
func (d *dmap) childInputs(items []readItem) ([]any, error) {
	s := d.s
	selected := make([]any, len(items))
	for i, it := range items {
		v := deepCopy(it.value)
		if s.ItemSelector != nil {
			ictx := d.stc
			ictx.mapItem = &mapItem{index: i, value: it.value, key: it.key, hasKey: it.hasKey, source: it.source}
			var err error
			if v, err = d.template(s.ItemSelector, ictx); err != nil {
				return nil, err
			}
		}
		selected[i] = v
	}

	b := s.ItemBatcher
	if b == nil {
		return selected, nil
	}

	maxItems, _, err := d.integer("MaxItemsPerBatch", b.MaxItemsPerBatch, b.MaxItemsPerBatchPath)
	if err != nil {
		return nil, err
	}
	maxBytes, _, err := d.integer("MaxInputBytesPerBatch", b.MaxInputBytesPerBatch, b.MaxInputBytesPerBatchPath)
	if err != nil {
		return nil, err
	}
	batchInput, err := d.template(b.BatchInput, d.stc)
	if err != nil {
		return nil, err
	}

	newBatch := func(items []any) map[string]any {
		m := map[string]any{"Items": items}
		if batchInput != nil {
			m["BatchInput"] = deepCopy(batchInput)
		}
		return m
	}
	size := func(items []any) int64 { return int64(len(mustEncodeJSON(newBatch(items)))) }

	var batches []any
	var cur []any
	for _, v := range selected {
		next := append(append([]any{}, cur...), v)
		full := (maxItems > 0 && int64(len(next)) > maxItems) || (maxBytes > 0 && size(next) > maxBytes)
		if full && len(cur) > 0 {
			batches = append(batches, newBatch(cur))
			next = []any{v}
		}
		cur = next
	}
	if len(cur) > 0 {
		batches = append(batches, newBatch(cur))
	}
	return batches, nil
}

type childResult struct {
	arn       string
	name      string
	input     any
	output    any
	err       *StatesError
	startTime string
	stopTime  string
	smArn     string
}

func (r *runner) runChild(m *machine, input any, label string, typ WorkflowType, ev *Event) (childResult, error) {
	exec := r.exec
	exec.StateMachineArn = r.exec.StateMachineArn + "/" + label
	exec.Name = newUUID()
	exec.StartTime = r.env.clock.Now()
	exec.RedriveCount = 0
	exec.RedriveTime = time.Time{}

	child := &runner{
		ctx:     r.ctx,
		sm:      r.sm,
		env:     r.env,
		exec:    exec,
		typ:     typ,
		input:   input,
		jsonata: r.jsonata,
	}

	var events []Event
	out, err := child.runMachine(m, deepCopy(input), newScope(nil), &events)
	ev.Branches = append(ev.Branches, events)

	c := childResult{
		arn:       child.executionID(),
		name:      exec.Name,
		input:     input,
		startTime: formatTimestamp(exec.StartTime),
		stopTime:  formatTimestamp(r.env.clock.Now()),
		smArn:     exec.StateMachineArn,
	}
	if err != nil {
		se, ok := asStatesError(err)
		if !ok || isFatal(err) {
			return c, err
		}
		c.err = se
		return c, nil
	}
	c.output = out
	return c, nil
}

func (d *dmap) checkTolerance(failed, total int) error {
	if failed == 0 {
		return nil
	}
	s := d.s
	count, countSet, err := d.integer("ToleratedFailureCount", s.ToleratedFailureCount, s.ToleratedFailureCountPath)
	if err != nil {
		return err
	}
	pct, pctSet, err := d.integer("ToleratedFailurePercentage", s.ToleratedFailurePercentage, s.ToleratedFailurePercentagePath)
	if err != nil {
		return err
	}

	exceeded := !countSet && !pctSet
	if countSet && int64(failed) > count {
		exceeded = true
	}
	if pctSet && float64(failed)*100/float64(total) > float64(pct) {
		exceeded = true
	}
	if exceeded {
		return statesErrorf(ErrorExceedToleratedFailureThreshold, "The specified tolerated failure threshold was exceeded: %d of %d items failed", failed, total)
	}
	return nil
}

func (c childResult) metadata() map[string]any {
	m := map[string]any{
		"ExecutionArn":        c.arn,
		"Input":               string(mustEncodeJSON(c.input)),
		"InputDetails":        map[string]any{"Included": true},
		"Name":                c.name,
		"RedriveCount":        mustNormalize(0),
		"RedriveStatus":       "NOT_REDRIVABLE",
		"RedriveStatusReason": "Execution is SUCCEEDED and cannot be redriven",
		"StartDate":           c.startTime,
		"StateMachineArn":     c.smArn,
		"Status":              "SUCCEEDED",
		"StopDate":            c.stopTime,
	}
	if c.err != nil {
		m["Status"] = "FAILED"
		m["Error"] = c.err.Name
		m["Cause"] = c.err.Cause
		m["RedriveStatus"] = "REDRIVABLE"
		delete(m, "RedriveStatusReason")
	} else {
		m["Output"] = string(mustEncodeJSON(c.output))
		m["OutputDetails"] = map[string]any{"Included": true}
	}
	return m
}

// results builds the Map result and writes it with the ResultWriter.
func (d *dmap) results(children []childResult, label, mapRunID string, ev *Event) (any, error) {
	w := d.s.ResultWriter
	transformation := "COMPACT"
	outputType := "JSON"
	if w != nil && w.Resource != "" {
		transformation = "NONE"
	}
	if w != nil && w.WriterConfig != nil {
		if w.WriterConfig.Transformation != "" {
			transformation = w.WriterConfig.Transformation
		}
		if w.WriterConfig.OutputType != "" {
			outputType = w.WriterConfig.OutputType
		}
	}

	var succeeded, failed []any
	for _, c := range children {
		if c.err != nil {
			// a failed child's result is returned as with NONE
			if transformation == "NONE" || (w != nil && w.Resource != "") {
				failed = append(failed, c.metadata())
			} else {
				failed = append(failed, map[string]any{"Error": c.err.Name, "Cause": c.err.Cause})
			}
			continue
		}
		switch transformation {
		case "NONE":
			succeeded = append(succeeded, c.metadata())
		case "FLATTEN":
			if a, ok := c.output.([]any); ok {
				succeeded = append(succeeded, a...)
			} else {
				succeeded = append(succeeded, c.output)
			}
		default:
			succeeded = append(succeeded, c.output)
		}
	}

	if w == nil || w.Resource == "" {
		// Keep the item order: failed children appear at their position.
		out := []any{}
		fi, si := 0, 0
		for _, c := range children {
			if c.err != nil {
				out = append(out, failed[fi])
				fi++
				continue
			}
			if transformation == "FLATTEN" {
				if a, ok := c.output.([]any); ok {
					out = append(out, succeeded[si:si+len(a)]...)
					si += len(a)
					continue
				}
			}
			out = append(out, succeeded[si])
			si++
		}
		return out, nil
	}

	pt := w.Parameters
	if d.jsonata() {
		pt = w.Arguments
	}
	pv, err := d.template(pt, d.stc)
	if err != nil {
		return nil, err
	}
	params, _ := pv.(map[string]any)
	bucket, _ := params["Bucket"].(string)
	prefix, _ := params["Prefix"].(string)
	dir := mapRunID + "/"
	if prefix != "" {
		dir = strings.TrimSuffix(prefix, "/") + "/" + dir
	}

	mapRunArn := d.mapRunArn(label, mapRunID)
	files := map[string][]any{"SUCCEEDED": {}, "FAILED": {}, "PENDING": {}}
	write := func(status string, entries []any) error {
		if len(entries) == 0 {
			return nil
		}
		key := fmt.Sprintf("%s%s_0.json", dir, status)
		body := encodeResults(entries, outputType)
		if _, err := d.r.callRaw(resourcePutObject, map[string]any{"Bucket": bucket, "Key": key, "Body": body}, ev, ErrorResultWriterFailed); err != nil {
			return err
		}
		files[status] = append(files[status], map[string]any{"Key": key, "Size": mustNormalize(len(body))})
		return nil
	}
	if err := write("SUCCEEDED", succeeded); err != nil {
		return nil, err
	}
	if err := write("FAILED", failed); err != nil {
		return nil, err
	}

	manifestKey := dir + "manifest.json"
	manifest := map[string]any{
		"DestinationBucket": bucket,
		"MapRunArn":         mapRunArn,
		"ResultFiles":       map[string]any{"SUCCEEDED": files["SUCCEEDED"], "FAILED": files["FAILED"], "PENDING": files["PENDING"]},
	}
	if _, err := d.r.callRaw(resourcePutObject, map[string]any{"Bucket": bucket, "Key": manifestKey, "Body": string(mustEncodeJSON(manifest))}, ev, ErrorResultWriterFailed); err != nil {
		return nil, err
	}

	return map[string]any{
		"MapRunArn":           mapRunArn,
		"ResultWriterDetails": map[string]any{"Bucket": bucket, "Key": manifestKey},
	}, nil
}

func encodeResults(entries []any, outputType string) string {
	if outputType == "JSONL" {
		var b bytes.Buffer
		for _, e := range entries {
			b.Write(mustEncodeJSON(e))
			b.WriteByte('\n')
		}
		return b.String()
	}
	return string(mustEncodeJSON(entries))
}

// mapRunArn returns arn:aws:states:<region>:<account>:mapRun:<name>/<label>:<id>.
func (d *dmap) mapRunArn(label, id string) string {
	return strings.Replace(d.r.exec.StateMachineArn, ":stateMachine:", ":mapRun:", 1) + "/" + label + ":" + id
}
