package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// waits records every backoff instead of sleeping.
type waits struct {
	mu sync.Mutex
	d  []time.Duration
}

func (w *waits) sleep(ctx context.Context, d time.Duration) error {
	w.mu.Lock()
	w.d = append(w.d, d)
	w.mu.Unlock()
	return ctx.Err()
}

func (w *waits) total() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	var t time.Duration
	for _, d := range w.d {
		t += d
	}
	return t
}

// accept: NET-06#2
func TestRetry429TwiceThen200(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"slow down"}}`)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	for _, seed := range []uint64{1, 2, 3, 42, 1 << 40} {
		mu.Lock()
		calls = 0
		mu.Unlock()
		w := &waits{}
		r := NewRetrier(seed, w.sleep)
		err := r.Do(context.Background(), func(ctx context.Context, _ int) error {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
			resp, err := srv.Client().Do(req)
			if err != nil {
				return err
			}
			if err := CheckResponse(resp, ""); err != nil {
				return err
			}
			return resp.Body.Close()
		})
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if calls != 3 || len(w.d) != 2 {
			t.Fatalf("seed %d: %d calls, %d waits; want 3, 2", seed, calls, len(w.d))
		}
		// Full jitter: wait n is in [0, min(16 s, 1 s·2^(n-1))].
		if w.d[0] < 0 || w.d[0] > time.Second || w.d[1] < 0 || w.d[1] > 2*time.Second {
			t.Fatalf("seed %d: waits %v outside [0,1s], [0,2s]", seed, w.d)
		}
		if tot := w.total(); tot > 3*time.Second {
			t.Fatalf("seed %d: total wait %v > 3s", seed, tot)
		}
		// Seeded: the same seed draws the same waits.
		again := NewRetrier(seed, nil)
		if d0, d1 := again.Delay(1), again.Delay(2); d0 != w.d[0] || d1 != w.d[1] {
			t.Fatalf("seed %d: redraw %v %v, want %v", seed, d0, d1, w.d)
		}
	}
}

func TestRetryAttemptLimitsPerClass(t *testing.T) {
	for class, want := range map[Class]int{
		ClassRateLimited: 5, ClassServer: 3, ClassNetwork: 3, ClassProtocol: 3,
		ClassRouteDenied: 1, ClassAuthExpired: 1, ClassInvalidRequest: 1,
	} {
		n := 0
		r := NewRetrier(7, (&waits{}).sleep)
		err := r.Do(context.Background(), func(context.Context, int) error {
			n++
			return &Error{Class: class}
		})
		if n != want || Classify(err) != class {
			t.Errorf("%s: %d attempts (err %v), want %d", class, n, err, want)
		}
	}
}

func TestBackoffCeilingCaps(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 16 * time.Second}
	for i, w := range want {
		if got := BackoffCeiling(i + 1); got != w {
			t.Errorf("ceiling(%d) = %v, want %v", i+1, got, w)
		}
	}
	r := NewRetrier(9, nil)
	for n := 1; n <= 8; n++ {
		for range 200 {
			if d := r.Delay(n); d < 0 || d > BackoffCeiling(n) {
				t.Fatalf("delay(%d) = %v", n, d)
			}
		}
	}
}

func TestRetryStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	r := NewRetrier(1, func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() })
	err := r.Do(ctx, func(context.Context, int) error { n++; return &Error{Class: ClassServer} })
	if n != 1 || Classify(err) != ClassServer {
		t.Fatalf("%d attempts, err %v", n, err)
	}
}

func TestGatePausesUntilReset(t *testing.T) {
	now := time.Unix(1000, 0)
	var slept []time.Duration
	g := NewGate(func() time.Time { return now }, func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		now = now.Add(d)
		return nil
	})
	if err := g.Wait(context.Background()); err != nil || len(slept) != 0 {
		t.Fatalf("open gate waited %v (%v)", slept, err)
	}
	g.PauseFor(3 * time.Second)
	g.PauseFor(time.Second) // an earlier reset does not shorten the pause
	if err := g.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 1 || slept[0] != 3*time.Second {
		t.Fatalf("slept %v, want [3s]", slept)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g.PauseFor(time.Second)
	g2 := NewGate(func() time.Time { return now }, nil)
	g2.PauseFor(time.Hour)
	if err := g2.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait: %v", err)
	}
}
