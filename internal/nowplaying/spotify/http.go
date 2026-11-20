package spotify

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --- errors ------------------------------------------------------------------

// LoginRequiredError means there is no usable token: the user has to run `login` again.
type LoginRequiredError struct{ Msg string }

func (e *LoginRequiredError) Error() string { return e.Msg }

// APIError is an HTTP status Spotify answered with that is not handled otherwise.
type APIError struct {
	Status int
	Detail string
}

func (e *APIError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Detail)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// RateLimitedError is a 429: wait RetryAfter seconds.
type RateLimitedError struct{ RetryAfter float64 }

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("rate limited, retry after %.0f s", e.RetryAfter)
}

// --- HTTP ----------------------------------------------------------------------

// RequestTimeout is the timeout of one HTTP request (the Python version's urlopen timeout).
const RequestTimeout = 10 * time.Second

type response struct {
	status int
	header http.Header
	body   []byte
}

// json decodes the body; an empty body is nil (Python's Response.json()).
func (r *response) json() (any, error) {
	if len(r.body) == 0 {
		return nil, nil
	}
	return Decode(r.body)
}

// send does one request. HTTP errors come back as a response; network errors as an error.
func send(ctx context.Context, hc *http.Client, method, url string, headers map[string]string, body []byte) (*response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if hc == nil {
		hc = &http.Client{Timeout: RequestTimeout}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &response{resp.StatusCode, resp.Header, data}, nil
}

// errorDetail is a short, token-free description of an error body (Spotify's error code/message only).
func errorDetail(r *response) string {
	v, err := r.json()
	if err != nil {
		return ""
	}
	o, ok := v.(*Object)
	if !ok {
		return ""
	}
	switch e := o.Val("error").(type) {
	case *Object:
		m, ok := e.Get("message")
		if !ok {
			return ""
		}
		return truncRunes(pyStr(m), 200)
	case string:
		return truncRunes(e, 100)
	}
	return ""
}

func retryAfter(r *response) float64 {
	s := r.header.Get("Retry-After")
	if _, ok := r.header["Retry-After"]; !ok {
		s = "5"
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 5
	}
	return math.Max(1, f)
}

// --- logging -------------------------------------------------------------------

// Logger writes lines like Python's logging format "%(asctime)s %(levelname)s %(message)s".
type Logger struct {
	mu      sync.Mutex
	w       io.Writer
	verbose bool
	now     func() time.Time
}

// NewLogger logs to w; debug lines only when verbose.
func NewLogger(w io.Writer, verbose bool) *Logger {
	return &Logger{w: w, verbose: verbose, now: time.Now}
}

func (l *Logger) logf(level, format string, args ...any) {
	if l == nil || l.w == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	fmt.Fprintf(l.w, "%s,%03d %s %s\n", t.Format("2006-01-02 15:04:05"), t.Nanosecond()/1e6, level,
		fmt.Sprintf(format, args...))
}

func (l *Logger) Debugf(format string, args ...any) {
	if l != nil && l.verbose {
		l.logf("DEBUG", format, args...)
	}
}
func (l *Logger) Infof(format string, args ...any)  { l.logf("INFO", format, args...) }
func (l *Logger) Warnf(format string, args ...any)  { l.logf("WARNING", format, args...) }
func (l *Logger) Errorf(format string, args ...any) { l.logf("ERROR", format, args...) }
