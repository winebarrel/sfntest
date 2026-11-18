package sfntest

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustDecode(t *testing.T, s string) any {
	t.Helper()
	v, err := decodeJSON([]byte(s))
	require.NoError(t, err)
	return v
}

func TestJSONPathEval(t *testing.T) {
	doc := mustDecode(t, `{
	  "a": {"b": [10, 20, 30, 40], "c": "x", "d.e": 1, "f g": 2},
	  "items": [{"n": 1, "s": "a"}, {"n": 5, "s": "b"}, {"n": 9, "s": "c", "opt": true}],
	  "obj": {"y": 2, "x": 1}
	}`)

	cases := map[string]string{
		`$`:                                  `{"a":{"b":[10,20,30,40],"c":"x","d.e":1,"f g":2},"items":[{"n":1,"s":"a"},{"n":5,"s":"b"},{"n":9,"opt":true,"s":"c"}],"obj":{"x":1,"y":2}}`,
		`$.a.b[1]`:                           `20`,
		`$.a.b[-1]`:                          `40`,
		`$['a']["c"]`:                        `"x"`,
		`$.items[?(@.s == 'it\'s')].n`:       `[]`,
		`$['it\'s']`:                         ``,
		`$.a.d\.e`:                           `1`,
		`$.a['f g']`:                         `2`,
		`$.a.b[1:3]`:                         `[20,30]`,
		`$.a.b[::2]`:                         `[10,30]`,
		`$.a.b[-2:]`:                         `[30,40]`,
		`$.a.b[::-1]`:                        `[40,30,20,10]`,
		`$.a.b[3:1:-1]`:                      `[40,30]`,
		`$.a.b[0,2,9]`:                       `[10,30]`,
		`$.obj['x','y','z']`:                 `[1,2]`,
		`$.obj.*`:                            `[1,2]`,
		`$.obj[*]`:                           `[1,2]`,
		`$.items[*].n`:                       `[1,5,9]`,
		`$..s`:                               `["a","b","c"]`,
		`$..[0]`:                             `[10,{"n":1,"s":"a"}]`,
		`$.items[?(@.n > 1 && @.n < 9)].s`:   `["b"]`,
		`$.items[?(@.n <= 1 || @.n >= 9)].s`: `["a","c"]`,
		`$.items[?(@.s == 'b')].n`:           `[5]`,
		`$.items[?(@.s != "b")].n`:           `[1,9]`,
		`$.items[?(@.opt)].n`:                `[9]`,
		`$.items[?((@.n == 1))].n`:           `[1]`,
		`$.items[?(@.s < 'b')].n`:            `[1]`,
		`$.items[?(@.s <= 'b')].n`:           `[1,5]`,
		`$.items[?(@.s > 'b')].n`:            `[9]`,
		`$.items[?(@.s >= 'b')].n`:           `[5,9]`,
		`$.items[?(@.s > 1)].n`:              `[]`,
		`$.items[?(@.n > 'a')].n`:            `[]`,
		`$.items[?(@.missing == 1)].n`:       `[]`,
		`$.items[?(@.n == @.n)].n`:           `[1,5,9]`,
		`$.items[?(@.n == true)].n`:          `[]`,
		`$.obj[?(@ > 1)]`:                    `[2]`,
		`$.a.c[0]`:                           ``,
		`$.a.c.*`:                            `[]`,
		`$.a.c[0:1]`:                         `[]`,
		`$.a.c[0,1]`:                         `[]`,
		`$.a.c[?(@)]`:                        `[]`,
		`$.a.b[0:4:0]`:                       `[]`,
	}
	for src, want := range cases {
		p, err := parsePath(src)
		require.NoError(t, err, src)
		got, err := p.eval(pathEnv{input: doc})
		if want == "" {
			assert.ErrorIs(t, err, errNoMatch, src)
			continue
		}
		require.NoError(t, err, src)
		assert.JSONEq(t, want, string(mustEncodeJSON(got)), src)
	}
}

