package mcpserver

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed kage_guide.txt
var kageGuide string

type noArgs struct{}

type nameArgs struct {
	Name string `json:"name" jsonschema:"shader name without the .kage extension, e.g. 01_uv. Must match ^[a-z0-9][a-z0-9_-]{0,63}$"`
}

type writeArgs struct {
	Name   string `json:"name" jsonschema:"shader name without the .kage extension, e.g. 10_plasma. Must match ^[a-z0-9][a-z0-9_-]{0,63}$"`
	Source string `json:"source" jsonschema:"the complete Kage source (max 64KiB). Must contain a //kage:unit pixels line"`
}

type sourceArgs struct {
	Source string `json:"source" jsonschema:"the complete Kage source (max 64KiB). Must contain a //kage:unit pixels line"`
}

type crossfadeArgs struct {
	Name  string   `json:"name" jsonschema:"shader to fade to"`
	Beats *float64 `json:"beats,omitempty" jsonschema:"fade length in beats, 0 < beats <= 64. Omit to use the current fade length shown by get_state"`
}

type bpmArgs struct {
	BPM float64 `json:"bpm" jsonschema:"tempo in beats per minute, 40 to 300"`
}

// stateResp は GET /v1/state の応答（ADR-006）。
// BPM は常に 40〜300 でタップ前は既定の 120。BPMMeasured は BPM がタップか set_bpm で
// 設定された値なら true、既定値のままなら false。
type stateResp struct {
	Active      string   `json:"active"`
	Shaders     []string `json:"shaders"`
	BPM         float64  `json:"bpm"`
	BPMMeasured bool     `json:"bpm_measured"`
	Fade        struct {
		Fading bool    `json:"fading"`
		Target string  `json:"target"`
		Mix    float64 `json:"mix"`
		Beats  float64 `json:"beats"`
	} `json:"fade"`
	FPS        float64 `json:"fps"`
	Resolution []int   `json:"resolution"`
	LastError  *struct {
		Shader  string `json:"shader"`
		Message string `json:"message"`
	} `json:"last_error"`
	// LLMSlots は -llm-slots の予約名（フラグの順）。フラグ無しなら空、古い GUI ではフィールドが無い。
	LLMSlots []string `json:"llm_slots"`
	// Features は GUI が対応している機能。-llm-slots に対応した GUI なら "llm_slots" を含む。
	Features []string `json:"features"`
}

// featureLLMSlots は -llm-slots に対応した GUI が features に出す名前。
const featureLLMSlots = "llm_slots"

// onScreen は name が映っているように見えるかを返す。active と同じ名前か、フェード中の fade.target。
// state からはスロットのコンパイルの成否が分からないので目安に留め、最終判断は GUI の 409 slot_live に従う。
func (s stateResp) onScreen(name string) bool {
	return s.Active == name || s.fadingTo(name)
}

// fadingTo は name へのフェードが走っているかを返す。フェードしていない間の fade.target の名残は数えない。
func (s stateResp) fadingTo(name string) bool { return s.Fade.Fading && s.Fade.Target == name }

