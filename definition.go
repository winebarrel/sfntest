package sfntest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/recolabs/gnata"
)

const (
	queryJSONPath = "JSONPath"
	queryJSONata  = "JSONata"
)

// optString distinguishes a missing field, an explicit null and a string.
type optString struct {
	set  bool
	null bool
	v    string
}

func (o *optString) UnmarshalJSON(b []byte) error {
	o.set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.null = true
		return nil
	}
	return json.Unmarshal(b, &o.v)
}

type retrier struct {
	ErrorEquals     []string `json:"ErrorEquals"`
	IntervalSeconds *int64   `json:"IntervalSeconds"`
	MaxAttempts     *int64   `json:"MaxAttempts"`
	BackoffRate     *float64 `json:"BackoffRate"`
	MaxDelaySeconds *int64   `json:"MaxDelaySeconds"`
	JitterStrategy  string   `json:"JitterStrategy"`
	Comment         string   `json:"Comment"`
}

type catcher struct {
	ErrorEquals []string       `json:"ErrorEquals"`
	Next        string         `json:"Next"`
	ResultPath  optString      `json:"ResultPath"`
	Assign      map[string]any `json:"Assign"`
	Output      any            `json:"Output"`
	Comment     string         `json:"Comment"`
}

type processorConfig struct {
	Mode          string `json:"Mode"`
	ExecutionType string `json:"ExecutionType"`
}

type itemReader struct {
	Resource     string        `json:"Resource"`
	Parameters   any           `json:"Parameters"`
	Arguments    any           `json:"Arguments"`
	ReaderConfig *readerConfig `json:"ReaderConfig"`
}

type readerConfig struct {
	InputType         string    `json:"InputType"`
	Transformation    string    `json:"Transformation"`
	ManifestType      string    `json:"ManifestType"`
	CSVDelimiter      string    `json:"CSVDelimiter"`
	CSVHeaderLocation string    `json:"CSVHeaderLocation"`
	CSVHeaders        []string  `json:"CSVHeaders"`
	ItemsPointer      string    `json:"ItemsPointer"`
	MaxItems          any       `json:"MaxItems"`
	MaxItemsPath      optString `json:"MaxItemsPath"`
}

type itemBatcher struct {
	MaxItemsPerBatch          any       `json:"MaxItemsPerBatch"`
	MaxItemsPerBatchPath      optString `json:"MaxItemsPerBatchPath"`
	MaxInputBytesPerBatch     any       `json:"MaxInputBytesPerBatch"`
	MaxInputBytesPerBatchPath optString `json:"MaxInputBytesPerBatchPath"`
	BatchInput                any       `json:"BatchInput"`
}

type resultWriter struct {
	Resource     string        `json:"Resource"`
	Parameters   any           `json:"Parameters"`
	Arguments    any           `json:"Arguments"`
	WriterConfig *writerConfig `json:"WriterConfig"`
}

type writerConfig struct {
	OutputType     string `json:"OutputType"`
	Transformation string `json:"Transformation"`
}

const (
	resourceGetObject     = "arn:aws:states:::s3:getObject"
	resourceListObjectsV2 = "arn:aws:states:::s3:listObjectsV2"
	resourcePutObject     = "arn:aws:states:::s3:putObject"
)

type machine struct {
	Comment         string                     `json:"Comment"`
	StartAt         string                     `json:"StartAt"`
	RawStates       map[string]json.RawMessage `json:"States"`
	Version         string                     `json:"Version"`
	TimeoutSeconds  *int64                     `json:"TimeoutSeconds"`
	QueryLanguage   string                     `json:"QueryLanguage"`
	ProcessorConfig *processorConfig           `json:"ProcessorConfig"`

	states map[string]*state
}

