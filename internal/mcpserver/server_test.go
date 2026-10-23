package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect は in-memory transport でサーバとクライアントをつなぐ。
func connect(t *testing.T, cfg Config) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := NewServer(cfg).Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// setup はフェイク制御口と、それを指す発見ファイルを用意してつなぐ。
func setup(t *testing.T) (*fakeControl, *mcp.ClientSession) {
	t.Helper()
	f := newFakeControl(t)
	p := f.writeDiscovery(t, t.TempDir())
	return f, connect(t, Config{ControlFile: p})
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) protocol error: %v", name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

func mustOK(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	txt, isErr := call(t, cs, name, args)
	if isErr {
		t.Fatalf("%s: unexpected isError: %s", name, txt)
	}
	return txt
}

func mustErr(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	txt, isErr := call(t, cs, name, args)
	if !isErr {
		t.Fatalf("%s: want isError, got: %s", name, txt)
	}
	return txt
}

func wantContains(t *testing.T, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(got, s) {
			t.Errorf("want %q in:\n%s", s, got)
		}
	}
}

// ── tools/list ─────────────────────────────────────────────

func TestListToolsHasEightToolsWithRequired(t *testing.T) {
	cs := connect(t, Config{ControlFile: filepath.Join(t.TempDir(), "none.json")})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"get_state":      nil,
		"list_shaders":   nil,
		"read_shader":    {"name"},
		"write_shader":   {"name", "source"},
		"switch_shader":  {"name"},
		"crossfade":      {"name"},
		"set_bpm":        {"bpm"},
		"get_kage_guide": nil,
	}
	if len(res.Tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(res.Tools), len(want))
	}
	for _, tool := range res.Tools {
		req, ok := want[tool.Name]
		if !ok {
			t.Errorf("unexpected tool %q", tool.Name)
			continue
		}
		if len(tool.Description) < 30 {
			t.Errorf("%s: description too short: %q", tool.Name, tool.Description)
		}
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Required   []string       `json:"required"`
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatal(err)
		}
		got := slices.Clone(schema.Required)
		slices.Sort(got)
		if !slices.Equal(got, req) && (len(got) != 0 || len(req) != 0) {
			t.Errorf("%s: required = %v, want %v", tool.Name, got, req)
		}
		if tool.Name == "crossfade" {
			if _, ok := schema.Properties["beats"]; !ok {
				t.Errorf("crossfade: beats property missing: %s", b)
			}
		}
	}
}

func TestWriteShaderDescriptionMentionsReplaceAndFallback(t *testing.T) {
	cs := connect(t, Config{ControlFile: filepath.Join(t.TempDir(), "none.json")})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name == "write_shader" {
			d := strings.ToLower(tool.Description)
			wantContains(t, d, "entire", "previous shader")
			return
		}
	}
	t.Fatal("write_shader not found")
}

// ── 発見 ───────────────────────────────────────────────────

func TestGUINotRunningWhenDiscoveryFileMissing(t *testing.T) {
	cs := connect(t, Config{ControlFile: filepath.Join(t.TempDir(), "none.json")})
	txt := mustErr(t, cs, "get_state", nil)
	wantContains(t, txt, "kagelife", "not running")
	// プロセス（セッション）は生きていて次の呼び出しにも答える。
	mustOK(t, cs, "get_kage_guide", nil)
}

func TestGUINotRunningWhenConnectionRefused(t *testing.T) {
	f := newFakeControl(t)
	addr := f.addr()
	f.srv.Close()
	p := writeDiscoveryFile(t, t.TempDir(), addr, f.token)
	cs := connect(t, Config{ControlFile: p})
	txt := mustErr(t, cs, "list_shaders", nil)
	wantContains(t, txt, "not running")
}

func TestRejectsNonLoopbackAddr(t *testing.T) {
	for _, addr := range []string{"192.168.0.10:8080", "example.com:80", "0.0.0.0:1234", "nonsense"} {
		p := writeDiscoveryFile(t, t.TempDir(), addr, "x")
		cs := connect(t, Config{ControlFile: p})
		txt := mustErr(t, cs, "get_state", nil)
		wantContains(t, txt, "loopback")
	}
}

