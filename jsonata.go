package sfntest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/recolabs/gnata"
)

// JSONataFuncs replaces or adds JSONata functions. Keys are function names
// with the leading "$", such as "$uuid" or "$now". Functions that are not in
// the map keep their default implementation.
//
// By default, the Step Functions functions ($partition, $range, $hash,
// $random, $uuid, $parse) are implemented as documented by AWS, $now and
// $millis read Env.Clock, and $eval is not available.
type JSONataFuncs map[string]Func

// jsonataTimeout is the evaluation timeout of a single JSONata expression.
const jsonataTimeout = time.Second

func isJSONataExpr(s string) bool {
	return strings.HasPrefix(s, "{%") && strings.HasSuffix(s, "%}") && len(s) >= 4
}

func jsonataSource(s string) string {
	return s[2 : len(s)-2]
}

func compileJSONata(src string) (*gnata.Expression, error) {
	return gnata.Compile(src, gnata.WithTimeout(jsonataTimeout))
}

func queryError(format string, args ...any) *StatesError {
	return statesErrorf(ErrorQueryEvaluationError, format, args...)
}

// jsonataEnv holds the JSONata functions of a single execution.
type jsonataEnv struct {
	env *gnata.CustomEnvironment
	// err records an error returned by a Func during an evaluation, so that
	// a States error keeps its name and other errors abort Run.
	err error
}

func (r *runner) newJSONataEnv(override JSONataFuncs) (*jsonataEnv, error) {
	je := &jsonataEnv{}
	funcs := r.defaultJSONataFuncs()
	for k, f := range override {
		if !strings.HasPrefix(k, "$") || len(k) < 2 {
			return nil, fmt.Errorf("JSONata function names must start with '$': %q", k)
		}
		if f != nil {
			funcs[k] = f
		}
	}

	custom := make(map[string]gnata.CustomFunc, len(funcs))
	for name, f := range funcs {
		custom[strings.TrimPrefix(name, "$")] = je.wrap(name, f)
	}
	je.env = gnata.NewCustomEnvironment(custom)
	return je, nil
}