type state struct {
	name  string
	query string
	keys  []string

	Type          string `json:"Type"`
	Comment       string `json:"Comment"`
	QueryLanguage string `json:"QueryLanguage"`
	Next          string `json:"Next"`
	End           bool   `json:"End"`

	InputPath      optString      `json:"InputPath"`
	OutputPath     optString      `json:"OutputPath"`
	ResultPath     optString      `json:"ResultPath"`
	Parameters     any            `json:"Parameters"`
	ResultSelector any            `json:"ResultSelector"`
	Result         any            `json:"Result"`
	Assign         map[string]any `json:"Assign"`

	// Task
	Resource             string     `json:"Resource"`
	TimeoutSeconds       any        `json:"TimeoutSeconds"`
	TimeoutSecondsPath   string     `json:"TimeoutSecondsPath"`
	HeartbeatSeconds     any        `json:"HeartbeatSeconds"`
	HeartbeatSecondsPath string     `json:"HeartbeatSecondsPath"`
	Credentials          any        `json:"Credentials"`
	Retry                []*retrier `json:"Retry"`
	Catch                []*catcher `json:"Catch"`

	// Choice
	Choices []map[string]any `json:"Choices"`
	Default string           `json:"Default"`

	// Wait
	Seconds       any       `json:"Seconds"`
	Timestamp     any       `json:"Timestamp"`
	SecondsPath   optString `json:"SecondsPath"`
	TimestampPath optString `json:"TimestampPath"`

	// Fail
	Error     optString `json:"Error"`
	Cause     optString `json:"Cause"`
	ErrorPath optString `json:"ErrorPath"`
	CausePath optString `json:"CausePath"`

	// Parallel
	Branches []*machine `json:"Branches"`

	// Map
	ItemsPath     optString `json:"ItemsPath"`
	ItemSelector  any       `json:"ItemSelector"`
	Iterator      *machine  `json:"Iterator"`
	ItemProcessor *machine  `json:"ItemProcessor"`

	// Distributed Map
	ItemReader                     *itemReader   `json:"ItemReader"`
	ItemBatcher                    *itemBatcher  `json:"ItemBatcher"`
	ResultWriter                   *resultWriter `json:"ResultWriter"`
	ToleratedFailureCount          any           `json:"ToleratedFailureCount"`
	ToleratedFailurePercentage     any           `json:"ToleratedFailurePercentage"`
	ToleratedFailureCountPath      optString     `json:"ToleratedFailureCountPath"`
	ToleratedFailurePercentagePath optString     `json:"ToleratedFailurePercentagePath"`
	Label                          string        `json:"Label"`

	// JSONata
	Arguments any `json:"Arguments"`
	Output    any `json:"Output"`
	Items     any `json:"Items"`
}

func (s *state) has(key string) bool {
	return slices.Contains(s.keys, key)
}

// processor returns the machine run for each Map item.
// distributed reports whether s is a distributed Map state.
func (s *state) distributed() bool {
	p := s.processor()
	return s.Type == "Map" && p != nil && p.ProcessorConfig != nil && p.ProcessorConfig.Mode == "DISTRIBUTED"
}

func (s *state) processor() *machine {
	if s.ItemProcessor != nil {
		return s.ItemProcessor
	}
	return s.Iterator
}

var commonFields = []string{"Type", "Comment", "QueryLanguage"}

var jsonPathFields = map[string][]string{
	"Pass":     {"Next", "End", "InputPath", "OutputPath", "ResultPath", "Parameters", "Result", "Assign"},
	"Task":     {"Next", "End", "InputPath", "OutputPath", "ResultPath", "Parameters", "ResultSelector", "Resource", "Retry", "Catch", "TimeoutSeconds", "TimeoutSecondsPath", "HeartbeatSeconds", "HeartbeatSecondsPath", "Credentials", "Assign"},
	"Choice":   {"InputPath", "OutputPath", "Choices", "Default", "Assign"},
	"Wait":     {"Next", "End", "InputPath", "OutputPath", "Seconds", "Timestamp", "SecondsPath", "TimestampPath", "Assign"},
	"Succeed":  {"InputPath", "OutputPath"},
	"Fail":     {"Error", "Cause", "ErrorPath", "CausePath"},
	"Parallel": {"Next", "End", "InputPath", "OutputPath", "ResultPath", "Parameters", "ResultSelector", "Branches", "Retry", "Catch", "Assign"},
	"Map":      {"Next", "End", "InputPath", "OutputPath", "ResultPath", "Parameters", "ResultSelector", "ItemsPath", "ItemSelector", "Iterator", "ItemProcessor", "MaxConcurrency", "MaxConcurrencyPath", "Retry", "Catch", "Assign", "ItemReader", "ItemBatcher", "ResultWriter", "ToleratedFailureCount", "ToleratedFailurePercentage", "ToleratedFailureCountPath", "ToleratedFailurePercentagePath", "Label"},
}

var jsonataFields = map[string][]string{
	"Pass":     {"Next", "End", "Output", "Assign"},
	"Task":     {"Next", "End", "Arguments", "Output", "Assign", "Resource", "Retry", "Catch", "TimeoutSeconds", "HeartbeatSeconds", "Credentials"},
	"Choice":   {"Choices", "Default", "Output", "Assign"},
	"Wait":     {"Next", "End", "Seconds", "Timestamp", "Output", "Assign"},
	"Succeed":  {"Output"},
	"Fail":     {"Error", "Cause"},
	"Parallel": {"Next", "End", "Arguments", "Output", "Assign", "Branches", "Retry", "Catch"},
	"Map":      {"Next", "End", "Items", "ItemSelector", "Iterator", "ItemProcessor", "MaxConcurrency", "Output", "Assign", "Retry", "Catch", "ItemReader", "ItemBatcher", "ResultWriter", "ToleratedFailureCount", "ToleratedFailurePercentage", "Label"},
}

func decodeStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(v)
}

type validator struct {
	typ   WorkflowType
	errs  []string
	exprs map[string]*gnata.Expression
}

func (v *validator) errorf(format string, args ...any) {
	v.errs = append(v.errs, fmt.Sprintf(format, args...))
}

func parseMachine(def []byte, typ WorkflowType) (*machine, map[string]*gnata.Expression, error) {
	m := &machine{}
	if err := decodeStrict(def, m); err != nil {
		return nil, nil, fmt.Errorf("invalid state machine definition: %w", err)
	}

	v := &validator{typ: typ, exprs: map[string]*gnata.Expression{}}
	query := queryJSONPath
	if m.QueryLanguage != "" {
		query = m.QueryLanguage
	}
	if query != queryJSONPath && query != queryJSONata {
		v.errorf("QueryLanguage: unknown query language %q", query)
	}
	v.machine(m, "", query, true)

	if len(v.errs) > 0 {
		return nil, nil, fmt.Errorf("invalid state machine definition:\n  %s", strings.Join(v.errs, "\n  "))
	}
	return m, v.exprs, nil
}

func (v *validator) machine(m *machine, prefix, query string, top bool) {
	if m.StartAt == "" {
		v.errorf("%sStartAt: required", prefix)
	}
	if len(m.RawStates) == 0 {
		v.errorf("%sStates: required", prefix)
	}

	m.states = map[string]*state{}
	for _, name := range sortedRawKeys(m.RawStates) {
		raw := m.RawStates[name]
		path := prefix + "States." + name

		if utf8.RuneCountInString(name) > 80 {
			v.errorf("%s: state name exceeds 80 characters", path)
		}

		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			v.errorf("%s: %s", path, err)
			continue
		}

		s := &state{name: name}
		for k := range keys {
			s.keys = append(s.keys, k)
		}
		if err := decodeStrict(raw, s); err != nil {
			v.errorf("%s: %s", path, err)
			continue
		}

		s.query = query
		if s.QueryLanguage != "" {
			if query == queryJSONata && s.QueryLanguage == queryJSONPath {
				v.errorf("%s: a JSONPath state cannot be used in a JSONata state machine", path)
			}
			s.query = s.QueryLanguage
		}

		m.states[name] = s
		v.state(s, path)
	}

	if _, ok := m.states[m.StartAt]; m.StartAt != "" && !ok {
		v.errorf("%sStartAt: state %q does not exist", prefix, m.StartAt)
	}

	for _, s := range m.states {
		path := prefix + "States." + s.name
		check := func(field, target string) {
			if target == "" {
				return
			}
			if _, ok := m.states[target]; !ok {
				v.errorf("%s.%s: state %q does not exist", path, field, target)
			}
		}
		check("Next", s.Next)
		check("Default", s.Default)
		for i, c := range s.Catch {
			check(fmt.Sprintf("Catch[%d].Next", i), c.Next)
		}
		for i, rule := range s.Choices {
			if next, ok := rule["Next"].(string); ok {
				check(fmt.Sprintf("Choices[%d].Next", i), next)
			}
		}
	}
}

func sortedRawKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (v *validator) state(s *state, path string) {
	fields := jsonPathFields
	if s.query == queryJSONata {
		fields = jsonataFields
	}
	allowed, ok := fields[s.Type]
	if !ok {
		v.errorf("%s.Type: unknown state type %q", path, s.Type)
		return
	}
	for _, k := range s.keys {
		if !slices.Contains(commonFields, k) && !slices.Contains(allowed, k) {
			v.errorf("%s: field %q is not allowed in a %s %s state", path, k, s.query, s.Type)
		}
	}

	// transitions
	switch s.Type {
	case "Pass", "Task", "Wait", "Parallel", "Map":
		if s.Next == "" && !s.End {
			v.errorf("%s: either Next or End is required", path)
		}
		if s.Next != "" && s.End {
			v.errorf("%s: Next and End cannot be used together", path)
		}
	}

	if s.query == queryJSONata {
		v.jsonataState(s, path)
	} else {
		v.jsonPathState(s, path)
	}
	v.retryCatch(path, s)

	switch s.Type {
	case "Task":
		if s.Resource == "" {
			v.errorf("%s.Resource: required", path)
		}
		if v.typ == Express {
			r := s.Resource
			if strings.HasSuffix(r, ".sync") || strings.HasSuffix(r, ".sync:2") || strings.HasSuffix(r, ".waitForTaskToken") {
				v.errorf("%s.Resource: %s is not supported in Express workflows", path, r)
			}
			if strings.Contains(r, ":activity:") {
				v.errorf("%s.Resource: activities are not supported in Express workflows", path)
			}
		}
	case "Choice":
		if len(s.Choices) == 0 {
			v.errorf("%s.Choices: required", path)
		}
		for i, rule := range s.Choices {
			p := fmt.Sprintf("%s.Choices[%d]", path, i)
			if s.query == queryJSONata {
				v.jsonataChoiceRule(p, rule)
			} else {
				v.choiceRule(p, rule, true)
			}
		}
	case "Parallel":
		if len(s.Branches) == 0 {
			v.errorf("%s.Branches: required", path)
		}
		for i, b := range s.Branches {
			v.machine(b, fmt.Sprintf("%s.Branches[%d].", path, i), s.query, false)
		}
	case "Map":
		p := s.processor()
		if p == nil {
			v.errorf("%s.ItemProcessor: required", path)
			return
		}
		if s.ItemProcessor != nil && s.Iterator != nil {
			v.errorf("%s: ItemProcessor and Iterator cannot be used together", path)
		}
		if s.distributed() {
			v.distributedMap(s, p, path)
			return
		}
		for _, k := range []string{"ItemReader", "ItemBatcher", "ResultWriter", "ToleratedFailureCount", "ToleratedFailurePercentage", "ToleratedFailureCountPath", "ToleratedFailurePercentagePath", "Label"} {
			if s.has(k) {
				v.errorf("%s: field %q is only allowed in a distributed Map", path, k)
			}
		}
		v.machine(p, path+".ItemProcessor.", s.query, false)
	}
}

func (v *validator) jsonPathState(s *state, path string) {
	v.pathField(path+".InputPath", s.InputPath, false)
	v.pathField(path+".OutputPath", s.OutputPath, false)
	v.resultPath(path+".ResultPath", s.ResultPath)
	v.template(path+".Parameters", s.Parameters)
	v.template(path+".ResultSelector", s.ResultSelector)
	v.assign(path+".Assign", s.Assign)

	switch s.Type {
	case "Wait":
		n := 0
		if s.Seconds != nil {
			n++
			if _, ok := toNonNegInt(s.Seconds); !ok {
				v.errorf("%s.Seconds: must be a non-negative integer", path)
			}
		}
		if s.Timestamp != nil {
			n++
			if ts, ok := s.Timestamp.(string); !ok || !isTimestamp(ts) {
				v.errorf("%s.Timestamp: must be a timestamp", path)
			}
		}
		if s.SecondsPath.set {
			n++
			v.pathField(path+".SecondsPath", s.SecondsPath, true)
		}
		if s.TimestampPath.set {
			n++
			v.pathField(path+".TimestampPath", s.TimestampPath, true)
		}
		if n != 1 {
			v.errorf("%s: exactly one of Seconds, Timestamp, SecondsPath and TimestampPath is required", path)
		}
	case "Fail":
		if s.Error.set && s.ErrorPath.set {
			v.errorf("%s: Error and ErrorPath cannot be used together", path)
		}
		if s.Cause.set && s.CausePath.set {
			v.errorf("%s: Cause and CausePath cannot be used together", path)
		}
		v.pathOrIntrinsic(path+".ErrorPath", s.ErrorPath)
		v.pathOrIntrinsic(path+".CausePath", s.CausePath)
	case "Map":
		if s.ItemSelector != nil && s.Parameters != nil {
			v.errorf("%s: ItemSelector and Parameters cannot be used together", path)
		}
		v.pathField(path+".ItemsPath", s.ItemsPath, false)
		v.template(path+".ItemSelector", s.ItemSelector)
	}
}

func (v *validator) pathField(path string, o optString, required bool) {
	if !o.set {
		return
	}
	if o.null {
		if required {
			v.errorf("%s: must not be null", path)
		}
		return
	}
	if _, err := parsePath(o.v); err != nil {
		v.errorf("%s: %s", path, err)
	}
}

func (v *validator) pathOrIntrinsic(path string, o optString) {
	if !o.set {
		return
	}
	if o.null {
		v.errorf("%s: must not be null", path)
		return
	}
	if isPath(o.v) {
		if _, err := parsePath(o.v); err != nil {
			v.errorf("%s: %s", path, err)
		}
		return
	}
	if _, err := parseIntrinsic(o.v); err != nil {
		v.errorf("%s: %s", path, err)
	}
}

