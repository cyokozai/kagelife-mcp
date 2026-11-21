package spotify

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PlayerClient is what the watcher reads (Client in production).
type PlayerClient interface {
	CurrentlyPlaying(ctx context.Context) (any, error)
	Queue(ctx context.Context) (any, error)
}

// iso formats like Python's dt.astimezone(utc).isoformat(timespec="milliseconds").
func iso(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "+00:00"
}

// parseISO is a subset of Python's datetime.fromisoformat; a time without a zone is UTC.
func parseISO(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
		for _, in := range []string{s, strings.Replace(s, " ", "T", 1)} {
			if t, err := time.Parse(layout, in); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// writePayload writes p as JSON atomically (tmp file in the same dir, then rename), mode 0644.
func writePayload(path string, p *Object) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	return writeFileAtomic(path, append(Encode(p, false), '\n'), 0o644)
}

func artistNames(item *Object) string {
	as, ok := item.Val("artists").([]any)
	if !ok {
		return ""
	}
	var names []string
	for _, a := range as {
		if o, ok := a.(*Object); ok && truthy(o.Val("name")) {
			names = append(names, pyStr(o.Val("name")))
		}
	}
	return strings.Join(names, ", ")
}

func getOr(o *Object, k string, def any) any {
	if v, ok := o.Get(k); ok {
		return v
	}
	return def
}

func nextTracks(queue any) []any {
	out := []any{}
	q, _ := queue.(*Object)
	items, _ := q.Val("queue").([]any)
	for _, it := range items {
		o, ok := it.(*Object)
		if !ok || o.Val("type") != "track" || !truthy(o.Val("id")) {
			continue
		}
		out = append(out, obj4("title", getOr(o, "name", ""), "artist", artistNames(o),
			"duration_ms", o.Val("duration_ms"), "track_id", o.Val("id")))
		if len(out) >= NextCount {
			break
		}
	}
	return out
}

func obj4(k1 string, v1 any, k2 string, v2 any, k3 string, v3 any, k4 string, v4 any) *Object {
	o := NewObject()
	o.Set(k1, v1)
	o.Set(k2, v2)
	o.Set(k3, v3)
	o.Set(k4, v4)
	return o
}

type observation struct {
	trackID    string
	progressMS int64
	durationMS int64
	isPlaying  bool
	fetchedAt  time.Time
}

// trackOf returns the track item of a currently-playing response, or nil (nothing, episode, ad,
// local file). Only string track IDs count as tracks.
func trackOf(cur any) *Object {
	c, ok := cur.(*Object)
	if !ok {
		return nil
	}
	if t, ok := c.Get("currently_playing_type"); ok && t != "track" {
		return nil
	}
	item, ok := c.Val("item").(*Object)
	if !ok || item.Val("type") != "track" {
		return nil
	}
	if id, ok := item.Val("id").(string); !ok || id == "" {
		return nil
	}
	return item
}

// isNewPlay decides whether obs starts a new play (a new history_id). See README for the rules.
//
// Same track: a new play only when the progress went backwards, the previous play would have
// reached within EndWindowMS of its end by now, and obs is no further from the start than the
// time since that expected end (+ StartWindowMS). Anything else is a seek.
func isNewPlay(prev *observation, obs observation) bool {
	if prev == nil || prev.trackID != obs.trackID {
		return true
	}
	if obs.progressMS >= prev.progressMS {
		return false
	}
	elapsed := 0.0
	if prev.isPlaying {
		elapsed = math.Max(0, float64(obs.fetchedAt.Sub(prev.fetchedAt).Microseconds())/1e6*1000)
	}
	expected := float64(prev.progressMS) + elapsed
	if expected < float64(prev.durationMS-EndWindowMS) {
		return false
	}
	sinceEnd := math.Max(0, expected-float64(prev.durationMS))
	return float64(obs.progressMS) <= sinceEnd+StartWindowMS
}

// makeHistoryID is "sp-<track id>-<ms>" with the ms computed like Python's
// int(started.timestamp() * 1000): microseconds / 1e6 as a float, times 1000, truncated.
// Letters, digits and '-' only: CONDUCTOR.md uses it as a file name.
func makeHistoryID(trackID string, started time.Time) string {
	sec := float64(started.UnixMicro()) / 1e6
	return fmt.Sprintf("sp-%s-%d", trackID, int64(sec*1000))
}

// Watcher polls Spotify and writes now_playing.json only when something a reader cares about
// changed: a new history_id, a change of is_playing, a change of `next`. Progress alone
// (including seeks inside the same play) is not written.
type Watcher struct {
	client  PlayerClient
	OutPath string
	clock   func() time.Time
	log     *Logger
	write   func(string, *Object) error

	payload *Object
	prev    *observation
	next    []any
}

// NewWatcher makes a watcher and picks up the last now_playing.json it wrote, if any.
func NewWatcher(client PlayerClient, outPath string, clock func() time.Time, log *Logger) *Watcher {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
	}
	w := &Watcher{client: client, OutPath: outPath, clock: clock, log: log, write: writePayload, next: []any{}}
	w.loadLast()
	return w
}

