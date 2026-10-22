package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// fakeControl は制御口 v1 契約どおりに振る舞う httptest のフェイク。
type fakeControl struct {
	t     *testing.T
	srv   *httptest.Server
	token string

	mu          sync.Mutex
	active      string
	order       []string
	sources     map[string]string
	bpm         float64 // 常に 40〜300。タップ前は既定の 120（ADR-006、ADR-008 D3）
	bpmMeasured bool    // bpm がタップか set_bpm で設定された値なら true
	beats       float64
	fading      bool
	target      string
	lastErr     map[string]string
	requests    []string // "METHOD /path" の記録

	// override が非 nil なら全リクエストをこれで処理する（異常系の注入用）。
	override http.HandlerFunc
}

var fakeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func newFakeControl(t *testing.T) *fakeControl {
	t.Helper()
	f := &fakeControl{
		t:       t,
		token:   strings.Repeat("ab", 32),
		active:  "01_uv",
		order:   []string{"01_uv", "02_time_sin"},
		sources: map[string]string{"01_uv": "//kage:unit pixels\npackage main\n", "02_time_sin": "//kage:unit pixels\npackage main\n// sin\n"},
		bpm:     120,
		beats:   4,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/state", f.state)
	mux.HandleFunc("GET /v1/shaders/{name}", f.getShader)
	mux.HandleFunc("PUT /v1/shaders/{name}", f.putShader)
	mux.HandleFunc("POST /v1/switch", f.switchShader)
	mux.HandleFunc("POST /v1/crossfade", f.crossfade)
	mux.HandleFunc("POST /v1/bpm", f.setBPM)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		ov := f.override
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			writeFakeErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		if ov != nil {
			ov(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeControl) addr() string { return f.srv.Listener.Addr().String() }

// writeDiscovery は発見ファイルを書き、そのパスを返す。
func (f *fakeControl) writeDiscovery(t *testing.T, dir string) string {
	t.Helper()
	return writeDiscoveryFile(t, dir, f.addr(), f.token)
}

func writeDiscoveryFile(t *testing.T, dir, addr, token string) string {
	t.Helper()
	return writeDiscoveryFileVersion(t, dir, addr, token, 1)
}

func writeDiscoveryFileVersion(t *testing.T, dir, addr, token string, version int) string {
	t.Helper()
	p := filepath.Join(dir, "control.json")
	b, err := json.Marshal(map[string]any{
		"addr": addr, "token": token, "pid": 123, "shader_dir": "/abs/shaders", "version": version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// with は mu を取って fn を実行する（テスト側からの状態の読み書き用）。
func (f *fakeControl) with(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *fakeControl) setOverride(h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.override = h
}

func (f *fakeControl) lastRequest() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return ""
	}
	return f.requests[len(f.requests)-1]
}

func writeFakeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeFakeErr(w http.ResponseWriter, status int, code, msg string) {
	writeFakeJSON(w, status, map[string]any{"error": code, "message": msg})
}

// decodeStrict は本文を v に読み込み、未知のフィールドがあれば 400 invalid_request を返す
// （#24 でマージ前に入る制御口の厳格なデコードに合わせる。クライアントの送りすぎを検出する）。
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeFakeErr(w, http.StatusBadRequest, "invalid_request", "request body must be a JSON object: "+err.Error())
		return false
	}
	return true
}

func notLoaded(name string) string {
	return "shader " + name + " is not loaded (missing, or its last compile failed; see last_error in GET /v1/state)"
}

func (f *fakeControl) state(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var le any
	if f.lastErr != nil {
		le = f.lastErr
	}
	writeFakeJSON(w, http.StatusOK, map[string]any{
		"active": f.active, "shaders": f.order, "bpm": f.bpm, "bpm_measured": f.bpmMeasured,
		"fade":       map[string]any{"fading": f.fading, "target": f.target, "mix": 0.25, "beats": f.beats},
		"fps":        59.9,
		"resolution": []int{1280, 720},
		"last_error": le,
	})
}

func (f *fakeControl) getShader(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := r.PathValue("name")
	src, ok := f.sources[name]
	if !ok {
		writeFakeErr(w, http.StatusNotFound, "not_found", "shader "+name+" not found")
		return
	}
	writeFakeJSON(w, http.StatusOK, map[string]any{"name": name, "source": src})
}

func (f *fakeControl) putShader(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !fakeNameRe.MatchString(name) {
		writeFakeErr(w, http.StatusBadRequest, "invalid_name", "shader name must match ^[a-z0-9][a-z0-9_-]{0,63}$")
		return
	}
	var req struct {
		Source *string `json:"source"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}
	if req.Source == nil {
		writeFakeErr(w, http.StatusBadRequest, "invalid_request", "missing required field: source")
		return
	}
	body := struct{ Source string }{*req.Source}
	if len(body.Source) > 64<<10 {
		writeFakeErr(w, http.StatusRequestEntityTooLarge, "too_large", "source exceeds 64KiB")
		return
	}
	if !strings.Contains(body.Source, "//kage:unit pixels") {
		writeFakeErr(w, http.StatusUnprocessableEntity, "unit_pixels_required", "the source must contain a //kage:unit pixels line")
		return
	}
	if strings.Contains(body.Source, "Nope") {
		writeFakeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":   "compile_error",
			"message": "2 errors",
			"diagnostics": []map[string]any{
				{"line": 5, "col": 14, "message": "unexpected identifier: Nope"},
				{"line": 0, "col": 0, "message": "something unparsed"},
			},
		})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, existed := f.sources[name]
	f.sources[name] = body.Source
	if !existed {
		f.order = append(f.order, name)
	}
	writeFakeJSON(w, http.StatusOK, map[string]any{"name": name, "created": !existed})
}

func (f *fakeControl) switchShader(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sources[body.Name]; !ok {
		writeFakeErr(w, http.StatusNotFound, "not_found", notLoaded(body.Name))
		return
	}
	f.active = body.Name
	writeFakeJSON(w, http.StatusOK, map[string]any{"active": body.Name})
}

func (f *fakeControl) crossfade(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  *string  `json:"name"`
		Beats *float64 `json:"beats"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	if body.Name == nil {
		writeFakeErr(w, http.StatusBadRequest, "invalid_request", "missing required field: name")
		return
	}
	name := *body.Name
	if !fakeNameRe.MatchString(name) {
		writeFakeErr(w, http.StatusBadRequest, "invalid_name", "shader name must match ^[a-z0-9][a-z0-9_-]{0,63}$")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	beats := f.beats
	if body.Beats != nil {
		b := *body.Beats
		if b <= 0 || b > 64 {
			writeFakeErr(w, http.StatusBadRequest, "invalid_beats", "beats must satisfy 0 < beats <= 64")
			return
		}
		beats = b
	}
	// 判定順は 名前 400 → beats 400 → 未読込 404。BPM は常に 40〜300 なので BPM では失敗しない。
	if _, ok := f.sources[name]; !ok {
		writeFakeErr(w, http.StatusNotFound, "not_found", notLoaded(name))
		return
	}
	f.fading, f.target = true, name
	writeFakeJSON(w, http.StatusOK, map[string]any{"target": name, "beats": beats})
}

func (f *fakeControl) setBPM(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BPM *float64 `json:"bpm"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	if body.BPM == nil {
		writeFakeErr(w, http.StatusBadRequest, "invalid_request", "missing required field: bpm")
		return
	}
	bpm := *body.BPM
	if bpm < 40 || bpm > 300 {
		writeFakeErr(w, http.StatusBadRequest, "invalid_bpm", "bpm must be between 40 and 300")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bpm, f.bpmMeasured = bpm, true
	writeFakeJSON(w, http.StatusOK, map[string]any{"bpm": bpm})
}