func (v *validator) resultPath(path string, o optString) {
	if !o.set || o.null {
		return
	}
	p, err := parsePath(o.v)
	if err != nil {
		v.errorf("%s: %s", path, err)
		return
	}
	if p.root != rootInput || !p.isReference() {
		v.errorf("%s: must be a reference path starting with '$'", path)
	}
}

func (v *validator) template(path string, t any) {
	switch t := t.(type) {
	case map[string]any:
		seen := map[string]bool{}
		for _, k := range sortedKeys(t) {
			name := strings.TrimSuffix(k, ".$")
			if seen[name] {
				v.errorf("%s: duplicate field %q", path, name)
			}
			seen[name] = true

			if strings.HasSuffix(k, ".$") {
				s, ok := t[k].(string)
				if !ok {
					v.errorf("%s.%s: must be a string", path, k)
					continue
				}
				v.pathOrIntrinsic(path+"."+k, optString{set: true, v: s})
				continue
			}
			v.template(path+"."+k, t[k])
		}
	case []any:
		for i, e := range t {
			v.template(fmt.Sprintf("%s[%d]", path, i), e)
		}
	}
}

func (v *validator) assign(path string, a map[string]any) {
	for k := range a {
		name := strings.TrimSuffix(k, ".$")
		if !validVarName(name) {
			v.errorf("%s: invalid variable name %q", path, name)
		}
	}
	v.template(path, map[string]any(a))
}

func (v *validator) retryCatch(path string, s *state) {
	checkErrors := func(p string, errs []string, last bool) {
		if len(errs) == 0 {
			v.errorf("%s.ErrorEquals: required", p)
		}
		if slices.Contains(errs, ErrorAll) {
			if len(errs) != 1 {
				v.errorf("%s.ErrorEquals: States.ALL must appear alone", p)
			}
			if !last {
				v.errorf("%s.ErrorEquals: States.ALL must be in the last element", p)
			}
		}
	}

	for i, r := range s.Retry {
		p := fmt.Sprintf("%s.Retry[%d]", path, i)
		checkErrors(p, r.ErrorEquals, i == len(s.Retry)-1)
		if r.IntervalSeconds != nil && *r.IntervalSeconds < 1 {
			v.errorf("%s.IntervalSeconds: must be a positive integer", p)
		}
		if r.MaxAttempts != nil && *r.MaxAttempts < 0 {
			v.errorf("%s.MaxAttempts: must be a non-negative integer", p)
		}
		if r.BackoffRate != nil && *r.BackoffRate < 1 {
			v.errorf("%s.BackoffRate: must be at least 1.0", p)
		}
		if r.MaxDelaySeconds != nil && *r.MaxDelaySeconds < 1 {
			v.errorf("%s.MaxDelaySeconds: must be a positive integer", p)
		}
		if r.JitterStrategy != "" && r.JitterStrategy != "FULL" && r.JitterStrategy != "NONE" {
			v.errorf("%s.JitterStrategy: must be FULL or NONE", p)
		}
	}

	for i, c := range s.Catch {
		p := fmt.Sprintf("%s.Catch[%d]", path, i)
		checkErrors(p, c.ErrorEquals, i == len(s.Catch)-1)
		if c.Next == "" {
			v.errorf("%s.Next: required", p)
		}
		if s.query == queryJSONata {
			if c.ResultPath.set {
				v.errorf("%s: field \"ResultPath\" is not allowed in a JSONata state", p)
			}
			v.jsonataAssign(p+".Assign", c.Assign)
			v.jsonata(p+".Output", c.Output)
		} else {
			if c.Output != nil {
				v.errorf("%s: field \"Output\" is not allowed in a JSONPath state", p)
			}
			v.resultPath(p+".ResultPath", c.ResultPath)
			v.assign(p+".Assign", c.Assign)
		}
	}
}

var choiceOperators = []string{
	"StringEquals", "StringEqualsPath", "StringLessThan", "StringLessThanPath",
	"StringGreaterThan", "StringGreaterThanPath", "StringLessThanEquals", "StringLessThanEqualsPath",
	"StringGreaterThanEquals", "StringGreaterThanEqualsPath", "StringMatches",
	"NumericEquals", "NumericEqualsPath", "NumericLessThan", "NumericLessThanPath",
	"NumericGreaterThan", "NumericGreaterThanPath", "NumericLessThanEquals", "NumericLessThanEqualsPath",
	"NumericGreaterThanEquals", "NumericGreaterThanEqualsPath",
	"BooleanEquals", "BooleanEqualsPath",
	"TimestampEquals", "TimestampEqualsPath", "TimestampLessThan", "TimestampLessThanPath",
	"TimestampGreaterThan", "TimestampGreaterThanPath", "TimestampLessThanEquals", "TimestampLessThanEqualsPath",
	"TimestampGreaterThanEquals", "TimestampGreaterThanEqualsPath",
	"IsNull", "IsPresent", "IsNumeric", "IsString", "IsBoolean", "IsTimestamp",
}

