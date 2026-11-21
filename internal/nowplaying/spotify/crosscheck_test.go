package spotify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCrossCheckWithPythonVersion runs testdata/crosscheck/scenario.json (song A, progress, song B,
// pause, an ad, resume, seek to the end, repeat, seek back, 204) through the Go version and compares
// now_playing.json byte for byte, after every step, with what the Python version wrote for the
// same scenario (golden.json, made by gen_golden.py with python:3.12-slim).
func TestCrossCheckWithPythonVersion(t *testing.T) {
	sb, err := os.ReadFile(filepath.Join("testdata", "crosscheck", "scenario.json"))
	if err != nil {
		t.Fatal(err)
	}
	gb, err := os.ReadFile(filepath.Join("testdata", "crosscheck", "golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	sv, err := Decode(sb)
	if err != nil {
		t.Fatal(err)
	}
	var golden []struct {
		Note    string  `json:"note"`
		Written bool    `json:"written"`
		File    *string `json:"file"`
	}
	if err := json.Unmarshal(gb, &golden); err != nil {
		t.Fatal(err)
	}
	scenario := sv.(*Object)
	start, ok := parseISO(scenario.Val("start"))
	if !ok {
		t.Fatal("bad start")
	}
	steps := scenario.Val("steps").([]any)
	if len(steps) != len(golden) {
		t.Fatalf("%d steps, %d golden", len(steps), len(golden))
	}
	_, out, _ := tmpPaths(t)
	clock := (&fakeClock{}).set(start)
	client := &fakePlayer{}
	lg, _ := testLogger()
	w := NewWatcher(client, out, clock.now, lg)
	for i, s := range steps {
		step := s.(*Object)
		adv, _ := pyFloatOf(step.Val("advance"))
		clock.t = clock.t.Add(time.Duration(adv * float64(time.Second)))
		client.current = step.Val("current")
		client.queueItems = step.Val("queue").([]any)
		p, err := w.Poll(bg)
		if err != nil {
			t.Fatalf("step %d (%s): %v", i, golden[i].Note, err)
		}
		if (p != nil) != golden[i].Written {
			t.Errorf("step %d (%s): written = %v, Python wrote %v", i, golden[i].Note, p != nil, golden[i].Written)
		}
		got, err := os.ReadFile(out)
		if golden[i].File == nil {
			if err == nil {
				t.Errorf("step %d (%s): Go wrote a file, Python did not", i, golden[i].Note)
			}
			continue
		}
		if string(got) != *golden[i].File {
			t.Errorf("step %d (%s): file differs\n--- Go\n%s\n--- Python\n%s", i, golden[i].Note, got, *golden[i].File)
		}
	}
}
