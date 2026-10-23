// Package mcpserver は kagelife（GUI）の制御口 v1 へツール呼び出しを中継する
// stdio MCP サーバ（kagelife-mcp）の本体。
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// usage は引数を付けて起動されたときに stderr へ出す説明。
const usage = "usage: kagelife-mcp\n\nRuns a stdio MCP server (no arguments) that relays tool calls to a running kagelife GUI.\n"

// Version はサーバ情報として MCP クライアントへ名乗る版。
var Version = "0.1.0"

// Config はサーバの設定。ゼロ値で本番の既定動作になる。
type Config struct {
	// ControlFile は発見ファイルのパス。空なら呼び出しごとに
	// KAGELIFE_CONTROL_FILE、無ければ os.UserCacheDir()/kagelife/control.json を使う。
	ControlFile string
	// Timeout は制御口への 1 リクエストの上限。0 なら 5 秒。
	Timeout time.Duration
}

// NewServer はツールを登録した MCP サーバを返す。
func NewServer(cfg Config) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "kagelife", Version: Version}, &mcp.ServerOptions{
		Instructions: "Tools for live-coding Kage shaders on a running kagelife VJ screen. " +
			"Start with get_state, read get_kage_guide before writing shaders, " +
			"then iterate with write_shader and switch_shader / crossfade.",
	})
	registerTools(srv, newClient(cfg))
	return srv
}

// Run は transport 上でサーバを動かす。ctx の取り消しと入力の EOF は正常終了として nil を返す。
func Run(ctx context.Context, cfg Config, t mcp.Transport) error {
	err := NewServer(cfg).Run(ctx, t)
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || ctx.Err() != nil {
		return nil
	}
	return err
}

// Main は kagelife-mcp の本体。stdout は JSON-RPC 専用で、ログは stderr に出す。
// 戻り値はプロセスの終了コード。
func Main(args []string) int {
	log.SetOutput(os.Stderr)
	if len(args) > 0 {
		fmt.Fprint(os.Stderr, usage)
		if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
			return 0
		}
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("kagelife-mcp: serving MCP on stdio (version %s)", Version)
	if err := Run(ctx, Config{}, &mcp.StdioTransport{}); err != nil {
		log.Printf("kagelife-mcp: %v", err)
		return 1
	}
	log.Printf("kagelife-mcp: exit")
	return 0
}
