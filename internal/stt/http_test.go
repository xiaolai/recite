package stt_test

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/stt"
)

var update = flag.Bool("update", false, "rewrite golden files")

const boundary = "recite-golden-boundary"

// sttServer answers POST /v1/audio/transcriptions with handle and records each request.
type sttServer struct {
	mu     sync.Mutex
	bodies [][]byte
	heads  []http.Header
	calls  int
}

func newSTTServer(t *testing.T, handle func(n int, w http.ResponseWriter, r *http.Request)) (*sttServer, *stt.HTTP) {
	t.Helper()
	s := &sttServer{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/transcriptions" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.calls++
		n := s.calls
		s.bodies = append(s.bodies, body)
		s.heads = append(s.heads, r.Header.Clone())
		s.mu.Unlock()
		handle(n, w, r)
	}))
	t.Cleanup(hs.Close)
	c, base := client(t, hs.URL)
	return s, &stt.HTTP{Client: c, BaseURL: base, Creds: staticCreds{}, Boundary: boundary}
}

func (s *sttServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func reply(body string) func(int, http.ResponseWriter, *http.Request) {
	return func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

// take is 10 ms of a ramp: distinguishable from the padding.
func take() []byte {
	pcm := make([]byte, 480)
	for i := range pcm {
		pcm[i] = byte(i%7 + 1)
	}
	return pcm
}

// accept: VER-01#1
func TestHTTPGoldenMultipartBody(t *testing.T) {
	srv, h := newSTTServer(t, reply(`{"text":"Hello","languages":[{"code":"en"}],"usage":{"type":"duration","seconds":1}}`))
	hint := stt.Hint{Languages: []string{"en"}, Keywords: []string{"Okafor", "42"}}
	if _, err := h.Transcribe(context.Background(), take(), hint); err != nil {
		t.Fatal(err)
	}
	head := srv.heads[0]
	if head.Get("Authorization") != "Bearer "+canaryBearer || head.Get("Content-Type") != "multipart/form-data; boundary="+boundary {
		t.Fatalf("headers: auth %v, type %q", head.Get("Authorization") != "", head.Get("Content-Type"))
	}
	golden := filepath.Join("testdata", "transcription-request.golden")
	if *update {
		if err := os.WriteFile(golden, srv.bodies[0], 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(srv.bodies[0], want) {
		t.Fatalf("request body differs from %s (run with -update after checking the change)", golden)
	}
	// The golden pins the field order and values; check the essentials readably too.
	body := string(srv.bodies[0])
	for _, field := range []string{
		"name=\"model\"\r\n\r\ngpt-transcribe\r\n",
		"name=\"languages[]\"\r\n\r\nen\r\n",
		"name=\"prompt\"\r\n\r\nOkafor, 42\r\n",
		"name=\"include[]\"\r\n\r\nlogprobs\r\n",
		"filename=\"take.wav\"\r\nContent-Type: audio/wav\r\n\r\nRIFF",
	} {
		if !strings.Contains(body, field) {
			t.Errorf("body lacks %q", field)
		}
	}
	// 300 ms of silence each side: 14,400 bytes, then the take, then 14,400 bytes.
	pad := make([]byte, 14400)
	if !bytes.Contains(srv.bodies[0], append(append(append([]byte{}, pad...), take()...), pad...)) {
		t.Error("take not padded with 300 ms of silence on each side")
	}
}

func TestHTTPOmitsAbsentHints(t *testing.T) {
	srv, h := newSTTServer(t, reply(`{"text":"Hello","languages":[]}`))
	if _, err := h.Transcribe(context.Background(), take(), stt.Hint{}); err != nil {
		t.Fatal(err)
	}
	body := string(srv.bodies[0])
	if strings.Contains(body, `name="languages[]"`) || strings.Contains(body, `name="prompt"`) || !strings.Contains(body, `name="include[]"`) {
		t.Fatal("languages[] or prompt sent without a hint, or include[] missing")
	}
}

// accept: VER-01#2
func TestDecodeTranscription(t *testing.T) {
	got, err := stt.DecodeTranscription([]byte(`{"text":"Hello","languages":[{"code":"en"}],"logprobs":[{"token":"Hello","logprob":-0.01,"bytes":[72,101]}],"usage":{"type":"duration","seconds":2.2}}`))
	if err != nil || got.Text != "Hello" || len(got.Languages) != 1 || got.Languages[0].Code != "en" ||
		len(got.Tokens) != 1 || got.Tokens[0].Token != "Hello" || got.Tokens[0].Logprob != -0.01 || len(got.Tokens[0].Bytes) != 2 || got.Seconds != 3 {
		t.Fatalf("populated: %+v %v", got, err)
	}
	got, err = stt.DecodeTranscription([]byte(`{"text":"Hello","languages":[]}`))
	if err != nil || got.Text != "Hello" || got.Languages == nil || len(got.Languages) != 0 {
		t.Fatalf("empty: %+v %v", got, err)
	}
	got, err = stt.DecodeTranscription([]byte(`{"text":"Hello"}`))
	if err != nil || got.Languages == nil {
		t.Fatalf("absent: %+v %v", got, err)
	}
	for _, bad := range []string{`{"languages":[]}`, `not json`} {
		if _, err := stt.DecodeTranscription([]byte(bad)); openai.Classify(err) != openai.ClassProtocol {
			t.Errorf("%s: err = %v, want protocol", bad, err)
		}
	}
}

func errorReply(status int, code string) func(int, http.ResponseWriter, *http.Request) {
	return func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"message":"denied","type":"invalid_request_error","code":"`+code+`","param":null}}`)
	}
}

// accept: VER-01#3
func TestHTTPMissingScopeIsRouteDeniedForHTTPSTTOnly(t *testing.T) {
	srv, h := newSTTServer(t, errorReply(http.StatusUnauthorized, "missing_scope"))
	_, err := h.Transcribe(context.Background(), take(), stt.Hint{})
	var oe *openai.Error
	if !errors.As(err, &oe) || oe.Class != openai.ClassRouteDenied || oe.Route != openai.RouteHTTPSTT {
		t.Fatalf("err = %v, want route-denied on http-stt", err)
	}
	if srv.count() != 1 {
		t.Fatalf("%d requests, want 1 (no retry)", srv.count())
	}
	// Any other 401 is the sign-in, not this route.
	_, h2 := newSTTServer(t, errorReply(http.StatusUnauthorized, "invalid_token"))
	if _, err := h2.Transcribe(context.Background(), take(), stt.Hint{}); openai.Classify(err) != openai.ClassAuthExpired {
		t.Fatalf("invalid_token: %v", err)
	}
}

// dropFirst hijacks and closes the first n connections before answering.
func dropFirst(n int, body string) func(int, http.ResponseWriter, *http.Request) {
	return func(call int, w http.ResponseWriter, r *http.Request) {
		if call <= n {
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		reply(body)(call, w, r)
	}
}

func TestHTTPRetriesOneDroppedConnection(t *testing.T) {
	srv, h := newSTTServer(t, dropFirst(1, `{"text":"Hello","languages":[{"code":"en"}]}`))
	got, err := h.Transcribe(context.Background(), take(), stt.Hint{})
	if err != nil || got.Text != "Hello" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if srv.count() != 2 {
		t.Fatalf("%d requests, want 2", srv.count())
	}
	if !bytes.Equal(srv.bodies[0], srv.bodies[1]) {
		t.Fatal("the retry sent a different body")
	}
}

func TestHTTPRetriesOnlyOnce(t *testing.T) {
	srv, h := newSTTServer(t, dropFirst(5, `{"text":"Hello"}`))
	_, err := h.Transcribe(context.Background(), take(), stt.Hint{})
	if openai.Classify(err) != openai.ClassNetwork || srv.count() != 2 {
		t.Fatalf("err = %v after %d requests, want network after 2", err, srv.count())
	}
	srv5, h5 := newSTTServer(t, errorReply(http.StatusInternalServerError, "server_error"))
	if _, err := h5.Transcribe(context.Background(), take(), stt.Hint{}); openai.Classify(err) != openai.ClassServer || srv5.count() != 1 {
		t.Fatalf("500: err = %v after %d requests, want server after 1", err, srv5.count())
	}
}

func TestHTTPRefusesNonTestOrigin(t *testing.T) {
	_, h := newSTTServer(t, reply(`{"text":"x"}`))
	h.BaseURL = "https://evil.invalid"
	if _, err := h.Transcribe(context.Background(), take(), stt.Hint{}); openai.Classify(err) != openai.ClassInternal {
		t.Fatalf("err = %v, want the origin policy's internal error", err)
	}
}
