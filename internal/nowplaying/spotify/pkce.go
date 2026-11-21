package spotify

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
)

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// NewCodeVerifier returns 64 random bytes as 86 characters of [A-Za-z0-9-_] (RFC 7636: 43..128).
func NewCodeVerifier() (string, error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b64url(b), nil
}

// CodeChallenge is the S256 challenge of a verifier.
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return b64url(sum[:])
}

// urlencode is Python's urllib.parse.urlencode for a list of pairs (keeps the order).
func urlencode(pairs [][2]string) string {
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, url.QueryEscape(p[0])+"="+url.QueryEscape(p[1]))
	}
	return strings.Join(parts, "&")
}

// AuthorizeURL builds the authorize URL. The verifier never goes into it, only its challenge.
func AuthorizeURL(base, clientID, redirectURI, verifier, state string) string {
	return base + "?" + urlencode([][2]string{
		{"client_id", clientID},
		{"response_type", "code"},
		{"redirect_uri", redirectURI},
		{"code_challenge_method", "S256"},
		{"code_challenge", CodeChallenge(verifier)},
		{"state", state},
		{"scope", Scopes},
	})
}