func (w *Watcher) loadLast() {
	b, err := os.ReadFile(w.OutPath)
	if err != nil {
		return
	}
	v, err := Decode(b)
	if err != nil {
		return
	}
	data, ok := v.(*Object)
	if !ok || data.Val("source") != SourceName {
		return
	}
	fetched, ok := parseISO(data.Val("fetched_at"))
	if !truthy(data.Val("content_id")) || !truthy(data.Val("history_id")) || !ok {
		return
	}
	progress, err1 := pyInt(data.Val("progress_ms"))
	duration, err2 := pyInt(data.Val("duration_ms"))
	if err1 != nil || err2 != nil {
		return
	}
	next := []any{}
	if nx, ok := data.Val("next").([]any); ok {
		for _, n := range nx {
			if o, ok := n.(*Object); ok {
				next = append(next, o)
			}
		}
	}
	w.payload = data
	w.next = next
	w.prev = &observation{pyStr(data.Val("content_id")), progress, duration, truthy(data.Val("is_playing")), fetched}
}

// key is what a reader cares about: history_id, is_playing and the track IDs of next.
func payloadKey(p *Object) string {
	if p == nil {
		return ""
	}
	nx, _ := p.Val("next").([]any)
	ids := []any{}
	for _, n := range nx {
		if o, ok := n.(*Object); ok {
			ids = append(ids, o.Val("track_id"))
		}
	}
	return "1" + string(Encode([]any{p.Val("history_id"), p.Val("is_playing"), ids}, false))
}

// fetchNext reads the next tracks. 429 and LoginRequired propagate: the whole poll is retried later.
func (w *Watcher) fetchNext(ctx context.Context) ([]any, error) {
	q, err := w.client.Queue(ctx)
	if err == nil {
		return nextTracks(q), nil
	}
	var rl *RateLimitedError
	var lr *LoginRequiredError
	if errors.As(err, &rl) || errors.As(err, &lr) || ctx.Err() != nil {
		return nil, err
	}
	w.log.Warnf("could not read the queue (%v): keeping the previous one", err)
	return w.next, nil
}

// Observe reads Spotify once and returns the payload that would be written (does not write).
func (w *Watcher) Observe(ctx context.Context) (*Object, error) {
	p, _, _, err := w.observe(ctx)
	return p, err
}

// observe reads Spotify once and returns (payload, prev, next) without changing w. The caller
// commits prev/next only after the payload was handled, so a failure halfway (a 429 on the
// queue, a failed write) cannot pair a new track with the old history_id on the next poll.
func (w *Watcher) observe(ctx context.Context) (*Object, *observation, []any, error) {
	cur, err := w.client.CurrentlyPlaying(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	now := w.clock()
	item := trackOf(cur)
	if item == nil {
		if cur != nil {
			kind := any(fmt.Sprintf("%T", cur))
			if c, ok := cur.(*Object); ok {
				kind = c.Val("currently_playing_type")
			}
			w.log.Debugf("ignoring non-track playback (%s)", pyStr(kind))
			return w.payload, w.prev, w.next, nil
		}
		// nothing playing (204): keep the last track and mark it stopped. progress_ms and
		// fetched_at stay those of the last time the track was seen, so played_at still
		// equals fetched_at - progress_ms.
		if w.payload == nil {
			return nil, w.prev, w.next, nil
		}
		p := w.payload.Clone()
		p.Set("is_playing", false)
		prev := w.prev
		if prev != nil {
			c := *prev
			c.isPlaying = false
			prev = &c
		}
		return p, prev, w.next, nil
	}
	c := cur.(*Object)
	progress, err := pyInt(c.Val("progress_ms"))
	if err != nil {
		return nil, nil, nil, err
	}
	duration, err := pyInt(item.Val("duration_ms"))
	if err != nil {
		return nil, nil, nil, err
	}
	obs := observation{item.Val("id").(string), progress, duration, truthy(c.Val("is_playing")), now}
	started := now.Add(-time.Duration(progress) * time.Millisecond)
	var historyID, detectedAt any
	if isNewPlay(w.prev, obs) || w.payload == nil {
		historyID, detectedAt = makeHistoryID(obs.trackID, started), iso(now)
	} else {
		historyID, detectedAt = w.payload.Val("history_id"), getOr(w.payload, "detected_at", iso(now))
	}
	next, err := w.fetchNext(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	album := any("")
	if a, ok := item.Val("album").(*Object); ok {
		album = getOr(a, "name", "")
	}
	p := NewObject()
	p.Set("title", getOr(item, "name", ""))
	p.Set("artist", artistNames(item))
	p.Set("bpm", nil)
	p.Set("key", nil)
	p.Set("genre", nil)
	p.Set("played_at", iso(started))
	p.Set("detected_at", detectedAt)
	p.Set("source", SourceName)
	p.Set("history_id", historyID)
	p.Set("content_id", obs.trackID)
	p.Set("album", album)
	p.Set("duration_ms", obs.durationMS)
	p.Set("progress_ms", obs.progressMS)
	p.Set("is_playing", obs.isPlaying)
	p.Set("fetched_at", iso(now))
	p.Set("next", next)
	return p, &obs, next, nil
}

// Poll observes once; it writes and returns the payload only if it changed.
func (w *Watcher) Poll(ctx context.Context) (*Object, error) {
	p, prev, next, err := w.observe(ctx)
	if err != nil {
		return nil, err
	}
	changed := p != nil && payloadKey(p) != payloadKey(w.payload)
	if changed {
		if err := w.write(w.OutPath, p); err != nil {
			return nil, err // nothing below is committed
		}
	}
	// commit: from here on the new observation is the reference for the next poll.
	// Unchanged payloads are kept in memory only (newest progress); the file is not rewritten.
	w.prev, w.next = prev, next
	if p != nil {
		w.payload = p
	}
	if changed {
		return p, nil
	}
	return nil, nil
}
