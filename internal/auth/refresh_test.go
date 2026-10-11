package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/openai"
)

// countingSource returns token "old" until refreshed, then "new"; Refresh blocks
// until release is closed so concurrent callers pile up behind it.
type countingSource struct {
	refreshes atomic.Int32
	release   chan struct{}
	fail      error
	newExp    time.Time
}

func (s *countingSource) Token(context.Context) (Token, error) {
	return NewToken(jwt(1800000000), time.Unix(1800000000, 0), "", "test"), nil
}

func (s *countingSource) Refresh(context.Context) (Token, error) {
	s.refreshes.Add(1)
	if s.release != nil {
		<-s.release
	}
	if s.fail != nil {
		return Token{}, s.fail
	}
	return NewToken(jwt(s.newExp.Unix()), s.newExp, "", "test"), nil
}

// accept: AUTH-04#1
func TestRefreshSingleFlight(t *testing.T) {
	src := &countingSource{release: make(chan struct{}), newExp: probeNow.Add(time.Hour)}
	c := NewCredentials(src, fixedNow)
	stale, err := c.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	got := make([]Token, 8)
	errs := make([]error, 8)
	var started sync.WaitGroup
	started.Add(8)
	for i := range 8 {
		wg.Go(func() {
			started.Done()
			got[i], errs[i] = c.RefreshAfter(context.Background(), stale)
		})
	}
	started.Wait()
	close(src.release)
	wg.Wait()
	if n := src.refreshes.Load(); n != 1 {
		t.Fatalf("%d refreshes, want 1", n)
	}
	for i := range 8 {
		if errs[i] != nil || got[i].Bearer() == stale.Bearer() {
			t.Fatalf("worker %d: err %v, stale token %v", i, errs[i], got[i].Bearer() == stale.Bearer())
		}
	}
	// A second refusal of the new token: no second refresh, exit 3.
	_, err = c.RefreshAfter(context.Background(), got[0])
	if openai.ExitCode(err) != 3 || src.refreshes.Load() != 1 {
		t.Fatalf("second refresh: %v (%d refreshes)", err, src.refreshes.Load())
	}
}

func TestRefreshWhenCloseToExpiry(t *testing.T) {
	src := &countingSource{newExp: probeNow.Add(time.Hour)}
	c := NewCredentials(src, func() time.Time { return time.Unix(1800000000, 0).Add(-5 * time.Minute) })
	if _, err := c.Token(context.Background()); err == nil || src.refreshes.Load() != 1 {
		// newExp is before the shifted clock, so the refreshed token is still too short.
		t.Fatalf("err %v refreshes %d", err, src.refreshes.Load())
	}
	if _, err := c.Token(context.Background()); openai.ExitCode(err) != 3 || src.refreshes.Load() != 1 {
		t.Fatalf("refresh not limited to once: %v %d", err, src.refreshes.Load())
	}
	ok := NewCredentials(&countingSource{newExp: probeNow.Add(time.Hour)}, fixedNow)
	if _, err := ok.Token(context.Background()); err != nil {
		t.Fatalf("fresh token: %v", err)
	}
}

// accept: AUTH-04#2
func TestRefreshFailsExit3(t *testing.T) {
	s, _, rec := run(t, 0, "refresh-fails")
	c := NewCredentials(s, fixedNow)
	stale, err := c.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.RefreshAfter(context.Background(), stale)
	if openai.ExitCode(err) != 3 || !strings.Contains(err.Error(), "run `codex login`") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(record(t, rec), `"refreshToken":true`) {
		t.Fatal("refresh did not ask Codex with refreshToken: true")
	}
	// The file source cannot refresh and says to run any codex command.
	f := NewCredentials(NewFileSource(homeWith(t, 1800000000)), fixedNow)
	stale, err = f.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.RefreshAfter(context.Background(), stale)
	var e *openai.Error
	if !errors.As(err, &e) || openai.ExitCode(err) != 3 || !strings.Contains(e.Message, "run any `codex` command") {
		t.Fatalf("file refresh: %v", err)
	}
}
