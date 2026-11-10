package mcpserver

import (
	"strings"
	"testing"
)

// 本体 v0.2.0（kagelife dev）への追従: -link と set_bpm、413 too_large、数字キーの割り当て。

// ── set_bpm と -link ─────────────────────────────────────

// -link で Link に同期しているあいだ、POST /v1/bpm は受け付けられるが次のフレームまでに Link の値へ戻る
// （kagelife README「Ableton Link で拍を合わせる」）。state には Link の状態が無い。
func TestSetBPMDescriptionWarnsAboutLink(t *testing.T) {
	d := toolDescription(t, "set_bpm")
	wantContains(t, d, "-link", "Ableton Link", "reverted", "do not call set_bpm", "get_state cannot tell")
}

func TestKageGuideBeatMentionsLink(t *testing.T) {
	beat := kageGuide[strings.Index(kageGuide, "Beat always runs"):strings.Index(kageGuide, "With -render-scale")]
	wantContains(t, beat, "-link", "Link", "set_bpm")
}

// ── 413 too_large ────────────────────────────────────────

func TestWriteShaderTooLargeNamesWriteShader(t *testing.T) {
	_, cs := setup(t)
	big := "//kage:unit pixels\npackage main\n" + strings.Repeat("// pad\n", 64<<10/7+1)
	txt := mustErr(t, cs, "write_shader", map[string]any{"name": "10_big", "source": big})
	wantContains(t, txt, "too_large", "64KiB", "shorter", "call write_shader again")
	if strings.Contains(txt, "write_back_slot") {
		t.Errorf("write_shader must not name write_back_slot:\n%s", txt)
	}
}

func TestWriteBackSlotTooLargeNamesWriteBackSlot(t *testing.T) {
	_, cs := setupSlots(t)
	big := slotSrc + strings.Repeat("// pad\n", 64<<10/7+1)
	txt := mustErr(t, cs, "write_back_slot", map[string]any{"source": big})
	wantContains(t, txt, "too_large", "64KiB", "shorter", "call write_back_slot again")
	if strings.Contains(txt, "call write_shader again") {
		t.Errorf("write_back_slot must not name write_shader:\n%s", txt)
	}
}

// ── 数字キーの割り当て（本体 llmkeys.go の keySlots と同じ規則） ──

func TestKeyAssignments(t *testing.T) {
	names := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = string(rune('a'+i)) + "_s"
		}
		return out
	}
	cases := []struct {
		name     string
		shaders  []string
		reserved []string
		want     [9]string
	}{
		{
			name:    "no flag: first 9 in file-name order",
			shaders: names(11),
			want:    [9]string{"a_s", "b_s", "c_s", "d_s", "e_s", "f_s", "g_s", "h_s", "i_s"},
		},
		{
			name:    "no flag: fewer than 9 leaves the rest empty",
			shaders: names(3),
			want:    [9]string{"a_s", "b_s", "c_s"},
		},
		{
			name:     "llm-slots: 1-7 skip reserved names, 8 and 9 are the reserved names",
			shaders:  []string{"a_s", "b_s", "llm_a", "c_s", "d_s", "e_s", "f_s", "g_s", "h_s", "llm_b"},
			reserved: []string{"llm_a", "llm_b"},
			want:     [9]string{"a_s", "b_s", "c_s", "d_s", "e_s", "f_s", "g_s", "llm_a", "llm_b"},
		},
		{
			name:     "llm-slots: reserved files missing leave 8 / 9 empty",
			shaders:  []string{"a_s", "llm_b"},
			reserved: []string{"llm_a", "llm_b"},
			want:     [9]string{"a_s", "", "", "", "", "", "", "", "llm_b"},
		},
		{
			name:     "llm-slots: a single reserved name uses only key 8",
			shaders:  []string{"a_s", "llm_a"},
			reserved: []string{"llm_a"},
			want:     [9]string{"a_s", "", "", "", "", "", "", "llm_a"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := keyAssignments(c.shaders, c.reserved); got != c.want {
				t.Errorf("keyAssignments(%v, %v)\n got  %q\n want %q", c.shaders, c.reserved, got, c.want)
			}
		})
	}
}

func TestGetStateShowsKeysWithoutFlag(t *testing.T) {
	f, cs := setup(t)
	f.with(func() {
		f.order = []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "b1", "b2"}
	})
	txt := mustOK(t, cs, "get_state", nil)
	wantContains(t, txt, "key 1 = a1\n", "key 9 = a9\n", "no key (use switch_shader / crossfade by name): b1, b2")
	if strings.Contains(txt, "key 0") {
		t.Errorf("key 0 exists only with -llm-slots:\n%s", txt)
	}
	if strings.Contains(txt, "GET /v1/previews") || f.previewCallCount() != 0 {
		t.Errorf("get_state must not call GET /v1/previews (%d calls)", f.previewCallCount())
	}
}

func TestGetStateShowsKeysWithLLMSlots(t *testing.T) {
	f, cs := setupSlots(t)
	f.with(func() { f.order = []string{"01_uv", "02_time_sin", "llm_a"} })
	txt := mustOK(t, cs, "get_state", nil)
	wantContains(t, txt,
		"key 1 = 01_uv\n", "key 2 = 02_time_sin\n",
		"key 8 = llm_a (reserved LLM slot)\n",
		"key 9 = llm_b (reserved LLM slot, no file yet)\n",
		"key 0 = back to the last non-LLM shader",
		"no key (use switch_shader / crossfade by name): none")
	if strings.Contains(txt, "key 3 =") {
		t.Errorf("key 3 has no shader and must not be listed:\n%s", txt)
	}
}

func TestGetStateShowsNoKeysWithoutShaders(t *testing.T) {
	f, cs := setup(t)
	f.with(func() { f.order, f.active = nil, "" })
	txt := mustOK(t, cs, "get_state", nil)
	wantContains(t, txt, "keys: none")
}

func (f *fakeControl) previewCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.previewCalls
}
