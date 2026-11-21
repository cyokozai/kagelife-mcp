package spotify

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type loginRun struct {
	url  string
	port int
	rc   chan int
	logs *syncBuffer
	out  *syncBuffer
}

func startLogin(t *testing.T, f *fakeSpotify, tok string) *loginRun {
	t.Helper()
	lg, logs := testLogger()
	out := &syncBuffer{}
	lr := &loginRun{rc: make(chan int, 1), logs: logs, out: out}
	ready := make(chan struct{})
	go func() {
		lr.rc <- Login(bg, LoginOptions{
			ClientID: "cid", TokenPath: tok, ListenHost: "127.0.0.1", ListenPort: 0,
			RedirectURI: DefaultRedirectURI, Timeout: 10 * time.Second,
			AuthorizeURL: DefaultAuthorizeURL, TokenURL: f.tokenURL(), HTTP: f.srv.Client(),
			OnURL:  func(u string, port int) { lr.url, lr.port = u, port; close(ready) },
			Now:    func() float64 { return 1000 },
			Log:    lg,
			Stdout: out,
		})
	}()
	select {
	case <-ready:
	case rc := <-lr.rc:
		t.Fatalf("login ended early with %d: %s", rc, logs.String())
	case <-time.After(10 * time.Second):
		t.Fatal("login did not start")
	}
	return lr
}

func (lr *loginRun) state(t *testing.T) string {
	u, err := url.Parse(lr.url)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

func (lr *loginRun) hit(t *testing.T, q url.Values) int {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?%s", lr.port, q.Encode()))
	if err != nil {
		t.Fatal(err, lr.logs.String())
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func (lr *loginRun) wait(t *testing.T) int {
	t.Helper()
	select {
	case rc := <-lr.rc:
		return rc
	case <-time.After(10 * time.Second):
		t.Fatal("login did not end")
	}
	return -1
}

// Python: LoginTest.test_login_validates_state_and_saves_tokens
func TestLoginValidatesStateAndSavesTokens(t *testing.T) {
	_, _, tok := tmpPaths(t)
	f := newFakeSpotify(t, reply{status: 200, body: map[string]any{
		"access_token": secretAccess, "refresh_token": secretRefresh, "expires_in": 3600,
		"token_type": "Bearer", "scope": "user-read-currently-playing user-read-playback-state"}})
	lr := startLogin(t, f, tok)
	state := lr.state(t)
	if c := lr.hit(t, url.Values{"code": {"c"}, "state": {"wrong"}}); c != 400 {
		t.Fatalf("wrong state: %d", c)
	}
	if c := lr.hit(t, url.Values{"code": {"c"}, "state": {"äあ"}}); c != 400 {
		t.Fatalf("non-ASCII state: %d", c)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/other?state=%s", lr.port, state))
	if err != nil || resp.StatusCode != 404 {
		t.Fatalf("other path: %v %v", resp, err)
	}
	_ = resp.Body.Close()
	if c := lr.hit(t, url.Values{"code": {"the-code"}, "state": {state}}); c != 200 {
		t.Fatalf("good callback: %d", c)
	}
	if rc := lr.wait(t); rc != 0 {
		t.Fatalf("rc = %d: %s", rc, lr.logs.String())
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	form := calls[0].form
	verifier := form.Get("code_verifier")
	said := lr.logs.String() + lr.out.String()
	for _, s := range []string{"the-code", verifier, secretAccess, secretRefresh} {
		if strings.Contains(said, s) {
			t.Fatalf("secret %q in output", s)
		}
	}
	if strings.Contains(lr.url, verifier) {
		t.Fatal("verifier in URL")
	}
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "the-code" ||
		form.Get("redirect_uri") != DefaultRedirectURI || form.Get("client_id") != "cid" {
		t.Fatalf("form = %v", form)
	}
	u, _ := url.Parse(lr.url)
	if CodeChallenge(verifier) != u.Query().Get("code_challenge") {
		t.Fatal("challenge does not match the verifier")
	}
	saved := readJSON(t, tok)
	if saved["refresh_token"] != secretRefresh || saved["expires_at"] != float64(1000+3600) {
		t.Fatalf("saved = %v", saved)
	}
	if st, _ := os.Stat(tok); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", st.Mode().Perm())
	}
	// the file has the Python version's key order
	b, _ := os.ReadFile(tok)
	if !strings.HasPrefix(string(b), "{\n  \"access_token\": ") ||
		strings.Index(string(b), "token_type") > strings.Index(string(b), "expires_at") {
		t.Fatalf("file = %s", b)
	}
}

// Python: LoginTest.test_login_denied
func TestLoginDenied(t *testing.T) {
	_, _, tok := tmpPaths(t)
	lr := startLogin(t, newFakeSpotify(t), tok)
	if c := lr.hit(t, url.Values{"error": {"access_denied"}, "state": {lr.state(t)}}); c != 400 {
		t.Fatalf("code = %d", c)
	}
	if rc := lr.wait(t); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	if _, err := os.Stat(tok); err == nil {
		t.Fatal("token saved")
	}
}

// Python: LoginTest.test_exchange_without_access_token_fails_cleanly
func TestExchangeWithoutAccessTokenFailsCleanly(t *testing.T) {
	_, _, tok := tmpPaths(t)
	lr := startLogin(t, newFakeSpotify(t, reply{status: 200, body: map[string]any{"token_type": "Bearer"}}), tok)
	if c := lr.hit(t, url.Values{"code": {"the-code"}, "state": {lr.state(t)}}); c != 200 {
		t.Fatalf("code = %d", c)
	}
	if rc := lr.wait(t); rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
	if _, err := os.Stat(tok); err == nil {
		t.Fatal("token saved")
	}
}

func TestLoginTimesOut(t *testing.T) {
	stdout := &syncBuffer{}
	_, _, tok := tmpPaths(t)
	lg, logs := testLogger()
	rc := Login(bg, LoginOptions{ClientID: "cid", TokenPath: tok, ListenHost: "127.0.0.1",
		RedirectURI: DefaultRedirectURI, Timeout: 50 * time.Millisecond, AuthorizeURL: DefaultAuthorizeURL,
		TokenURL: "http://127.0.0.1:1/unused", Now: func() float64 { return 0 }, Log: lg, Stdout: stdout})
	if rc != 1 || !strings.Contains(logs.String(), "login timed out") ||
		!strings.Contains(stdout.String(), "Open this URL in a browser and allow access:\n\nhttps://accounts.spotify.com/authorize?client_id=cid&") {
		t.Fatalf("rc = %d, logs = %s", rc, logs.String())
	}
}
