package spotify

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func saveTestTokens(t *testing.T, path string, expiresAt float64) {
	t.Helper()
	if err := SaveTokens(path, obj("access_token", "old-access", "refresh_token", secretRefresh,
		"expires_at", expiresAt, "token_type", "Bearer")); err != nil {
		t.Fatal(err)
	}
}

func newTM(path string, f *fakeSpotify, now float64) (*TokenManager, *syncBuffer) {
	lg, buf := testLogger()
	tm := &TokenManager{Path: path, ClientID: "cid", TokenURL: f.tokenURL(), HTTP: f.srv.Client(),
		Now: func() float64 { return now }, Log: lg}
	return tm, buf
}

// Python: TokenTest.test_save_tokens_is_0600
func TestSaveTokensIs0600(t *testing.T) {
	_, _, tok := tmpPaths(t)
	saveTestTokens(t, tok, 1e10)
	st, err := os.Stat(tok)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", st.Mode().Perm())
	}
	ents, _ := os.ReadDir(strings.TrimSuffix(tok, "/spotify-token.json"))
	if len(ents) != 1 || ents[0].Name() != "spotify-token.json" {
		t.Fatalf("dir has %v", ents)
	}
	dst, _ := os.Stat(strings.TrimSuffix(tok, "/spotify-token.json"))
	if dst.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o", dst.Mode().Perm())
	}
}