// slotCandidates は書き込みを試す予約名を、映っていない名前（llm_slots の順）→ active だけの名前 →
// フェード先の順に並べて返す。2 本とも映っているように見えても候補から外さない
// （active がコンパイルに失敗したスロットなら GUI は映っていないと判定する）。
func (s stateResp) slotCandidates() []string {
	rank := func(name string) int {
		switch {
		case s.fadingTo(name):
			return 2
		case s.Active == name:
			return 1
		}
		return 0
	}
	c := slices.Clone(s.LLMSlots)
	slices.SortStableFunc(c, func(a, b string) int { return rank(a) - rank(b) })
	return c
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func errText(s string) *mcp.CallToolResult {
	r := text(s)
	r.IsError = true
	return r
}

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func shaderPath(name string) string { return "/v1/shaders/" + url.PathEscape(name) }

// describeErr は制御口のエラーを LLM が次の行動を決められる文面にする。
func describeErr(err error) string { return describeErrFor(err, "write_shader") }

// describeErrFor は describeErr と同じだが、本文の誤り（422）でもう一度呼ぶべきツールを tool と名指しする。
func describeErrFor(err error, tool string) string {
	var ae *apiError
	if !errors.As(err, &ae) {
		return err.Error()
	}
	head := fmt.Sprintf("%s: %s", ae.Code, ae.Message)
	switch {
	case ae.Status == http.StatusUnauthorized:
		return head + "\nThe token in the control file was rejected. The control file is probably stale; restart `kagelife` and try again."
	case ae.Code == "loop_timeout" || ae.Status == http.StatusServiceUnavailable:
		return head + "\nkagelife's render loop was too busy to answer. Wait a moment and retry. If this keeps happening, the active shader may be too heavy."
	case ae.Code == "not_found" || ae.Status == http.StatusNotFound:
		return head + "\nCall list_shaders to see the available shader names. " +
			"A shader that is listed but failed to compile is also not_found for switch_shader and crossfade: " +
			"call get_state and read last_error, then fix it with write_shader."
	case ae.Code == "invalid_name":
		return head + "\nShader names must match ^[a-z0-9][a-z0-9_-]{0,63}$: lower-case letters, digits, _ and -, " +
			"starting with a letter or digit, at most 64 characters, without the .kage extension (e.g. 10_plasma). " +
			"Call list_shaders to check the exact name, then call the tool again."
	case ae.Code == "invalid_beats":
		return head + "\nbeats must satisfy 0 < beats <= 64, or omit beats to use the current default fade length shown by get_state."
	case ae.Code == "invalid_bpm":
		return head + "\nbpm must be between 40 and 300. Call set_bpm again with a value in that range."
	case ae.Code == "unit_pixels_required":
		return head + "\nThe source must include the line `//kage:unit pixels` (put it at the very top, before `package main`). Add it and call " + tool + " again."
	case ae.Code == "forbidden_name":
		return head + "\nkagelife was started with -llm-slots, so shaders can be written only to the reserved LLM slots (llm_slots in get_state). " +
			"Call write_back_slot with the full source: it writes the reserved slot that is not on screen. Then crossfade to it."
	case ae.Code == "slot_live":
		return head + "\nThat reserved slot is on screen (active, or the target of a running crossfade), so it cannot be overwritten. " +
			"Call write_back_slot to write the reserved slot that is not on screen, or wait for the crossfade to finish and check get_state."
	case ae.Code == "too_large" || ae.Status == http.StatusRequestEntityTooLarge:
		return head + "\nThe source can be at most 64KiB. Make it shorter (drop comments, unused helpers and repeated code) and call " + tool + " again with the full source."
	case ae.Code == "invalid_request":
		return head + "\nkagelife rejected the request as malformed or missing a required field. Check the tool arguments and call the tool again."
	case ae.Code == "internal_error" || ae.Status == http.StatusInternalServerError:
		return head + "\nkagelife hit an internal error. Check the kagelife GUI log, call get_state to see the current state, then retry."
	case ae.Code == "compile_error":
		return formatCompileError(ae, tool)
	}
	return head
}

// mayHaveWritten は PUT が応答を返さなかった（loop_timeout / 503 か、MCP 側のタイムアウト）かを返す。
// このときファイルは書けている場合がある（ADR-006 補足 11。-llm-slots 付きの PUT はゲームループを最大 3 回待つ）。
func mayHaveWritten(err error) bool {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Code == "loop_timeout" || ae.Status == http.StatusServiceUnavailable
	}
	var te *timeoutError
	return errors.As(err, &te)
}

// describeWriteErr は PUT の失敗を describeErrFor で文面にし、応答が無かったときは再送の前に確かめるよう添える。
func describeWriteErr(err error, tool, name string) string {
	msg := describeErrFor(err, tool)
	if mayHaveWritten(err) {
		msg += "\nThe shader file " + name + " may already have been written even though kagelife did not answer. " +
			"Before sending it again, check with read_shader name=" + name + " (or get_state) whether the new source is already there."
	}
	return msg
}

func formatCompileError(ae *apiError, tool string) string {
	var b strings.Builder
	b.WriteString("compile_error: the shader did not compile. Nothing was saved and the screen still shows the previous shader.\n")
	if ae.Message != "" {
		b.WriteString("summary: " + ae.Message + "\n")
	}
	if len(ae.Diagnostics) > 0 {
		b.WriteString("diagnostics (line:col: message):\n")
		for _, d := range ae.Diagnostics {
			if d.Line > 0 {
				fmt.Fprintf(&b, "%d:%d: %s\n", d.Line, d.Col, d.Message)
			} else {
				b.WriteString(d.Message + "\n")
			}
		}
	}
	b.WriteString("Fix the source and call " + tool + " again with the full corrected source. Call get_kage_guide if you are unsure what Kage allows.")
	return b.String()
}

