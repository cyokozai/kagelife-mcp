package spotify

import (
	"strings"
	"testing"
)

// The writer must produce what Python's json.dump(obj, indent=2, ensure_ascii=...) produces,
// so that the files written by the Go and Python versions are byte-for-byte the same.
func TestEncodeLikePythonIndent2(t *testing.T) {
	o := NewObject()
	o.Set("title", "äあ\"\\\n<&>/")
	o.Set("bpm", nil)
	o.Set("n", Int(3))
	o.Set("f", Float(4600))
	o.Set("ok", true)
	o.Set("empty", []any{})
	o.Set("eo", NewObject())
	inner := NewObject()
	inner.Set("a", "b")
	o.Set("next", []any{inner})

	got := string(Encode(o, false))
	want := `{
  "title": "äあ\"\\\n<&>/",
  "bpm": null,
  "n": 3,
  "f": 4600.0,
  "ok": true,
  "empty": [],
  "eo": {},
  "next": [
    {
      "a": "b"
    }
  ]
}`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	ascii := string(Encode("äあ😀\x01", true))
	if want := strings.ReplaceAll(`"|u00e4|u3042|ud83d|ude00|u0001"`, "|", string(rune(92))); ascii != want {
		t.Fatalf("ascii = %s", ascii)
	}
}

func TestDecodeKeepsKeyOrderAndNumbers(t *testing.T) {
	v, err := Decode([]byte(`{"z": 1, "a": [1.50, {"y": true, "b": null}], "m": "x"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := string(Encode(v, false))
	if !strings.HasPrefix(got, "{\n  \"z\": 1,\n  \"a\": [\n    1.5,\n    {\n      \"y\": true,\n      \"b\": null") {
		t.Fatalf("got %s", got)
	}
	if _, err := Decode([]byte(`{"a": 1} x`)); err == nil {
		t.Fatal("trailing data accepted")
	}
}
