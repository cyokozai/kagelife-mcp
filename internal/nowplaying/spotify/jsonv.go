package spotify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
)

// JSON values are held as: nil, bool, int64, float64, string, []any and *Object.
// Object keeps the key order, so a file read back and rewritten keeps its order, the way
// Python's dict does. Encode writes exactly what Python's json.dump(indent=2) writes.

// Object is a JSON object that remembers the order of its keys.
type Object struct {
	keys []string
	vals map[string]any
}

// NewObject returns an empty object.
func NewObject() *Object { return &Object{vals: map[string]any{}} }

// Set sets k to v. A new key goes to the end; an existing key keeps its place.
func (o *Object) Set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// Get returns the value of k and whether k is present.
func (o *Object) Get(k string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[k]
	return v, ok
}

// Val returns the value of k, or nil when it is missing (Python's dict.get).
func (o *Object) Val(k string) any {
	v, _ := o.Get(k)
	return v
}

// Len is the number of keys.
func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Clone returns a shallow copy (Python's dict(d)).
func (o *Object) Clone() *Object {
	c := NewObject()
	for _, k := range o.keys {
		c.Set(k, o.vals[k])
	}
	return c
}

// Int and Float only document the intended JSON type at call sites.
func Int(n int64) int64       { return n }
func Float(f float64) float64 { return f }

// Decode parses one JSON value. Objects keep their key order.
func Decode(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	v, err := decodeValue(d)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("invalid JSON: extra data after the value")
	}
	return v, nil
}

func decodeValue(d *json.Decoder) (any, error) {
	tok, err := d.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := NewObject()
			for d.More() {
				kt, err := d.Token()
				if err != nil {
					return nil, fmt.Errorf("invalid JSON: %w", err)
				}
				k, _ := kt.(string)
				v, err := decodeValue(d)
				if err != nil {
					return nil, err
				}
				o.Set(k, v)
			}
			if _, err := d.Token(); err != nil {
				return nil, fmt.Errorf("invalid JSON: %w", err)
			}
			return o, nil
		case '[':
			a := []any{}
			for d.More() {
				v, err := decodeValue(d)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			if _, err := d.Token(); err != nil {
				return nil, fmt.Errorf("invalid JSON: %w", err)
			}
			return a, nil
		}
		return nil, errors.New("invalid JSON")
	case json.Number:
		s := string(t)
		if !strings.ContainsAny(s, ".eE") {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return n, nil
			}
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid JSON number %q", s)
		}
		return f, nil
	default: // nil, bool, string
		return t, nil
	}
}

// Encode writes v like Python's json.dumps(v, indent=2, ensure_ascii=ascii).
func Encode(v any, ascii bool) []byte {
	var b bytes.Buffer
	encode(&b, v, ascii, 0)
	return b.Bytes()
}

func encode(b *bytes.Buffer, v any, ascii bool, depth int) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int:
		b.WriteString(strconv.Itoa(t))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case float64:
		b.WriteString(pyFloat(t))
	case string:
		encodeString(b, t, ascii)
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[")
		for i, e := range t {
			if i > 0 {
				b.WriteString(",")
			}
			newline(b, depth+1)
			encode(b, e, ascii, depth+1)
		}
		newline(b, depth)
		b.WriteString("]")
	case *Object:
		if t.Len() == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{")
		for i, k := range t.keys {
			if i > 0 {
				b.WriteString(",")
			}
			newline(b, depth+1)
			encodeString(b, k, ascii)
			b.WriteString(": ")
			encode(b, t.vals[k], ascii, depth+1)
		}
		newline(b, depth)
		b.WriteString("}")
	default:
		// not produced by this package; keep the output valid
		encodeString(b, fmt.Sprint(t), ascii)
	}
}

func newline(b *bytes.Buffer, depth int) {
	b.WriteByte('\n')
	for i := 0; i < depth; i++ {
		b.WriteString("  ")
	}
}

// pyFloat formats f like Python's repr(float).
func pyFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	a := math.Abs(f)
	if a != 0 && (a < 1e-4 || a >= 1e16) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func encodeString(b *bytes.Buffer, s string, ascii bool) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				fmt.Fprintf(b, `\u%04x`, r)
			case ascii && r > 0x7f:
				if r >= 0x10000 {
					r1, r2 := utf16.EncodeRune(r)
					fmt.Fprintf(b, `\u%04x\u%04x`, r1, r2)
				} else {
					fmt.Fprintf(b, `\u%04x`, r)
				}
			default:
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// --- Python-like helpers on decoded values --------------------------------------

// truthy is Python's bool(v).
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case int64:
		return t != 0
	case int:
		return t != 0
	case float64:
		return t != 0
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	case *Object:
		return t.Len() > 0
	}
	return true
}

// pyInt is Python's int(v or 0).
func pyInt(v any) (int64, error) {
	if !truthy(v) {
		return 0, nil
	}
	switch t := v.(type) {
	case bool:
		return 1, nil
	case int64:
		return t, nil
	case int:
		return int64(t), nil
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, fmt.Errorf("cannot convert %v to an integer", t)
		}
		return int64(t), nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid literal for int(): %q", t)
		}
		return n, nil
	}
	return 0, fmt.Errorf("int() argument must be a number, not %T", v)
}

// pyFloatOf is Python's float(v).
func pyFloatOf(v any) (float64, error) {
	switch t := v.(type) {
	case bool:
		if t {
			return 1, nil
		}
		return 0, nil
	case int64:
		return float64(t), nil
	case int:
		return float64(t), nil
	case float64:
		return t, nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, fmt.Errorf("could not convert string to float: %q", t)
		}
		return f, nil
	}
	return 0, fmt.Errorf("float() argument must be a string or a number, not %T", v)
}

// pyStr is Python's str(v) for the scalar values a JSON document can hold.
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case bool:
		if t {
			return "True"
		}
		return "False"
	case string:
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return pyFloat(t)
	}
	return string(Encode(v, false))
}

// truncRunes is Python's s[:n].
func truncRunes(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}
