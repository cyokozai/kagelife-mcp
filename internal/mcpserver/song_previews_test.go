package mcpserver

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ── プロンプト vj_song ─────────────────────────────────────

func getPrompt(t *testing.T, args map[string]string) string {
	t.Helper()
	_, cs := setup(t)
	res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "vj_song", Arguments: args})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Role != "user" {
		t.Fatalf("want one user message, got %+v", res.Messages)
	}
	tc, ok := res.Messages[0].Content.(*mcp.TextContent)
	if !ok {
		t.Fatalf("want text content, got %T", res.Messages[0].Content)
	}
	return tc.Text
}

func TestListPromptsHasVJSongWithOptionalArgs(t *testing.T) {
	_, cs := setup(t)
	res, err := cs.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Prompts) != 1 || res.Prompts[0].Name != "vj_song" {
		t.Fatalf("want only vj_song, got %+v", res.Prompts)
	}
	p := res.Prompts[0]
	if len(p.Description) < 30 {
		t.Errorf("description too short: %q", p.Description)
	}
	got := map[string]bool{}
	for _, a := range p.Arguments {
		got[a.Name] = true
		if a.Required {
			t.Errorf("argument %s must be optional", a.Name)
		}
		if a.Description == "" {
			t.Errorf("argument %s has no description", a.Name)
		}
	}
	if len(got) != 2 || !got["mood"] || !got["bpm"] {
		t.Errorf("want arguments mood and bpm, got %+v", p.Arguments)
	}
}

func TestVJSongPromptSteps(t *testing.T) {
	txt := getPrompt(t, nil)
	wantContains(t, txt,
		// 状態と書き方を読む
		"get_state", "active", "llm_slots", "keys", "fps", "get_kage_guide",
		// 予約スロットの有無で書き方を変える
		"write_back_slot", "write_shader", "-llm-slots",
		// 422 は診断を読んで直し、同じツールで 3 回まで
		"422", "diagnostics", "same tool", "3 times",
		// 通ったら crossfade、次の曲は逆側
		"crossfade", "8 beats", "next song", "other",
		// 拍と明滅と -link
		"Beat", "Time", "more than 3 times per second", "set_bpm", "-link",
		// 重さ
		"below 50", "lighter")
	for _, s := range []string{"Mood:", "Tempo:"} {
		if strings.Contains(txt, s) {
			t.Errorf("without arguments %q must not appear:\n%s", s, txt)
		}
	}
}

func TestVJSongPromptInsertsArgs(t *testing.T) {
	txt := getPrompt(t, map[string]string{"mood": "dark minimal techno", "bpm": "128"})
	wantContains(t, txt, "Mood: dark minimal techno", "Tempo: about 128 BPM")
}

func TestVJSongPromptIgnoresBlankArgs(t *testing.T) {
	txt := getPrompt(t, map[string]string{"mood": "  ", "bpm": ""})
	for _, s := range []string{"Mood:", "Tempo:"} {
		if strings.Contains(txt, s) {
			t.Errorf("blank arguments must not insert %q:\n%s", s, txt)
		}
	}
}

// ── get_previews ────────────────────────────────────────────

var (
	jpegA = []byte("\xff\xd8\xffA-jpeg")
	jpegB = []byte("\xff\xd8\xffB-jpeg")
)

// callContent は CallTool の Content をそのまま返す。
func callContent(t *testing.T, cs *mcp.ClientSession, name string) ([]mcp.Content, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool(%s) protocol error: %v", name, err)
	}
	return res.Content, res.IsError
}

