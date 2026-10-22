package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// defaultTimeout は制御口への 1 リクエストあたりの上限。
const defaultTimeout = 5 * time.Second

// controlFileEnv は発見ファイルの場所を上書きする環境変数。
const controlFileEnv = "KAGELIFE_CONTROL_FILE"

// controlVersion は対応する制御口の版（発見ファイルの version）。
const controlVersion = 1

// maxResponseBytes は制御口の応答を読む上限（シェーダは 64KiB 以下なので十分）。
const maxResponseBytes = 4 << 20

// errNotRunning は GUI が起動していない（発見ファイルが無い・接続拒否）ことを表す。
var errNotRunning = errors.New("kagelife (the GUI) is not running. Start `kagelife` first, then call this tool again")

// endpoint は発見ファイルの中身。
type endpoint struct {
	Addr      string `json:"addr"`
	Token     string `json:"token"`
	PID       int    `json:"pid"`
	ShaderDir string `json:"shader_dir"`
	Version   int    `json:"version"`

	addrPort netip.AddrPort // Addr を checkLoopback で解析した値。URL はこれから組み立てる
}

// apiError は制御口のエラー共通形。
type apiError struct {
	Status      int          `json:"-"`
	Code        string       `json:"error"`
	Message     string       `json:"message"`
	Diagnostics []diagnostic `json:"diagnostics,omitempty"`
}

type diagnostic struct {
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Message string `json:"message"`
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s (HTTP %d): %s", e.Code, e.Status, e.Message)
}

// discoveryPath は発見ファイルのパスを決める。override > 環境変数 > UserCacheDir の順。
func discoveryPath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if p := os.Getenv(controlFileEnv); p != "" {
		return p, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate the kagelife control file: %w", err)
	}
	return filepath.Join(dir, "kagelife", "control.json"), nil
}

// checkLoopback は addr がループバックの IP リテラルと 0 以外の数字の port であることを確かめ、
// 解析済みの値を返す。GUI は待受の IP リテラルをそのまま書く（ADR-006 補足 9）ので、
// ホスト名（localhost を含む）や zone 付きの IPv6 は受け付けない。
// net.SplitHostPort は port が数字かを確かめず、"127.0.0.1:80@evil.example" を通してしまうため使わない。
func checkLoopback(addr string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil || !ap.Addr().IsLoopback() || ap.Addr().Zone() != "" || ap.Port() == 0 {
		return netip.AddrPort{}, fmt.Errorf("refusing control address %q: only a loopback IP literal with a port (e.g. 127.0.0.1:12345) is allowed", addr)
	}
	return ap, nil
}

// controlURL は解析済みのアドレスと path（シェーダ名は PathEscape 済み）から URL を組み立てる。
func controlURL(ap netip.AddrPort, path string) (string, error) {
	p, err := url.PathUnescape(path)
	if err != nil {
		return "", fmt.Errorf("invalid request path %q: %w", path, err)
	}
	u := url.URL{Scheme: "http", Host: ap.String(), Path: p, RawPath: path}
	return u.String(), nil
}

// client は制御口への HTTP クライアント。呼び出しごとに発見ファイルを読み直す。
type client struct {
	controlFile string
	http        *http.Client
}

func newClient(cfg Config) *client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &client{controlFile: cfg.ControlFile, http: &http.Client{Timeout: timeout}}
}

func (c *client) discover() (endpoint, error) {
	p, err := discoveryPath(c.controlFile)
	if err != nil {
		return endpoint{}, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return endpoint{}, fmt.Errorf("%w (no control file at %s)", errNotRunning, p)
	}
	if err != nil {
		return endpoint{}, fmt.Errorf("cannot read the kagelife control file %s: %w", p, err)
	}
	var ep endpoint
	if err := json.Unmarshal(b, &ep); err != nil {
		return endpoint{}, fmt.Errorf("the kagelife control file %s is malformed: %w", p, err)
	}
	if ep.Version != controlVersion {
		return endpoint{}, fmt.Errorf("version mismatch: kagelife and kagelife-mcp are not compatible (control file version %d, kagelife-mcp supports version %d). Update kagelife or kagelife-mcp so that they match", ep.Version, controlVersion)
	}
	ap, err := checkLoopback(ep.Addr)
	if err != nil {
		return endpoint{}, err
	}
	ep.addrPort = ap
	return ep, nil
}

// do は method path に body（nil 可）を JSON で送り、2xx なら out へ復号する。
// 非 2xx は *apiError を返す。
func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	ep, err := c.discover()
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	u, err := controlURL(ep.addrPort, path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return fmt.Errorf("%w (nothing is listening on %s; the control file may be stale)", errNotRunning, ep.Addr)
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return fmt.Errorf("kagelife did not respond within %s; it may be busy or frozen. Try again", c.http.Timeout)
		}
		return fmt.Errorf("cannot reach kagelife at %s: %w", ep.Addr, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("reading kagelife response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		ae := &apiError{Status: resp.StatusCode}
		if json.Unmarshal(data, ae) != nil || ae.Code == "" {
			ae.Code = http.StatusText(resp.StatusCode)
			ae.Message = string(bytes.TrimSpace(data))
		}
		return ae
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("unexpected response from kagelife: %w", err)
	}
	return nil
}
