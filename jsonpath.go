package sfntest

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type pathRoot int

const (
	rootInput   pathRoot = iota // $
	rootContext                 // $$
	rootVar                     // $name
	rootCurrent                 // @ (inside filters)
)

type segKind int

const (
	segName segKind = iota
	segIndex
	segWildcard
	segSlice
	segUnion
	segFilter
)

type segment struct {
	kind      segKind
	recursive bool // preceded by ".."
	name      string
	index     int
	names     []string
	indexes   []int
	start     *int
	end       *int
	step      *int
	filter    filterExpr
}

// jsonPath is a parsed JSONPath expression as used by the JSONPath query language of ASL.
type jsonPath struct {
	src     string
	root    pathRoot
	varName string
	segs    []segment
}

// definite reports whether the path can match at most one node.
// Paths that may match several nodes return their matches as an array.
func (p *jsonPath) definite() bool {
	for _, s := range p.segs {
		if s.recursive || (s.kind != segName && s.kind != segIndex) {
			return false
		}
	}
	return true
}

// isReference reports whether the path is a Reference Path (only names and indexes).
func (p *jsonPath) isReference() bool {
	return p.definite()
}

func isPath(s string) bool {
	return strings.HasPrefix(s, "$")
}

func parsePath(s string) (*jsonPath, error) {
	p := &jsonPath{src: s}
	var rest string

	switch {
	case strings.HasPrefix(s, "$$"):
		p.root = rootContext
		rest = s[2:]
	case strings.HasPrefix(s, "@"):
		p.root = rootCurrent
		rest = s[1:]
	case s == "$" || strings.HasPrefix(s, "$.") || strings.HasPrefix(s, "$["):
		p.root = rootInput
		rest = s[1:]
	case strings.HasPrefix(s, "$"):
		p.root = rootVar
		i := 1
		for i < len(s) && s[i] != '.' && s[i] != '[' {
			i++
		}
		p.varName = s[1:i]
		if !validVarName(p.varName) {
			return nil, fmt.Errorf("invalid variable name in path: %s", s)
		}
		rest = s[i:]
	default:
		return nil, fmt.Errorf("path must start with '$': %s", s)
	}

	for len(rest) > 0 {
		var seg segment
		var err error

		switch {
		case strings.HasPrefix(rest, ".."):
			seg.recursive = true
			rest = rest[2:]
			if strings.HasPrefix(rest, "[") {
				seg, rest, err = parseBracket(rest)
				seg.recursive = true
			} else {
				seg, rest, err = parseDotName(rest)
				seg.recursive = true
			}
		case rest[0] == '.':
			seg, rest, err = parseDotName(rest[1:])
		case rest[0] == '[':
			seg, rest, err = parseBracket(rest)
		default:
			err = fmt.Errorf("unexpected character %q", rest[0])
		}

		if err != nil {
			return nil, fmt.Errorf("invalid path %s: %w", s, err)
		}

		p.segs = append(p.segs, seg)
	}

	return p, nil
}

func validVarName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 0x7f {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func parseDotName(s string) (segment, string, error) {
	if strings.HasPrefix(s, "*") {
		return segment{kind: segWildcard}, s[1:], nil
	}

	var b strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		if c == '.' || c == '[' {
			break
		}
		if c == ']' {
			return segment{}, "", fmt.Errorf("unexpected ']'")
		}
		b.WriteByte(c)
		i++
	}

	if b.Len() == 0 {
		return segment{}, "", fmt.Errorf("empty member name")
	}

	return segment{kind: segName, name: b.String()}, s[i:], nil
}

