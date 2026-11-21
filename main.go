// Command kagelife-mcp is a stdio MCP server that lets LLMs live-code Kage
// shaders on a running kagelife GUI. Run without arguments it serves MCP on stdio.
//
// `kagelife-mcp nowplaying-spotify [login] ...` instead runs the Spotify watcher that writes
// now_playing.json (see tools/nowplaying-spotify/README.md).
package main

import (
	"os"

	"github.com/cyokozai/kagelife-mcp/internal/mcpserver"
	"github.com/cyokozai/kagelife-mcp/internal/nowplaying/spotify"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 && args[0] == spotify.Command {
		return spotify.Main(args[1:])
	}
	return mcpserver.Main(args)
}
