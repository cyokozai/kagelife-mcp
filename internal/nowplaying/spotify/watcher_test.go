package spotify

import (
	"bytes"
	"context"
	"errors"
	"os"
	"regexp"
	"testing"
	"time"
)

// --- fakes -------------------------------------------------------------------------

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time             { return c.t }
func (c *fakeClock) advance(sec float64)        { c.t = c.t.Add(time.Duration(sec * float64(time.Second))) }
func newClock() *fakeClock                      { return &fakeClock{t: t0} }
func (c *fakeClock) set(t time.Time) *fakeClock { c.t = t; return c }

// fakePlayer stands in for Client: CurrentlyPlaying and Queue return scripted values.
type fakePlayer struct {
	current    any
	currentErr error
	queueItems []any
	queueFn    func() (any, error)
	queueCalls int
}

func (f *fakePlayer) CurrentlyPlaying(context.Context) (any, error) {
	if f.currentErr != nil {
		return nil, f.currentErr
	}
	return f.current, nil
}

func (f *fakePlayer) Queue(context.Context) (any, error) {
	f.queueCalls++
	if f.queueFn != nil {
		return f.queueFn()
	}
	return obj("currently_playing", nil, "queue", append([]any{}, f.queueItems...)), nil
}

func trackItemFull(id, name string, artists []string, durationMS int64, album string) *Object {
	as := []any{}
	for _, a := range artists {
		as = append(as, obj("name", a))
	}
	return obj("type", "track", "id", id, "name", name, "duration_ms", durationMS,
		"album", obj("name", album), "artists", as)
}

func trackItem(id, name string) *Object {
	return trackItemFull(id, name, []string{"Artist A"}, 200000, "Album A")
}

func song() *Object { return trackItem("t1", "Song A") }

func playingP(progress int64, item *Object, isPlaying bool) *Object {
	if item == nil {
		item = song()
	}
	return obj("currently_playing_type", "track", "is_playing", isPlaying, "progress_ms", progress, "item", item)
}

func playing(progress int64) *Object { return playingP(progress, nil, true) }

type wfix struct {
	t      *testing.T
	out    string
	clock  *fakeClock
	client *fakePlayer
}

func newWfix(t *testing.T) *wfix {
	_, out, _ := tmpPaths(t)
	return &wfix{t: t, out: out, clock: newClock(), client: &fakePlayer{}}
}

func (f *wfix) watcher() *Watcher {
	lg, _ := testLogger()
	return NewWatcher(f.client, f.out, f.clock.now, lg)
}

func (f *wfix) poll(w *Watcher) *Object {
	f.t.Helper()
	p, err := w.Poll(bg)
	if err != nil {
		f.t.Fatalf("poll: %v", err)
	}
	return p
}

func (f *wfix) read() map[string]any { return readJSON(f.t, f.out) }
func (f *wfix) hid() any             { return f.read()["history_id"] }

// --- tests ---------------------------------------------------------------------------

