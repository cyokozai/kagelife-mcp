// Command kagelife-mcp is a stdio MCP server that lets LLMs live-code Kage
// shaders on a running kagelife GUI. It takes no arguments.
package main

import (
	"os"

	"github.com/cyokozai/kagelife-mcp/internal/mcpserver"
)

func main() {
	os.Exit(mcpserver.Main(os.Args[1:]))
}
