package sfntest

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"math"
	"math/rand/v2"
	"strings"
	"unicode/utf8"
)

// Intrinsic implements the intrinsic functions of the JSONPath query language.
// Embed DefaultIntrinsic to override only some of them.
//
// Return an error made by [Error] (usually States.IntrinsicFailure) to fail
// the state. Any other error aborts [StateMachine.Run].
type Intrinsic interface {
	// Format replaces each {} in template with the next argument.
	// \{, \} and \\ in template are literal characters.
	Format(template string, args ...any) (string, error)
	StringToJson(s string) (any, error)
	JsonToString(v any) (string, error)
	Array(args ...any) ([]any, error)
	ArrayPartition(array []any, size int64) ([]any, error)
	ArrayContains(array []any, v any) (bool, error)
	ArrayRange(first, last, step int64) ([]any, error)
	ArrayGetItem(array []any, index int64) (any, error)
	ArrayLength(array []any) (int64, error)
	ArrayUnique(array []any) ([]any, error)
	Base64Encode(s string) (string, error)
	Base64Decode(s string) (string, error)
	Hash(data any, algorithm string) (string, error)
	JsonMerge(a, b map[string]any, deep bool) (map[string]any, error)
	// MathRandom returns a random integer in [start, end). seed is nil when omitted.
	MathRandom(start, end int64, seed *int64) (int64, error)
	MathAdd(a, b int64) (int64, error)
	StringSplit(s, delimiters string) ([]any, error)
	UUID() (string, error)
}

// DefaultIntrinsic implements the intrinsic functions as documented by AWS.
type DefaultIntrinsic struct{}

var _ Intrinsic = DefaultIntrinsic{}

func intrinsicFailure(format string, args ...any) error {
	return statesErrorf(ErrorIntrinsicFailure, format, args...)
}

const intrinsicStringLimit = 10000

