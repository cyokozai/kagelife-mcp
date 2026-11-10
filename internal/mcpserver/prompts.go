package mcpserver

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// vjSongSteps は vj_song が返す、曲に合わせて 1 本書く手順と注意。
const vjSongSteps = `Write one new Kage shader for the song that is playing now on the kagelife VJ screen, and bring it on screen.

1. Call get_state. Look at active, llm_slots, keys (which shader each number key selects) and fps. Call get_kage_guide and follow it.
2. If get_state lists llm_slots (kagelife started with -llm-slots), write with write_back_slot: it writes the reserved slot that is not on screen.
   If llm_slots is none, write a new name with write_shader, and suggest to the user that they restart kagelife with -llm-slots llm_a,llm_b so the screen is never overwritten.
3. If the write fails with 422 (compile_error or unit_pixels_required), read the diagnostics (line:col: message), fix the source and send the full source again with the same tool. Try at most 3 times; then stop and tell the user what failed.
4. When it compiles, call crossfade with the name you wrote (e.g. 8 beats). For the next song, write the other side: write_back_slot picks it by itself.

Rules:
- Make the motion ride the music with Beat (phase within the beat, 0 on the beat) and Time (slow drift), so one shader works for the whole song.
- Never flash the brightness of the whole screen more than 3 times per second (photosensitive viewers). Pulse on the beat with a smooth decay instead of hard full-screen strobes.
- If kagelife was started with -link (synced to Ableton Link, e.g. rekordbox LINK), never call set_bpm: the tempo comes from Link and set_bpm is reverted. get_state cannot tell whether Link is synced; if unsure, ask the user and leave the tempo alone.
- After the crossfade, call get_state again. If fps is below 50, write a lighter version (fewer loop iterations, fewer noise octaves, no deep raymarching) to the slot that is not on screen and crossfade to it.`

// vjSongPrompt は mood と bpm（どちらも任意）を差し込んだ vj_song の本文を返す。空白だけの値は無いものとして扱う。
func vjSongPrompt(args map[string]string) string {
	var b strings.Builder
	b.WriteString(vjSongSteps)
	mood := strings.TrimSpace(args["mood"])
	bpm := strings.TrimSpace(args["bpm"])
	if mood == "" && bpm == "" {
		return b.String()
	}
	b.WriteString("\n\nThe song:\n")
	if mood != "" {
		b.WriteString("- Mood: " + mood + "\n  Choose colors, shapes and motion that match this mood.\n")
	}
	if bpm != "" {
		b.WriteString("- Tempo: about " + bpm + " BPM\n  Compare it with bpm in get_state. Call set_bpm with it only if the user confirms that kagelife is not synced to Link (-link) and nobody is tapping the tempo.\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func registerPrompts(srv *mcp.Server) {
	srv.AddPrompt(&mcp.Prompt{
		Name:  "vj_song",
		Title: "Write a shader for the current song",
		Description: "Step-by-step instructions for writing one Kage shader that matches the song playing now and crossfading to it: " +
			"read get_state and get_kage_guide, write to the reserved LLM slot that is not on screen (write_back_slot) or with write_shader, " +
			"fix compile errors up to 3 times, crossfade over about 8 beats, and keep it safe (no fast full-screen flashing) and light (fps of at least 50).",
		Arguments: []*mcp.PromptArgument{
			{Name: "mood", Description: "optional: words describing the song's mood, e.g. dark minimal techno, euphoric trance"},
			{Name: "bpm", Description: "optional: the song's tempo in BPM, e.g. 128"},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{
			Description: "Write a shader for the current song on kagelife",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: vjSongPrompt(req.Params.Arguments)},
			}},
		}, nil
	})
}
