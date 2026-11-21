package spotify

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type staticTokens struct{ refreshed int }

func (s *staticTokens) AccessToken(context.Context) (string, error) {
	if s.refreshed == 0 {
		return secretAccess, nil
	}
	return "new-access", nil
}

func (s *staticTokens) Refresh(context.Context) error { s.refreshed++; return nil }

func newClient(t *testing.T, replies ...reply) (*Client, *staticTokens, *fakeSpotify) {
	f := newFakeSpotify(t, replies...)
	toks := &staticTokens{}
	lg, _ := testLogger()
	return &Client{Tokens: toks, HTTP: f.srv.Client(), APIBase: f.apiBase(), Log: lg}, toks, f
}

// Python: ClientTest.test_200_returns_json_and_sends_bearer
func Test200ReturnsJSONAndSendsBearer(t *testing.T) {
	c, _, f := newClient(t, reply{status: 200, body: map[string]any{"progress_ms": 1000}})
	v, err := c.CurrentlyPlaying(bg)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.(*Object).Val("progress_ms"); got != int64(1000) {
		t.Fatalf("progress_ms = %v", got)
	}
	cl := f.Calls()[0]
	if cl.path != "/v1/me/player/currently-playing" {
		t.Fatalf("path = %s", cl.path)
	}
	if cl.header.Get("Authorization") != "Bearer "+secretAccess || cl.header.Get("Accept") != "application/json" {
		t.Fatalf("headers = %v", cl.header)
	}
}

// Python: ClientTest.test_204_means_nothing_playing
func Test204MeansNothingPlaying(t *testing.T) {
	c, _, _ := newClient(t, reply{status: 204})
	v, err := c.CurrentlyPlaying(bg)
	if err != nil || v != nil {
		t.Fatalf("v = %v, err = %v", v, err)
	}
}

// Python: ClientTest.test_401_refreshes_once_and_retries
func Test401RefreshesOnceAndRetries(t *testing.T) {
	c, toks, f := newClient(t,
		reply{status: 401, body: map[string]any{"error": map[string]any{"status": 401, "message": "The access token expired"}}},
		reply{status: 200, body: map[string]any{"queue": []any{}}})
	v, err := c.Queue(bg)
	if err != nil {
		t.Fatal(err)
	}
	if string(Encode(v, false)) != "{\n  \"queue\": []\n}" {
		t.Fatalf("v = %s", Encode(v, false))
	}
	if toks.refreshed != 1 {
		t.Fatalf("refreshed = %d", toks.refreshed)
	}
	calls := f.Calls()
	if calls[0].path != "/v1/me/player/queue" || calls[1].header.Get("Authorization") != "Bearer new-access" {
		t.Fatalf("calls = %v", calls)
	}
}

// Python: ClientTest.test_401_twice_raises
func Test401TwiceRaises(t *testing.T) {
	c, toks, _ := newClient(t, reply{status: 401}, reply{status: 401})
	_, err := c.CurrentlyPlaying(bg)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 401 {
		t.Fatalf("err = %v", err)
	}
	if toks.refreshed != 1 {
		t.Fatalf("refreshed = %d", toks.refreshed)
	}
}

// Python: ClientTest.test_429_raises_with_retry_after
func Test429RaisesWithRetryAfter(t *testing.T) {
	c, _, _ := newClient(t, reply{status: 429, headers: map[string]string{"Retry-After": "7"}})
	_, err := c.CurrentlyPlaying(bg)
	var rl *RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter != 7 {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryAfterDefaultsAndFloor(t *testing.T) {
	for in, want := range map[string]float64{"": 5, "abc": 5, "0": 1, "0.2": 1, " 12 ": 12} {
		h := map[string]string{}
		if in != "" {
			h["Retry-After"] = in
		}
		c, _, _ := newClient(t, reply{status: 429, headers: h})
		_, err := c.CurrentlyPlaying(bg)
		var rl *RateLimitedError
		if !errors.As(err, &rl) || rl.RetryAfter != want {
			t.Fatalf("%q: err = %v", in, err)
		}
	}
}

// Python: ClientTest.test_5xx_raises_api_error_without_token
func Test5xxRaisesAPIErrorWithoutToken(t *testing.T) {
	c, _, _ := newClient(t, reply{status: 503})
	_, err := c.CurrentlyPlaying(bg)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 503 {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), secretAccess) {
		t.Fatal("token in error")
	}
}

func TestErrorDetailFromSpotifyBody(t *testing.T) {
	c, _, _ := newClient(t, reply{status: 403, body: map[string]any{"error": map[string]any{"status": 403, "message": "Forbidden"}}})
	_, err := c.CurrentlyPlaying(bg)
	if err == nil || err.Error() != "HTTP 403: Forbidden" {
		t.Fatalf("err = %v", err)
	}
}

// Python: UrllibTransportTest.test_broken_http_response_becomes_oserror.
// In Go a broken connection is an ordinary error, which the run loop treats like a 5xx.
func TestBrokenHTTPResponseIsAnError(t *testing.T) {
	c, _, f := newClient(t)
	f.srv.Close()
	if _, err := c.CurrentlyPlaying(bg); err == nil {
		t.Fatal("no error from a closed server")
	}
}

func TestInvalidJSONBodyIsAnError(t *testing.T) {
	c, _, _ := newClient(t, reply{status: 200, raw: "{not json"})
	if _, err := c.CurrentlyPlaying(bg); err == nil {
		t.Fatal("no error")
	}
}