func (v *validator) choiceRule(path string, rule map[string]any, top bool) {
	_, hasNext := rule["Next"]
	if top && !hasNext {
		v.errorf("%s.Next: required", path)
	}
	if !top && hasNext {
		v.errorf("%s.Next: not allowed in a nested rule", path)
	}
	if !top {
		if _, ok := rule["Assign"]; ok {
			v.errorf("%s.Assign: not allowed in a nested rule", path)
		}
	}
	if a, ok := rule["Assign"].(map[string]any); ok {
		v.assign(path+".Assign", a)
	}

	var ops []string
	for k := range rule {
		switch k {
		case "Next", "Comment", "Assign", "Variable":
		case "And", "Or", "Not":
			ops = append(ops, k)
		default:
			if !slices.Contains(choiceOperators, k) {
				v.errorf("%s: unknown field %q", path, k)
				continue
			}
			ops = append(ops, k)
		}
	}
	if len(ops) != 1 {
		v.errorf("%s: exactly one comparison operator is required", path)
		return
	}

	op := ops[0]
	switch op {
	case "And", "Or":
		rules, ok := rule[op].([]any)
		if !ok || len(rules) == 0 {
			v.errorf("%s.%s: must be a non-empty array", path, op)
			return
		}
		for i, r := range rules {
			m, ok := r.(map[string]any)
			if !ok {
				v.errorf("%s.%s[%d]: must be an object", path, op, i)
				continue
			}
			v.choiceRule(fmt.Sprintf("%s.%s[%d]", path, op, i), m, false)
		}
		return
	case "Not":
		m, ok := rule[op].(map[string]any)
		if !ok {
			v.errorf("%s.Not: must be an object", path)
			return
		}
		v.choiceRule(path+".Not", m, false)
		return
	}

	vs, ok := rule["Variable"].(string)
	if !ok {
		v.errorf("%s.Variable: required", path)
	} else if _, err := parsePath(vs); err != nil {
		v.errorf("%s.Variable: %s", path, err)
	}

	val := rule[op]
	switch {
	case strings.HasSuffix(op, "Path"):
		if s, ok := val.(string); !ok {
			v.errorf("%s.%s: must be a path", path, op)
		} else if _, err := parsePath(s); err != nil {
			v.errorf("%s.%s: %s", path, op, err)
		}
	case strings.HasPrefix(op, "String"):
		if _, ok := val.(string); !ok {
			v.errorf("%s.%s: must be a string", path, op)
		}
	case strings.HasPrefix(op, "Numeric"):
		if _, ok := val.(json.Number); !ok {
			v.errorf("%s.%s: must be a number", path, op)
		}
	case strings.HasPrefix(op, "Timestamp"):
		if s, ok := val.(string); !ok || !isTimestamp(s) {
			v.errorf("%s.%s: must be a timestamp", path, op)
		}
	default: // BooleanEquals, Is*
		if _, ok := val.(bool); !ok {
			v.errorf("%s.%s: must be a boolean", path, op)
		}
	}
}

func toNonNegInt(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	if err != nil || i < 0 {
		return 0, false
	}
	return i, true
}

// jsonata compiles every "{% ... %}" string at any depth of t.
func (v *validator) jsonata(path string, t any) {
	switch t := t.(type) {
	case string:
		open, closed := strings.HasPrefix(t, "{%"), strings.HasSuffix(t, "%}")
		if !open && !closed {
			return
		}
		if !isJSONataExpr(t) {
			v.errorf("%s: a JSONata expression must start with '{%%' and end with '%%}'", path)
			return
		}
		if _, ok := v.exprs[t]; ok {
			return
		}
		e, err := compileJSONata(jsonataSource(t))
		if err != nil {
			v.errorf("%s: %s", path, err)
			return
		}
		v.exprs[t] = e
	case map[string]any:
		for _, k := range sortedKeys(t) {
			v.jsonata(path+"."+k, t[k])
		}
	case []any:
		for i, e := range t {
			v.jsonata(fmt.Sprintf("%s[%d]", path, i), e)
		}
	}
}