// 発見ファイルの addr は GUI が書くループバックの IP リテラルと数字の port に限る
// （ADR-006 補足 9。GUI も localhost を受け付けない）。
func TestCheckLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:1234", "[::1]:1234", "127.0.0.2:1234"} {
		if _, err := checkLoopback(addr); err != nil {
			t.Errorf("%q rejected: %v", addr, err)
		}
	}
	for _, addr := range []string{
		"127.0.0.1:80@evil.example", // userinfo 経由で別ホストへ向く
		"127.0.0.1:abc",
		"127.0.0.1:0",
		"localhost:1234",
		"evil.example:80",
		"[fe80::1%eth0]:80",
		"127.0.0.1:1234/x",
	} {
		if _, err := checkLoopback(addr); err == nil {
			t.Errorf("%q accepted", addr)
		}
	}
}

// 発見ファイルに悪性の addr を書かれても、トークンを他のホストへ送らない。
// 旧実装では "http://"+addr+path が evil.example をホストと解釈し、Bearer トークンを送っていた。
// 実際のネットワークには出さず、送ろうとした要求を RoundTripper で記録して確かめる。
func TestMaliciousAddrDoesNotLeakToken(t *testing.T) {
	p := writeDiscoveryFile(t, t.TempDir(), "127.0.0.1:80@evil.example", "secret-token")
	var sent []string
	c := &client{controlFile: p, http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sent = append(sent, r.URL.Host+" "+r.Header.Get("Authorization"))
		return nil, errors.New("blocked by test")
	})}}
	err := c.do(context.Background(), http.MethodGet, "/v1/state", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("want loopback error, got %v", err)
	}
	if len(sent) != 0 {
		t.Fatalf("request was sent: %v", sent)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// URL は解析済みのアドレスから組み立て、PathEscape 済みの名前を二重にエスケープしない。
func TestControlURL(t *testing.T) {
	ap, err := checkLoopback("[::1]:1234")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"/v1/state":                  "http://[::1]:1234/v1/state",
		shaderPath("01_uv"):          "http://[::1]:1234/v1/shaders/01_uv",
		shaderPath("../x"):           "http://[::1]:1234/v1/shaders/..%2Fx",
		shaderPath("a b"):            "http://[::1]:1234/v1/shaders/a%20b",
		shaderPath("x@evil.example"): "http://[::1]:1234/v1/shaders/x@evil.example",
	} {
		got, err := controlURL(ap, path)
		if err != nil || got != want {
			t.Errorf("controlURL(%q) = %q, %v; want %q", path, got, err, want)
		}
	}
}

func TestDiscoveryFileIsReReadOnEveryCall(t *testing.T) {
	dir := t.TempDir()
	a := newFakeControl(t)
	p := a.writeDiscovery(t, dir)
	cs := connect(t, Config{ControlFile: p})
	mustOK(t, cs, "get_state", nil)

	// GUI が再起動してポートとトークンが変わった。
	b := newFakeControl(t)
	b.token = strings.Repeat("cd", 32)
	b.with(func() { b.active = "02_time_sin" })
	b.writeDiscovery(t, dir)
	txt := mustOK(t, cs, "get_state", nil)
	wantContains(t, txt, "02_time_sin")
	if b.lastRequest() != "GET /v1/state" {
		t.Fatalf("second call did not reach new GUI: %q", b.lastRequest())
	}
}

func TestDiscoveryFromEnvVar(t *testing.T) {
	f := newFakeControl(t)
	p := f.writeDiscovery(t, t.TempDir())
	t.Setenv("KAGELIFE_CONTROL_FILE", p)
	got, err := discoveryPath("")
	if err != nil || got != p {
		t.Fatalf("discoveryPath = %q, %v; want %q", got, err, p)
	}
	cs := connect(t, Config{})
	mustOK(t, cs, "get_state", nil)
}