func (DefaultIntrinsic) Format(template string, args ...any) (string, error) {
	var b strings.Builder
	n := 0
	for i := 0; i < len(template); i++ {
		if template[i] == '\\' && i+1 < len(template) && strings.ContainsRune(`{}\`, rune(template[i+1])) {
			i++
			b.WriteByte(template[i])
			continue
		}
		if strings.HasPrefix(template[i:], "{}") {
			if n >= len(args) {
				return "", intrinsicFailure("States.Format: the number of arguments does not match the number of placeholders")
			}
			if s, ok := args[n].(string); ok {
				b.WriteString(s)
			} else {
				b.Write(mustEncodeJSON(args[n]))
			}
			n++
			i++
			continue
		}
		b.WriteByte(template[i])
	}
	if n != len(args) {
		return "", intrinsicFailure("States.Format: the number of arguments does not match the number of placeholders")
	}
	return b.String(), nil
}

func (DefaultIntrinsic) StringToJson(s string) (any, error) {
	v, err := decodeJSON([]byte(s))
	if err != nil {
		return nil, intrinsicFailure("States.StringToJson: %s", err)
	}
	return v, nil
}

func (DefaultIntrinsic) JsonToString(v any) (string, error) {
	return string(mustEncodeJSON(v)), nil
}

func (DefaultIntrinsic) Array(args ...any) ([]any, error) {
	return append([]any{}, args...), nil
}

func (DefaultIntrinsic) ArrayPartition(array []any, size int64) ([]any, error) {
	if size <= 0 {
		return nil, intrinsicFailure("States.ArrayPartition: chunk size must be a positive integer")
	}
	out := []any{}
	for i := 0; i < len(array); i += int(size) {
		out = append(out, append([]any{}, array[i:min(i+int(size), len(array))]...))
	}
	return out, nil
}

func (DefaultIntrinsic) ArrayContains(array []any, v any) (bool, error) {
	for _, e := range array {
		if jsonEqual(e, v) {
			return true, nil
		}
	}
	return false, nil
}

const arrayRangeLimit = 1000

func (DefaultIntrinsic) ArrayRange(first, last, step int64) ([]any, error) {
	if step == 0 {
		return nil, intrinsicFailure("States.ArrayRange: step must not be zero")
	}
	out := []any{}
	for i := first; (step > 0 && i <= last) || (step < 0 && i >= last); i += step {
		if len(out) >= arrayRangeLimit {
			return nil, intrinsicFailure("States.ArrayRange: the result exceeds %d elements", arrayRangeLimit)
		}
		out = append(out, i)
	}
	return out, nil
}

func (DefaultIntrinsic) ArrayGetItem(array []any, index int64) (any, error) {
	if index < 0 || index >= int64(len(array)) {
		return nil, intrinsicFailure("States.ArrayGetItem: index %d is out of range", index)
	}
	return array[index], nil
}

func (DefaultIntrinsic) ArrayLength(array []any) (int64, error) {
	return int64(len(array)), nil
}

func (DefaultIntrinsic) ArrayUnique(array []any) ([]any, error) {
	out := []any{}
	for _, e := range array {
		dup := false
		for _, o := range out {
			if jsonEqual(e, o) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, e)
		}
	}
	return out, nil
}

func (DefaultIntrinsic) Base64Encode(s string) (string, error) {
	if utf8.RuneCountInString(s) > intrinsicStringLimit {
		return "", intrinsicFailure("States.Base64Encode: the input exceeds %d characters", intrinsicStringLimit)
	}
	return base64.StdEncoding.EncodeToString([]byte(s)), nil
}

func (DefaultIntrinsic) Base64Decode(s string) (string, error) {
	if utf8.RuneCountInString(s) > intrinsicStringLimit {
		return "", intrinsicFailure("States.Base64Decode: the input exceeds %d characters", intrinsicStringLimit)
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", intrinsicFailure("States.Base64Decode: %s", err)
	}
	return string(b), nil
}

func (DefaultIntrinsic) Hash(data any, algorithm string) (string, error) {
	s, ok := data.(string)
	if !ok {
		s = string(mustEncodeJSON(data))
	}
	if utf8.RuneCountInString(s) > intrinsicStringLimit {
		return "", intrinsicFailure("States.Hash: the input exceeds %d characters", intrinsicStringLimit)
	}

	var h hash.Hash
	switch algorithm {
	case "MD5":
		h = md5.New()
	case "SHA-1":
		h = sha1.New()
	case "SHA-256":
		h = sha256.New()
	case "SHA-384":
		h = sha512.New384()
	case "SHA-512":
		h = sha512.New()
	default:
		return "", intrinsicFailure("States.Hash: unsupported algorithm: %s", algorithm)
	}
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (DefaultIntrinsic) JsonMerge(a, b map[string]any, deep bool) (map[string]any, error) {
	if deep {
		return nil, intrinsicFailure("States.JsonMerge: only shallow merge (false) is supported")
	}
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out, nil
}

func (DefaultIntrinsic) MathRandom(start, end int64, seed *int64) (int64, error) {
	if start >= end {
		return 0, intrinsicFailure("States.MathRandom: start must be less than end")
	}
	n := uint64(end - start)
	if seed != nil {
		r := rand.New(rand.NewPCG(uint64(*seed), 0))
		return start + int64(r.Uint64N(n)), nil
	}
	return start + int64(rand.Uint64N(n)), nil
}

func (DefaultIntrinsic) MathAdd(a, b int64) (int64, error) {
	for _, v := range []int64{a, b} {
		if v < math.MinInt32 || v > math.MaxInt32 {
			return 0, intrinsicFailure("States.MathAdd: %d is out of the 32-bit integer range", v)
		}
	}
	return a + b, nil
}

func (DefaultIntrinsic) StringSplit(s, delimiters string) ([]any, error) {
	out := []any{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(delimiters, r) }) {
		out = append(out, f)
	}
	return out, nil
}

func (DefaultIntrinsic) UUID() (string, error) {
	return newUUID(), nil
}

// ---- parsing ----

type argKind int

const (
	argString argKind = iota
	argNumber
	argNull
	argBool
	argPath
	argCall
)

type fnArg struct {
	kind argKind
	str  string
	// tmpl is the string literal with \{, \} and \\ kept escaped, for States.Format.
	tmpl string
	num  json.Number
	b    bool
	path *jsonPath
	call *fnCall
}

type fnCall struct {
	name string
	args []fnArg
}

const maxIntrinsicNesting = 10

func parseIntrinsic(s string) (*fnCall, error) {
	c, rest, err := parseCall(strings.TrimSpace(s), 1)
	if err != nil {
		return nil, fmt.Errorf("invalid intrinsic function %s: %w", s, err)
	}
	if strings.TrimSpace(rest) != "" {
		return nil, fmt.Errorf("invalid intrinsic function %s: unexpected %q", s, rest)
	}
	return c, nil
}

func parseCall(s string, depth int) (*fnCall, string, error) {
	if depth > maxIntrinsicNesting {
		return nil, "", fmt.Errorf("intrinsic functions are nested more than %d levels", maxIntrinsicNesting)
	}

	open := strings.IndexByte(s, '(')
	if open < 0 {
		return nil, "", fmt.Errorf("missing '('")
	}
	name := strings.TrimSpace(s[:open])
	if _, ok := intrinsicArity[name]; !ok {
		return nil, "", fmt.Errorf("unknown function %q", name)
	}

	c := &fnCall{name: name}
	rest := strings.TrimLeft(s[open+1:], " \t\n")
	if strings.HasPrefix(rest, ")") {
		return c, rest[1:], checkArity(c)
	}

	for {
		arg, r, err := parseArg(strings.TrimLeft(rest, " \t\n"), depth)
		if err != nil {
			return nil, "", err
		}
		c.args = append(c.args, arg)
		r = strings.TrimLeft(r, " \t\n")
		if r == "" {
			return nil, "", fmt.Errorf("missing ')'")
		}
		switch r[0] {
		case ',':
			rest = r[1:]
		case ')':
			return c, r[1:], checkArity(c)
		default:
			return nil, "", fmt.Errorf("unexpected %q", r)
		}
	}
}

func parseArg(s string, depth int) (fnArg, string, error) {
	switch {
	case s == "":
		return fnArg{}, "", fmt.Errorf("missing argument")
	case s[0] == '\'':
		var b, t strings.Builder
		for i := 1; i < len(s); i++ {
			c := s[i]
			if c == '\\' {
				if i+1 >= len(s) || !strings.ContainsRune(`'{}\`, rune(s[i+1])) {
					return fnArg{}, "", fmt.Errorf("open escape in string literal")
				}
				i++
				b.WriteByte(s[i])
				if s[i] != '\'' {
					t.WriteByte('\\')
				}
				t.WriteByte(s[i])
				continue
			}
			if c == '\'' {
				return fnArg{kind: argString, str: b.String(), tmpl: t.String()}, s[i+1:], nil
			}
			b.WriteByte(c)
			t.WriteByte(c)
		}
		return fnArg{}, "", fmt.Errorf("unclosed string literal")
	case s[0] == '$':
		end := argEnd(s)
		p, err := parsePath(strings.TrimSpace(s[:end]))
		if err != nil {
			return fnArg{}, "", err
		}
		return fnArg{kind: argPath, path: p}, s[end:], nil
	case strings.HasPrefix(s, "States."):
		c, rest, err := parseCall(s, depth+1)
		if err != nil {
			return fnArg{}, "", err
		}
		return fnArg{kind: argCall, call: c}, rest, nil
	}

	end := argEnd(s)
	tok := strings.TrimSpace(s[:end])
	switch tok {
	case "null":
		return fnArg{kind: argNull}, s[end:], nil
	case "true", "false":
		return fnArg{kind: argBool, b: tok == "true"}, s[end:], nil
	}
	v, err := decodeJSON([]byte(tok))
	if n, ok := v.(json.Number); err == nil && ok {
		return fnArg{kind: argNumber, num: n}, s[end:], nil
	}
	return fnArg{}, "", fmt.Errorf("invalid argument %q", tok)
}