func parseBracket(s string) (segment, string, error) {
	// s starts with '['
	end, err := findBracketEnd(s)
	if err != nil {
		return segment{}, "", err
	}

	inner := strings.TrimSpace(s[1:end])
	rest := s[end+1:]

	switch {
	case inner == "*":
		return segment{kind: segWildcard}, rest, nil
	case strings.HasPrefix(inner, "?"):
		expr := strings.TrimSpace(inner[1:])
		if !strings.HasPrefix(expr, "(") || !strings.HasSuffix(expr, ")") {
			return segment{}, "", fmt.Errorf("invalid filter: %s", inner)
		}
		f, err := parseFilter(expr[1 : len(expr)-1])
		if err != nil {
			return segment{}, "", err
		}
		return segment{kind: segFilter, filter: f}, rest, nil
	case strings.HasPrefix(inner, "'") || strings.HasPrefix(inner, `"`):
		names, err := splitQuotedNames(inner)
		if err != nil {
			return segment{}, "", err
		}
		if len(names) == 1 {
			return segment{kind: segName, name: names[0]}, rest, nil
		}
		return segment{kind: segUnion, names: names}, rest, nil
	case strings.Contains(inner, ":"):
		parts := strings.Split(inner, ":")
		if len(parts) > 3 {
			return segment{}, "", fmt.Errorf("invalid slice: %s", inner)
		}
		seg := segment{kind: segSlice}
		ptrs := []**int{&seg.start, &seg.end, &seg.step}
		for i, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			n, err := strconv.Atoi(part)
			if err != nil {
				return segment{}, "", fmt.Errorf("invalid slice: %s", inner)
			}
			*ptrs[i] = &n
		}
		return seg, rest, nil
	default:
		parts := strings.Split(inner, ",")
		idx := make([]int, 0, len(parts))
		for _, part := range parts {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				return segment{}, "", fmt.Errorf("invalid index: %s", inner)
			}
			idx = append(idx, n)
		}
		if len(idx) == 1 {
			return segment{kind: segIndex, index: idx[0]}, rest, nil
		}
		return segment{kind: segUnion, indexes: idx}, rest, nil
	}
}

func findBracketEnd(s string) (int, error) {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			switch c {
			case '\\':
				i++
			case quote:
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("unclosed bracket")
}

func splitQuotedNames(s string) ([]string, error) {
	var names []string
	for {
		s = strings.TrimSpace(s)
		if s == "" || (s[0] != '\'' && s[0] != '"') {
			return nil, fmt.Errorf("invalid member name: %s", s)
		}
		q := s[0]
		var b strings.Builder
		i := 1
		for ; i < len(s) && s[i] != q; i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
			}
			b.WriteByte(s[i])
		}
		if i >= len(s) {
			return nil, fmt.Errorf("unclosed quote")
		}
		names = append(names, b.String())
		s = strings.TrimSpace(s[i+1:])
		if s == "" {
			return names, nil
		}
		if s[0] != ',' {
			return nil, fmt.Errorf("invalid member name list")
		}
		s = s[1:]
	}
}

// pathEnv supplies the roots a path can refer to.
type pathEnv struct {
	input   any
	context any
	vars    *scope
}

var errNoMatch = fmt.Errorf("no match")

// eval evaluates the path. A definite path returns the single matched value;
// other paths return all matches as an array.
func (p *jsonPath) eval(env pathEnv) (any, error) {
	var root any
	switch p.root {
	case rootInput, rootCurrent:
		root = env.input
	case rootContext:
		root = env.context
	case rootVar:
		v, ok := env.vars.get(p.varName)
		if !ok {
			return nil, fmt.Errorf("variable $%s is not defined", p.varName)
		}
		root = v
	}

	nodes := []any{root}
	for _, seg := range p.segs {
		var next []any
		for _, n := range nodes {
			if seg.recursive {
				for _, d := range descendants(n) {
					next = append(next, seg.apply(d)...)
				}
			} else {
				next = append(next, seg.apply(n)...)
			}
		}
		nodes = next
	}

	if p.definite() {
		if len(nodes) == 0 {
			return nil, errNoMatch
		}
		return nodes[0], nil
	}

	if nodes == nil {
		nodes = []any{}
	}
	return nodes, nil
}

