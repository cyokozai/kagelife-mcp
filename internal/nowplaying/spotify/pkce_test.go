package spotify

import (
	"net/url"
	"strings"
	"testing"
)

// Python: PkceTest.test_rfc7636_appendix_b_vector
func TestCodeChallengeRFC7636AppendixB(t *testing.T) {
	got := CodeChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
	if want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"; got != want {
		t.Fatalf("CodeChallenge = %q, want %q", got, want)
	}
}

// Python: PkceTest.test_verifier_is_url_safe_and_long_enough
func TestCodeVerifierIsURLSafeAndLongEnough(t *testing.T) {
	v, err := NewCodeVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if len(v) < 43 || len(v) > 128 {
		t.Fatalf("len = %d", len(v))
	}
	const allowed = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	for _, r := range v {
		if !strings.ContainsRune(allowed, r) {
			t.Fatalf("verifier has %q", r)
		}
	}
	v2, _ := NewCodeVerifier()
	if v == v2 {
		t.Fatal("two verifiers are equal")
	}
}

// Python: PkceTest.test_authorize_url_has_challenge_but_not_verifier
func TestAuthorizeURLHasChallengeButNotVerifier(t *testing.T) {
	v, _ := NewCodeVerifier()
	u := AuthorizeURL(DefaultAuthorizeURL, "cid", "http://127.0.0.1:8888/callback", v, "st")
	if !strings.HasPrefix(u, "https://accounts.spotify.com/authorize?") {
		t.Fatalf("url = %s", u)
	}
	pu, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	q := pu.Query()
	want := map[string]string{
		"client_id":             "cid",
		"response_type":         "code",
		"redirect_uri":          "http://127.0.0.1:8888/callback",
		"code_challenge_method": "S256",
		"code_challenge":        CodeChallenge(v),
		"state":                 "st",
		"scope":                 "user-read-currently-playing user-read-playback-state",
	}
	for k, w := range want {
		if got := q.Get(k); got != w {
			t.Errorf("%s = %q, want %q", k, got, w)
		}
	}
	if strings.Contains(u, v) {
		t.Fatal("verifier leaked into the authorize URL")
	}
}