func TestDiscoveryDefaultPath(t *testing.T) {
	t.Setenv("KAGELIFE_CONTROL_FILE", "")
	got, err := discoveryPath("")
	if err != nil {
		t.Skipf("no user cache dir: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("kagelife", "control.json")) {
		t.Fatalf("default path = %q", got)
	}
}

func TestUnauthorizedIsError(t *testing.T) {
	f := newFakeControl(t)
	p := writeDiscoveryFile(t, t.TempDir(), f.addr(), "wrong")
	cs := connect(t, Config{ControlFile: p})
	txt := mustErr(t, cs, "get_state", nil)
	wantContains(t, txt, "unauthorized", "restart")
}

func TestLoopTimeoutIsError(t *testing.T) {
	f, cs := setup(t)
	f.setOverride(func(w http.ResponseWriter, _ *http.Request) {
		writeFakeErr(w, http.StatusServiceUnavailable, "loop_timeout", "game loop did not respond within 2s")
	})
	txt := mustErr(t, cs, "switch_shader", map[string]any{"name": "01_uv"})
	wantContains(t, txt, "loop_timeout", "retry")
}

func TestHTTPTimeoutIsError(t *testing.T) {
	f := newFakeControl(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	f.setOverride(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	p := f.writeDiscovery(t, t.TempDir())
	cs := connect(t, Config{ControlFile: p, Timeout: 200 * time.Millisecond})
	start := time.Now()
	txt := mustErr(t, cs, "get_state", nil)
	if time.Since(start) > 3*time.Second {
		t.Fatalf("timeout not honored")
	}
	wantContains(t, txt, "did not respond")
}

func TestDefaultTimeoutIsFiveSeconds(t *testing.T) {
	if defaultTimeout != 5*time.Second {
		t.Fatalf("defaultTimeout = %v", defaultTimeout)
	}
}

// ── 各ツール ─────────────────────────────────────────────

func TestGetState(t *testing.T) {
	f, cs := setup(t)
	f.with(func() { f.lastErr = map[string]string{"shader": "03_bad", "message": "3:1: boom"} })
	txt := mustOK(t, cs, "get_state", nil)
	wantContains(t, txt, "active: 01_uv", "01_uv, 02_time_sin", "bpm: 120", "fps: 59.9", "1280x720", "03_bad", "3:1: boom", "beats 4")
}

// BPM は常に 40〜300 で、タップ前は既定の 120（ADR-006 GET /v1/state、PRD Q-008）。
// 「未設定」は存在せず、既定値か設定・測定された値かを bpm_measured で見分ける。
func TestGetStateShowsDefaultBPM(t *testing.T) {
	f, cs := setup(t)
	f.with(func() { f.bpm, f.bpmMeasured = 120, false })
	txt := mustOK(t, cs, "get_state", nil)
	wantContains(t, txt, "bpm: 120 (default")
	if strings.Contains(txt, "not set") {
		t.Errorf("BPM is never unset; got:\n%s", txt)
	}
}

func TestGetStateShowsMeasuredBPM(t *testing.T) {
	f, cs := setup(t)
	f.with(func() { f.bpm, f.bpmMeasured = 128, true })
	txt := mustOK(t, cs, "get_state", nil)
	wantContains(t, txt, "bpm: 128 (measured")
}

func TestSetBPMMakesStateMeasured(t *testing.T) {
	_, cs := setup(t)
	wantContains(t, mustOK(t, cs, "get_state", nil), "bpm: 120 (default")
	mustOK(t, cs, "set_bpm", map[string]any{"bpm": 140})
	wantContains(t, mustOK(t, cs, "get_state", nil), "bpm: 140 (measured")
}

func TestListShaders(t *testing.T) {
	_, cs := setup(t)
	txt := mustOK(t, cs, "list_shaders", nil)
	wantContains(t, txt, "* 01_uv (active)", "  02_time_sin")
}

func TestListShadersEmpty(t *testing.T) {
	f, cs := setup(t)
	f.with(func() { f.order, f.active = []string{}, "" })
	txt := mustOK(t, cs, "list_shaders", nil)
	wantContains(t, txt, "no shaders")
}

func TestReadShader(t *testing.T) {
	_, cs := setup(t)
	txt := mustOK(t, cs, "read_shader", map[string]any{"name": "02_time_sin"})
	if txt != "//kage:unit pixels\npackage main\n// sin\n" {
		t.Fatalf("source = %q", txt)
	}
}

func TestReadShaderNotFound(t *testing.T) {
	_, cs := setup(t)
	txt := mustErr(t, cs, "read_shader", map[string]any{"name": "zz"})
	wantContains(t, txt, "not_found", "list_shaders")
}

func TestReadShaderMissingArg(t *testing.T) {
	_, cs := setup(t)
	mustErr(t, cs, "read_shader", map[string]any{})
}

func TestWriteShaderCreated(t *testing.T) {
	f, cs := setup(t)
	txt := mustOK(t, cs, "write_shader", map[string]any{"name": "03_new", "source": "//kage:unit pixels\npackage main\n"})
	wantContains(t, txt, "03_new", "created")
	if f.lastRequest() != "PUT /v1/shaders/03_new" {
		t.Fatalf("request = %q", f.lastRequest())
	}
	var stored string
	f.with(func() { stored = f.sources["03_new"] })
	if stored == "" {
		t.Fatal("fake did not store source")
	}
}

func TestWriteShaderReplaced(t *testing.T) {
	_, cs := setup(t)
	txt := mustOK(t, cs, "write_shader", map[string]any{"name": "01_uv", "source": "//kage:unit pixels\npackage main\n"})
	wantContains(t, txt, "replaced")
}

func TestWriteShaderCompileError(t *testing.T) {
	_, cs := setup(t)
	txt := mustErr(t, cs, "write_shader", map[string]any{"name": "01_uv", "source": "//kage:unit pixels\npackage main\nNope\n"})
	wantContains(t, txt, "compile", "5:14: unexpected identifier: Nope", "something unparsed", "write_shader", "previous shader")
}

func TestWriteShaderUnitPixelsRequired(t *testing.T) {
	_, cs := setup(t)
	txt := mustErr(t, cs, "write_shader", map[string]any{"name": "01_uv", "source": "package main\n"})
	wantContains(t, txt, "unit_pixels_required", "//kage:unit pixels")
}

func TestWriteShaderInvalidName(t *testing.T) {
	_, cs := setup(t)
	txt := mustErr(t, cs, "write_shader", map[string]any{"name": "Bad Name", "source": "//kage:unit pixels\n"})
	wantContains(t, txt, "invalid_name")
}

func TestWriteShaderNameWithSlashStaysInPath(t *testing.T) {
	f, cs := setup(t)
	mustErr(t, cs, "write_shader", map[string]any{"name": "../x", "source": "//kage:unit pixels\n"})
	if got := f.lastRequest(); strings.Contains(got, "/v1/x") || !strings.HasPrefix(got, "PUT /v1/shaders/") {
		t.Fatalf("path escaped the shaders collection: %q", got)
	}
}

func TestSwitchShader(t *testing.T) {
	f, cs := setup(t)
	txt := mustOK(t, cs, "switch_shader", map[string]any{"name": "02_time_sin"})
	wantContains(t, txt, "02_time_sin")
	var active string
	f.with(func() { active = f.active })
	if active != "02_time_sin" {
		t.Fatalf("active = %q", active)
	}
}

func TestSwitchShaderNotFound(t *testing.T) {
	_, cs := setup(t)
	txt := mustErr(t, cs, "switch_shader", map[string]any{"name": "zz"})
	wantContains(t, txt, "not_found")
}

func TestCrossfadeDefaultBeats(t *testing.T) {
	f, cs := setup(t)
	txt := mustOK(t, cs, "crossfade", map[string]any{"name": "02_time_sin"})
	wantContains(t, txt, "02_time_sin", "4 beats")
	var fading bool
	var target string
	f.with(func() { fading, target = f.fading, f.target })
	if !fading || target != "02_time_sin" {
		t.Fatalf("fake not fading")
	}
}

func TestCrossfadeExplicitBeats(t *testing.T) {
	_, cs := setup(t)
	txt := mustOK(t, cs, "crossfade", map[string]any{"name": "02_time_sin", "beats": 8})
	wantContains(t, txt, "8 beats")
}

func TestCrossfadeOmitsBeatsWhenNotGiven(t *testing.T) {
	f, cs := setup(t)
	var got map[string]any
	f.setOverride(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		f.with(func() { got = m })
		writeFakeJSON(w, http.StatusOK, map[string]any{"target": "02_time_sin", "beats": 4.0})
	})
	mustOK(t, cs, "crossfade", map[string]any{"name": "02_time_sin"})
	var sent map[string]any
	f.with(func() { sent = got })
	if _, ok := sent["beats"]; ok {
		t.Fatalf("beats should be omitted: %v", sent)
	}
}

// タップ前（既定の 120 BPM）でも crossfade は通る。BPM を理由に失敗しない（ADR-006 補足 4）。
func TestCrossfadeWorksWithDefaultBPM(t *testing.T) {
	f, cs := setup(t)
	f.with(func() { f.bpm, f.bpmMeasured = 120, false })
	txt := mustOK(t, cs, "crossfade", map[string]any{"name": "02_time_sin"})
	wantContains(t, txt, "02_time_sin", "4 beats")
}

func TestNoToolTellsToSetBPMFirst(t *testing.T) {
	cs := connect(t, Config{ControlFile: filepath.Join(t.TempDir(), "none.json")})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		d := strings.ToLower(tool.Description)
		for _, bad := range []string{"set_bpm first", "requires a bpm", "bpm_not_set", "not set"} {
			if strings.Contains(d, bad) {
				t.Errorf("%s: description still says %q: %s", tool.Name, bad, tool.Description)
			}
		}
		if tool.Name == "crossfade" {
			wantContains(t, d, "120")
		}
	}
}

func TestSwitchNotFoundMentionsLastError(t *testing.T) {
	_, cs := setup(t)
	txt := mustErr(t, cs, "switch_shader", map[string]any{"name": "zz"})
	// コンパイルに失敗したファイルも 404（ADR-006 補足 16）なので、last_error を見るよう促す。
	wantContains(t, txt, "not_found", "list_shaders", "last_error", "get_state")
}

func TestCrossfadeInvalidBeats(t *testing.T) {
	_, cs := setup(t)
	txt := mustErr(t, cs, "crossfade", map[string]any{"name": "02_time_sin", "beats": 100})
	wantContains(t, txt, "invalid_beats")
}

func TestSetBPM(t *testing.T) {
	f, cs := setup(t)
	txt := mustOK(t, cs, "set_bpm", map[string]any{"bpm": 128})
	wantContains(t, txt, "128")
	var bpm float64
	f.with(func() { bpm = f.bpm })
	if bpm != 128 {
		t.Fatalf("bpm = %v", bpm)
	}
}

func TestSetBPMInvalid(t *testing.T) {
	_, cs := setup(t)
	txt := mustErr(t, cs, "set_bpm", map[string]any{"bpm": 5})
	wantContains(t, txt, "invalid_bpm")
}

// set_bpm の範囲は 40〜300（ADR-006 POST /v1/bpm、ADR-008 D3）。
func TestSetBPMRange(t *testing.T) {
	for _, bpm := range []float64{20, 39.9, 300.1} {
		_, cs := setup(t)
		txt := mustErr(t, cs, "set_bpm", map[string]any{"bpm": bpm})
		wantContains(t, txt, "invalid_bpm", "40", "300")
	}
	for _, bpm := range []float64{40, 300} {
		_, cs := setup(t)
		txt := mustOK(t, cs, "set_bpm", map[string]any{"bpm": bpm})
		wantContains(t, txt, fmtNum(bpm))
	}
}

func TestSetBPMDescriptionRange(t *testing.T) {
	cs := connect(t, Config{ControlFile: filepath.Join(t.TempDir(), "none.json")})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "set_bpm" {
			continue
		}
		wantContains(t, tool.Description, "40 to 300")
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		wantContains(t, string(b), "40 to 300")
		if strings.Contains(tool.Description+string(b), "20 to") {
			t.Errorf("set_bpm still mentions the old range 20: %s %s", tool.Description, b)
		}
		return
	}
	t.Fatal("set_bpm not found")
}