func descendants(v any) []any {
	out := []any{v}
	switch t := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			out = append(out, descendants(t[k])...)
		}
	case []any:
		for _, e := range t {
			out = append(out, descendants(e)...)
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func normIndex(i, n int) (int, bool) {
	if i < 0 {
		i += n
	}
	return i, i >= 0 && i < n
}

func (s *segment) apply(v any) []any {
	switch s.kind {
	case segName:
		if m, ok := v.(map[string]any); ok {
			if c, ok := m[s.name]; ok {
				return []any{c}
			}
		}
	case segIndex:
		if a, ok := v.([]any); ok {
			if i, ok := normIndex(s.index, len(a)); ok {
				return []any{a[i]}
			}
		}
	case segWildcard:
		switch t := v.(type) {
		case map[string]any:
			out := make([]any, 0, len(t))
			for _, k := range sortedKeys(t) {
				out = append(out, t[k])
			}
			return out
		case []any:
			return slices.Clone(t)
		}
	case segUnion:
		var out []any
		switch t := v.(type) {
		case map[string]any:
			for _, n := range s.names {
				if c, ok := t[n]; ok {
					out = append(out, c)
				}
			}
		case []any:
			for _, idx := range s.indexes {
				if i, ok := normIndex(idx, len(t)); ok {
					out = append(out, t[i])
				}
			}
		}
		return out
	case segSlice:
		a, ok := v.([]any)
		if !ok {
			return nil
		}
		return sliceArray(a, s.start, s.end, s.step)
	case segFilter:
		var out []any
		var elems []any
		switch t := v.(type) {
		case []any:
			elems = t
		case map[string]any:
			for _, k := range sortedKeys(t) {
				elems = append(elems, t[k])
			}
		}
		for _, e := range elems {
			if s.filter.test(e) {
				out = append(out, e)
			}
		}
		return out
	}
	return nil
}

func sliceArray(a []any, start, end, step *int) []any {
	n := len(a)
	st := 1
	if step != nil {
		st = *step
	}
	if st == 0 {
		return nil
	}

	clamp := func(i, lo, hi int) int { return max(lo, min(hi, i)) }
	norm := func(p *int, def int) int {
		if p == nil {
			return def
		}
		i := *p
		if i < 0 {
			i += n
		}
		return i
	}

	var out []any
	if st > 0 {
		lo := clamp(norm(start, 0), 0, n)
		hi := clamp(norm(end, n), 0, n)
		for i := lo; i < hi; i += st {
			out = append(out, a[i])
		}
	} else {
		hi := clamp(norm(start, n-1), -1, n-1)
		lo := clamp(norm(end, -1), -1, n-1)
		for i := hi; i > lo; i += st {
			out = append(out, a[i])
		}
	}
	return out
}

// setPath writes value at the reference path p inside root, creating
// intermediate objects as needed. root is not modified; a copy is returned.
func setPath(root any, p *jsonPath, value any) (any, error) {
	if len(p.segs) == 0 {
		return value, nil
	}
	return setSegs(deepCopy(root), p.segs, value)
}

func setSegs(cur any, segs []segment, value any) (any, error) {
	if len(segs) == 0 {
		return value, nil
	}

	seg := segs[0]
	switch seg.kind {
	case segName:
		var m map[string]any
		switch t := cur.(type) {
		case map[string]any:
			m = t
		case nil:
			m = map[string]any{}
		default:
			return nil, fmt.Errorf("cannot set field %q on a non-object value", seg.name)
		}
		child, err := setSegs(m[seg.name], segs[1:], value)
		if err != nil {
			return nil, err
		}
		m[seg.name] = child
		return m, nil
	case segIndex:
		a, ok := cur.([]any)
		if !ok {
			return nil, fmt.Errorf("cannot set index %d on a non-array value", seg.index)
		}
		i, ok := normIndex(seg.index, len(a))
		if !ok {
			return nil, fmt.Errorf("index %d out of range", seg.index)
		}
		child, err := setSegs(a[i], segs[1:], value)
		if err != nil {
			return nil, err
		}
		a[i] = child
		return a, nil
	}
	return nil, fmt.Errorf("not a reference path")
}

func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, e := range t {
			m[k] = deepCopy(e)
		}
		return m
	case []any:
		a := make([]any, len(t))
		for i, e := range t {
			a[i] = deepCopy(e)
		}
		return a
	}
	return v
}

// ---- filter expressions: [?(...)] ----