func (je *jsonataEnv) wrap(name string, f Func) gnata.CustomFunc {
	return func(args []any, _ any) (any, error) {
		in := make([]any, len(args))
		for i, a := range args {
			v, err := normalize(gnata.NormalizeValue(a))
			if err != nil {
				// e.g. a function value; pass it through as is
				v = a
			}
			in[i] = v
		}

		v, err := f(in...)
		if err != nil {
			if je.err == nil {
				if se, ok := asStatesError(err); ok {
					je.err = &StatesError{Name: se.Name, Cause: se.Cause}
				} else {
					je.err = fatal(fmt.Errorf("%s: %w", name, err))
				}
			}
			return nil, err
		}

		n, err := normalize(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return toGnata(n)
	}
}

// toGnata converts a JSON value into the representation gnata evaluates.
func toGnata(v any) (any, error) {
	if v == nil {
		return gnata.JSONNull{}, nil
	}
	return gnata.DecodeJSON(mustEncodeJSON(v))
}

func (r *runner) defaultJSONataFuncs() map[string]Func {
	return map[string]Func{
		"$partition": func(args ...any) (any, error) {
			if len(args) != 2 {
				return nil, queryError("$partition: expected 2 arguments")
			}
			a, ok := args[0].([]any)
			if !ok {
				return nil, queryError("$partition: expected an array, got %s", typeName(args[0]))
			}
			n, err := jsonataInt("$partition", args[1])
			if err != nil {
				return nil, err
			}
			if n <= 0 {
				return nil, queryError("$partition: chunk size must be a positive integer")
			}
			return partition(a, n), nil
		},
		"$range": func(args ...any) (any, error) {
			if len(args) != 3 {
				return nil, queryError("$range: expected 3 arguments")
			}
			var n [3]int64
			for i := range n {
				v, err := jsonataInt("$range", args[i])
				if err != nil {
					return nil, err
				}
				n[i] = v
			}
			if n[2] == 0 {
				return nil, queryError("$range: delta must not be zero")
			}
			out, ok := numberRange(n[0], n[1], n[2], 10_000_000)
			if !ok {
				return nil, queryError("$range: the result is too large")
			}
			return out, nil
		},
		"$hash": func(args ...any) (any, error) {
			if len(args) != 2 {
				return nil, queryError("$hash: expected 2 arguments")
			}
			data, ok := args[0].(string)
			if !ok {
				data = string(mustEncodeJSON(args[0]))
			}
			alg, ok := args[1].(string)
			if !ok {
				return nil, queryError("$hash: expected a string algorithm, got %s", typeName(args[1]))
			}
			h, err := hashString(data, alg)
			if err != nil {
				return nil, queryError("$hash: %s", err)
			}
			return h, nil
		},
		"$random": func(args ...any) (any, error) {
			switch len(args) {
			case 0:
				return rand.Float64(), nil
			case 1:
				seed, err := jsonataInt("$random", args[0])
				if err != nil {
					return nil, err
				}
				return rand.New(rand.NewPCG(uint64(seed), 0)).Float64(), nil
			}
			return nil, queryError("$random: expected at most 1 argument")
		},
		"$uuid": func(args ...any) (any, error) {
			if len(args) != 0 {
				return nil, queryError("$uuid: expected no arguments")
			}
			return newUUID(), nil
		},
		"$parse": func(args ...any) (any, error) {
			if len(args) != 1 {
				return nil, queryError("$parse: expected 1 argument")
			}
			s, ok := args[0].(string)
			if !ok {
				return nil, queryError("$parse: expected a string, got %s", typeName(args[0]))
			}
			v, err := decodeJSON([]byte(s))
			if err != nil {
				return nil, queryError("$parse: %s", err)
			}
			return v, nil
		},
		"$eval": func(...any) (any, error) {
			return nil, queryError("$eval is not available in Step Functions; use $parse")
		},
		"$millis": func(args ...any) (any, error) {
			if len(args) != 0 {
				return nil, queryError("$millis: expected no arguments")
			}
			return r.env.clock.Now().UnixMilli(), nil
		},
		"$now": func(args ...any) (any, error) {
			now := r.env.clock.Now()
			if len(args) == 0 {
				return now.UTC().Format("2006-01-02T15:04:05.000Z"), nil
			}
			return r.fromMillis(now.UnixMilli(), args)
		},
	}
}

var fromMillisExpr = func() *gnata.Expression {
	e, err := gnata.Compile(`$fromMillis($ms, $picture, $timezone)`)
	if err != nil {
		panic(err)
	}
	return e
}()

// fromMillis formats ms with the built-in $fromMillis, for $now(picture, timezone).
func (r *runner) fromMillis(ms int64, args []any) (any, error) {
	vars := map[string]any{"ms": ms}
	if len(args) > 0 {
		vars["picture"] = args[0]
	}
	if len(args) > 1 {
		vars["timezone"] = args[1]
	}
	v, err := fromMillisExpr.EvalWithVars(r.ctx, nil, vars)
	if err != nil {
		return nil, queryError("$now: %s", err)
	}
	return gnata.NormalizeValue(v), nil
}

// jsonataInt converts a number to an integer, rounding down as Step Functions does.
func jsonataInt(name string, v any) (int64, error) {
	f, ok := toFloat(v)
	if !ok {
		return 0, queryError("%s: expected a number, got %s", name, typeName(v))
	}
	f = math.Floor(f)
	if f < math.MinInt64 || f > math.MaxInt64 {
		return 0, queryError("%s: %v is out of range", name, v)
	}
	return int64(f), nil
}

// ---- evaluation ----

// jsonataStates is the $states variable.
type jsonataStates struct {
	input       any
	result      any
	errorOutput any
	hasResult   bool
	hasError    bool
	context     map[string]any
}

func (r *runner) jsonataVars(sc *scope, st jsonataStates) (map[string]any, error) {
	states := map[string]any{"input": st.input, "context": st.context}
	if st.hasResult {
		states["result"] = st.result
	}
	if st.hasError {
		states["errorOutput"] = st.errorOutput
	}

	vars := map[string]any{}
	var chain []*scope
	for s := sc; s != nil; s = s.parent {
		chain = append(chain, s)
	}
	for i := len(chain) - 1; i >= 0; i-- {
		for k, v := range chain[i].vars {
			vars[k] = v
		}
	}
	vars["states"] = states

	out := make(map[string]any, len(vars))
	for k, v := range vars {
		g, err := toGnata(v)
		if err != nil {
			return nil, fatal(err)
		}
		out[k] = g
	}
	return out, nil
}

// evalJSONata evaluates a JSONata template: every "{% ... %}" string at any
// depth is evaluated, and other values are used as they are.
func (r *runner) evalJSONata(t any, vars map[string]any) (any, error) {
	switch t := t.(type) {
	case string:
		if !isJSONataExpr(t) {
			return t, nil
		}
		return r.evalJSONataExpr(t, vars)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, v := range t {
			val, err := r.evalJSONata(v, vars)
			if err != nil {
				return nil, err
			}
			out[k] = val
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, v := range t {
			val, err := r.evalJSONata(v, vars)
			if err != nil {
				return nil, err
			}
			out[i] = val
		}
		return out, nil
	}
	return t, nil
}

func (r *runner) evalJSONataExpr(s string, vars map[string]any) (any, error) {
	expr, ok := r.sm.jsonata[s]
	if !ok {
		var err error
		if expr, err = compileJSONata(jsonataSource(s)); err != nil {
			return nil, fatal(err)
		}
	}

	r.jsonata.err = nil
	res, err := expr.EvalWithCustomEnvironmentAndVars(r.ctx, nil, r.jsonata.env, vars)
	if r.jsonata.err != nil {
		return nil, r.jsonata.err
	}
	if err != nil {
		if r.ctx.Err() != nil {
			return nil, fatal(context.Cause(r.ctx))
		}
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "D1012") {
			return nil, queryError("Expression evaluation timeout: %s", s)
		}
		return nil, queryError("%s: %s", s, err)
	}
	if res == nil {
		return nil, queryError("the JSONata expression %s returned no result (undefined)", s)
	}

	v, err := normalize(gnata.NormalizeValue(res))
	if err != nil {
		return nil, queryError("%s: %s", s, err)
	}
	return v, nil
}
