package sfntest

import (
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"
)

func parseTimestamp(s string) (time.Time, bool) {
	if !strings.Contains(s, "T") {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func isTimestamp(s string) bool {
	_, ok := parseTimestamp(s)
	return ok
}

func (r *runner) evalRule(rule map[string]any, penv pathEnv) (bool, error) {
	if rules, ok := rule["And"].([]any); ok {
		for _, sub := range rules {
			ok, err := r.evalRule(sub.(map[string]any), penv)
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	}
	if rules, ok := rule["Or"].([]any); ok {
		for _, sub := range rules {
			ok, err := r.evalRule(sub.(map[string]any), penv)
			if err != nil || ok {
				return ok, err
			}
		}
		return false, nil
	}
	if sub, ok := rule["Not"].(map[string]any); ok {
		ok, err := r.evalRule(sub, penv)
		return !ok, err
	}

	op, operand := ruleOperator(rule)
	vs, _ := rule["Variable"].(string)
	p, err := parsePath(vs)
	if err != nil {
		return false, fatal(err)
	}
	v, err := p.eval(penv)
	present := err == nil

	if op == "IsPresent" {
		return present == operand.(bool), nil
	}
	if !present {
		if errors.Is(err, errNoMatch) {
			return false, statesErrorf(ErrorRuntime, "Variable %s: no value found", vs)
		}
		return false, statesErrorf(ErrorRuntime, "Variable %s: %s", vs, err)
	}

	switch op {
	case "IsNull":
		return (v == nil) == operand.(bool), nil
	case "IsNumeric":
		_, ok := v.(json.Number)
		return ok == operand.(bool), nil
	case "IsString":
		_, ok := v.(string)
		return ok == operand.(bool), nil
	case "IsBoolean":
		_, ok := v.(bool)
		return ok == operand.(bool), nil
	case "IsTimestamp":
		s, ok := v.(string)
		return (ok && isTimestamp(s)) == operand.(bool), nil
	case "StringMatches":
		s, ok := v.(string)
		return ok && stringMatches(s, operand.(string)), nil
	}

	if name, ok := strings.CutSuffix(op, "Path"); ok {
		pp, err := parsePath(operand.(string))
		if err != nil {
			return false, fatal(err)
		}
		if operand, err = pp.eval(penv); err != nil {
			return false, statesErrorf(ErrorRuntime, "%s %s: %s", op, pp.src, err)
		}
		op = name
	}

	switch {
	case strings.HasPrefix(op, "String"):
		a, ok1 := v.(string)
		b, ok2 := operand.(string)
		if !ok1 || !ok2 {
			return false, nil
		}
		return compareResult(op, "String", strings.Compare(a, b)), nil
	case strings.HasPrefix(op, "Numeric"):
		a, ok1 := toBigFloat(v)
		b, ok2 := toBigFloat(operand)
		if !ok1 || !ok2 {
			return false, nil
		}
		return compareResult(op, "Numeric", a.Cmp(b)), nil
	case op == "BooleanEquals":
		a, ok1 := v.(bool)
		b, ok2 := operand.(bool)
		return ok1 && ok2 && a == b, nil
	case strings.HasPrefix(op, "Timestamp"):
		as, _ := v.(string)
		bs, _ := operand.(string)
		a, ok1 := parseTimestamp(as)
		b, ok2 := parseTimestamp(bs)
		if !ok1 || !ok2 {
			return false, nil
		}
		return compareResult(op, "Timestamp", a.Compare(b)), nil
	}
	return false, fatal(errors.New("unknown choice operator: " + op))
}

func ruleOperator(rule map[string]any) (string, any) {
	for k, v := range rule {
		switch k {
		case "Next", "Comment", "Assign", "Variable":
			continue
		}
		return k, v
	}
	return "", nil
}

func compareResult(op, prefix string, c int) bool {
	switch strings.TrimPrefix(op, prefix) {
	case "Equals":
		return c == 0
	case "LessThan":
		return c < 0
	case "GreaterThan":
		return c > 0
	case "LessThanEquals":
		return c <= 0
	case "GreaterThanEquals":
		return c >= 0
	}
	return false
}

func toBigFloat(v any) (*big.Float, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return nil, false
	}
	f, ok := new(big.Float).SetPrec(256).SetString(string(n))
	return f, ok
}

// stringMatches matches s against a pattern where '*' matches any sequence
// of characters. "\*" is a literal '*' and "\\" is a literal '\'.
func stringMatches(s, pattern string) bool {
	type tok struct {
		wild bool
		r    rune
	}
	var toks []tok
	pr := []rune(pattern)
	for i := 0; i < len(pr); i++ {
		switch {
		case pr[i] == '\\' && i+1 < len(pr):
			i++
			toks = append(toks, tok{r: pr[i]})
		case pr[i] == '*':
			toks = append(toks, tok{wild: true})
		default:
			toks = append(toks, tok{r: pr[i]})
		}
	}

	sr := []rune(s)
	// dp[j] reports whether toks[:i] matches sr[:j]
	dp := make([]bool, len(sr)+1)
	dp[0] = true
	for _, t := range toks {
		next := make([]bool, len(sr)+1)
		if t.wild {
			next[0] = dp[0]
			for j := 1; j <= len(sr); j++ {
				next[j] = dp[j] || next[j-1]
			}
		} else {
			for j := 1; j <= len(sr); j++ {
				next[j] = dp[j-1] && sr[j-1] == t.r
			}
		}
		dp = next
	}
	return dp[len(sr)]
}