// Python: WatcherTest.test_first_track_is_written_with_compatible_schema
func TestFirstTrackIsWrittenWithCompatibleSchema(t *testing.T) {
	f := newWfix(t)
	f.client.current = playingP(30000, trackItemFull("t1", "Song A", []string{"A", "B"}, 200000, "Album A"), true)
	for i := 0; i < 5; i++ {
		f.client.queueItems = append(f.client.queueItems, trackItem("n"+string(rune('0'+i)), "Next "+string(rune('0'+i))))
	}
	if f.poll(f.watcher()) == nil {
		t.Fatal("not written")
	}
	b, _ := os.ReadFile(f.out)
	// key order and formatting are those of the Python version (json.dump(indent=2))
	want := `{
  "title": "Song A",
  "artist": "A, B",
  "bpm": null,
  "key": null,
  "genre": null,
  "played_at": "2026-11-21T11:59:30.000+00:00",
  "detected_at": "2026-11-21T12:00:00.000+00:00",
  "source": "spotify",
  "history_id": "sp-t1-1795262370000",
  "content_id": "t1",
  "album": "Album A",
  "duration_ms": 200000,
  "progress_ms": 30000,
  "is_playing": true,
  "fetched_at": "2026-11-21T12:00:00.000+00:00",
  "next": [
    {
      "title": "Next 0",
      "artist": "Artist A",
      "duration_ms": 200000,
      "track_id": "n0"
    },
    {
      "title": "Next 1",
      "artist": "Artist A",
      "duration_ms": 200000,
      "track_id": "n1"
    },
    {
      "title": "Next 2",
      "artist": "Artist A",
      "duration_ms": 200000,
      "track_id": "n2"
    }
  ]
}
`
	if string(b) != want {
		t.Fatalf("got\n%s", b)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9-]+$`).MatchString(f.hid().(string)) {
		t.Fatal("history_id has other characters")
	}
	st, _ := os.Stat(f.out)
	if st.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %o", st.Mode().Perm())
	}
}

// Python: WatcherTest.test_same_track_progressing_is_not_rewritten
func TestSameTrackProgressingIsNotRewritten(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(30000)
	w := f.watcher()
	f.poll(w)
	before, _ := os.Stat(f.out)
	f.clock.advance(3)
	f.client.current = playing(33000)
	if f.poll(w) != nil {
		t.Fatal("rewritten")
	}
	after, _ := os.Stat(f.out)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("file touched")
	}
}

// Python: WatcherTest.test_new_track_gets_new_history_id
func TestNewTrackGetsNewHistoryID(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(30000)
	w := f.watcher()
	f.poll(w)
	h1 := f.hid()
	f.clock.advance(3)
	f.client.current = playingP(1000, trackItem("t2", "Song B"), true)
	if f.poll(w) == nil {
		t.Fatal("not written")
	}
	if d := f.read(); d["title"] != "Song B" || d["history_id"] == h1 {
		t.Fatalf("d = %v", d)
	}
}

// Python: WatcherTest.test_seek_back_and_forward_keeps_history_id
func TestSeekBackAndForwardKeepsHistoryID(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(100000)
	w := f.watcher()
	f.poll(w)
	h1 := f.hid()
	for _, p := range []int64{5000, 150000, 2000} {
		f.clock.advance(3)
		f.client.current = playing(p)
		if f.poll(w) != nil || f.hid() != h1 {
			t.Fatalf("seek to %d changed the history_id", p)
		}
	}
}

// Python: WatcherTest.test_repeat_after_near_end_gets_new_history_id
func TestRepeatAfterNearEndGetsNewHistoryID(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(195000)
	w := f.watcher()
	f.poll(w)
	h1 := f.hid()
	f.clock.advance(6)
	f.client.current = playing(1000)
	if f.poll(w) == nil {
		t.Fatal("repeat not written")
	}
	h2 := f.hid()
	if h2 == h1 {
		t.Fatal("same history_id")
	}
	f.clock.advance(3)
	f.client.current = playing(4000)
	if f.poll(w) != nil || f.hid() != h2 {
		t.Fatal("second play lost its id")
	}
}

// Python: WatcherTest.test_repeat_after_long_gap_gets_new_history_id
func TestRepeatAfterLongGapGetsNewHistoryID(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(100000)
	w := f.watcher()
	f.poll(w)
	h1 := f.hid()
	f.clock.advance(110)
	f.client.current = playing(8000)
	if f.poll(w) == nil || f.hid() == h1 {
		t.Fatal("repeat after a long gap not detected")
	}
}

// Python: WatcherTest.test_paused_time_is_not_counted_as_playback
func TestPausedTimeIsNotCountedAsPlayback(t *testing.T) {
	f := newWfix(t)
	f.client.current = playingP(100000, nil, false)
	w := f.watcher()
	f.poll(w)
	h1 := f.hid()
	f.clock.advance(300)
	f.client.current = playingP(1000, nil, false)
	if f.poll(w) != nil || f.hid() != h1 {
		t.Fatal("paused time counted")
	}
}

// Python: WatcherTest.test_is_playing_change_is_written_without_new_history
func TestIsPlayingChangeIsWrittenWithoutNewHistory(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(30000)
	w := f.watcher()
	f.poll(w)
	h1 := f.hid()
	f.clock.advance(3)
	f.client.current = playingP(31000, nil, false)
	if f.poll(w) == nil {
		t.Fatal("not written")
	}
	if d := f.read(); d["is_playing"] != false || d["history_id"] != h1 {
		t.Fatalf("d = %v", d)
	}
}

// Python: WatcherTest.test_next_change_is_written
func TestNextChangeIsWritten(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(30000)
	f.client.queueItems = []any{trackItem("n1", "x")}
	w := f.watcher()
	f.poll(w)
	f.clock.advance(3)
	f.client.current = playing(33000)
	f.client.queueItems = []any{trackItem("n2", "x")}
	if f.poll(w) == nil {
		t.Fatal("not written")
	}
	if got := f.read()["next"].([]any)[0].(map[string]any)["track_id"]; got != "n2" {
		t.Fatalf("next = %v", got)
	}
}

// Python: WatcherTest.test_204_marks_not_playing_once
func Test204MarksNotPlayingOnce(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(30000)
	w := f.watcher()
	f.poll(w)
	h1 := f.hid()
	f.clock.advance(3)
	f.client.current = nil
	if f.poll(w) == nil {
		t.Fatal("not written")
	}
	if d := f.read(); d["is_playing"] != false || d["history_id"] != h1 {
		t.Fatalf("d = %v", d)
	}
	f.clock.advance(3)
	if f.poll(w) != nil {
		t.Fatal("written twice")
	}
}

// Python: WatcherTest.test_204_with_nothing_known_writes_nothing
func Test204WithNothingKnownWritesNothing(t *testing.T) {
	f := newWfix(t)
	if f.poll(f.watcher()) != nil {
		t.Fatal("written")
	}
	if _, err := os.Stat(f.out); err == nil {
		t.Fatal("file exists")
	}
}

// Python: WatcherTest.test_episode_and_ad_are_ignored
func TestEpisodeAndAdAreIgnored(t *testing.T) {
	f := newWfix(t)
	w := f.watcher()
	for _, cur := range []*Object{
		obj("currently_playing_type", "episode", "is_playing", true, "progress_ms", int64(1000), "item", nil),
		obj("currently_playing_type", "ad", "is_playing", true, "progress_ms", int64(1000), "item", nil),
		obj("currently_playing_type", "episode", "is_playing", true, "progress_ms", int64(1000),
			"item", obj("type", "episode", "id", "e1", "name", "Ep")),
	} {
		f.client.current = cur
		if f.poll(w) != nil {
			t.Fatal("written")
		}
	}
	if _, err := os.Stat(f.out); err == nil {
		t.Fatal("file exists")
	}
}

// Python: WatcherTest.test_local_file_is_ignored_and_last_track_kept
func TestLocalFileIsIgnoredAndLastTrackKept(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(30000)
	w := f.watcher()
	f.poll(w)
	before, _ := os.ReadFile(f.out)
	f.clock.advance(3)
	local := song().Clone()
	local.Set("id", nil)
	local.Set("is_local", true)
	local.Set("uri", "spotify:local:a:b:c:200")
	f.client.current = playingP(1000, local, true)
	if f.poll(w) != nil {
		t.Fatal("written")
	}
	after, _ := os.ReadFile(f.out)
	if !bytes.Equal(before, after) {
		t.Fatal("file changed")
	}
}

// Python: WatcherTest.test_queue_skips_episodes
func TestQueueSkipsEpisodes(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(30000)
	f.client.queueItems = []any{obj("type", "episode", "id", "e1", "name", "Ep"), trackItem("n1", "x")}
	f.poll(f.watcher())
	nx := f.read()["next"].([]any)
	if len(nx) != 1 || nx[0].(map[string]any)["track_id"] != "n1" {
		t.Fatalf("next = %v", nx)
	}
}

// Python: WatcherTest.test_queue_failure_keeps_previous_next
func TestQueueFailureKeepsPreviousNext(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(30000)
	f.client.queueItems = []any{trackItem("n1", "x")}
	w := f.watcher()
	f.poll(w)
	f.client.queueFn = func() (any, error) { return nil, &APIError{Status: 503} }
	f.clock.advance(3)
	f.client.current = playing(33000)
	if f.poll(w) != nil {
		t.Fatal("written")
	}
	if got := f.read()["next"].([]any)[0].(map[string]any)["track_id"]; got != "n1" {
		t.Fatalf("next = %v", got)
	}
}

// Python: WatcherTest.test_restart_does_not_rewrite_same_song
func TestRestartDoesNotRewriteSameSong(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(30000)
	f.poll(f.watcher())
	before, _ := os.ReadFile(f.out)
	h1 := f.hid()
	f.clock.advance(5)
	f.client.current = playing(35000)
	w2 := f.watcher()
	if f.poll(w2) != nil {
		t.Fatal("rewritten after restart")
	}
	after, _ := os.ReadFile(f.out)
	if !bytes.Equal(before, after) {
		t.Fatal("file changed")
	}
	f.clock.advance(3)
	f.client.current = playingP(38000, nil, false)
	f.poll(w2)
	if f.hid() != h1 {
		t.Fatal("restored watcher lost the history_id")
	}
}

// Python: WatcherTest.test_rekordbox_file_is_replaced
func TestRekordboxFileIsReplaced(t *testing.T) {
	f := newWfix(t)
	_ = os.WriteFile(f.out, []byte(`{"title": "x", "history_id": "123", "content_id": "t1", "source": "rekordbox-history"}`), 0o644)
	f.client.current = playing(30000)
	if f.poll(f.watcher()) == nil {
		t.Fatal("not written")
	}
	if d := f.read(); d["source"] != "spotify" || d["history_id"] == "123" {
		t.Fatalf("d = %v", d)
	}
}

// Python: WatcherTest.test_observe_does_not_write
func TestObserveDoesNotWrite(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(30000)
	p, err := f.watcher().Observe(bg)
	if err != nil || p.Val("title") != "Song A" {
		t.Fatalf("p = %v, err = %v", p, err)
	}
	if _, err := os.Stat(f.out); err == nil {
		t.Fatal("written")
	}
}

// Python: WatcherTest.test_repeat_seen_late_after_near_end_gets_new_history_id
func TestRepeatSeenLateAfterNearEndGetsNewHistoryID(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(195000)
	w := f.watcher()
	f.poll(w)
	h1 := f.hid()
	f.clock.advance(25)
	f.client.current = playing(20000)
	if f.poll(w) == nil || f.hid() == h1 {
		t.Fatal("late repeat not detected")
	}
}

// Python: WatcherTest.test_seek_back_far_from_start_after_long_gap_keeps_history_id
func TestSeekBackFarFromStartAfterLongGapKeepsHistoryID(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(100000)
	w := f.watcher()
	f.poll(w)
	h1 := f.hid()
	f.clock.advance(100)
	f.client.current = playing(60000)
	if f.poll(w) != nil || f.hid() != h1 {
		t.Fatal("history_id changed")
	}
}

// Python: WatcherTest.test_queue_rate_limited_on_new_track_keeps_track_change
func TestQueueRateLimitedOnNewTrackKeepsTrackChange(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(30000)
	w := f.watcher()
	f.poll(w)
	h1 := f.hid()
	f.client.queueFn = func() (any, error) { return nil, &RateLimitedError{RetryAfter: 10} }
	f.clock.advance(3)
	f.client.current = playingP(1000, trackItem("t2", "Song B"), true)
	_, err := w.Poll(bg)
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v", err)
	}
	if f.read()["title"] != "Song A" {
		t.Fatal("written on 429")
	}
	f.client.queueFn = nil
	f.clock.advance(10)
	f.client.current = playingP(11000, trackItem("t2", "Song B"), true)
	if f.poll(w) == nil {
		t.Fatal("not written")
	}
	if d := f.read(); d["title"] != "Song B" || d["history_id"] == h1 {
		t.Fatalf("d = %v", d)
	}
}

// Python: WatcherTest.test_failed_write_is_retried_with_new_history_id
func TestFailedWriteIsRetriedWithNewHistoryID(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(30000)
	w := f.watcher()
	f.poll(w)
	h1 := f.hid()
	w.write = func(string, *Object) error { return errors.New("disk full") }
	f.clock.advance(3)
	f.client.current = playingP(1000, trackItem("t2", "Song B"), true)
	if _, err := w.Poll(bg); err == nil {
		t.Fatal("no error")
	}
	w.write = writePayload
	f.clock.advance(3)
	f.client.current = playingP(4000, trackItem("t2", "Song B"), true)
	if f.poll(w) == nil {
		t.Fatal("not written")
	}
	if d := f.read(); d["title"] != "Song B" || d["history_id"] == h1 {
		t.Fatalf("d = %v", d)
	}
}

// Python: WatcherTest.test_204_keeps_played_at_consistent
func Test204KeepsPlayedAtConsistent(t *testing.T) {
	f := newWfix(t)
	f.client.current = playing(30000)
	w := f.watcher()
	f.poll(w)
	f.clock.advance(3)
	f.client.current = playing(33000)
	f.poll(w)
	f.clock.advance(3)
	f.client.current = nil
	f.poll(w)
	d := f.read()
	played, _ := parseISO(d["played_at"])
	fetched, _ := parseISO(d["fetched_at"])
	if d["is_playing"] != false || d["progress_ms"] != float64(33000) ||
		fetched.Sub(played) != 33*time.Second {
		t.Fatalf("d = %v", d)
	}
}

// Python: WatcherTest.test_unexpected_json_shapes_do_not_crash
func TestUnexpectedJSONShapesDoNotCrash(t *testing.T) {
	f := newWfix(t)
	w := f.watcher()
	f.client.current = []any{"not", "a", "dict"}
	if f.poll(w) != nil {
		t.Fatal("written")
	}
	item := song().Clone()
	item.Set("artists", "nobody")
	f.client.current = playingP(30000, item, true)
	f.client.queueFn = func() (any, error) { return []any{"not", "a", "dict"}, nil }
	if f.poll(w) == nil {
		t.Fatal("not written")
	}
	d := f.read()
	if d["artist"] != "" || len(d["next"].([]any)) != 0 {
		t.Fatalf("d = %v", d)
	}
}

// Python: WatcherTest.test_restart_with_broken_next_in_file
func TestRestartWithBrokenNextInFile(t *testing.T) {
	f := newWfix(t)
	_ = os.WriteFile(f.out, []byte(`{"source": "spotify", "history_id": "sp-t1-1", "content_id": "t1",
		"fetched_at": "2026-11-21T12:00:00+00:00", "progress_ms": 30000,
		"duration_ms": 200000, "is_playing": true, "next": ["x", 3]}`), 0o644)
	f.client.current = playing(31000)
	f.poll(f.watcher())
	if f.hid() != "sp-t1-1" {
		t.Fatalf("history_id = %v", f.hid())
	}
}

// history_id is computed the way Python's int(started.timestamp() * 1000) computes it,
// including its float rounding, so both versions give the same id for the same play.
func TestHistoryIDMatchesPythonFloatMath(t *testing.T) {
	cases := map[time.Time]string{
		time.Date(2026, 11, 21, 11, 59, 30, 0, time.UTC):         "sp-x-1795262370000",
		time.Date(2026, 11, 21, 11, 59, 30, 123456000, time.UTC): "sp-x-1795262370123",
	}
	for tm, want := range cases {
		if got := makeHistoryID("x", tm); got != want {
			t.Errorf("%v: %s, want %s", tm, got, want)
		}
	}
}

func TestParseISO(t *testing.T) {
	for _, s := range []string{"2026-11-21T12:00:00.000+00:00", "2026-11-21T12:00:00+00:00",
		"2026-11-21T21:00:00+09:00", "2026-11-21T12:00:00Z", "2026-11-21T12:00:00", "2026-11-21 12:00:00.5"} {
		got, ok := parseISO(s)
		if !ok || !got.Truncate(time.Second).Equal(t0) {
			t.Errorf("%s: %v %v", s, got, ok)
		}
	}
	for _, v := range []any{"nope", int64(3), nil} {
		if _, ok := parseISO(v); ok {
			t.Errorf("%v parsed", v)
		}
	}
}