func TestGetKageGuide(t *testing.T) {
	cs := connect(t, Config{ControlFile: filepath.Join(t.TempDir(), "none.json")})
	txt := mustOK(t, cs, "get_kage_guide", nil)
	wantContains(t, txt,
		"//kage:unit pixels",
		"func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4",
		"atan2", "struct", "switch",
		"Time", "Resolution", "Beat", "Cursor", "Frame", "Random",
		"dstPos.xy / Resolution",
		"60",
	)
}

// ── 契約 v1 の補足 ─────────────────────────────────────────

func TestDiscoveryVersionMismatchIsError(t *testing.T) {
	f := newFakeControl(t)
	for _, v := range []int{0, 2} {
		p := writeDiscoveryFileVersion(t, t.TempDir(), f.addr(), f.token, v)
		cs := connect(t, Config{ControlFile: p})
		txt := mustErr(t, cs, "get_state", nil)
		wantContains(t, txt, "version", "kagelife-mcp")
	}
	if got := f.lastRequest(); got != "" {
		t.Fatalf("must not contact a GUI with a mismatched version: %q", got)
	}
}

func TestInvalidRequestIsError(t *testing.T) {
	f, cs := setup(t)
	f.setOverride(func(w http.ResponseWriter, _ *http.Request) {
		writeFakeErr(w, http.StatusBadRequest, "invalid_request", "missing field: source")
	})
	txt := mustErr(t, cs, "write_shader", map[string]any{"name": "01_uv", "source": "//kage:unit pixels\n"})
	wantContains(t, txt, "invalid_request", "missing field: source", "arguments")
}