// argEnd returns the end of a bare (path, number, literal) argument.
func argEnd(s string) int {
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
		case '[', '(':
			depth++
		case ']':
			depth--
		case ')':
			if depth == 0 {
				return i
			}
			depth--
		case ',':
			if depth == 0 {
				return i
			}
		}
	}
	return len(s)
}

// intrinsicArity holds the minimum and maximum number of arguments (-1 = unlimited).
var intrinsicArity = map[string][2]int{
	"States.Format":         {1, -1},
	"States.StringToJson":   {1, 1},
	"States.JsonToString":   {1, 1},
	"States.Array":          {0, -1},
	"States.ArrayPartition": {2, 2},
	"States.ArrayContains":  {2, 2},
	"States.ArrayRange":     {3, 3},
	"States.ArrayGetItem":   {2, 2},
	"States.ArrayLength":    {1, 1},
	"States.ArrayUnique":    {1, 1},
	"States.Base64Encode":   {1, 1},
	"States.Base64Decode":   {1, 1},
	"States.Hash":           {2, 2},
	"States.JsonMerge":      {3, 3},
	"States.MathRandom":     {2, 3},
	"States.MathAdd":        {2, 2},
	"States.StringSplit":    {2, 2},
	"States.UUID":           {0, 0},
}

func checkArity(c *fnCall) error {
	a := intrinsicArity[c.name]
	n := len(c.args)
	if n < a[0] || (a[1] >= 0 && n > a[1]) {
		return fmt.Errorf("%s: wrong number of arguments: %d", c.name, n)
	}
	return nil
}

// ---- evaluation ----

func (r *runner) evalIntrinsic(c *fnCall, penv pathEnv) (any, error) {
	args := make([]any, len(c.args))
	for i, a := range c.args {
		switch a.kind {
		case argString:
			args[i] = a.str
			if i == 0 && c.name == "States.Format" {
				args[i] = a.tmpl
			}
		case argNumber:
			args[i] = a.num
		case argNull:
			args[i] = nil
		case argBool:
			args[i] = a.b
		case argPath:
			v, err := a.path.eval(penv)
			if err != nil {
				return nil, intrinsicFailure("%s: %s: %s", c.name, a.path.src, err)
			}
			args[i] = v
		case argCall:
			v, err := r.evalIntrinsic(a.call, penv)
			if err != nil {
				return nil, err
			}
			args[i] = v
		}
	}

	v, err := callIntrinsic(r.env.intrinsic, c.name, args)
	if err != nil {
		return nil, err
	}
	n, err := normalize(v)
	if err != nil {
		return nil, intrinsicFailure("%s: %s", c.name, err)
	}
	return n, nil
}