func TestJSONPathRoots(t *testing.T) {
	sc := newScope(nil)
	sc.vars["v"] = mustDecode(t, `{"k":[1,2]}`)

	p, err := parsePath("$v.k[1]")
	require.NoError(t, err)
	got, err := p.eval(pathEnv{vars: sc})
	require.NoError(t, err)
	assert.Equal(t, json.Number("2"), got)

	p, err = parsePath("$undefined")
	require.NoError(t, err)
	_, err = p.eval(pathEnv{vars: sc})
	assert.ErrorContains(t, err, "not defined")

	p, err = parsePath("$$.Execution")
	require.NoError(t, err)
	got, err = p.eval(pathEnv{context: map[string]any{"Execution": "e"}})
	require.NoError(t, err)
	assert.Equal(t, "e", got)
}

func TestParsePathErrors(t *testing.T) {
	for _, src := range []string{
		"a", "$1x", "$.", "$..", "$[", "$[1", "$['a'", "$['a' 'b']", "$[x]", "$[1:2:3:4]", "$[a:b]",
		"$[?(@.a)", "$[?@.a]", "$[?(1)]", "$[?(@.a == )]", "$[?(@.a == 'x)]", "$[?(@[)]", "$a$b", "$.a]",
		"$[?(@.a == 1 && @[)]", "$[?(@[ || @.b)]", "$[?(@.a == 1 || @[)]", "$[?(@[ && @.b)]", "$[?(@[ == 1)]", "$[?('a' == )]",
		"$[ 'a' , ]", "$['a',x]", "$[0]x",
		"$[?(@.a == 1 && @[x])]", "$[?(@[x] && @.a)]", "$[?(@.a == 1 || @[x])]", "$[?(@[x] || @.a)]",
		"$[?(@[x] == 1)]", "$[?(1 == @[x])]", "$[?(@[x])]", "$[?('a' 'b' == 1)]",
	} {
		_, err := parsePath(src)
		assert.Error(t, err, src)
	}
}

func TestValidVarName(t *testing.T) {
	assert.True(t, validVarName("_a1"))
	assert.True(t, validVarName("日本"))
	assert.False(t, validVarName(""))
	assert.False(t, validVarName("1a"))
	assert.False(t, validVarName("a-b"))
}

func TestSetPath(t *testing.T) {
	root := mustDecode(t, `{"a":{"b":[1,{"c":2}]},"s":"str"}`)
	cases := map[string]string{
		`$`:           `9`,
		`$.x.y`:       `{"a":{"b":[1,{"c":2}]},"s":"str","x":{"y":9}}`,
		`$.a.b[-1].c`: `{"a":{"b":[1,{"c":9}]},"s":"str"}`,
		`$.a.b[0]`:    `{"a":{"b":[9,{"c":2}]},"s":"str"}`,
	}
	for src, want := range cases {
		p, err := parsePath(src)
		require.NoError(t, err)
		got, err := setPath(root, p, json.Number("9"))
		require.NoError(t, err, src)
		assert.JSONEq(t, want, string(mustEncodeJSON(got)), src)
	}
	// the root is not modified
	assert.JSONEq(t, `{"a":{"b":[1,{"c":2}]},"s":"str"}`, string(mustEncodeJSON(root)))

	for _, src := range []string{`$.s.x`, `$.a.x[0]`, `$.a.b[5]`, `$.a.b[0].x`, `$.a[*]`} {
		p, err := parsePath(src)
		require.NoError(t, err)
		_, err = setPath(root, p, 1)
		assert.Error(t, err, src)
	}
}

func TestParseCSV(t *testing.T) {
	rows, err := parseCSV([]byte("a,b\r\n\"x\ny\",\"q\"\"q\"\n\\,\\\\\\\"\\z,c\n\nlast"), ',')
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"a", "b"}, {"x\ny", `q"q`}, {`,\"z`, "c"}, {"last"}}, rows)

	_, err = parseCSV([]byte(`"open`), ',')
	assert.ErrorContains(t, err, "unclosed quote")

	assert.Nil(t, csvItems(nil, false, nil))
}

