package stt_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/realtime/fake"
	"github.com/xiaolai/recite/internal/stt"
)

// pacer records the sender's sleeps instead of waiting.
type pacer struct {
	mu     sync.Mutex
	sleeps []time.Duration
}

func (p *pacer) sleep(_ context.Context, d time.Duration) {
	p.mu.Lock()
	p.sleeps = append(p.sleeps, d)
	p.mu.Unlock()
}

func newRealtime(t *testing.T, opts ...fake.Option) (*stt.Realtime, *fake.Server, *pacer) {
	t.Helper()
	sc, err := fake.LoadScenario("transcription")
	if err != nil {
		t.Fatal(err)
	}
	srv := fake.NewServer(t, sc, opts...)
	c, base := client(t, srv.URL)
	p := &pacer{}
	r := &stt.Realtime{
		Minter:  &realtime.Minter{Client: c, BaseURL: base, Creds: staticCreds{}},
		Client:  c,
		BaseURL: base,
		Sleep:   p.sleep,
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, srv, p
}

func clientTypes(t *testing.T, srv *fake.Server) []string {
	t.Helper()
	var out []string
	for _, raw := range srv.ClientEvents() {
		var ev struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev.Type)
	}
	return out
}

// accept: VER-02#1
func TestRealtimeThreeCommitsOnOneSession(t *testing.T) {
	r, srv, p := newRealtime(t, fake.WithTranscripts("one fish", "two fish", "red fish"))
	ctx := context.Background()
	pcm := make([]byte, 48000) // 1 s: with 2 × 300 ms padding, 1.6 s = 8 frames of 200 ms
	for i := range pcm {
		pcm[i] = byte(i % 251)
	}
	var got []string
	for range 3 {
		tr, err := r.Transcribe(ctx, pcm, stt.Hint{Languages: []string{"en"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(tr.Languages) != 0 || tr.Seconds != 2 {
			t.Fatalf("transcript %+v", tr)
		}
		got = append(got, tr.Text)
	}
	if strings.Join(got, "|") != "one fish|two fish|red fish" {
		t.Fatalf("transcripts %q", got)
	}
	if n := len(srv.MintRequests()); n != 1 {
		t.Fatalf("%d mints, want 1 session reused across takes", n)
	}
	if n := len(srv.WebSocketHeaders()); n != 1 {
		t.Fatalf("%d WebSocket sessions, want 1", n)
	}
	mint := srv.MintRequests()[0].Body
	if !bytes.Contains(mint, []byte(`"model":"gpt-live-transcribe"`)) || !bytes.Contains(mint, []byte(`"turn_detection":null`)) {
		t.Fatalf("mint body %s", mint)
	}
	types := clientTypes(t, srv)
	want := strings.Repeat(strings.Repeat("input_audio_buffer.append ", 8)+"input_audio_buffer.commit ", 3)
	if strings.Join(types, " ")+" " != want {
		t.Fatalf("client events %v", types)
	}
	// The appended audio is the padded take, in order.
	var audio []byte
	for _, raw := range srv.ClientEvents()[:8] {
		var ev struct {
			Audio string `json:"audio"`
		}
		_ = json.Unmarshal(raw, &ev)
		b, err := base64.StdEncoding.DecodeString(ev.Audio)
		if err != nil {
			t.Fatal(err)
		}
		audio = append(audio, b...)
	}
	if !bytes.Equal(audio, stt.Pad(pcm)) {
		t.Fatal("streamed audio is not the padded take")
	}
	// 8× pace: 25 ms between 200 ms frames, 7 gaps per take.
	if len(p.sleeps) != 21 || p.sleeps[0] != 25*time.Millisecond {
		t.Fatalf("sleeps %v", p.sleeps)
	}
	if errs := srv.Errors(); len(errs) != 0 {
		t.Fatalf("fake errors: %v", errs)
	}
}

func TestRealtimeErrorDropsSession(t *testing.T) {
	r, srv, _ := newRealtime(t, fake.WithTranscripts("only one"))
	ctx := context.Background()
	if _, err := r.Transcribe(ctx, make([]byte, 4800), stt.Hint{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Transcribe(ctx, make([]byte, 4800), stt.Hint{}); err == nil {
		t.Fatal("no error after the script ran out")
	}
	// The failed session is closed; the next take opens a new one.
	_, _ = r.Transcribe(ctx, make([]byte, 4800), stt.Hint{})
	if n := len(srv.MintRequests()); n != 2 {
		t.Fatalf("%d mints, want 2", n)
	}
}

func TestRealtimeMintDeniedIsRouteDenied(t *testing.T) {
	r, _, _ := newRealtime(t, fake.WithMintErrorDetail(http.StatusUnauthorized, "missing_scope", ""))
	_, err := r.Transcribe(context.Background(), make([]byte, 4800), stt.Hint{})
	var oe *openai.Error
	if !errors.As(err, &oe) || oe.Class != openai.ClassRouteDenied || oe.Route != openai.RouteRealtimeSTT {
		t.Fatalf("err = %v", err)
	}
}

// scripted is a Transcriber returning fixed results in order.
type scripted struct {
	mu    sync.Mutex
	errs  []error
	text  string
	calls int
}

func (s *scripted) Transcribe(context.Context, []byte, stt.Hint) (stt.Transcript, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		if err != nil {
			return stt.Transcript{}, err
		}
	}
	return stt.Transcript{Text: s.text}, nil
}

func TestChainSwitchesOnceAndLogsOnce(t *testing.T) {
	denied := &openai.Error{Class: openai.ClassRouteDenied, Route: openai.RouteHTTPSTT, Status: 401, Code: "missing_scope"}
	primary := &scripted{text: "http", errs: []error{nil, denied, denied}}
	fallback := &scripted{text: "realtime"}
	var logs bytes.Buffer
	var switches []error
	c := &stt.Chain{
		Primary: primary, Fallback: fallback,
		Logger:   slog.New(slog.NewTextHandler(&logs, nil)),
		OnSwitch: func(err error) { switches = append(switches, err) },
	}
	ctx := context.Background()
	var got []string
	for range 4 {
		tr, err := c.Transcribe(ctx, nil, stt.Hint{})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, tr.Text)
	}
	if strings.Join(got, " ") != "http realtime realtime realtime" {
		t.Fatalf("transcripts %q", got)
	}
	if primary.calls != 2 || !c.Switched() || len(switches) != 1 || !errors.Is(switches[0], denied) {
		t.Fatalf("primary calls %d, switched %v, switches %v", primary.calls, c.Switched(), switches)
	}
	if n := strings.Count(logs.String(), "realtime transcription for the rest of the run"); n != 1 {
		t.Fatalf("switch logged %d times:\n%s", n, logs.String())
	}
}

func TestChainPassesOtherErrorsThrough(t *testing.T) {
	primary := &scripted{errs: []error{&openai.Error{Class: openai.ClassServer}}}
	fallback := &scripted{text: "realtime"}
	c := &stt.Chain{Primary: primary, Fallback: fallback}
	if _, err := c.Transcribe(context.Background(), nil, stt.Hint{}); openai.Classify(err) != openai.ClassServer || c.Switched() || fallback.calls != 0 {
		t.Fatalf("err = %v, switched %v", err, c.Switched())
	}
}

func TestChainConcurrentRefusalsSwitchOnce(t *testing.T) {
	denied := &openai.Error{Class: openai.ClassRouteDenied, Route: openai.RouteHTTPSTT}
	primary := &scripted{errs: []error{denied, denied, denied, denied, denied, denied, denied, denied}}
	n := 0
	var mu sync.Mutex
	c := &stt.Chain{Primary: primary, Fallback: &scripted{text: "rt"}, OnSwitch: func(error) { mu.Lock(); n++; mu.Unlock() }}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { _, _ = c.Transcribe(context.Background(), nil, stt.Hint{}) })
	}
	wg.Wait()
	if n != 1 {
		t.Fatalf("OnSwitch called %d times", n)
	}
}
