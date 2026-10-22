package mcpserver

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
func describeErr(err error) string {
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
		return head + "\nThe source must include the line `//kage:unit pixels` (put it at the very top, before `package main`). Add it and call write_shader again."
	case ae.Code == "invalid_request":
		return head + "\nkagelife rejected the request as malformed or missing a required field. Check the tool arguments and call the tool again."
	case ae.Code == "internal_error" || ae.Status == http.StatusInternalServerError:
		return head + "\nkagelife hit an internal error. Check the kagelife GUI log, call get_state to see the current state, then retry."
	case ae.Code == "compile_error":
		return formatCompileError(ae)
	}
	return head
}

func formatCompileError(ae *apiError) string {
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
	b.WriteString("Fix the source and call write_shader again with the full corrected source. Call get_kage_guide if you are unsure what Kage allows.")
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

func registerTools(srv *mcp.Server, c *client) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_state",
		Description: "Get the live state of the kagelife VJ screen: active shader, all shader names, BPM, " +
			"crossfade progress, FPS, resolution and the most recent shader compile error (including errors from files edited on disk; " +
			"it is cleared as soon as any shader compiles successfully). " +
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
			"Call get_kage_guide first if you have not written Kage before.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in writeArgs) (*mcp.CallToolResult, any, error) {
		var out struct {
			Name    string `json:"name"`
			Created bool   `json:"created"`
		}
		body := map[string]string{"source": in.Source}
		if err := c.do(ctx, http.MethodPut, shaderPath(in.Name), body, &out); err != nil {
			return errText(describeErr(err)), nil, nil
		}
		if out.Created {
			return text(fmt.Sprintf("Compiled and created new shader %s. Writing does not switch the screen: call switch_shader or crossfade to show it "+
				"(only if nothing was on screen before is it shown right away).", in.Name)), nil, nil
		}
		return text(fmt.Sprintf("Compiled and replaced shader %s. If it is the active shader, the screen already shows the new version.", in.Name)), nil, nil
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
			"A human tapping the tempo in the GUI may override it later.",
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
			"the required //kage:unit pixels directive and Fragment signature, the uniforms kagelife provides (Time, Resolution, Beat, Cursor, Frame, Random), " +
			"a minimal template and VJ performance tips. Read this before your first write_shader. Works even when kagelife is not running.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
		return text(kageGuide), nil, nil
	})
}