func TestJSONPointer(t *testing.T) {
	doc := mustDecode(t, `{"a/b":{"m~n":[1,{"x":2}]}}`)
	v, err := jsonPointer(doc, "/a~1b/m~0n/1/x")
	require.NoError(t, err)
	assert.Equal(t, json.Number("2"), v)

	for _, ptr := range []string{"a", "/missing", "/a~1b/m~0n/9", "/a~1b/m~0n/x", "/a~1b/m~0n/0/x"} {
		_, err := jsonPointer(doc, ptr)
		assert.Error(t, err, ptr)
	}

	_, _, err = parseJSONItems([]byte(`1`), "")
	assert.ErrorContains(t, err, "must be an array or an object")
	_, _, err = parseJSONItems([]byte(`{`), "")
	assert.Error(t, err)
	_, _, err = parseJSONItems([]byte(`{}`), "/x")
	assert.Error(t, err)
	_, err = parseJSONL([]byte("1\n{"))
	assert.ErrorContains(t, err, "line 2")
}

func TestDecompress(t *testing.T) {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte("hello"))
	_ = w.Close()

	got, err := decompress(b.Bytes())
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))

	_, err = decompress([]byte{0x1f, 0x8b, 0x00})
	assert.ErrorContains(t, err, "invalid gzip")
	_, err = decompress(append(b.Bytes()[:len(b.Bytes())-4], 0, 0, 0, 0))
	assert.ErrorContains(t, err, "invalid gzip")
	_, err = decompress([]byte{0x28, 0xb5, 0x2f, 0xfd, 0})
	assert.ErrorContains(t, err, "ZSTD")
}

func TestParseObjectErrors(t *testing.T) {
	_, err := parseObject([]byte("x"), "PARQUET", &readerConfig{}, "src")
	assert.ErrorContains(t, err, "unsupported InputType")
	_, err = parseObject([]byte("{"), "JSON", &readerConfig{}, "src")
	assert.ErrorContains(t, err, "src")
}

func TestListItem(t *testing.T) {
	item, err := listItem(map[string]any{"Key": "k", "Etag": "e", "LastModified": json.Number("5")})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"Key": "k", "Etag": "e", "LastModified": json.Number("5")}, item)

	item, err = listItem(map[string]any{"Key": "k"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"Key": "k"}, item)

	_, err = listItem(map[string]any{"LastModified": "yesterday"})
	assert.ErrorContains(t, err, "invalid LastModified")
	_, err = listItem(map[string]any{"LastModified": true})
	assert.ErrorContains(t, err, "invalid LastModified")
}

type stringer struct{ S string }

func TestNormalize(t *testing.T) {
	v, err := normalize(map[string]any{"a": []any{1, int64(2), 1.5, true, nil, "s"}})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"a": []any{json.Number("1"), json.Number("2"), json.Number("1.5"), true, nil, "s"}}, v)

	v, err = normalize([]string(nil))
	require.NoError(t, err)
	assert.Equal(t, []any{}, v)

	v, err = normalize(stringer{S: "x"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"S": "x"}, v)

	_, err = normalize(math.Inf(1))
	assert.Error(t, err)
	_, err = normalize([]any{math.NaN()})
	assert.Error(t, err)
	_, err = normalize(map[string]any{"x": math.NaN()})
	assert.Error(t, err)
	_, err = normalize(func() {})
	assert.Error(t, err)

	assert.Panics(t, func() { mustNormalize(math.Inf(1)) })
	assert.Panics(t, func() { mustEncodeJSON(func() {}) })

	_, err = decodeJSON([]byte(`1 2`))
	assert.Error(t, err)
}

func TestTypeName(t *testing.T) {
	for v, want := range map[any]string{nil: "null", "s": "string", true: "boolean", json.Number("1"): "number", 1: "int"} {
		assert.Equal(t, want, typeName(v))
	}
	assert.Equal(t, "array", typeName([]any{}))
	assert.Equal(t, "object", typeName(map[string]any{}))
}

