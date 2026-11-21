package spotify

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

type cliRun struct {
	rc       int
	out, err string
}

func runCLITest(t *testing.T, env map[string]string, cfg cliConfig, args ...string) cliRun {
	t.Helper()
	var out, errb syncBuffer
	if cfg.Home == "" {
		cfg.Home = t.TempDir()
	}
	rc := runCLI(bg, args, func(k string) string { return env[k] }, &out, &errb, cfg)
	return cliRun{rc, out.String(), errb.String()}
}

// Python: CliTest.test_default_command_is_watch
func TestParseDefaults(t *testing.T) {
	o, rc := parseArgs([]string{"--client-id", "x"}, func(string) string { return "" }, "/home/u", &syncBuffer{}, &syncBuffer{})
	if rc != -1 {
		t.Fatalf("rc = %d", rc)
	}
	want := options{command: "watch", clientID: "x", token: "/home/u/.config/kagelife/spotify-token.json",
		out: "/home/u/.cache/kagelife/now_playing.json", interval: 3, redirectURI: DefaultRedirectURI,
		listenHost: "0.0.0.0", listenPort: 8888, loginTimeout: 300}
	if o != want {
		t.Fatalf("o = %+v", o)
	}
}

// Python: CliTest.test_client_id_from_env; the command may come before or after the flags.
func TestClientIDFromEnvAndCommandAnywhere(t *testing.T) {
	env := func(k string) string {
		if k == ClientIDEnv {
			return "from-env"
		}
		return ""
	}
	for _, args := range [][]string{{"login", "--token", "/t"}, {"--token", "/t", "login"}, {"--token=/t", "-v", "login"}} {
		o, rc := parseArgs(args, env, "/h", &syncBuffer{}, &syncBuffer{})
		if rc != -1 || o.command != "login" || o.clientID != "from-env" || o.token != "/t" {
			t.Fatalf("%v: %+v rc=%d", args, o, rc)
		}
	}
}

func TestArgumentErrorsExit2(t *testing.T) {
	cases := [][]string{
		{"--nope"}, {"play"}, {"watch", "login"}, {"--interval", "abc"},
		{"--client-id", ""}, {"--client-id", "x", "--interval", "0"}, {"--client-id", "x", "--interval", "-1"},
	}
	for _, args := range cases {
		if r := runCLITest(t, nil, cliConfig{}, args...); r.rc != 2 {
			t.Errorf("%v: rc = %d (%s)", args, r.rc, r.err)
		}
	}
	if r := runCLITest(t, nil, cliConfig{}); r.rc != 2 || !strings.Contains(r.err, "no Client ID") {
		t.Fatalf("no client id: %+v", r)
	}
	if r := runCLITest(t, nil, cliConfig{}, "-h"); r.rc != 0 || !strings.Contains(r.out, "--interval") {
		t.Fatalf("help: %+v", r)
	}
}

// onceFixture: a fake Spotify that answers currently-playing and queue, and a valid token file.
func onceFixture(t *testing.T, replies ...reply) (cliConfig, string, string) {
	dir, out, tok := tmpPaths(t)
	_ = dir
	saveTestTokens(t, tok, 1e10)
	f := newFakeSpotify(t, replies...)
	return cliConfig{TokenURL: f.tokenURL(), APIBase: f.apiBase(), HTTP: f.srv.Client(),
		Clock: newClock().now}, out, tok
}

// Python: OnceTest.test_once_prints_and_does_not_write
func TestOncePrintsAndDoesNotWrite(t *testing.T) {
	cur := map[string]any{"currently_playing_type": "track", "is_playing": true, "progress_ms": 30000,
		"item": map[string]any{"type": "track", "id": "t1", "name": "Song A", "duration_ms": 200000,
			"album": map[string]any{"name": "Album A"}, "artists": []any{map[string]any{"name": "Artist A"}}}}
	cfg, out, tok := onceFixture(t, reply{status: 200, body: cur}, reply{status: 200, body: map[string]any{"queue": []any{}}})
	r := runCLITest(t, nil, cfg, "--client-id", "cid", "--once", "--out", out, "--token", tok)
	if r.rc != 0 {
		t.Fatalf("rc = %d: %s", r.rc, r.err)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(r.out), &p); err != nil || p["title"] != "Song A" {
		t.Fatalf("out = %q", r.out)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("written")
	}
}

