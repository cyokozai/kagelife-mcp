package spotify

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// SaveTokens writes tokens atomically with mode 0600 (tmp file in the same dir, then rename).
// The shape is that of the Python version's json.dump(tokens, indent=2) + "\n".
func SaveTokens(path string, tokens *Object) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(path, append(Encode(tokens, true), '\n'), 0o600)
}

// writeFileAtomic writes data to a tmp file in the same dir, fsyncs it, sets mode and renames it.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func tokenRequest(ctx context.Context, hc *http.Client, tokenURL string, form [][2]string) (*response, error) {
	return send(ctx, hc, "POST", tokenURL,
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, []byte(urlencode(form)))
}

// tokensFromResponse merges a token endpoint answer into old (Python's _tokens_from_response).
func tokensFromResponse(v any, now float64, old *Object) (*Object, error) {
	o, ok := v.(*Object)
	if !ok {
		return nil, errors.New("token response is not a JSON object")
	}
	access, ok := o.Get("access_token")
	if !ok {
		return nil, errors.New("token response has no access_token")
	}
	t := NewObject()
	if old != nil {
		t = old.Clone()
	}
	t.Set("access_token", access)
	tokenType, ok := o.Get("token_type")
	if !ok {
		tokenType = "Bearer"
	}
	t.Set("token_type", tokenType)
	expiresIn, ok := o.Get("expires_in")
	if !ok {
		expiresIn = int64(3600)
	}
	sec, err := pyFloatOf(expiresIn)
	if err != nil {
		return nil, err
	}
	t.Set("expires_at", now+sec)
	if s := o.Val("scope"); truthy(s) {
		t.Set("scope", s)
	}
	if r := o.Val("refresh_token"); truthy(r) { // Spotify may rotate it
		t.Set("refresh_token", r)
	}
	return t, nil
}

// TokenManager hands out a valid access token, refreshing it when needed.
type TokenManager struct {
	Path     string
	ClientID string
	TokenURL string
	HTTP     *http.Client
	Now      func() float64 // seconds since the epoch
	Log      *Logger

	tokens *Object
}

func (m *TokenManager) load() (*Object, error) {
	if m.tokens != nil {
		return m.tokens, nil
	}
	b, err := os.ReadFile(m.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &LoginRequiredError{fmt.Sprintf("no token file at %s: run `login` first", m.Path)}
	}
	if err != nil {
		return nil, err
	}
	v, err := Decode(b)
	o, ok := v.(*Object)
	if err != nil || !ok {
		return nil, &LoginRequiredError{fmt.Sprintf("token file %s is broken: run `login` again", m.Path)}
	}
	m.tokens = o
	return o, nil
}

// AccessToken returns a token that is valid for at least ExpiryMarginS more seconds.
func (m *TokenManager) AccessToken(ctx context.Context) (string, error) {
	t, err := m.load()
	if err != nil {
		return "", err
	}
	exp := any(int64(0))
	if v, ok := t.Get("expires_at"); ok {
		exp = v
	}
	expiresAt, err := pyFloatOf(exp)
	if err != nil {
		return "", err
	}
	if !truthy(t.Val("access_token")) || expiresAt-ExpiryMarginS <= m.Now() {
		if err := m.Refresh(ctx); err != nil {
			return "", err
		}
		if t, err = m.load(); err != nil {
			return "", err
		}
	}
	return pyStr(t.Val("access_token")), nil
}

// Refresh exchanges the refresh token for a new access token and saves it.
func (m *TokenManager) Refresh(ctx context.Context) error {
	t, err := m.load()
	if err != nil {
		return err
	}
	rt := t.Val("refresh_token")
	if !truthy(rt) {
		return &LoginRequiredError{"no refresh token: run `login` again"}
	}
	r, err := tokenRequest(ctx, m.HTTP, m.TokenURL, [][2]string{
		{"grant_type", "refresh_token"},
		{"refresh_token", pyStr(rt)},
		{"client_id", m.ClientID},
	})
	if err != nil {
		return err
	}
	switch {
	case r.status == 429:
		return &RateLimitedError{retryAfter(r)}
	case r.status == 400 || r.status == 401:
		detail := errorDetail(r)
		m.Log.Errorf("token refresh rejected (HTTP %d %s)", r.status, detail)
		if detail == "" {
			detail = strconv.Itoa(r.status)
		}
		return &LoginRequiredError{fmt.Sprintf("token refresh rejected (%s): run `login` again", detail)}
	case r.status != 200:
		return &APIError{r.status, errorDetail(r)}
	}
	v, err := r.json()
	if err != nil {
		return err
	}
	nt, err := tokensFromResponse(v, m.Now(), t)
	if err != nil {
		return err
	}
	m.tokens = nt
	if err := SaveTokens(m.Path, nt); err != nil {
		return err
	}
	m.Log.Infof("access token refreshed")
	return nil
}