func formatState(s stateResp) string {
	var b strings.Builder
	active := s.Active
	if active == "" {
		active = "(none)"
	}
	fmt.Fprintf(&b, "active: %s\n", active)
	fmt.Fprintf(&b, "shaders (%d): %s\n", len(s.Shaders), strings.Join(s.Shaders, ", "))
	b.WriteString(formatKeys(s))
	if s.BPMMeasured {
		fmt.Fprintf(&b, "bpm: %s (measured: tapped in the GUI or set with set_bpm)\n", fmtNum(s.BPM))
	} else {
		fmt.Fprintf(&b, "bpm: %s (default; nobody has tapped or called set_bpm yet)\n", fmtNum(s.BPM))
	}
	if s.Fade.Fading {
		fmt.Fprintf(&b, "fade: fading to %s, mix %.2f, beats %s\n", s.Fade.Target, s.Fade.Mix, fmtNum(s.Fade.Beats))
	} else {
		fmt.Fprintf(&b, "fade: idle (default length beats %s)\n", fmtNum(s.Fade.Beats))
	}
	fmt.Fprintf(&b, "llm_slots: %s\n", formatSlots(s))
	fmt.Fprintf(&b, "features: %s\n", orNone(strings.Join(s.Features, ", ")))
	fmt.Fprintf(&b, "fps: %s\n", fmtNum(s.FPS))
	if len(s.Resolution) == 2 {
		fmt.Fprintf(&b, "resolution: %dx%d\n", s.Resolution[0], s.Resolution[1])
	}
	if s.LastError != nil {
		fmt.Fprintf(&b, "last_error: shader %s: %s\n", s.LastError.Shader, s.LastError.Message)
	} else {
		b.WriteString("last_error: none\n")
	}
	return b.String()
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// formatSlots は予約名ごとに on screen / free を付けて並べる。
func formatSlots(s stateResp) string {
	parts := make([]string, 0, len(s.LLMSlots))
	for _, n := range s.LLMSlots {
		if s.onScreen(n) {
			parts = append(parts, n+" (on screen)")
		} else {
			parts = append(parts, n+" (free)")
		}
	}
	return orNone(strings.Join(parts, ", "))
}

// noSlotsMessage は llm_slots が空のときの案内。features で GUI が対応しているかを見分ける。
func noSlotsMessage(s stateResp) string {
	if !slices.Contains(s.Features, featureLLMSlots) {
		return "This kagelife does not support reserved LLM slots (get_state shows no llm_slots feature). " +
			"Update kagelife and start it with -llm-slots llm_a,llm_b to use write_back_slot. Nothing was written."
	}
	return "kagelife is running without reserved LLM slots. Ask the user to restart it with -llm-slots llm_a,llm_b to use write_back_slot. " +
		"Until then, use write_shader and switch_shader / crossfade. Nothing was written."
}

// writeBackSlot は映っていない予約名へ source を書く。409 slot_live なら state を 1 回だけ読み直して残りを試す。
func writeBackSlot(ctx context.Context, c *client, source string) *mcp.CallToolResult {
	var s stateResp
	if err := c.do(ctx, http.MethodGet, "/v1/state", nil, &s); err != nil {
		return errText(describeErr(err))
	}
	if len(s.LLMSlots) == 0 {
		return errText(noSlotsMessage(s))
	}
	var out struct {
		Name    string `json:"name"`
		Created bool   `json:"created"`
	}
	body := map[string]string{"source": source}
	put := func(name string) error { return c.do(ctx, http.MethodPut, shaderPath(name), body, &out) }

	name := s.slotCandidates()[0]
	err := put(name)
	if isSlotLive(err) {
		live := name
		if err := c.do(ctx, http.MethodGet, "/v1/state", nil, &s); err != nil {
			return errText(describeErr(err))
		}
		rest := slices.DeleteFunc(s.slotCandidates(), func(n string) bool { return n == live })
		if len(rest) == 0 {
			return errText(slotsBusyMessage([]string{live}))
		}
		name = rest[0]
		err = put(name)
		if isSlotLive(err) {
			return errText(slotsBusyMessage([]string{live, name}))
		}
	}
	if err != nil {
		return errText("While writing reserved slot " + name + ":\n" + describeWriteErr(err, "write_back_slot", name))
	}

	verb := "replaced"
	if out.Created {
		verb = "created"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Compiled and %s reserved slot %s (not on screen yet).\n", verb, name)
	fmt.Fprintf(&b, "Next: crossfade name=%s (e.g. 8 beats) to bring it on screen.\n", name)
	others := slices.DeleteFunc(slices.Clone(s.LLMSlots), func(n string) bool { return n == name })
	if len(others) > 0 {
		fmt.Fprintf(&b, "For the next song, call write_back_slot again: it will write the other side (%s), since %s will then be on screen.", strings.Join(others, ", "), name)
	} else {
		fmt.Fprintf(&b, "For the next song, %s can be written again only after it is off screen.", name)
	}
	return text(b.String())
}

func isSlotLive(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Code == "slot_live"
}

// slotsBusyMessage は試した予約名がすべて 409 slot_live だったときの案内。
// GUI の message は「他方に書け」と添えるが、他方も 409 だったので、その文面は見せない。
func slotsBusyMessage(tried []string) string {
	return fmt.Sprintf("slot_live: tried reserved slot %s, and kagelife answered slot_live (on screen) every time. "+
		"Nothing was written. A crossfade between the reserved slots is probably running, so writing the other slot by hand would fail too. "+
		"Wait for the fade to finish, call get_state, then call write_back_slot again.", strings.Join(tried, ", then "))
}

func registerTools(srv *mcp.Server, c *client) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_state",
		Description: "Get the live state of the kagelife VJ screen: active shader, all shader names, which shader each number key 1-9 selects, BPM, " +
			"crossfade progress, FPS, resolution and the most recent shader compile error (including errors from files edited on disk; " +
			"it is cleared as soon as any shader compiles successfully). " +
			"When kagelife runs with -llm-slots it also lists the reserved LLM slots (llm_slots) and which of them is on screen or free. " +
			"resolution is the window size; with -render-scale below 1 shaders are drawn smaller, so never hard-code it in a shader: always use the Resolution uniform. " +
			"Call this first to orient yourself, and after changes to confirm FPS stays near 60.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
		var s stateResp
		if err := c.do(ctx, http.MethodGet, "/v1/state", nil, &s); err != nil {
			return errText(describeErr(err)), nil, nil
		}
		return text(formatState(s)), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_shaders",
		Description: "List the names of all shader files (without the .kage extension) and mark the one currently on screen. " +
			"Use these names with read_shader, switch_shader and crossfade. " +
			"The list also includes files that failed to compile; those cannot be switched to until fixed (see last_error in get_state).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
		var s stateResp
		if err := c.do(ctx, http.MethodGet, "/v1/state", nil, &s); err != nil {
			return errText(describeErr(err)), nil, nil
		}
		if len(s.Shaders) == 0 {
			return text("There are no shaders yet. Create one with write_shader (see get_kage_guide for a template)."), nil, nil
		}
		var b strings.Builder
		for _, n := range s.Shaders {
			if n == s.Active {
				fmt.Fprintf(&b, "* %s (active)\n", n)
			} else {
				fmt.Fprintf(&b, "  %s\n", n)
			}
		}
		return text(b.String()), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "read_shader",
		Description: "Return the full Kage source of a shader by name. The result is the raw source text only. Read a shader before modifying it with write_shader.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameArgs) (*mcp.CallToolResult, any, error) {
		var out struct {
			Source string `json:"source"`
		}
		if err := c.do(ctx, http.MethodGet, shaderPath(in.Name), nil, &out); err != nil {
			return errText(describeErr(err)), nil, nil
		}
		return text(out.Source), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "write_shader",
		Description: "Create a shader or replace the entire source of an existing one (full replacement, not a patch: always send the complete file). " +
			"The source is compiled before anything is saved. If compilation fails, nothing is written, the screen keeps showing the previous shader, " +
			"and the error lists line:col diagnostics so you can fix the source and call write_shader again. " +
			"The source must contain `//kage:unit pixels`. Writing does not switch the screen to a new shader; " +
			"call switch_shader or crossfade afterwards (exception: if nothing was on screen, the written shader is shown). " +
			"If you replace the active shader, the screen updates immediately. " +
			"If get_state lists llm_slots (kagelife started with -llm-slots), only those reserved names can be written and not the one on screen: " +
			"use write_back_slot instead, then crossfade to the name it returns. " +
			"Call get_kage_guide first if you have not written Kage before.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in writeArgs) (*mcp.CallToolResult, any, error) {
		var out struct {
			Name    string `json:"name"`
			Created bool   `json:"created"`
		}
		body := map[string]string{"source": in.Source}
		if err := c.do(ctx, http.MethodPut, shaderPath(in.Name), body, &out); err != nil {
			return errText(describeWriteErr(err, "write_shader", in.Name)), nil, nil
		}
		if out.Created {
			return text(fmt.Sprintf("Compiled and created new shader %s. Writing does not switch the screen: call switch_shader or crossfade to show it "+
				"(only if nothing was on screen before is it shown right away).", in.Name)), nil, nil
		}
		return text(fmt.Sprintf("Compiled and replaced shader %s. If it is the active shader, the screen already shows the new version.", in.Name)), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "write_back_slot",
		Description: "Write a complete Kage source to the reserved LLM background slot that is not on screen " +
			"(kagelife started with -llm-slots, e.g. -llm-slots llm_a,llm_b on keys 8 and 9). " +
			"It reads get_state and writes the first reserved slot that is neither active nor the target of a running crossfade " +
			"(full replacement, compiled before saving; on a compile error nothing is saved and line:col diagnostics are returned). " +
			"Writing does not change the screen: then call crossfade with the returned name (e.g. 8 beats). " +
			"The next call writes the other side. If kagelife has no reserved slots, nothing is written and the reply says what to do.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sourceArgs) (*mcp.CallToolResult, any, error) {
		return writeBackSlot(ctx, c, in.Source), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "switch_shader",
		Description: "Cut to a shader immediately (hard switch, no transition). Use crossfade for a smooth beat-synced transition.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameArgs) (*mcp.CallToolResult, any, error) {
		var out struct {
			Active string `json:"active"`
		}
		if err := c.do(ctx, http.MethodPost, "/v1/switch", map[string]string{"name": in.Name}, &out); err != nil {
			return errText(describeErr(err)), nil, nil
		}
		return text("Now showing " + out.Active + "."), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "crossfade",
		Description: "Start a smooth crossfade from the current shader to another one, timed in beats of the current BPM. " +
			"The BPM is always between 40 and 300 (120 by default before anyone taps), so crossfade never fails because of the tempo. " +
			"beats is optional (0 < beats <= 64); when omitted the current default fade length is used. " +
			"The call returns immediately; the fade then runs on screen. " +
			"Calling crossfade again during a fade may continue, cancel or restart the fade; call get_state to see the result in active and fade.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in crossfadeArgs) (*mcp.CallToolResult, any, error) {
		body := map[string]any{"name": in.Name}
		if in.Beats != nil {
			body["beats"] = *in.Beats
		}
		var out struct {
			Target string  `json:"target"`
			Beats  float64 `json:"beats"`
		}
		if err := c.do(ctx, http.MethodPost, "/v1/crossfade", body, &out); err != nil {
			return errText(describeErr(err)), nil, nil
		}
		return text(fmt.Sprintf("Crossfading to %s over %s beats.", out.Target, fmtNum(out.Beats))), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_bpm",
		Description: "Set the tempo in BPM (40 to 300). This drives the Beat uniform (phase 0..1 within each beat) and crossfade timing. " +
			"Without it the tempo is the default 120 BPM (or whatever was tapped). " +
			"The moment you call it becomes the beat start (Beat = 0), and it discards the taps recorded so far. " +
			"Do not call it while the performer is tapping the tempo in the GUI to match the music. " +
			"A human tapping the tempo in the GUI may override it later. " +
			"If kagelife was started with -link and is synced to Ableton Link (e.g. rekordbox LINK, once Link has peers), BPM and Beat come from Link: " +
			"set_bpm is accepted but reverted to the Link tempo by the next frame, so do not call set_bpm while synced. " +
			"get_state cannot tell whether kagelife is synced to Link (the state has no Link field); ask the user if unsure.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in bpmArgs) (*mcp.CallToolResult, any, error) {
		var out struct {
			BPM float64 `json:"bpm"`
		}
		if err := c.do(ctx, http.MethodPost, "/v1/bpm", map[string]float64{"bpm": in.BPM}, &out); err != nil {
			return errText(describeErr(err)), nil, nil
		}
		return text("BPM set to " + fmtNum(out.BPM) + "."), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_kage_guide",
		Description: "Return a concise guide to writing Kage shaders for kagelife: language limits (Kage is Go-like but not Go), " +
			"the required //kage:unit pixels directive and Fragment signature, the uniforms kagelife provides (Time, Resolution, Beat, Cursor, Frame, Random, " +
			"and AudioBass, AudioMid, AudioHigh, AudioOnset, which are 0 unless kagelife was started with -audio), " +
			"a minimal template and VJ performance tips. Read this before your first write_shader. Works even when kagelife is not running.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
		return text(kageGuide), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_previews",
		Description: "Return a small preview of the shader on each number key 1-9: for each key its number, shader name and whether it failed to compile, " +
			"followed by a 72x72 JPEG image of that shader drawn alone with the current Time and Beat. " +
			"Use it to look at what you wrote before crossfading to it. " +
			"Each call makes kagelife draw these demo previews for about 2 seconds (extra GPU work). Do not call it repeatedly or in a loop: once per new look is enough. " +
			"The images are not the composite on screen (crossfades are not drawn); get_state tells what is active. " +
			"If kagelife was started with -previews=false, nothing is drawn and the reply says so.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
		return getPreviews(ctx, c), nil, nil
	})
}
