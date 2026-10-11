package stt_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/stt"
)

const freshBearer = "eyJcanary.fresh.canary"

// refreshingCreds hands out the canary token and, on RefreshAfter, a fresh one (or fails).
type refreshingCreds struct {
	mu        sync.Mutex
	refreshes int
	fail      bool
}

func (c *refreshingCreds) Token(context.Context) (auth.Token, error) {
	return auth.NewToken(canaryBearer, time.Unix(1800000000, 0), "", "test"), nil
}

func (c *refreshingCreds) RefreshAfter(context.Context, auth.Token) (auth.Token, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshes++
	if c.fail {
		return auth.Token{}, &openai.Error{Class: openai.ClassAuthExpired, Message: "refresh failed"}
	}
	return auth.NewToken(freshBearer, time.Unix(1800000000, 0), "", "test"), nil
}

// expiredUnlessFresh refuses the stale bearer with 401 invalid_token.
func expiredUnlessFresh(_ int, w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+freshBearer {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"expired","type":"invalid_request_error","code":"invalid_token"}}`)
		return
	}
	_, _ = io.WriteString(w, `{"text":"Hello","languages":[]}`)
}

func TestHTTPAuthExpiredRefreshesOnceAndRetries(t *testing.T) {
	srv, h := newSTTServer(t, expiredUnlessFresh)
	creds := &refreshingCreds{}
	h.Creds = creds
	tr, err := h.Transcribe(context.Background(), take(), stt.Hint{})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if tr.Text != "Hello" || creds.refreshes != 1 || srv.count() != 2 {
		t.Fatalf("text %q, refreshes %d, calls %d; want Hello, 1, 2", tr.Text, creds.refreshes, srv.count())
	}
}

func TestHTTPAuthExpiredTwiceIsExit3(t *testing.T) {
	srv, h := newSTTServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_token"}}`)
	})
	creds := &refreshingCreds{}
	h.Creds = creds
	_, err := h.Transcribe(context.Background(), take(), stt.Hint{})
	if openai.ExitCode(err) != 3 || creds.refreshes != 1 || srv.count() != 2 {
		t.Fatalf("err %v (exit %d), refreshes %d, calls %d; want exit 3, 1, 2", err, openai.ExitCode(err), creds.refreshes, srv.count())
	}
}

func TestHTTPRefreshFailureIsReturned(t *testing.T) {
	srv, h := newSTTServer(t, expiredUnlessFresh)
	creds := &refreshingCreds{fail: true}
	h.Creds = creds
	_, err := h.Transcribe(context.Background(), take(), stt.Hint{})
	var oe *openai.Error
	if !errors.As(err, &oe) || oe.Message != "refresh failed" || srv.count() != 1 {
		t.Fatalf("err %v, calls %d; want the refresh failure after 1 call", err, srv.count())
	}
}
