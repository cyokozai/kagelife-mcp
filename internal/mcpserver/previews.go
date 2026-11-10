package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// featurePreviews は GET /v1/previews を持つ GUI が features に出す名前（-previews=false のときは出さない）。
const featurePreviews = "previews"

// featureKeys は v0.2.0 以降の GUI が起動時のフラグによらず features に出す名前。
// previews が無くて keys が有れば、-previews=false で止めていると分かる。
const featureKeys = "keys"

// previewRetryDelay は jpeg が null のキーがあったときに取り直すまでの待ち時間。
// 止まっていた GUI への最初の要求は jpeg をすべて null で返し、約 150ms 後から描いた絵が入る（ADR-006 補足 28）。
const previewRetryDelay = 200 * time.Millisecond

// previewKey は GET /v1/previews の keys の 1 件。jpeg は base64 の文字列で届き、encoding/json が []byte へ復号する。
type previewKey struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	JPEG         []byte `json:"jpeg"`
	CompileError bool   `json:"compile_error"`
}

type previewsResp struct {
	Size  int          `json:"size"`
	Frame int          `json:"frame"`
	Keys  []previewKey `json:"keys"`
}

// notDrawn はまだ描けていない（コンパイル失敗ではないのに jpeg が null の）キーがあるかを返す。
func (p previewsResp) notDrawn() bool {
	return slices.ContainsFunc(p.Keys, func(k previewKey) bool { return k.JPEG == nil && !k.CompileError })
}

const previewsDisabledMessage = "kagelife is running with -previews=false, so GET /v1/previews is disabled and no previews can be shown. " +
	"Ask the user to restart kagelife without -previews=false if you need them; otherwise use get_state (keys) to see which shader each number key selects."

const previewsUnsupportedMessage = "This kagelife does not support previews (get_state shows no previews feature). " +
	"Update kagelife to v0.2.0 or later to use get_previews; until then use get_state (keys) to see which shader each number key selects."

// getPreviews は数字キーごとの 72x72 のプレビューを、キー・名前・状態の文字と JPEG の画像で返す。
func getPreviews(ctx context.Context, c *client) *mcp.CallToolResult {
	var s stateResp
	if err := c.do(ctx, http.MethodGet, "/v1/state", nil, &s); err != nil {
		return errText(describeErr(err))
	}
	if !slices.Contains(s.Features, featurePreviews) {
		if slices.Contains(s.Features, featureKeys) {
			return errText(previewsDisabledMessage)
		}
		return errText(previewsUnsupportedMessage)
	}

	fetch := func() (previewsResp, *mcp.CallToolResult) {
		var p previewsResp
		err := c.do(ctx, http.MethodGet, "/v1/previews", nil, &p)
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			return p, errText(previewsDisabledMessage)
		}
		if err != nil {
			return p, errText(describeErr(err))
		}
		return p, nil
	}
	p, res := fetch()
	if res != nil {
		return res
	}
	if p.notDrawn() {
		select {
		case <-ctx.Done():
			return errText(ctx.Err().Error())
		case <-time.After(previewRetryDelay):
		}
		if p, res = fetch(); res != nil {
			return res
		}
	}

	if p.Size <= 0 {
		p.Size = 72
	}
	if len(p.Keys) == 0 {
		return text("No shader is assigned to the number keys yet, so there is nothing to preview. Create one with write_shader.")
	}
	content := []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(
		"Previews of the shaders on the number keys (%dx%d JPEG each). Each image is that shader drawn alone with the current Time and Beat, "+
			"not the composite on screen (crossfades are not drawn). Call this again only when you need a new look; each call makes kagelife draw previews for about 2 seconds.",
		p.Size, p.Size)}}
	for _, k := range p.Keys {
		switch {
		case k.CompileError:
			content = append(content, &mcp.TextContent{Text: fmt.Sprintf("key %s = %s: compile error, no preview (see last_error in get_state, fix it with write_shader or write_back_slot)", k.Key, k.Name)})
		case k.JPEG == nil:
			content = append(content, &mcp.TextContent{Text: fmt.Sprintf("key %s = %s: not drawn yet (kagelife has just started drawing previews)", k.Key, k.Name)})
		default:
			content = append(content,
				&mcp.TextContent{Text: fmt.Sprintf("key %s = %s:", k.Key, k.Name)},
				// Data は生のバイト列。SDK が線上で base64 にする（本体の base64 は encoding/json が復号済み）。
				&mcp.ImageContent{MIMEType: "image/jpeg", Data: k.JPEG})
		}
	}
	return &mcp.CallToolResult{Content: content}
}
