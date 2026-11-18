package sfntest

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"math"
	"math/rand/v2"
	"strings"
	"unicode/utf8"
)

// Func is an intrinsic function or a JSONata function.
//
// args and the return value are JSON values: nil, bool, string,
// json.Number, []any or map[string]any. Go numbers (int, float64, ...) are
// also accepted as return values.
//
// Return an error made by [Error] to fail the state (States.IntrinsicFailure
// for intrinsic functions, States.QueryEvaluationError for JSONata).
// Any other error aborts [StateMachine.Run].
type Func func(args ...any) (any, error)

// IntrinsicFuncs replaces intrinsic functions of the JSONPath query language.
// Keys are function names such as "States.UUID". Functions that are not in
// the map keep their default implementation.
type IntrinsicFuncs map[string]Func

func intrinsicFailure(format string, args ...any) error {
	return statesErrorf(ErrorIntrinsicFailure, format, args...)
}

const (
	intrinsicStringLimit = 10000
	arrayRangeLimit      = 1000
)

// defaultIntrinsics implements the intrinsic functions as documented by AWS.
// The number of arguments is checked by the parser.
var defaultIntrinsics = map[string]Func{
	"States.Format": func(args ...any) (any, error) {
		t, err := argStr("States.Format", args[0])
		if err != nil {
			return nil, err
		}
		return format(t, args[1:])
	},
	"States.StringToJson": func(args ...any) (any, error) {
		s, err := argStr("States.StringToJson", args[0])
		if err != nil {
			return nil, err
		}
		v, err := decodeJSON([]byte(s))
		if err != nil {
			return nil, intrinsicFailure("States.StringToJson: %s", err)
		}
		return v, nil
	},
	"States.JsonToString": func(args ...any) (any, error) {
		return string(mustEncodeJSON(args[0])), nil
	},
	"States.Array": func(args ...any) (any, error) {
		return append([]any{}, args...), nil
	},
	"States.ArrayPartition": func(args ...any) (any, error) {
		a, err := argArray("States.ArrayPartition", args[0])
		if err != nil {
			return nil, err
		}
		n, err := argInt("States.ArrayPartition", args[1], math.Round)
		if err != nil {
			return nil, err
		}
		if n <= 0 {
			return nil, intrinsicFailure("States.ArrayPartition: chunk size must be a positive integer")
		}
		return partition(a, n), nil
	},
	"States.ArrayContains": func(args ...any) (any, error) {
		a, err := argArray("States.ArrayContains", args[0])
		if err != nil {
			return nil, err
		}
		for _, e := range a {
			if jsonEqual(e, args[1]) {
				return true, nil
			}
		}
		return false, nil
	},
	"States.ArrayRange": func(args ...any) (any, error) {
		var n [3]int64
		for i := range n {
			v, err := argInt("States.ArrayRange", args[i], math.Round)
			if err != nil {
				return nil, err
			}
			n[i] = v
		}
		if n[2] == 0 {
			return nil, intrinsicFailure("States.ArrayRange: step must not be zero")
		}
		out, ok := numberRange(n[0], n[1], n[2], arrayRangeLimit)
		if !ok {
			return nil, intrinsicFailure("States.ArrayRange: the result exceeds %d elements", arrayRangeLimit)
		}
		return out, nil
	},
	"States.ArrayGetItem": func(args ...any) (any, error) {
		a, err := argArray("States.ArrayGetItem", args[0])
		if err != nil {
			return nil, err
		}
		i, err := argInt("States.ArrayGetItem", args[1], math.Round)
		if err != nil {
			return nil, err
		}
		if i < 0 || i >= int64(len(a)) {
			return nil, intrinsicFailure("States.ArrayGetItem: index %d is out of range", i)
		}
		return a[i], nil
	},
	"States.ArrayLength": func(args ...any) (any, error) {
		a, err := argArray("States.ArrayLength", args[0])
		if err != nil {
			return nil, err
		}
		return len(a), nil
	},
	"States.ArrayUnique": func(args ...any) (any, error) {
		a, err := argArray("States.ArrayUnique", args[0])
		if err != nil {
			return nil, err
		}
		out := []any{}
		for _, e := range a {
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
	},
	"States.Base64Encode": func(args ...any) (any, error) {
		s, err := argLimitedStr("States.Base64Encode", args[0])
		if err != nil {
			return nil, err
		}
		return base64.StdEncoding.EncodeToString([]byte(s)), nil
	},
	"States.Base64Decode": func(args ...any) (any, error) {
		s, err := argLimitedStr("States.Base64Decode", args[0])
		if err != nil {
			return nil, err
		}
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, intrinsicFailure("States.Base64Decode: %s", err)
		}
		return string(b), nil
	},
	"States.Hash": func(args ...any) (any, error) {
		data, ok := args[0].(string)
		if !ok {
			data = string(mustEncodeJSON(args[0]))
		}
		if utf8.RuneCountInString(data) > intrinsicStringLimit {
			return nil, intrinsicFailure("States.Hash: the input exceeds %d characters", intrinsicStringLimit)
		}
		alg, err := argStr("States.Hash", args[1])
		if err != nil {
			return nil, err
		}
		h, err := hashString(data, alg)
		if err != nil {
			return nil, intrinsicFailure("States.Hash: %s", err)
		}
		return h, nil
	},
	"States.JsonMerge": func(args ...any) (any, error) {
		a, err := argObject("States.JsonMerge", args[0])
		if err != nil {
			return nil, err
		}
		b, err := argObject("States.JsonMerge", args[1])
		if err != nil {
			return nil, err
		}
		if deep, ok := args[2].(bool); !ok || deep {
			return nil, intrinsicFailure("States.JsonMerge: the third argument must be false (only shallow merge is supported)")
		}
		out := make(map[string]any, len(a)+len(b))
		for k, v := range a {
			out[k] = v
		}
		for k, v := range b {
			out[k] = v
		}
		return out, nil
	},
	"States.MathRandom": func(args ...any) (any, error) {
		start, err := argInt("States.MathRandom", args[0], math.Round)
		if err != nil {
			return nil, err
		}
		end, err := argInt("States.MathRandom", args[1], math.Round)
		if err != nil {
			return nil, err
		}
		if start >= end {
			return nil, intrinsicFailure("States.MathRandom: start must be less than end")
		}
		n := uint64(end - start)
		if len(args) > 2 {
			seed, err := argInt("States.MathRandom", args[2], math.Round)
			if err != nil {
				return nil, err
			}
			r := rand.New(rand.NewPCG(uint64(seed), 0))
			return start + int64(r.Uint64N(n)), nil
		}
		return start + int64(rand.Uint64N(n)), nil
	},
	"States.MathAdd": func(args ...any) (any, error) {
		var n [2]int64
		for i := range n {
			v, err := argInt("States.MathAdd", args[i], math.Round)
			if err != nil {
				return nil, err
			}
			if v < math.MinInt32 || v > math.MaxInt32 {
				return nil, intrinsicFailure("States.MathAdd: %d is out of the 32-bit integer range", v)
			}
			n[i] = v
		}
		return n[0] + n[1], nil
	},
	"States.StringSplit": func(args ...any) (any, error) {
		s, err := argStr("States.StringSplit", args[0])
		if err != nil {
			return nil, err
		}
		d, err := argStr("States.StringSplit", args[1])
		if err != nil {
			return nil, err
		}
		out := []any{}
		for _, f := range strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(d, r) }) {
			out = append(out, f)
		}
		return out, nil
	},
	"States.UUID": func(...any) (any, error) {
		return newUUID(), nil
	},
}