type filterExpr interface {
	test(v any) bool
}

type filterAnd struct{ l, r filterExpr }
type filterOr struct{ l, r filterExpr }
type filterCmp struct {
	l, r filterOperand
	op   string // "" means existence test of l
}

func (f filterAnd) test(v any) bool { return f.l.test(v) && f.r.test(v) }
func (f filterOr) test(v any) bool  { return f.l.test(v) || f.r.test(v) }

type filterOperand struct {
	path *jsonPath
	lit  any
}

func (o filterOperand) value(cur any) (any, bool) {
	if o.path == nil {
		return o.lit, true
	}
	v, err := o.path.eval(pathEnv{input: cur})
	if err != nil {
		return nil, false
	}
	return v, true
}

func (f filterCmp) test(cur any) bool {
	lv, ok := f.l.value(cur)
	if !ok {
		return false
	}
	if f.op == "" {
		return true
	}
	rv, ok := f.r.value(cur)
	if !ok {
		return false
	}

	switch f.op {
	case "==":
		return jsonEqual(lv, rv)
	case "!=":
		return !jsonEqual(lv, rv)
	}

	if ln, ok := toFloat(lv); ok {
		rn, ok := toFloat(rv)
		if !ok {
			return false
		}
		switch f.op {
		case "<":
			return ln < rn
		case "<=":
			return ln <= rn
		case ">":
			return ln > rn
		case ">=":
			return ln >= rn
		}
	}

	if ls, ok := lv.(string); ok {
		rs, ok := rv.(string)
		if !ok {
			return false
		}
		switch f.op {
		case "<":
			return ls < rs
		case "<=":
			return ls <= rs
		case ">":
			return ls > rs
		case ">=":
			return ls >= rs
		}
	}
	return false
}

func parseFilter(s string) (filterExpr, error) {
	s = strings.TrimSpace(s)
	if i := indexTopLevel(s, "||"); i >= 0 {
		l, err := parseFilter(s[:i])
		if err != nil {
			return nil, err
		}
		r, err := parseFilter(s[i+2:])
		if err != nil {
			return nil, err
		}
		return filterOr{l, r}, nil
	}
	if i := indexTopLevel(s, "&&"); i >= 0 {
		l, err := parseFilter(s[:i])
		if err != nil {
			return nil, err
		}
		r, err := parseFilter(s[i+2:])
		if err != nil {
			return nil, err
		}
		return filterAnd{l, r}, nil
	}
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		return parseFilter(s[1 : len(s)-1])
	}

	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		if i := indexTopLevel(s, op); i >= 0 {
			l, err := parseOperand(s[:i])
			if err != nil {
				return nil, err
			}
			r, err := parseOperand(s[i+len(op):])
			if err != nil {
				return nil, err
			}
			return filterCmp{l: l, r: r, op: op}, nil
		}
	}

	l, err := parseOperand(s)
	if err != nil {
		return nil, err
	}
	if l.path == nil {
		return nil, fmt.Errorf("invalid filter: %s", s)
	}
	return filterCmp{l: l}, nil
}

func indexTopLevel(s, sub string) int {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			switch c {
			case '\\':
				i++
			case quote:
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			continue
		case '(', '[':
			depth++
			continue
		case ')', ']':
			depth--
			continue
		}
		if depth == 0 && strings.HasPrefix(s[i:], sub) {
			return i
		}
	}
	return -1
}

func parseOperand(s string) (filterOperand, error) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "@"):
		p, err := parsePath(s)
		if err != nil {
			return filterOperand{}, err
		}
		return filterOperand{path: p}, nil
	case strings.HasPrefix(s, "'"):
		names, err := splitQuotedNames(s)
		if err != nil || len(names) != 1 {
			return filterOperand{}, fmt.Errorf("invalid string literal: %s", s)
		}
		return filterOperand{lit: names[0]}, nil
	}

	v, err := decodeJSON([]byte(s))
	if err != nil {
		return filterOperand{}, fmt.Errorf("invalid literal: %s", s)
	}
	return filterOperand{lit: v}, nil
}

func toFloat(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}
