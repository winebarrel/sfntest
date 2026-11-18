package sfntest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
)

// decodeJSON decodes a single JSON value. Numbers are kept as json.Number.
func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("invalid JSON: unexpected data after the value")
	}
	return v, nil
}

// encodeJSON encodes v as compact JSON without HTML escaping.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func mustEncodeJSON(v any) []byte {
	b, err := encodeJSON(v)
	if err != nil {
		panic(err)
	}
	return b
}

func jsonEqual(a, b any) bool {
	an, aok := a.(json.Number)
	bn, bok := b.(json.Number)
	if aok && bok {
		af, err1 := an.Float64()
		bf, err2 := bn.Float64()
		if err1 == nil && err2 == nil {
			return af == bf
		}
		return an == bn
	}
	return bytes.Equal(mustEncodeJSON(a), mustEncodeJSON(b))
}

// normalize converts a Go value into the JSON value representation used
// internally (map[string]any, []any, string, bool, nil, json.Number).
func normalize(v any) (any, error) {
	switch t := v.(type) {
	case nil, string, bool, json.Number:
		return t, nil
	case int:
		return json.Number(strconv.Itoa(t)), nil
	case int64:
		return json.Number(strconv.FormatInt(t, 10)), nil
	case float64:
		return floatNumber(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			n, err := normalize(e)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			n, err := normalize(e)
			if err != nil {
				return nil, err
			}
			out[k] = n
		}
		return out, nil
	}

	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Slice && rv.IsNil() {
		return []any{}, nil
	}

	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return decodeJSON(b)
}

func floatNumber(f float64) (json.Number, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", fmt.Errorf("number is not finite: %v", f)
	}
	return json.Number(strconv.FormatFloat(f, 'f', -1, 64)), nil
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}