func argStr(name string, v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", intrinsicFailure("%s: expected a string, got %s", name, typeName(v))
	}
	return s, nil
}

func argArray(name string, v any) ([]any, error) {
	a, ok := v.([]any)
	if !ok {
		return nil, intrinsicFailure("%s: expected an array, got %s", name, typeName(v))
	}
	return a, nil
}

func argObject(name string, v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, intrinsicFailure("%s: expected an object, got %s", name, typeName(v))
	}
	return m, nil
}

// argInt rounds a number to the nearest integer.
func argInt(name string, v any) (int64, error) {
	f, ok := toFloat(v)
	if !ok {
		return 0, intrinsicFailure("%s: expected a number, got %s", name, typeName(v))
	}
	r := math.Round(f)
	if r < math.MinInt64 || r > math.MaxInt64 {
		return 0, intrinsicFailure("%s: %v is out of range", name, v)
	}
	return int64(r), nil
}

func callIntrinsic(f Intrinsic, name string, args []any) (any, error) {
	switch name {
	case "States.Format":
		t, err := argStr(name, args[0])
		if err != nil {
			return nil, err
		}
		return f.Format(t, args[1:]...)
	case "States.StringToJson":
		s, err := argStr(name, args[0])
		if err != nil {
			return nil, err
		}
		return f.StringToJson(s)
	case "States.JsonToString":
		return f.JsonToString(args[0])
	case "States.Array":
		return f.Array(args...)
	case "States.ArrayPartition":
		a, err := argArray(name, args[0])
		if err != nil {
			return nil, err
		}
		n, err := argInt(name, args[1])
		if err != nil {
			return nil, err
		}
		return f.ArrayPartition(a, n)
	case "States.ArrayContains":
		a, err := argArray(name, args[0])
		if err != nil {
			return nil, err
		}
		return f.ArrayContains(a, args[1])
	case "States.ArrayRange":
		var n [3]int64
		for i := range n {
			v, err := argInt(name, args[i])
			if err != nil {
				return nil, err
			}
			n[i] = v
		}
		return f.ArrayRange(n[0], n[1], n[2])
	case "States.ArrayGetItem":
		a, err := argArray(name, args[0])
		if err != nil {
			return nil, err
		}
		i, err := argInt(name, args[1])
		if err != nil {
			return nil, err
		}
		return f.ArrayGetItem(a, i)
	case "States.ArrayLength":
		a, err := argArray(name, args[0])
		if err != nil {
			return nil, err
		}
		return f.ArrayLength(a)
	case "States.ArrayUnique":
		a, err := argArray(name, args[0])
		if err != nil {
			return nil, err
		}
		return f.ArrayUnique(a)
	case "States.Base64Encode", "States.Base64Decode":
		s, err := argStr(name, args[0])
		if err != nil {
			return nil, err
		}
		if name == "States.Base64Encode" {
			return f.Base64Encode(s)
		}
		return f.Base64Decode(s)
	case "States.Hash":
		alg, err := argStr(name, args[1])
		if err != nil {
			return nil, err
		}
		return f.Hash(args[0], alg)
	case "States.JsonMerge":
		a, err := argObject(name, args[0])
		if err != nil {
			return nil, err
		}
		b, err := argObject(name, args[1])
		if err != nil {
			return nil, err
		}
		deep, ok := args[2].(bool)
		if !ok {
			return nil, intrinsicFailure("%s: expected a boolean, got %s", name, typeName(args[2]))
		}
		return f.JsonMerge(a, b, deep)
	case "States.MathRandom":
		start, err := argInt(name, args[0])
		if err != nil {
			return nil, err
		}
		end, err := argInt(name, args[1])
		if err != nil {
			return nil, err
		}
		var seed *int64
		if len(args) > 2 {
			s, err := argInt(name, args[2])
			if err != nil {
				return nil, err
			}
			seed = &s
		}
		return f.MathRandom(start, end, seed)
	case "States.MathAdd":
		a, err := argInt(name, args[0])
		if err != nil {
			return nil, err
		}
		b, err := argInt(name, args[1])
		if err != nil {
			return nil, err
		}
		return f.MathAdd(a, b)
	case "States.StringSplit":
		s, err := argStr(name, args[0])
		if err != nil {
			return nil, err
		}
		d, err := argStr(name, args[1])
		if err != nil {
			return nil, err
		}
		return f.StringSplit(s, d)
	case "States.UUID":
		return f.UUID()
	}
	return nil, fmt.Errorf("unknown intrinsic function: %s", name)
}
