package stt_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/stt"
)

// A client that did not come from openai.NewClient is refused before any bearer is sent: it
// would skip the origin policy, the redirect refusal and the test guard.
func TestHTTPRefusesAnUnguardedClient(t *testing.T) {
	srv, h := newSTTServer(t, reply(`{"text":"x"}`))
	h.Client = &http.Client{}
	if _, err := h.Transcribe(context.Background(), take(), stt.Hint{}); openai.Classify(err) != openai.ClassInternal {
		t.Fatalf("err = %v, want internal", err)
	}
	if srv.count() != 0 {
		t.Fatalf("%d requests reached the server", srv.count())
	}
}

// The one drop retry is shared across the refresh: 401, refresh, a dropped connection, then
// the retry succeeds.
func TestHTTPDropAfterRefreshIsRetried(t *testing.T) {
	srv, h := newSTTServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		switch n {
		case 1:
			expiredUnlessFresh(n, w, r)
		case 2:
			if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
				_ = conn.Close()
			}
		default:
			expiredUnlessFresh(n, w, r)
		}
	})
	creds := &refreshingCreds{}
	h.Creds = creds
	got, err := h.Transcribe(context.Background(), take(), stt.Hint{})
	if err != nil || got.Text != "Hello" || creds.refreshes != 1 || srv.count() != 3 {
		t.Fatalf("got %q, %v after %d requests and %d refreshes; want Hello after 3 and 1", got.Text, err, srv.count(), creds.refreshes)
	}
}

// A reply over 16 MiB is refused, even when what fits under the limit is valid JSON.
func TestHTTPOversizeReplyIsProtocol(t *testing.T) {
	_, h := newSTTServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"text":"Hello"}`+strings.Repeat(" ", 16<<20)+"garbage")
	})
	if _, err := h.Transcribe(context.Background(), take(), stt.Hint{}); openai.Classify(err) != openai.ClassProtocol {
		t.Fatalf("err = %v, want protocol", err)
	}
}

// Billed seconds must be in range: a negative or absurd usage.seconds is a protocol fault.
func TestDecodeTranscriptionBoundsSeconds(t *testing.T) {
	for _, raw := range []string{`{"text":"a","usage":{"seconds":-1}}`, `{"text":"a","usage":{"seconds":1e300}}`} {
		if _, err := stt.DecodeTranscription([]byte(raw)); openai.Classify(err) != openai.ClassProtocol {
			t.Errorf("%s: err = %v, want protocol", raw, err)
		}
	}
	if tr, err := stt.DecodeTranscription([]byte(`{"text":"a","usage":{"seconds":2.1}}`)); err != nil || tr.Seconds != 3 {
		t.Errorf("2.1 s: %+v, %v", tr, err)
	}
}