func (v *validator) jsonataAssign(path string, a map[string]any) {
	for k := range a {
		if !validVarName(k) {
			v.errorf("%s: invalid variable name %q", path, k)
		}
	}
	v.jsonata(path, map[string]any(a))
}

func (v *validator) jsonataState(s *state, path string) {
	v.jsonata(path+".Arguments", s.Arguments)
	v.jsonata(path+".Output", s.Output)
	v.jsonataAssign(path+".Assign", s.Assign)
	v.jsonata(path+".Items", s.Items)
	v.jsonata(path+".ItemSelector", s.ItemSelector)
	v.jsonata(path+".TimeoutSeconds", s.TimeoutSeconds)
	v.jsonata(path+".HeartbeatSeconds", s.HeartbeatSeconds)

	switch s.Type {
	case "Wait":
		if (s.Seconds == nil) == (s.Timestamp == nil) {
			v.errorf("%s: exactly one of Seconds and Timestamp is required", path)
		}
		if str, ok := s.Seconds.(string); ok {
			v.jsonata(path+".Seconds", str)
		} else if s.Seconds != nil {
			if _, ok := toNonNegInt(s.Seconds); !ok {
				v.errorf("%s.Seconds: must be a non-negative integer", path)
			}
		}
		if str, ok := s.Timestamp.(string); ok {
			if isJSONataExpr(str) {
				v.jsonata(path+".Timestamp", str)
			} else if !isTimestamp(str) {
				v.errorf("%s.Timestamp: must be a timestamp", path)
			}
		} else if s.Timestamp != nil {
			v.errorf("%s.Timestamp: must be a timestamp", path)
		}
	case "Fail":
		v.jsonata(path+".Error", s.Error.v)
		v.jsonata(path+".Cause", s.Cause.v)
	}
}

func (v *validator) jsonataChoiceRule(path string, rule map[string]any) {
	for k := range rule {
		switch k {
		case "Condition", "Next", "Assign", "Comment":
		default:
			v.errorf("%s: field %q is not allowed in a JSONata Choice rule", path, k)
		}
	}
	if _, ok := rule["Next"].(string); !ok {
		v.errorf("%s.Next: required", path)
	}
	switch c := rule["Condition"].(type) {
	case bool:
	case string:
		if !isJSONataExpr(c) {
			v.errorf("%s.Condition: must be a JSONata expression or a boolean", path)
		}
		v.jsonata(path+".Condition", c)
	default:
		v.errorf("%s.Condition: must be a JSONata expression or a boolean", path)
	}
	if a, ok := rule["Assign"].(map[string]any); ok {
		v.jsonataAssign(path+".Assign", a)
	}
}