// Python: OnceTest.test_once_errors_exit_1_without_traceback
func TestOnceErrorsExit1(t *testing.T) {
	for _, rp := range []reply{
		{status: 429, headers: map[string]string{"Retry-After": "5"}},
		{status: 503},
	} {
		cfg, out, tok := onceFixture(t, rp)
		r := runCLITest(t, nil, cfg, "--client-id", "cid", "--once", "--out", out, "--token", tok)
		if r.rc != 1 || r.out != "" {
			t.Fatalf("%d: %+v", rp.status, r)
		}
		if _, err := os.Stat(out); err == nil {
			t.Fatal("written")
		}
	}
	// network error
	cfg, out, tok := onceFixture(t)
	cfg.APIBase = "http://127.0.0.1:1/v1"
	if r := runCLITest(t, nil, cfg, "--client-id", "cid", "--once", "--out", out, "--token", tok); r.rc != 1 || r.out != "" {
		t.Fatalf("network: %+v", r)
	}
	// no token file
	_, out2, tok2 := tmpPaths(t)
	if r := runCLITest(t, nil, cfg, "--client-id", "cid", "--once", "--out", out2, "--token", tok2); r.rc != 1 ||
		!strings.Contains(r.err, "run `login` first") {
		t.Fatalf("no token: %+v", r)
	}
}

func TestOncePrintsNullWhenNothingPlays(t *testing.T) {
	cfg, out, tok := onceFixture(t, reply{status: 204})
	r := runCLITest(t, nil, cfg, "--client-id", "cid", "--once", "--out", out, "--token", tok)
	if r.rc != 0 || r.out != "null\n" {
		t.Fatalf("%+v", r)
	}
}

func TestWatchWithoutTokenExits1(t *testing.T) {
	_, out, tok := tmpPaths(t)
	r := runCLITest(t, nil, cliConfig{}, "--client-id", "cid", "--out", out, "--token", tok, "--interval", "0.01")
	if r.rc != 1 || !strings.Contains(r.err, "run `login` first") {
		t.Fatalf("%+v", r)
	}
}

// The real process: SIGTERM and SIGINT end the watch with exit code 0.
func TestSignalEndsWatchWithZero(t *testing.T) {
	if os.Getenv("NPGO_HELPER") == "1" {
		return
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		_, out, tok := tmpPaths(t)
		saveTestTokens(t, tok, 1e10)
		f := newFakeSpotify(t)
		f.mu.Lock()
		for i := 0; i < 50; i++ {
			f.replies = append(f.replies, reply{status: 204})
		}
		f.mu.Unlock()
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		cmd.Env = append(os.Environ(), "NPGO_HELPER=1", "NPGO_API="+f.apiBase(),
			"NPGO_ARGS=--client-id\x1fcid\x1f--out\x1f"+out+"\x1f--token\x1f"+tok+"\x1f--interval\x1f0.05")
		var errb syncBuffer
		cmd.Stderr = &errb
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(errb.String(), "watching Spotify") && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		_ = cmd.Process.Signal(sig)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%v: %v\n%s", sig, err, errb.String())
			}
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatalf("%v: did not stop", sig)
		}
	}
}

// TestHelperProcess runs Main against a fake API in a child process (see TestSignalEndsWatchWithZero).
func TestHelperProcess(t *testing.T) {
	if os.Getenv("NPGO_HELPER") != "1" {
		t.Skip("helper process only")
	}
	api := os.Getenv("NPGO_API")
	args := strings.Split(os.Getenv("NPGO_ARGS"), "\x1f")
	ctx, stop := signalContext()
	defer stop()
	rc := runCLI(ctx, args, os.Getenv, os.Stdout, os.Stderr, cliConfig{APIBase: api, Home: os.TempDir()})
	os.Exit(rc)
}