// format replaces each {} in template with the next argument.
// \{, \} and \\ in template are literal characters.
func format(template string, args []any) (string, error) {
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

func partition(a []any, size int64) []any {
	out := []any{}
	for i := 0; i < len(a); i += int(size) {
		out = append(out, append([]any{}, a[i:min(i+int(size), len(a))]...))
	}
	return out
}

// numberRange returns first, first+step, ... up to last (inclusive).
// ok is false when the result would exceed limit elements.
func numberRange(first, last, step int64, limit int) ([]any, bool) {
	out := []any{}
	for i := first; (step > 0 && i <= last) || (step < 0 && i >= last); i += step {
		if len(out) >= limit {
			return nil, false
		}
		out = append(out, i)
	}
	return out, true
}

func hashString(data, algorithm string) (string, error) {
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
		return "", fmt.Errorf("unsupported algorithm: %s", algorithm)
	}
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func argStr(name string, v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", intrinsicFailure("%s: expected a string, got %s", name, typeName(v))
	}
	return s, nil
}

func argLimitedStr(name string, v any) (string, error) {
	s, err := argStr(name, v)
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(s) > intrinsicStringLimit {
		return "", intrinsicFailure("%s: the input exceeds %d characters", name, intrinsicStringLimit)
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

// argInt converts a number to an integer with round (math.Round or math.Floor).
func argInt(name string, v any, round func(float64) float64) (int64, error) {
	f, ok := toFloat(v)
	if !ok {
		return 0, intrinsicFailure("%s: expected a number, got %s", name, typeName(v))
	}
	r := round(f)
	if r < math.MinInt64 || r > math.MaxInt64 {
		return 0, intrinsicFailure("%s: %v is out of range", name, v)
	}
	return int64(r), nil
}

func mergeIntrinsics(override IntrinsicFuncs) (map[string]Func, error) {
	funcs := make(map[string]Func, len(defaultIntrinsics))
	for k, f := range defaultIntrinsics {
		funcs[k] = f
	}
	for k, f := range override {
		if _, ok := defaultIntrinsics[k]; !ok {
			return nil, fmt.Errorf("unknown intrinsic function: %s", k)
		}
		if f != nil {
			funcs[k] = f
		}
	}
	return funcs, nil
}

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
			args[i] = deepCopy(v)
		case argCall:
			v, err := r.evalIntrinsic(a.call, penv)
			if err != nil {
				return nil, err
			}
			args[i] = v
		}
	}

	v, err := r.env.intrinsic[c.name](args...)
	if err != nil {
		if se, ok := asStatesError(err); ok {
			return nil, &StatesError{Name: se.Name, Cause: se.Cause}
		}
		return nil, fatal(fmt.Errorf("%s: %w", c.name, err))
	}
	n, err := normalize(v)
	if err != nil {
		return nil, intrinsicFailure("%s: %s", c.name, err)
	}
	return n, nil
}