func (v *validator) distributedMap(s *state, p *machine, path string) {
	if v.typ == Express {
		v.errorf("%s: distributed Map is not supported in Express workflows", path)
	}

	typ := Standard
	switch p.ProcessorConfig.ExecutionType {
	case "STANDARD":
	case "EXPRESS":
		typ = Express
	default:
		v.errorf("%s.ItemProcessor.ProcessorConfig.ExecutionType: must be STANDARD or EXPRESS", path)
	}

	if n := utf8.RuneCountInString(s.Label); n > 40 || strings.ContainsAny(s.Label, " \t\n?*<>{}[]:;,\\|^~$#%&`\"") {
		v.errorf("%s.Label: must be at most 40 characters without whitespace or special characters", path)
	}

	jsonata := s.query == queryJSONata
	expr := func(field string, val any) {
		if jsonata {
			v.jsonata(path+"."+field, val)
		} else {
			v.template(path+"."+field, val)
		}
	}
	number := func(field string, val any, p optString) {
		if val != nil && p.set {
			v.errorf("%s: %s and %sPath cannot be used together", path, field, field)
		}
		if str, ok := val.(string); ok && jsonata {
			v.jsonata(path+"."+field, str)
		} else if val != nil {
			if _, ok := toNonNegInt(val); !ok {
				v.errorf("%s.%s: must be a non-negative integer", path, field)
			}
		}
		if p.set && jsonata {
			v.errorf("%s: field %q is not allowed in a JSONata state", path, field+"Path")
		}
		v.pathField(path+"."+field+"Path", p, true)
	}

	number("ToleratedFailureCount", s.ToleratedFailureCount, s.ToleratedFailureCountPath)
	number("ToleratedFailurePercentage", s.ToleratedFailurePercentage, s.ToleratedFailurePercentagePath)
	if n, ok := toNonNegInt(s.ToleratedFailurePercentage); ok && n > 100 {
		v.errorf("%s.ToleratedFailurePercentage: must be between 0 and 100", path)
	}

	if r := s.ItemReader; r != nil {
		rp := path + ".ItemReader"
		if r.Resource != resourceGetObject && r.Resource != resourceListObjectsV2 {
			v.errorf("%s.Resource: must be %s or %s", rp, resourceGetObject, resourceListObjectsV2)
		}
		if jsonata {
			if r.Parameters != nil {
				v.errorf("%s: field \"Parameters\" is not allowed in a JSONata state", rp)
			}
			v.jsonata(rp+".Arguments", r.Arguments)
		} else {
			if r.Arguments != nil {
				v.errorf("%s: field \"Arguments\" is not allowed in a JSONPath state", rp)
			}
			v.template(rp+".Parameters", r.Parameters)
		}

		c := r.ReaderConfig
		if c == nil {
			c = &readerConfig{}
		}
		cp := rp + ".ReaderConfig"
		switch c.InputType {
		case "", "JSON", "JSONL", "CSV", "MANIFEST":
		case "PARQUET":
			v.errorf("%s.InputType: PARQUET is not supported yet", cp)
		default:
			v.errorf("%s.InputType: unknown input type %q", cp, c.InputType)
		}
		switch c.ManifestType {
		case "":
		case "S3_INVENTORY":
			if c.InputType != "" {
				v.errorf("%s: InputType must not be specified with the S3_INVENTORY manifest type", cp)
			}
		case "ATHENA_DATA":
		default:
			v.errorf("%s.ManifestType: unknown manifest type %q", cp, c.ManifestType)
		}
		switch c.Transformation {
		case "", "NONE":
		case "LOAD_AND_FLATTEN":
			if c.InputType == "" {
				v.errorf("%s: InputType is required with LOAD_AND_FLATTEN", cp)
			}
		default:
			v.errorf("%s.Transformation: must be NONE or LOAD_AND_FLATTEN", cp)
		}
		if _, ok := csvDelimiters[c.CSVDelimiter]; !ok {
			v.errorf("%s.CSVDelimiter: unknown delimiter %q", cp, c.CSVDelimiter)
		}
		switch c.CSVHeaderLocation {
		case "", "FIRST_ROW":
		case "GIVEN":
			if len(c.CSVHeaders) == 0 {
				v.errorf("%s.CSVHeaders: required when CSVHeaderLocation is GIVEN", cp)
			}
		default:
			v.errorf("%s.CSVHeaderLocation: must be FIRST_ROW or GIVEN", cp)
		}
		number("ItemReader.ReaderConfig.MaxItems", c.MaxItems, c.MaxItemsPath)
	} else if jsonata {
		v.jsonata(path+".Items", s.Items)
	}

	if b := s.ItemBatcher; b != nil {
		if b.MaxItemsPerBatch == nil && !b.MaxItemsPerBatchPath.set && b.MaxInputBytesPerBatch == nil && !b.MaxInputBytesPerBatchPath.set {
			v.errorf("%s.ItemBatcher: MaxItemsPerBatch or MaxInputBytesPerBatch is required", path)
		}
		number("ItemBatcher.MaxItemsPerBatch", b.MaxItemsPerBatch, b.MaxItemsPerBatchPath)
		number("ItemBatcher.MaxInputBytesPerBatch", b.MaxInputBytesPerBatch, b.MaxInputBytesPerBatchPath)
		expr("ItemBatcher.BatchInput", b.BatchInput)
	}

	if w := s.ResultWriter; w != nil {
		wp := path + ".ResultWriter"
		if w.Resource == "" && w.WriterConfig == nil {
			v.errorf("%s: Resource or WriterConfig is required", wp)
		}
		if w.Resource != "" && w.Resource != resourcePutObject {
			v.errorf("%s.Resource: must be %s", wp, resourcePutObject)
		}
		if jsonata {
			v.jsonata(wp+".Arguments", w.Arguments)
		} else {
			v.template(wp+".Parameters", w.Parameters)
		}
		if c := w.WriterConfig; c != nil {
			switch c.OutputType {
			case "", "JSON", "JSONL":
			default:
				v.errorf("%s.WriterConfig.OutputType: must be JSON or JSONL", wp)
			}
			switch c.Transformation {
			case "", "NONE", "COMPACT", "FLATTEN":
			default:
				v.errorf("%s.WriterConfig.Transformation: must be NONE, COMPACT or FLATTEN", wp)
			}
		}
	}

	parent := v.typ
	v.typ = typ
	v.machine(p, path+".ItemProcessor.", s.query, false)
	v.typ = parent
}
