package spotify

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// LoginOptions configures Login. The zero values of the URLs are not filled in: the CLI does that.
type LoginOptions struct {
	ClientID     string
	TokenPath    string
	ListenHost   string
	ListenPort   int
	RedirectURI  string
	Timeout      time.Duration
	AuthorizeURL string
	TokenURL     string
	HTTP         *http.Client
	OnURL        func(url string, port int) // tests; when nil the URL is printed to Stdout
	Now          func() float64
	Log          *Logger
	Stdout       io.Writer
}

type loginResult struct {
	code   string
	errMsg string
}

// firstNonBlank is (parse_qs(q).get(key) or [""])[0]: parse_qs drops blank values.
func firstNonBlank(q url.Values, key string) (string, bool) {
	for _, v := range q[key] {
		if v != "" {
			return v, true
		}
	}
	return "", false
}

// Login runs the PKCE login: print the URL, wait for one callback, exchange the code, save tokens.
func Login(ctx context.Context, o LoginOptions) int {
	verifier, err := NewCodeVerifier()
	if err != nil {
		o.Log.Errorf("could not make a code verifier: %v", err)
		return 1
	}
	sb := make([]byte, 24)
	if _, err := rand.Read(sb); err != nil {
		o.Log.Errorf("could not make a state: %v", err)
		return 1
	}
	state := b64url(sb)
	callbackPath := "/"
	if u, err := url.Parse(o.RedirectURI); err == nil && u.Path != "" {
		callbackPath = u.Path
	}

	results := make(chan loginResult, 1)
	var once sync.Once
	deliver := func(r loginResult) { once.Do(func() { results <- r }) }

	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// the query holds the code: never log requests
		reply := func(status int, text string) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Length", strconv.Itoa(len(text)))
			w.WriteHeader(status)
			_, _ = io.WriteString(w, text)
		}
		if r.Method != http.MethodGet {
			reply(http.StatusNotImplemented, "unsupported method")
			return
		}
		if r.URL.Path != callbackPath {
			reply(404, "not found")
			return
		}
		q, _ := url.ParseQuery(r.URL.RawQuery)
		got, _ := firstNonBlank(q, "state")
		if subtle.ConstantTimeCompare([]byte(got), []byte(state)) != 1 {
			reply(400, "state mismatch: ignored")
			return
		}
		if e, ok := firstNonBlank(q, "error"); ok {
			e = truncRunes(e, 100)
			reply(400, "login failed: "+e)
			deliver(loginResult{errMsg: e})
			return
		}
		code, ok := firstNonBlank(q, "code")
		if !ok {
			reply(400, "no code")
			return
		}
		reply(200, "Login OK. You can close this tab.")
		deliver(loginResult{code: code})
	})

	ln, err := net.Listen("tcp", net.JoinHostPort(o.ListenHost, strconv.Itoa(o.ListenPort)))
	if err != nil {
		o.Log.Errorf("cannot listen on %s:%d: %v", o.ListenHost, o.ListenPort, err)
		return 1
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	// Shutdown, not Close: the browser gets the whole reply before the server goes away.
	shutdown := func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}
	defer shutdown()

	authURL := AuthorizeURL(o.AuthorizeURL, o.ClientID, o.RedirectURI, verifier, state)
	port := ln.Addr().(*net.TCPAddr).Port
	if o.OnURL != nil {
		o.OnURL(authURL, port)
	} else {
		_, _ = fmt.Fprintf(o.Stdout, "Open this URL in a browser and allow access:\n\n%s\n\n", authURL)
		_, _ = fmt.Fprintf(o.Stdout, "Waiting for %s (listening on %s:%d) ...\n", o.RedirectURI, o.ListenHost, port)
	}

	timer := time.NewTimer(o.Timeout)
	defer timer.Stop()
	var res loginResult
	select {
	case res = <-results:
	case <-timer.C:
		o.Log.Errorf("login timed out after %.0f s", o.Timeout.Seconds())
		return 1
	case <-ctx.Done():
		o.Log.Errorf("login cancelled")
		return 1
	}
	shutdown()

	if res.errMsg != "" {
		o.Log.Errorf("login was not allowed: %s", res.errMsg)
		return 1
	}
	r, err := tokenRequest(ctx, o.HTTP, o.TokenURL, [][2]string{
		{"grant_type", "authorization_code"},
		{"code", res.code},
		{"redirect_uri", o.RedirectURI},
		{"client_id", o.ClientID},
		{"code_verifier", verifier},
	})
	if err != nil {
		o.Log.Errorf("code exchange failed (%v)", err)
		return 1
	}
	if r.status != 200 {
		o.Log.Errorf("code exchange failed (HTTP %d %s)", r.status, errorDetail(r))
		return 1
	}
	v, err := r.json()
	var tokens *Object
	if err == nil {
		tokens, err = tokensFromResponse(v, o.Now(), nil)
	}
	if err != nil {
		o.Log.Errorf("code exchange returned no access token")
		return 1
	}
	if !truthy(tokens.Val("refresh_token")) {
		o.Log.Warnf("no refresh token was returned: `login` will be needed again in about an hour")
	}
	if err := SaveTokens(o.TokenPath, tokens); err != nil {
		o.Log.Errorf("could not save the tokens: %v", err)
		return 1
	}
	o.Log.Infof("logged in; tokens saved to %s", o.TokenPath)
	return 0
}
