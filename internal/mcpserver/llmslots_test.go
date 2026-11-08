package mcpserver

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// LLM 背景スロット（kagelife -llm-slots）。LLM は映っていない予約名に書いてから crossfade し、
// 次の曲では逆側に書く。映っているかの最終判断は GUI の 409 slot_live に従う。

const slotSrc = "//kage:unit pixels\npackage main\n"

// setupSlots は予約名 llm_a, llm_b を持つフェイクにつなぐ。
func setupSlots(t *testing.T) (*fakeControl, *mcp.ClientSession) {
	t.Helper()
	f := newFakeControl(t)
	f.with(func() { f.llmSlots = []string{"llm_a", "llm_b"} })
	p := f.writeDiscovery(t, t.TempDir())
	return f, connect(t, Config{ControlFile: p})
}

func (f *fakeControl) requestLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func (f *fakeControl) source(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sources[name]
}

// ── get_state ─────────────────────────────────────────────

func TestGetStateShowsLLMSlots(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() {
		f.active = "llm_a"
		f.order = append(f.order, "llm_a", "llm_b")
	})
	txt := mustOK(t, cs, "get_state", nil)
	wantContains(t, txt, "llm_slots: llm_a (on screen), llm_b (free)", "features: llm_slots")
}

func TestGetStateShowsFadeTargetSlotOnScreen(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() { f.fading, f.target = true, "llm_b" })
	txt := mustOK(t, cs, "get_state", nil)
	wantContains(t, txt, "llm_slots: llm_a (free), llm_b (on screen)")
}

// フェードしていない間の fade.target の名残は映っていない。
func TestGetStateIgnoresStaleFadeTarget(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() { f.fading, f.target = false, "llm_b" })
	txt := mustOK(t, cs, "get_state", nil)
	wantContains(t, txt, "llm_slots: llm_a (free), llm_b (free)")
}

func TestGetStateWithoutLLMSlotsFlag(t *testing.T) {
	_, cs := setup(t)
	txt := mustOK(t, cs, "get_state", nil)
	wantContains(t, txt, "llm_slots: none", "features: llm_slots")
}

func TestGetStateLegacyGUI(t *testing.T) {
	f, cs := setup(t)
	f.with(func() { f.legacy = true })
	txt := mustOK(t, cs, "get_state", nil)
	wantContains(t, txt, "llm_slots: none", "features: none", "active: 01_uv")
}

// ── write_back_slot の選び方 ───────────────────────────────

func TestWriteBackSlotSkipsActive(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() {
		f.active = "llm_a"
		f.sources["llm_a"] = slotSrc
		f.order = append(f.order, "llm_a")
	})
	txt := mustOK(t, cs, "write_back_slot", map[string]any{"source": slotSrc + "// b\n"})
	wantContains(t, txt, "llm_b", "created", "crossfade name=llm_b", "8 beats", "llm_a")
	if got := f.source("llm_b"); got != slotSrc+"// b\n" {
		t.Fatalf("llm_b = %q", got)
	}
	if got := f.requestLog(); !slices.Equal(got, []string{"GET /v1/state", "PUT /v1/shaders/llm_b"}) {
		t.Fatalf("requests = %v", got)
	}
}

func TestWriteBackSlotPicksFirstFreeInOrder(t *testing.T) {
	f, cs := setupSlots(t)
	txt := mustOK(t, cs, "write_back_slot", map[string]any{"source": slotSrc})
	wantContains(t, txt, "llm_a", "crossfade name=llm_a")
	if f.lastRequest() != "PUT /v1/shaders/llm_a" {
		t.Fatalf("request = %q", f.lastRequest())
	}
}

func TestWriteBackSlotReplaced(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() {
		f.active = "llm_b"
		f.sources["llm_a"], f.sources["llm_b"] = slotSrc, slotSrc
		f.order = append(f.order, "llm_a", "llm_b")
	})
	txt := mustOK(t, cs, "write_back_slot", map[string]any{"source": slotSrc + "// new\n"})
	wantContains(t, txt, "llm_a", "replaced", "crossfade name=llm_a")
	// 次の曲では逆側（いま映っている llm_b）に書く。
	wantContains(t, txt, "next", "llm_b")
}

func TestWriteBackSlotSkipsFadeTarget(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() { f.fading, f.target = true, "llm_a" })
	txt := mustOK(t, cs, "write_back_slot", map[string]any{"source": slotSrc})
	wantContains(t, txt, "crossfade name=llm_b")
	if f.lastRequest() != "PUT /v1/shaders/llm_b" {
		t.Fatalf("request = %q", f.lastRequest())
	}
}

// フェードしていない間の fade.target の名残は飛ばさない。
func TestWriteBackSlotIgnoresStaleFadeTarget(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() { f.fading, f.target = false, "llm_a" })
	mustOK(t, cs, "write_back_slot", map[string]any{"source": slotSrc})
	if f.lastRequest() != "PUT /v1/shaders/llm_a" {
		t.Fatalf("request = %q", f.lastRequest())
	}
}

