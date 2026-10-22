package mcpserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const helperEnv = "KAGEMCP_STDIO_HELPER"

// TestMain は helperEnv が立っているとき、テストバイナリ自身を
// kagelife-mcp の stdio サーバとして動かす（E2E 用）。
func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		os.Exit(Main(nil))
	}
	os.Exit(m.Run())
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type proc struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan string
	stderr *bytes.Buffer
}

func startHelper(t *testing.T, controlFile string) *proc {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"=1", "KAGELIFE_CONTROL_FILE="+controlFile)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &proc{t: t, cmd: cmd, stdin: stdin, lines: make(chan string, 64), stderr: &bytes.Buffer{}}
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
		close(p.lines)
	}()
	return p
}

func (p *proc) send(s string) {
	p.t.Helper()
	if _, err := io.WriteString(p.stdin, s+"\n"); err != nil {
		p.t.Fatal(err)
	}
}

// recv は id が一致する応答まで読む。stdout の全行が JSON-RPC 2.0 であることも検査する。
func (p *proc) recv(id int) rpcMsg {
	p.t.Helper()
	for {
		select {
		case l, ok := <-p.lines:
			if !ok {
				p.t.Fatalf("stdout closed; stderr=%s", p.stderr.String())
			}
			var m rpcMsg
			if err := json.Unmarshal([]byte(l), &m); err != nil || m.JSONRPC != "2.0" {
				p.t.Fatalf("non JSON-RPC line on stdout: %q", l)
			}
			if m.ID != nil && *m.ID == id {
				if len(m.Error) > 0 {
					p.t.Fatalf("id %d error: %s", id, m.Error)
				}
				return m
			}
		case <-time.After(10 * time.Second):
			p.t.Fatalf("timeout waiting id %d; stderr=%s", id, p.stderr.String())
		}
	}
}

func (p *proc) waitExit(timeout time.Duration) {
	p.t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			p.t.Fatalf("exit: %v; stderr=%s", err, p.stderr.String())
		}
	case <-time.After(timeout):
		p.t.Fatal("process did not exit")
	}
	for l := range p.lines {
		if !json.Valid([]byte(l)) {
			p.t.Fatalf("non JSON line on stdout: %q", l)
		}
	}
}

func (p *proc) initialize() {
	p.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`)
	if r := p.recv(1); !strings.Contains(string(r.Result), `"kagelife"`) {
		p.t.Fatalf("initialize result: %s", r.Result)
	}
	p.send(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)
}

func TestStdioRawJSONRPC(t *testing.T) {
	f := newFakeControl(t)
	p := startHelper(t, f.writeDiscovery(t, t.TempDir()))
	p.initialize()

	p.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	list := string(p.recv(2).Result)
	for _, n := range []string{"get_state", "list_shaders", "read_shader", "write_shader", "switch_shader", "crossfade", "set_bpm", "get_kage_guide"} {
		if !strings.Contains(list, `"`+n+`"`) {
			t.Fatalf("tools/list missing %s", n)
		}
	}

	p.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_state","arguments":{}}}`)
	if r := string(p.recv(3).Result); !strings.Contains(r, "01_uv") || strings.Contains(r, `"isError":true`) {
		t.Fatalf("get_state: %s", r)
	}

	p.send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"write_shader","arguments":{"name":"x","source":"package main"}}}`)
	if r := string(p.recv(4).Result); !strings.Contains(r, `"isError":true`) || !strings.Contains(r, "unit_pixels_required") {
		t.Fatalf("write_shader: %s", r)
	}

	_ = p.stdin.Close() // EOF で正常終了する
	p.waitExit(5 * time.Second)
	if !strings.Contains(p.stderr.String(), "kagelife-mcp") {
		t.Fatalf("expected logs on stderr, got %q", p.stderr.String())
	}
}

func TestStdioSurvivesGUINotRunning(t *testing.T) {
	p := startHelper(t, t.TempDir()+"/none.json")
	p.initialize()
	for id := 2; id <= 3; id++ {
		p.send(`{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"tools/call","params":{"name":"get_state","arguments":{}}}`)
		r := string(p.recv(id).Result)
		if !strings.Contains(r, `"isError":true`) || !strings.Contains(r, "not running") {
			t.Fatalf("get_state: %s", r)
		}
	}
	_ = p.stdin.Close()
	p.waitExit(5 * time.Second)
}

func TestStdioExitsOnSIGTERM(t *testing.T) {
	p := startHelper(t, t.TempDir()+"/none.json")
	p.initialize()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	p.waitExit(5 * time.Second)
}
