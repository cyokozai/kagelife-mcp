package spotify

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type scriptedPoller struct{ errs []error }

func (s *scriptedPoller) Poll(context.Context) (*Object, error) {
	if len(s.errs) == 0 {
		return nil, nil
	}
	e := s.errs[0]
	s.errs = s.errs[1:]
	return nil, e
}

func runLoop(t *testing.T, errs []error, polls int) (int, []float64) {
	t.Helper()
	var sleeps []float64
	sleep := func(_ context.Context, d time.Duration) error {
		sleeps = append(sleeps, d.Seconds())
		return nil
	}
	lg, _ := testLogger()
	rc := RunForever(bg, &scriptedPoller{errs}, "out.json", 3*time.Second, sleep, polls, lg)
	return rc, sleeps
}

// Python: RunLoopTest.test_429_sleeps_retry_after
func Test429SleepsRetryAfter(t *testing.T) {
	rc, sleeps := runLoop(t, []error{&RateLimitedError{20}, nil}, 2)
	if rc != 0 || !reflect.DeepEqual(sleeps, []float64{20, 3}) {
		t.Fatalf("rc = %d, sleeps = %v", rc, sleeps)
	}
	// a Retry-After shorter than the interval waits the interval
	_, sleeps = runLoop(t, []error{&RateLimitedError{1}}, 1)
	if !reflect.DeepEqual(sleeps, []float64{3}) {
		t.Fatalf("sleeps = %v", sleeps)
	}
}

// Python: RunLoopTest.test_5xx_and_network_errors_back_off_and_continue
func Test5xxAndNetworkErrorsBackOffAndContinue(t *testing.T) {
	rc, sleeps := runLoop(t, []error{&APIError{Status: 502}, errors.New("down"), nil, &APIError{Status: 500}}, 4)
	if rc != 0 || !reflect.DeepEqual(sleeps, []float64{6, 12, 3, 6}) {
		t.Fatalf("rc = %d, sleeps = %v", rc, sleeps)
	}
}

// Python: RunLoopTest.test_backoff_is_capped
func TestBackoffIsCapped(t *testing.T) {
	errs := make([]error, 8)
	for i := range errs {
		errs[i] = &APIError{Status: 500}
	}
	_, sleeps := runLoop(t, errs, 8)
	maxS := 0.0
	for _, s := range sleeps {
		maxS = max(maxS, s)
	}
	if maxS != MaxBackoff.Seconds() {
		t.Fatalf("max = %v", maxS)
	}
}

// Python: RunLoopTest.test_login_required_stops
func TestLoginRequiredStops(t *testing.T) {
	rc, _ := runLoop(t, []error{&LoginRequiredError{"run login"}}, 5)
	if rc != 1 {
		t.Fatalf("rc = %d", rc)
	}
}

// SIGTERM / SIGINT cancel the context: the loop ends with 0, even in the middle of a sleep.
func TestCancelledContextEndsWithZero(t *testing.T) {
	ctx, cancel := context.WithCancel(bg)
	lg, _ := testLogger()
	done := make(chan int, 1)
	go func() {
		done <- RunForever(ctx, &scriptedPoller{}, "out.json", time.Hour, SleepContext, 0, lg)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case rc := <-done:
		if rc != 0 {
			t.Fatalf("rc = %d", rc)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop")
	}
}