func TestJSONEqual(t *testing.T) {
	assert.True(t, jsonEqual(json.Number("1"), json.Number("1.0")))
	assert.True(t, jsonEqual(json.Number("1e999"), json.Number("1e999")))
	assert.False(t, jsonEqual(json.Number("1"), "1"))
}

func TestErrorMatches(t *testing.T) {
	assert.False(t, errorMatches([]string{"States.ALL"}, ErrorRuntime))
	assert.False(t, errorMatches([]string{"States.ALL"}, ErrorDataLimitExceeded))
	assert.True(t, errorMatches([]string{ErrorDataLimitExceeded}, ErrorDataLimitExceeded))
	assert.True(t, errorMatches([]string{"States.TaskFailed"}, "Custom"))
	assert.False(t, errorMatches([]string{"States.TaskFailed"}, ErrorTimeout))
	assert.False(t, errorMatches([]string{"Other"}, "Custom"))

	assert.Equal(t, "E", (&StatesError{Name: "E"}).Error())
	assert.Equal(t, "E: c", (&StatesError{Name: "E", Cause: "c"}).Error())

	fe := fatal(errors.New("boom"))
	assert.Equal(t, "boom", fe.Error())
	assert.Same(t, fe, fatal(fe))
	assert.True(t, isFatal(fe))
	assert.EqualError(t, errUnsupportedState("X"), "unsupported state type: X")
}

func TestChoiceHelpers(t *testing.T) {
	assert.False(t, compareResult("NumericBogus", "Numeric", 0))
	assert.True(t, stringMatches("log-2026.txt", `log-*.txt`))
	assert.True(t, stringMatches("a*b", `a\*b`))
	assert.False(t, stringMatches("axb", `a\*b`))
	assert.True(t, stringMatches(`a\b`, `a\\b`))
	assert.True(t, stringMatches("", "*"))
	assert.False(t, stringMatches("abc", "a*d"))
	assert.False(t, isTimestamp("2026-01-01"))
	assert.False(t, isTimestamp("2026-01-01T00:00:00"))
	assert.True(t, isTimestamp("2026-01-01T00:00:00+09:00"))
}

func TestMisc(t *testing.T) {
	assert.Equal(t, "STANDARD", Standard.String())
	assert.Equal(t, "EXPRESS", Express.String())
	assert.Len(t, randomTaskToken{}.New("s"), 128)
	_, err := resolveEnv(&Env{Execution: &Execution{StateMachineArn: "bad"}})
	assert.ErrorContains(t, err, "invalid state machine ARN")
}

func TestParseIntrinsicErrors(t *testing.T) {
	for _, src := range []string{
		"States.UUID", "States.Nope()", "States.UUID(1)", "States.Array(", "States.Array(1", "States.Array(1 2)",
		"States.Format('a\\x')", "States.Format('open)", "States.Array(@)", "States.Array($[)", "States.UUID() x",
		"States.Array(States.Nope())", "States.Array(,)", "States.Array('a' 'b')",
		"States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(1)))))))))))",
	} {
		_, err := parseIntrinsic(src)
		assert.Error(t, err, src)
	}

	_, err := parseIntrinsic("States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(1))))))))))")
	assert.NoError(t, err, "10 levels of nesting are allowed")

	c, err := parseIntrinsic(`States.Array(null, true, false, -1.5, $['a(b)'], "x")`)
	if assert.Error(t, err) {
		assert.Nil(t, c)
	}
	c, err = parseIntrinsic(`States.Array(null, true, false, -1.5, $['a(b)\'c'], $.x[0])`)
	require.NoError(t, err)
	assert.Len(t, c.args, 6)
}

func callDefault(t *testing.T, name string, args ...any) (any, error) {
	t.Helper()
	return defaultIntrinsics[name](args...)
}

