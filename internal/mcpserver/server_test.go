package mcpserver

import (
	"context"
	"encoding/json"
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

func TestAcceptsIPv6Loopback(t *testing.T) {
	if got := checkLoopback("[::1]:1234"); got != nil {
		t.Fatalf("[::1] rejected: %v", got)
	}
	if got := checkLoopback("localhost:1234"); got != nil {
		t.Fatalf("localhost rejected: %v", got)
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

func TestGetStateBPMNotSetHint(t *testing.T) {
	f, cs := setup(t)
	f.with(func() { f.bpm = 0 })
	txt := mustOK(t, cs, "get_state", nil)
	wantContains(t, txt, "not set")
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

func TestCrossfadeBPMNotSet(t *testing.T) {
	f, cs := setup(t)
	f.with(func() { f.bpm = 0 })
	txt := mustErr(t, cs, "crossfade", map[string]any{"name": "02_time_sin"})
	wantContains(t, txt, "bpm_not_set", "set_bpm")
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

func TestCrossfadeNotFoundBeforeBPMNotSet(t *testing.T) {
	f, cs := setup(t)
	f.with(func() { f.bpm = 0 })
	txt := mustErr(t, cs, "crossfade", map[string]any{"name": "zz"})
	wantContains(t, txt, "not_found")
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