// 2 本とも映っているように見えても、GUI の判定が正なので 1 回は試す。
// active がコンパイルに失敗した予約スロットなら GUI は映っていないと判定し、書いて直せる。
func TestWriteBackSlotTriesEvenWhenBothLookLive(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() {
		f.active, f.failed = "llm_a", map[string]bool{"llm_a": true}
		f.fading, f.target = true, "llm_b"
	})
	txt := mustOK(t, cs, "write_back_slot", map[string]any{"source": slotSrc})
	wantContains(t, txt, "crossfade name=llm_a")
	if got := f.requestLog(); !slices.Equal(got, []string{"GET /v1/state", "PUT /v1/shaders/llm_a"}) {
		t.Fatalf("requests = %v", got)
	}
}

// ── write_back_slot の拒否 ─────────────────────────────────

func TestWriteBackSlotWithoutFlag(t *testing.T) {
	f, cs := setup(t)
	txt := mustErr(t, cs, "write_back_slot", map[string]any{"source": slotSrc})
	wantContains(t, txt, "-llm-slots", "restart", "write_shader")
	if got := f.requestLog(); !slices.Equal(got, []string{"GET /v1/state"}) {
		t.Fatalf("must not PUT: %v", got)
	}
}

func TestWriteBackSlotLegacyGUI(t *testing.T) {
	f, cs := setup(t)
	f.with(func() { f.legacy = true })
	txt := mustErr(t, cs, "write_back_slot", map[string]any{"source": slotSrc})
	wantContains(t, txt, "Update kagelife", "-llm-slots")
	if got := f.requestLog(); !slices.Equal(got, []string{"GET /v1/state"}) {
		t.Fatalf("must not PUT: %v", got)
	}
}

// ── 409 の再試行 ───────────────────────────────────────────

// state を読んでから PUT までに人が llm_a に切り替えた。state を 1 回だけ読み直して llm_b に書く。
func TestWriteBackSlotRetriesOnceOnSlotLive(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() {
		f.beforePut = func(name string) {
			f.with(func() {
				if name == "llm_a" {
					f.active = "llm_a"
				}
			})
		}
	})
	txt := mustOK(t, cs, "write_back_slot", map[string]any{"source": slotSrc})
	wantContains(t, txt, "crossfade name=llm_b")
	want := []string{"GET /v1/state", "PUT /v1/shaders/llm_a", "GET /v1/state", "PUT /v1/shaders/llm_b"}
	if got := f.requestLog(); !slices.Equal(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	if f.source("llm_b") == "" || f.source("llm_a") != "" {
		t.Fatalf("wrong slot written: a=%q b=%q", f.source("llm_a"), f.source("llm_b"))
	}
}

func TestWriteBackSlotGivesUpAfterSecondSlotLive(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() {
		// PUT のたびに、書こうとした予約名が映る（人が先回りして切り替え続ける）。
		f.beforePut = func(name string) { f.with(func() { f.active = name }) }
	})
	txt := mustErr(t, cs, "write_back_slot", map[string]any{"source": slotSrc})
	wantContains(t, txt, "slot_live", "fade", "get_state", "write_back_slot")
	want := []string{"GET /v1/state", "PUT /v1/shaders/llm_a", "GET /v1/state", "PUT /v1/shaders/llm_b"}
	if got := f.requestLog(); !slices.Equal(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
}

// 読み直した state で残りの予約名が無ければ PUT せずに案内する（予約名が 1 つだけの GUI）。
func TestWriteBackSlotSingleSlotLive(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() {
		f.llmSlots = []string{"llm_a"}
		f.beforePut = func(name string) { f.with(func() { f.active = name }) }
	})
	txt := mustErr(t, cs, "write_back_slot", map[string]any{"source": slotSrc})
	wantContains(t, txt, "slot_live", "get_state")
	want := []string{"GET /v1/state", "PUT /v1/shaders/llm_a", "GET /v1/state"}
	if got := f.requestLog(); !slices.Equal(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
}

// ── 本文の失敗 ─────────────────────────────────────────────

func TestWriteBackSlotCompileError(t *testing.T) {
	_, cs := setupSlots(t)
	txt := mustErr(t, cs, "write_back_slot", map[string]any{"source": slotSrc + "Nope\n"})
	wantContains(t, txt, "compile", "5:14: unexpected identifier: Nope", "something unparsed", "llm_a", "write_back_slot")
	if strings.Contains(txt, "write_shader") {
		t.Errorf("should name write_back_slot, not write_shader:\n%s", txt)
	}
}

func TestWriteBackSlotUnitPixelsRequired(t *testing.T) {
	_, cs := setupSlots(t)
	txt := mustErr(t, cs, "write_back_slot", map[string]any{"source": "package main\n"})
	wantContains(t, txt, "unit_pixels_required", "//kage:unit pixels", "write_back_slot")
	if strings.Contains(txt, "write_shader") {
		t.Errorf("should name write_back_slot, not write_shader:\n%s", txt)
	}
}

// ── write_shader と -llm-slots ─────────────────────────────

