package spotify

import (
	"context"
	"net/http"
)

// TokenSource hands out access tokens (TokenManager in production).
type TokenSource interface {
	AccessToken(ctx context.Context) (string, error)
	Refresh(ctx context.Context) error
}

// Client reads the player endpoints of the Spotify Web API.
type Client struct {
	Tokens  TokenSource
	HTTP    *http.Client
	APIBase string
	Log     *Logger
}

func (c *Client) get(ctx context.Context, path string) (any, error) {
	var r *response
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := c.Tokens.AccessToken(ctx)
		if err != nil {
			return nil, err
		}
		r, err = send(ctx, c.HTTP, "GET", c.APIBase+path,
			map[string]string{"Authorization": "Bearer " + tok, "Accept": "application/json"}, nil)
		if err != nil {
			return nil, err
		}
		if r.status == 401 && attempt == 0 {
			c.Log.Infof("HTTP 401 on %s: refreshing the token once", path)
			if err := c.Tokens.Refresh(ctx); err != nil {
				return nil, err
			}
			continue
		}
		break
	}
	switch {
	case r.status == 204:
		return nil, nil
	case r.status == 429:
		return nil, &RateLimitedError{retryAfter(r)}
	case r.status != 200:
		return nil, &APIError{r.status, errorDetail(r)}
	}
	return r.json()
}

// CurrentlyPlaying is GET /me/player/currently-playing (nil on 204).
func (c *Client) CurrentlyPlaying(ctx context.Context) (any, error) {
	return c.get(ctx, "/me/player/currently-playing")
}

// Queue is GET /me/player/queue.
func (c *Client) Queue(ctx context.Context) (any, error) {
	return c.get(ctx, "/me/player/queue")
}
