package mcpserver

import (
	"fmt"
	"slices"
	"strings"
)

// numKeys は kagelife の数字キー 1〜9 の数。
const numKeys = 9

// maxLLMSlots は -llm-slots に書ける予約名の上限（キー 8 と 9。本体 internal/control の MaxLLMSlots）。
const maxLLMSlots = 2

// keyAssignments はキー 1〜9（添字 0〜8）が指すシェーダ名を返す。何も指さないキーは空文字。
// shaders は GET /v1/state の shaders（ファイル名順）、reserved は llm_slots。
//
// 本体 kagelife の llmkeys.go の keySlots（2026-11-11 の dev、v0.2.0）の規則を書き写したもの:
//   - reserved が空: ファイル名順の先頭 9 本。
//   - reserved が有り: 1〜7 は予約名を除いたシェーダのファイル名順の先頭 7 本、
//     8 / 9 は予約名の 1 個目 / 2 個目（ファイルがまだ無ければ空）。
//
// 本体の規則が変わったら、ここも合わせて直す必要がある（GET /v1/previews の keys が本体の正だが、
// 呼ぶたびに本体が約 2 秒プレビューを描くので get_state からは呼ばない）。
func keyAssignments(shaders, reserved []string) [numKeys]string {
	var keys [numKeys]string
	if len(reserved) == 0 {
		copy(keys[:], shaders)
		return keys
	}
	plain := numKeys - maxLLMSlots
	k := 0
	for _, n := range shaders {
		if k >= plain {
			break
		}
		if !slices.Contains(reserved, n) {
			keys[k] = n
			k++
		}
	}
	for j, n := range reserved {
		if j < maxLLMSlots && slices.Contains(shaders, n) {
			keys[plain+j] = n
		}
	}
	return keys
}

// formatKeys は get_state に出す数字キーの割り当て。予約名のキーはファイルが無くても出す。
func formatKeys(s stateResp) string {
	keys := keyAssignments(s.Shaders, s.LLMSlots)
	plain := numKeys - maxLLMSlots
	var b strings.Builder
	for i, n := range keys {
		switch {
		case len(s.LLMSlots) > 0 && i >= plain && i-plain < len(s.LLMSlots):
			slot := s.LLMSlots[i-plain]
			if n == "" {
				fmt.Fprintf(&b, "  key %d = %s (reserved LLM slot, no file yet)\n", i+1, slot)
			} else {
				fmt.Fprintf(&b, "  key %d = %s (reserved LLM slot)\n", i+1, n)
			}
		case n != "":
			fmt.Fprintf(&b, "  key %d = %s\n", i+1, n)
		}
	}
	if b.Len() == 0 {
		return "keys: none\n"
	}
	if len(s.LLMSlots) > 0 {
		b.WriteString("  key 0 = back to the last non-LLM shader\n")
	}
	var none []string
	for _, n := range s.Shaders {
		if !slices.Contains(keys[:], n) {
			none = append(none, n)
		}
	}
	return "keys (number keys in the kagelife window, also used by Stream Deck):\n" + b.String() +
		"no key (use switch_shader / crossfade by name): " + orNone(strings.Join(none, ", ")) + "\n"
}
