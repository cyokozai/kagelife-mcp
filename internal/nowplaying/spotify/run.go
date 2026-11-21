package spotify

import (
	"context"
	"errors"
	"time"
)

// Poller is one poll of the watcher (Watcher in production).
type Poller interface {
	Poll(ctx context.Context) (*Object, error)
}

// SleepContext waits d or until ctx is done (then it returns ctx's error).
func SleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func seconds(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

// RunForever polls until ctx is done (exit code 0). It returns 1 when a new login is needed.
// maxPolls 0 means no limit (tests set it).
func RunForever(ctx context.Context, p Poller, outPath string, interval time.Duration,
	sleep func(context.Context, time.Duration) error, maxPolls int, log *Logger) int {
	log.Infof("watching Spotify every %.1f s -> %s", interval.Seconds(), outPath)
	backoff := interval
	for polls := 0; maxPolls == 0 || polls < maxPolls; polls++ {
		if ctx.Err() != nil {
			return 0
		}
		payload, err := p.Poll(ctx)
		var wait time.Duration
		var rl *RateLimitedError
		var lr *LoginRequiredError
		switch {
		case err == nil:
			if payload != nil {
				state := "stopped"
				if truthy(payload.Val("is_playing")) {
					state = "playing"
				}
				log.Infof("%s: %s / %s (history_id %s)", state, pyStr(payload.Val("artist")),
					pyStr(payload.Val("title")), pyStr(payload.Val("history_id")))
			}
			backoff = interval
			wait = interval
		case ctx.Err() != nil:
			return 0
		case errors.As(err, &lr):
			log.Errorf("%s", lr)
			return 1
		case errors.As(err, &rl):
			log.Warnf("rate limited: waiting %.0f s", rl.RetryAfter)
			wait = max(seconds(rl.RetryAfter), interval)
		default:
			backoff = min(backoff*2, MaxBackoff)
			log.Warnf("read failed (%v): retrying in %.0f s", err, backoff.Seconds())
			wait = backoff
		}
		if sleep(ctx, wait) != nil {
			return 0
		}
	}
	return 0
}