// The token file written by the Python version (json.dump(indent=2), float expires_at) is read as is.
func TestReadsPythonTokenFile(t *testing.T) {
	_, _, tok := tmpPaths(t)
	if err := os.MkdirAll(strings.TrimSuffix(tok, "/spotify-token.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	py := "{\n  \"access_token\": \"py-access\",\n  \"token_type\": \"Bearer\",\n  \"expires_at\": 1763730000.123456,\n" +
		"  \"scope\": \"user-read-currently-playing user-read-playback-state\",\n  \"refresh_token\": \"py-refresh\"\n}\n"
	if err := os.WriteFile(tok, []byte(py), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFakeSpotify(t)
	tm, _ := newTM(tok, f, 1763726400)
	got, err := tm.AccessToken(bg)
	if err != nil || got != "py-access" {
		t.Fatalf("AccessToken = %q, %v", got, err)
	}
	if len(f.Calls()) != 0 {
		t.Fatal("refreshed a valid token")
	}
}

// Python: TokenTest.test_valid_token_is_used_without_refresh
func TestValidTokenIsUsedWithoutRefresh(t *testing.T) {
	_, _, tok := tmpPaths(t)
	saveTestTokens(t, tok, 1000+3600)
	f := newFakeSpotify(t)
	tm, _ := newTM(tok, f, 1000)
	got, err := tm.AccessToken(bg)
	if err != nil || got != "old-access" {
		t.Fatalf("AccessToken = %q, %v", got, err)
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("calls = %v", f.Calls())
	}
}

// Python: TokenTest.test_expired_token_is_refreshed_and_rotated_refresh_token_saved
func TestExpiredTokenIsRefreshedAndRotatedRefreshTokenSaved(t *testing.T) {
	_, _, tok := tmpPaths(t)
	saveTestTokens(t, tok, 1000-1)
	f := newFakeSpotify(t, reply{status: 200, body: map[string]any{
		"access_token": secretAccess, "token_type": "Bearer", "expires_in": 3600,
		"refresh_token": "rotated-refresh"}})
	tm, _ := newTM(tok, f, 1000)
	got, err := tm.AccessToken(bg)
	if err != nil || got != secretAccess {
		t.Fatalf("AccessToken = %q, %v", got, err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	c := calls[0]
	if c.method != "POST" || c.path != "/api/token" {
		t.Fatalf("call = %s %s", c.method, c.path)
	}
	if c.form.Get("grant_type") != "refresh_token" || c.form.Get("refresh_token") != secretRefresh ||
		c.form.Get("client_id") != "cid" {
		t.Fatalf("form = %v", c.form)
	}
	if _, ok := c.form["client_secret"]; ok {
		t.Fatal("client_secret sent")
	}
	if c.header.Get("Authorization") != "" {
		t.Fatal("Authorization header sent to the token endpoint")
	}
	if ct := c.header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type = %q", ct)
	}
	saved := readJSON(t, tok)
	if saved["access_token"] != secretAccess || saved["refresh_token"] != "rotated-refresh" ||
		saved["expires_at"] != float64(1000+3600) {
		t.Fatalf("saved = %v", saved)
	}
	if st, _ := os.Stat(tok); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", st.Mode().Perm())
	}
	// same shape as the Python version: the float keeps its ".0"
	b, _ := os.ReadFile(tok)
	if !strings.Contains(string(b), `"expires_at": 4600.0,`) {
		t.Fatalf("file = %s", b)
	}
}

// Python: TokenTest.test_refresh_without_rotation_keeps_old_refresh_token
func TestRefreshWithoutRotationKeepsOldRefreshToken(t *testing.T) {
	_, _, tok := tmpPaths(t)
	saveTestTokens(t, tok, 0)
	f := newFakeSpotify(t, reply{status: 200, body: map[string]any{"access_token": secretAccess, "expires_in": 3600}})
	tm, _ := newTM(tok, f, 1000)
	if _, err := tm.AccessToken(bg); err != nil {
		t.Fatal(err)
	}
	if got := readJSON(t, tok)["refresh_token"]; got != secretRefresh {
		t.Fatalf("refresh_token = %v", got)
	}
}

// Python: TokenTest.test_missing_token_file_requires_login
func TestMissingTokenFileRequiresLogin(t *testing.T) {
	_, _, tok := tmpPaths(t)
	tm, _ := newTM(tok, newFakeSpotify(t), 0)
	_, err := tm.AccessToken(bg)
	var lr *LoginRequiredError
	if !errors.As(err, &lr) {
		t.Fatalf("err = %v", err)
	}
}

func TestBrokenTokenFileRequiresLogin(t *testing.T) {
	_, _, tok := tmpPaths(t)
	_ = os.MkdirAll(strings.TrimSuffix(tok, "/spotify-token.json"), 0o700)
	for _, body := range []string{"{not json", "[1, 2]"} {
		_ = os.WriteFile(tok, []byte(body), 0o600)
		tm, _ := newTM(tok, newFakeSpotify(t), 0)
		_, err := tm.AccessToken(bg)
		var lr *LoginRequiredError
		if !errors.As(err, &lr) {
			t.Fatalf("%s: err = %v", body, err)
		}
	}
}

// Python: TokenTest.test_invalid_grant_requires_login_and_logs_no_secret
func TestInvalidGrantRequiresLoginAndLogsNoSecret(t *testing.T) {
	_, _, tok := tmpPaths(t)
	saveTestTokens(t, tok, 0)
	f := newFakeSpotify(t, reply{status: 400, body: map[string]any{"error": "invalid_grant",
		"error_description": "Refresh token revoked"}})
	tm, buf := newTM(tok, f, 1000)
	_, err := tm.AccessToken(bg)
	var lr *LoginRequiredError
	if !errors.As(err, &lr) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(buf.String()+err.Error(), secretRefresh) {
		t.Fatal("refresh token leaked")
	}
	if !strings.Contains(buf.String(), "token refresh rejected (HTTP 400 invalid_grant)") {
		t.Fatalf("log = %s", buf.String())
	}
}

func TestRefresh429IsRateLimited(t *testing.T) {
	_, _, tok := tmpPaths(t)
	saveTestTokens(t, tok, 0)
	f := newFakeSpotify(t, reply{status: 429, headers: map[string]string{"Retry-After": "9"}})
	tm, _ := newTM(tok, f, 1000)
	_, err := tm.AccessToken(bg)
	var rl *RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter != 9 {
		t.Fatalf("err = %v", err)
	}
}