func TestInternalErrorIsError(t *testing.T) {
	f, cs := setup(t)
	f.setOverride(func(w http.ResponseWriter, _ *http.Request) {
		writeFakeErr(w, http.StatusInternalServerError, "internal_error", "write failed: disk full")
	})
	txt := mustErr(t, cs, "write_shader", map[string]any{"name": "01_uv", "source": "//kage:unit pixels\n"})
	wantContains(t, txt, "internal_error", "disk full", "kagelife")
}

func TestCrossfadeInvalidBeatsBeforeNotFound(t *testing.T) {
	_, cs := setup(t)
	txt := mustErr(t, cs, "crossfade", map[string]any{"name": "zz", "beats": 100})
	wantContains(t, txt, "invalid_beats")
}

func TestCrossfadeNotFound(t *testing.T) {
	_, cs := setup(t)
	txt := mustErr(t, cs, "crossfade", map[string]any{"name": "zz"})
	wantContains(t, txt, "not_found", "last_error")
}

// フェイクは未知のフィールドを 400 にする（#24 の厳格なデコードに合わせる）。
// これでクライアントが契約に無いフィールドを送ると、各ツールのテストが落ちる。
func TestFakeRejectsUnknownFields(t *testing.T) {
	f := newFakeControl(t)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPut, "/v1/shaders/01_uv", `{"source":"//kage:unit pixels\n","extra":1}`},
		{http.MethodPost, "/v1/switch", `{"name":"01_uv","extra":1}`},
		{http.MethodPost, "/v1/crossfade", `{"name":"01_uv","beats":4,"extra":1}`},
		{http.MethodPost, "/v1/bpm", `{"bpm":128,"extra":1}`},
	} {
		req, err := http.NewRequest(c.method, f.srv.URL+c.path, strings.NewReader(c.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+f.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s %s with unknown field: status %d, want 400", c.method, c.path, resp.StatusCode)
		}
	}
}