func TestDefaultIntrinsicErrors(t *testing.T) {
	n := func(s string) json.Number { return json.Number(s) }
	long := string(bytes.Repeat([]byte("a"), intrinsicStringLimit+1))
	cases := []struct {
		name string
		args []any
	}{
		{"States.Format", []any{1}},
		{"States.Format", []any{"{}"}},
		{"States.Format", []any{"x", "extra"}},
		{"States.StringToJson", []any{1}},
		{"States.StringToJson", []any{"{"}},
		{"States.ArrayPartition", []any{"x", n("1")}},
		{"States.ArrayPartition", []any{[]any{}, "x"}},
		{"States.ArrayPartition", []any{[]any{}, n("0")}},
		{"States.ArrayContains", []any{"x", 1}},
		{"States.ArrayRange", []any{n("1"), "x", n("1")}},
		{"States.ArrayRange", []any{n("1"), n("2"), n("0")}},
		{"States.ArrayRange", []any{n("0"), n("2000"), n("1")}},
		{"States.ArrayGetItem", []any{"x", n("0")}},
		{"States.ArrayGetItem", []any{[]any{}, "x"}},
		{"States.ArrayLength", []any{"x"}},
		{"States.ArrayUnique", []any{"x"}},
		{"States.Base64Encode", []any{1}},
		{"States.Base64Encode", []any{long}},
		{"States.Base64Decode", []any{"!!!"}},
		{"States.Base64Decode", []any{1}},
		{"States.Hash", []any{long, "MD5"}},
		{"States.Hash", []any{"a", 1}},
		{"States.Hash", []any{"a", "SHA-3"}},
		{"States.JsonMerge", []any{"x", map[string]any{}, false}},
		{"States.JsonMerge", []any{map[string]any{}, "x", false}},
		{"States.JsonMerge", []any{map[string]any{}, map[string]any{}, true}},
		{"States.MathRandom", []any{"x", n("1")}},
		{"States.MathRandom", []any{n("1"), "x"}},
		{"States.MathRandom", []any{n("1"), n("1")}},
		{"States.MathRandom", []any{n("1"), n("2"), "x"}},
		{"States.MathAdd", []any{"x", n("1")}},
		{"States.MathAdd", []any{n("3000000000"), n("1")}},
		{"States.StringSplit", []any{1, ","}},
		{"States.StringSplit", []any{"a", 1}},
	}
	for _, c := range cases {
		_, err := callDefault(t, c.name, c.args...)
		se, ok := asStatesError(err)
		if assert.True(t, ok, "%s %v", c.name, c.args) {
			assert.Equal(t, ErrorIntrinsicFailure, se.Name)
		}
	}

	_, err := argInt("f", json.Number("1e300"), math.Round)
	assert.Error(t, err)

	for _, alg := range []string{"MD5", "SHA-256", "SHA-384", "SHA-512"} {
		h, err := callDefault(t, "States.Hash", map[string]any{"a": 1}, alg)
		require.NoError(t, err)
		assert.NotEmpty(t, h)
	}

	v, err := callDefault(t, "States.MathRandom", n("1"), n("100"), n("7"))
	require.NoError(t, err)
	w, err := callDefault(t, "States.MathRandom", n("1"), n("100"), n("7"))
	require.NoError(t, err)
	assert.Equal(t, v, w)

	v, err = callDefault(t, "States.ArrayRange", n("5"), n("1"), n("-2"))
	require.NoError(t, err)
	assert.Equal(t, []any{int64(5), int64(3), int64(1)}, v)

	v, err = callDefault(t, "States.Format", `\{\} {} \\`, map[string]any{"a": 1})
	require.NoError(t, err)
	assert.Equal(t, `{} {"a":1} \`, v)
}

func TestJSONataInt(t *testing.T) {
	_, err := jsonataInt("f", "x")
	assert.Error(t, err)
	_, err = jsonataInt("f", json.Number("1e300"))
	assert.Error(t, err)
	n, err := jsonataInt("f", json.Number("2.9"))
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
}