func contentText(cs []mcp.Content) string {
	var b strings.Builder
	for _, c := range cs {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func images(cs []mcp.Content) []*mcp.ImageContent {
	var out []*mcp.ImageContent
	for _, c := range cs {
		if ic, ok := c.(*mcp.ImageContent); ok {
			out = append(out, ic)
		}
	}
	return out
}

func TestGetPreviewsDescription(t *testing.T) {
	d := toolDescription(t, "get_previews")
	wantContains(t, d, "about 2 seconds", "Do not call it repeatedly", "not the composite", "-previews=false")
}

func TestGetPreviewsReturnsImagesPerKey(t *testing.T) {
	f, cs := setup(t)
	f.with(func() {
		f.previewFrames = [][]fakePreviewKey{{
			{Key: "1", Name: "01_uv", JPEG: jpegA},
			{Key: "2", Name: "02_time_sin", JPEG: jpegB},
		}}
	})
	content, isErr := callContent(t, cs, "get_previews")
	if isErr {
		t.Fatalf("unexpected isError: %s", contentText(content))
	}
	imgs := images(content)
	if len(imgs) != 2 {
		t.Fatalf("want 2 images, got %d: %+v", len(imgs), content)
	}
	for i, want := range [][]byte{jpegA, jpegB} {
		if imgs[i].MIMEType != "image/jpeg" {
			t.Errorf("image %d: mimeType %q", i, imgs[i].MIMEType)
		}
		// 本体の base64 は encoding/json が復号済み。ImageContent.Data は生のバイト列で、SDK が線上で base64 にする。
		if !bytes.Equal(imgs[i].Data, want) {
			t.Errorf("image %d: data %q, want %q", i, imgs[i].Data, want)
		}
	}
	txt := contentText(content)
	wantContains(t, txt, "key 1 = 01_uv", "key 2 = 02_time_sin", "not the composite")
	// 各キーの文字の直後にそのキーの画像を置く。
	if _, ok := content[1].(*mcp.TextContent); !ok {
		t.Errorf("content[1] should be the text for key 1, got %T", content[1])
	}
	if ic, ok := content[2].(*mcp.ImageContent); !ok || !bytes.Equal(ic.Data, jpegA) {
		t.Errorf("content[2] should be the image for key 1, got %T", content[2])
	}
	if n := f.previewCallCount(); n != 1 {
		t.Errorf("GET /v1/previews called %d times, want 1 (no retry when every jpeg is present)", n)
	}
}

// 止まった状態からの最初の要求は jpeg が null で、約 150ms 後から入る（ADR-006 補足 28）。1 回だけ取り直す。
func TestGetPreviewsRetriesOnceWhenNotDrawnYet(t *testing.T) {
	f, cs := setup(t)
	f.with(func() {
		f.previewFrames = [][]fakePreviewKey{
			{{Key: "1", Name: "01_uv"}, {Key: "2", Name: "02_time_sin"}, {Key: "3", Name: "03_bad", CompileError: true}},
			{{Key: "1", Name: "01_uv", JPEG: jpegA}, {Key: "2", Name: "02_time_sin"}, {Key: "3", Name: "03_bad", CompileError: true}},
			{{Key: "1", Name: "01_uv", JPEG: jpegA}, {Key: "2", Name: "02_time_sin", JPEG: jpegB}, {Key: "3", Name: "03_bad", CompileError: true}},
		}
	})
	content, isErr := callContent(t, cs, "get_previews")
	if isErr {
		t.Fatalf("unexpected isError: %s", contentText(content))
	}
	if n := f.previewCallCount(); n != 2 {
		t.Fatalf("GET /v1/previews called %d times, want 2 (one retry)", n)
	}
	imgs := images(content)
	if len(imgs) != 1 || !bytes.Equal(imgs[0].Data, jpegA) {
		t.Fatalf("want only the image of key 1 from the retry, got %+v", imgs)
	}
	txt := contentText(content)
	wantContains(t, txt, "key 2 = 02_time_sin: not drawn yet", "key 3 = 03_bad: compile error")
}

// コンパイル失敗の null は待っても埋まらないので、取り直さない。
func TestGetPreviewsDoesNotRetryForCompileErrors(t *testing.T) {
	f, cs := setup(t)
	f.with(func() {
		f.previewFrames = [][]fakePreviewKey{{{Key: "1", Name: "01_uv", JPEG: jpegA}, {Key: "2", Name: "02_bad", CompileError: true}}}
	})
	content, _ := callContent(t, cs, "get_previews")
	if n := f.previewCallCount(); n != 1 {
		t.Errorf("GET /v1/previews called %d times, want 1", n)
	}
	wantContains(t, contentText(content), "key 2 = 02_bad: compile error", "last_error")
}

func TestGetPreviewsNoKeys(t *testing.T) {
	f, cs := setup(t)
	f.with(func() { f.order, f.active = nil, "" })
	content, isErr := callContent(t, cs, "get_previews")
	if isErr {
		t.Fatalf("unexpected isError: %s", contentText(content))
	}
	wantContains(t, contentText(content), "No shader is assigned to the number keys")
}

// -previews=false: features に previews が無いので GET /v1/previews を呼ばずに案内する。
func TestGetPreviewsDisabledByFeatures(t *testing.T) {
	f, cs := setup(t)
	f.with(func() { f.noPreviews = true })
	content, isErr := callContent(t, cs, "get_previews")
	if !isErr {
		t.Fatalf("want isError, got %s", contentText(content))
	}
	wantContains(t, contentText(content), "-previews=false")
	if n := f.previewCallCount(); n != 0 {
		t.Errorf("GET /v1/previews called %d times, want 0", n)
	}
}

// features に previews があっても 404 なら同じ案内をする。
func TestGetPreviews404(t *testing.T) {
	f, cs := setup(t)
	f.setOverride(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/previews" {
			writeFakeErr(w, http.StatusNotFound, "not_found", "no such endpoint")
			return
		}
		f.state(w, r)
	})
	content, isErr := callContent(t, cs, "get_previews")
	if !isErr {
		t.Fatalf("want isError, got %s", contentText(content))
	}
	txt := contentText(content)
	wantContains(t, txt, "-previews=false")
	if strings.Contains(txt, "list_shaders") {
		t.Errorf("a 404 here is not a missing shader:\n%s", txt)
	}
}

// v0.2.0 より前の kagelife（features に keys も previews も無い）には更新を勧める。
func TestGetPreviewsOldKagelife(t *testing.T) {
	f, cs := setup(t)
	f.with(func() { f.legacy = true })
	content, isErr := callContent(t, cs, "get_previews")
	if !isErr {
		t.Fatalf("want isError, got %s", contentText(content))
	}
	wantContains(t, contentText(content), "does not support previews", "Update kagelife")
	if n := f.previewCallCount(); n != 0 {
		t.Errorf("GET /v1/previews called %d times, want 0", n)
	}
}