func TestListShadersDescriptionIncludesFailedFiles(t *testing.T) {
	cs := connect(t, Config{ControlFile: filepath.Join(t.TempDir(), "none.json")})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name == "list_shaders" {
			// shaders にはコンパイルに失敗したファイルも出る（ADR-006 補足 16）。
			wantContains(t, strings.ToLower(tool.Description), "failed to compile")
			return
		}
	}
	t.Fatal("list_shaders not found")
}

func TestWriteShaderCreatedMentionsEmptyScreenException(t *testing.T) {
	_, cs := setup(t)
	txt := mustOK(t, cs, "write_shader", map[string]any{"name": "03_new", "source": "//kage:unit pixels\npackage main\n"})
	// 表示中のシェーダが無ければ PUT の成功で表示される（ADR-006 補足 14）。
	wantContains(t, txt, "nothing was on screen")
}

// 400 の検証エラーには、次に何をすればよいかを添える。制御口の message に頼らず
// 中継側の文面で固定するため、message は短いものを返す。
func TestValidationErrorHints(t *testing.T) {
	cases := []struct {
		code, tool string
		args       map[string]any
		want       []string
	}{
		{"invalid_name", "switch_shader", map[string]any{"name": "01_uv"},
			[]string{"invalid_name", "^[a-z0-9][a-z0-9_-]{0,63}$", "64", ".kage", "list_shaders"}},
		{"invalid_beats", "crossfade", map[string]any{"name": "01_uv", "beats": 8},
			[]string{"invalid_beats", "0 < beats <= 64", "omit"}},
		{"invalid_bpm", "set_bpm", map[string]any{"bpm": 128},
			[]string{"invalid_bpm", "40", "300"}},
	}
	for _, c := range cases {
		f, cs := setup(t)
		f.setOverride(func(w http.ResponseWriter, _ *http.Request) {
			writeFakeErr(w, http.StatusBadRequest, c.code, "x")
		})
		txt := mustErr(t, cs, c.tool, c.args)
		wantContains(t, txt, c.want...)
	}
}

func TestKageGuideSaysBeatRunsAtDefaultBPM(t *testing.T) {
	cs := connect(t, Config{ControlFile: filepath.Join(t.TempDir(), "none.json")})
	txt := mustOK(t, cs, "get_kage_guide", nil)
	wantContains(t, txt, "120 BPM", "40-300")
}

func TestUsageMentionsBinaryName(t *testing.T) {
	if !strings.Contains(usage, "kagelife-mcp") || strings.Contains(usage, "kagelife mcp") {
		t.Fatalf("usage = %q", usage)
	}
}

func TestGetStateDescriptionExplainsLastErrorReset(t *testing.T) {
	cs := connect(t, Config{ControlFile: filepath.Join(t.TempDir(), "none.json")})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name == "get_state" {
			wantContains(t, strings.ToLower(tool.Description), "cleared")
			return
		}
	}
	t.Fatal("get_state not found")
}
