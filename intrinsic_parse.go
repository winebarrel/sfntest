package sfntest

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
