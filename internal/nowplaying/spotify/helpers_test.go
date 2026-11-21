package spotify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const (
	secretAccess  = "ACCESS-SECRET-aaaa"
	secretRefresh = "REFRESH-SECRET-bbbb"
)

var t0 = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

// fakeSpotify is an httptest server standing in for accounts.spotify.com and api.spotify.com.
// Each request takes the next scripted reply; every request is recorded.
type fakeSpotify struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	replies []reply
	calls   []call
}

type reply struct {
	status  int
	body    any // marshalled with encoding/json; nil means an empty body
	headers map[string]string
	raw     string // used instead of body when set
}

type call struct {
	method string
	path   string
	header http.Header
	form   url.Values
}

func newFakeSpotify(t *testing.T, replies ...reply) *fakeSpotify {
	f := &fakeSpotify{t: t, replies: replies}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSpotify) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	f.mu.Lock()
	f.calls = append(f.calls, call{r.Method, r.URL.Path, r.Header.Clone(), form})
	if len(f.replies) == 0 {
		f.mu.Unlock()
		f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(599)
		return
	}
	rp := f.replies[0]
	f.replies = f.replies[1:]
	f.mu.Unlock()
	for k, v := range rp.headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(rp.status)
	switch {
	case rp.raw != "":
		_, _ = io.WriteString(w, rp.raw)
	case rp.body != nil:
		_ = json.NewEncoder(w).Encode(rp.body)
	}
}

func (f *fakeSpotify) Calls() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func (f *fakeSpotify) tokenURL() string { return f.srv.URL + "/api/token" }
func (f *fakeSpotify) apiBase() string  { return f.srv.URL + "/v1" }

// testLogger collects everything that is logged, at debug level.
func testLogger() (*Logger, *syncBuffer) {
	b := &syncBuffer{}
	return NewLogger(b, true), b
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func tmpPaths(t *testing.T) (dir, out, token string) {
	dir = t.TempDir()
	return dir, filepath.Join(dir, "now_playing.json"), filepath.Join(dir, "cfg", "spotify-token.json")
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func obj(kv ...any) *Object {
	o := NewObject()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

var bg = context.Background()