func TestWriteShaderForbiddenName(t *testing.T) {
	_, cs := setupSlots(t)
	txt := mustErr(t, cs, "write_shader", map[string]any{"name": "10_plasma", "source": slotSrc})
	wantContains(t, txt, "forbidden_name", "llm_slots", "write_back_slot")
}

func TestWriteShaderSlotLive(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() { f.active = "llm_a" })
	txt := mustErr(t, cs, "write_shader", map[string]any{"name": "llm_a", "source": slotSrc})
	wantContains(t, txt, "slot_live", "on screen", "write_back_slot")
}

// 判定の順は 名前 400 → 403 → 409 → 本文（kagelife の putShader）。本文の誤りより前に名前で断る。
func TestPutCheckOrder(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() { f.active = "llm_a" })
	bad := "package main\n" // unit_pixels_required になる本文
	for _, c := range []struct{ name, want string }{
		{"Bad Name", "invalid_name"},
		{"10_plasma", "forbidden_name"},
		{"llm_a", "slot_live"},
		{"llm_b", "unit_pixels_required"},
	} {
		txt := mustErr(t, cs, "write_shader", map[string]any{"name": c.name, "source": bad})
		wantContains(t, txt, c.want)
	}
}

// ── 説明文 ─────────────────────────────────────────────────

func TestDescriptionsMentionLLMSlots(t *testing.T) {
	cs := connect(t, Config{ControlFile: filepath.Join(t.TempDir(), "none.json")})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"get_state":       {"llm_slots"},
		"write_shader":    {"-llm-slots", "write_back_slot", "crossfade"},
		"write_back_slot": {"-llm-slots", "crossfade", "on screen"},
	}
	for _, tool := range res.Tools {
		if subs, ok := want[tool.Name]; ok {
			wantContains(t, tool.Description, subs...)
			delete(want, tool.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("tools not found: %v", want)
	}
	init := cs.InitializeResult()
	wantContains(t, init.Instructions, "write_back_slot", "crossfade")
}

func TestKageGuideHasReservedSlotsSection(t *testing.T) {
	cs := connect(t, Config{ControlFile: filepath.Join(t.TempDir(), "none.json")})
	txt := mustOK(t, cs, "get_kage_guide", nil)
	wantContains(t, txt, "RESERVED LLM SLOTS", "write_back_slot", "crossfade", "8 beats", "Beat", "Time")
}

// ── 本体 dev（#33）の 409 の文面 ─────────────────────────────

// 本体の slotLiveMessage は他方の予約名を挙げて「そちらに書け」と添える。
func TestWriteShaderSlotLiveNamesOtherSlot(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() { f.active = "llm_a" })
	txt := mustErr(t, cs, "write_shader", map[string]any{"name": "llm_a", "source": slotSrc})
	wantContains(t, txt, "write the other reserved slot instead (llm_b)")
}

// 2 回目も 409 のとき、本体の「他方に書け」をそのまま見せると、他方も 409 だったのと食い違う。
func TestWriteBackSlotBusyDoesNotSayWriteOther(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() {
		f.beforePut = func(name string) { f.with(func() { f.active = name }) }
	})
	txt := mustErr(t, cs, "write_back_slot", map[string]any{"source": slotSrc})
	wantContains(t, txt, "llm_a", "llm_b", "Nothing was written")
	if strings.Contains(txt, "write the other reserved slot instead") {
		t.Errorf("contradicts the second slot_live:\n%s", txt)
	}
}

// ── PUT が応答しなかったとき ─────────────────────────────────

// -llm-slots 付きの PUT はゲームループを最大 3 回待つ。loop_timeout でもファイルが書けている
// 場合がある（ADR-006 補足 11）ので、再送の前に確かめさせる。
func TestWriteBackSlotLoopTimeoutMayHaveWritten(t *testing.T) {
	f, cs := setupSlots(t)
	f.setOverride(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			f.state(w, r)
			return
		}
		writeFakeErr(w, http.StatusServiceUnavailable, "loop_timeout", "game loop did not respond within 2s")
	})
	txt := mustErr(t, cs, "write_back_slot", map[string]any{"source": slotSrc})
	wantContains(t, txt, "loop_timeout", "llm_a", "may already have been written", "read_shader", "get_state")
}

func TestWriteBackSlotHTTPTimeoutMayHaveWritten(t *testing.T) {
	f := newFakeControl(t)
	f.with(func() { f.llmSlots = []string{"llm_a", "llm_b"} })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	f.setOverride(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			f.state(w, r)
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	cs := connect(t, Config{ControlFile: f.writeDiscovery(t, t.TempDir()), Timeout: 200 * time.Millisecond})
	txt := mustErr(t, cs, "write_back_slot", map[string]any{"source": slotSrc})
	wantContains(t, txt, "did not respond", "may already have been written", "read_shader")
	// 再送はしない（PUT は 1 回だけ）。
	if got := f.requestLog(); !slices.Equal(got, []string{"GET /v1/state", "PUT /v1/shaders/llm_a"}) {
		t.Fatalf("requests = %v", got)
	}
}
