package realtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/goleak"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/realtime/fake"
)

// firstClientEvent returns the scenario's first client event, sent verbatim.
func firstClientEvent(t testing.TB, sc fake.Scenario) json.RawMessage {
	t.Helper()
	for _, st := range sc.Steps {
		if st.Dir == "client" {
			return st.Event
		}
	}
	t.Fatal("scenario has no client event")
	return nil
}

// render sends the scenario's request and reads events until response.done,
// returning the PCM byte count and the done event.
func render(t testing.TB, s *realtime.Session, sc fake.Scenario) (int, realtime.ResponseDone, error) {
	t.Helper()
	ctx := context.Background()
	ev, err := s.Next(ctx)
	if err != nil {
		return 0, realtime.ResponseDone{}, err
	}
	if _, ok := ev.(realtime.SessionCreated); !ok {
		t.Fatalf("first event %T", ev)
	}
	if err := s.Send(ctx, firstClientEvent(t, sc)); err != nil {
		return 0, realtime.ResponseDone{}, err
	}
	pcm := 0
	for {
		ev, err := s.Next(ctx)
		if err != nil {
			return pcm, realtime.ResponseDone{}, err
		}
		switch e := ev.(type) {
		case realtime.AudioDelta:
			pcm += len(e.PCM)
		case realtime.ErrorEvent:
			return pcm, realtime.ResponseDone{}, e.Err
		case realtime.ResponseDone:
			return pcm, e, nil
		}
	}
}

// accept: NET-03#1
func TestNoClientPingsDuringTenSecondRender(t *testing.T) {
	sc := loadScenario(t, "speak")
	deltas := 0
	for _, st := range sc.Steps {
		if strings.HasSuffix(st.Type, ".delta") {
			deltas++
		}
	}
	// The fake sends no pings either; the render lasts at least 10 s of real time.
	srv := fake.NewServer(t, sc, fake.WithDeltaDelay(10*time.Second/time.Duration(deltas), nil))
	s := dial(t, srv)
	start := time.Now()
	pcm, done, err := render(t, s, sc)
	if err != nil || pcm == 0 || done.Status != realtime.StatusCompleted {
		t.Fatalf("render: pcm %d done %+v err %v", pcm, done, err)
	}
	if time.Since(start) < 10*time.Second {
		t.Fatalf("render took %v, want at least 10 s", time.Since(start))
	}
	if n := srv.Pings(); n != 0 {
		t.Fatalf("client sent %d pings", n)
	}
	if h := srv.WebSocketHeaders()[0].Get("Authorization"); h != "Bearer ek_fixture" {
		t.Fatalf("upgrade Authorization %q", h)
	}
}

// accept: NET-03#2
func TestContextCancelUnblocksRead(t *testing.T) {
	sc := loadScenario(t, "speak")
	s := dial(t, fake.NewServer(t, sc))
	if _, err := s.Next(context.Background()); err != nil { // session.created
		t.Fatal(err)
	}
	// The fake now waits for a client event, so this read blocks until cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := s.Next(ctx)
		errc <- err
	}()
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read not unblocked within 2 s")
	}
	// The session survives a cancelled read.
	if err := s.Send(context.Background(), firstClientEvent(t, sc)); err != nil {
		t.Fatal(err)
	}
	for {
		ev, err := s.Next(context.Background())
		if err != nil {
			t.Fatalf("after a cancelled read: %v", err)
		}
		if _, ok := ev.(realtime.ResponseDone); ok {
			return
		}
	}
}

// BenchmarkCancelUnblocksRead measures the NET-03 latency target (100 ms).
func BenchmarkCancelUnblocksRead(b *testing.B) {
	sc := loadScenario(b, "speak")
	s := dial(b, fake.NewServer(b, sc))
	if _, err := s.Next(context.Background()); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			_, _ = s.Next(ctx)
			close(done)
		}()
		cancel()
		<-done
	}
}

// accept: NET-03#3
func TestCloseWithSilentPeerUsesCloseNow(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	release := make(chan struct{})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		// Never read, so the close frame is never acknowledged.
		<-release
		_ = c.CloseNow()
	}))
	c, base := client(t, hs.URL)
	s, err := realtime.Dial(context.Background(), realtime.DialConfig{
		Client: c, URL: realtime.WebSocketURL(base, "m"), Secret: auth.NewClientSecret("ek_canary", time.Unix(1800000000, 0)),
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Fatalf("Close took %v, want ≤ 2.5 s", d)
	}
	if _, err := s.Next(context.Background()); !errors.Is(err, realtime.ErrClosed) {
		t.Fatalf("Next after Close: %v", err)
	}
	if err := s.Send(context.Background(), map[string]string{"type": "x"}); !errors.Is(err, realtime.ErrClosed) {
		t.Fatalf("Send after Close: %v", err)
	}
	close(release)
	hs.Close()
	c.CloseIdleConnections()
}

func TestCloseIsPromptWithAnsweringPeer(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	srv := fake.NewServer(t, loadScenario(t, "speak"))
	s := dial(t, srv)
	if _, err := s.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil { // idempotent
		t.Fatal(err)
	}
	srv.Close()
}

// Unknown events are counted and do not stop a render.
// accept: NET-04#2
func TestUnknownEventStillRenders(t *testing.T) {
	sc := loadScenario(t, "speak")
	srv := fake.NewServer(t, sc, fake.WithUnknownEvent(3))
	s := dial(t, srv)
	pcm, done, err := render(t, s, sc)
	if err != nil || pcm == 0 || done.Status != realtime.StatusCompleted {
		t.Fatalf("render: %v", err)
	}
	if got := s.Unknown(); got[fake.UnknownEventType] != 1 || len(got) != 1 {
		t.Fatalf("unknown counts %v", got)
	}
}

// WebSocket rows of design §5.3, end to end through the session.
// accept: NET-05#1
func TestSessionClassifiesWebSocketFailures(t *testing.T) {
	sc := loadScenario(t, "speak")
	t.Run("error event server_error", func(t *testing.T) {
		s := dial(t, fake.NewServer(t, sc, fake.WithErrorEvent(3, "server_error")))
		_, _, err := render(t, s, sc)
		wantClass(t, err, openai.ClassServer)
	})
	t.Run("error event from fixture is invalid-request", func(t *testing.T) {
		esc := loadScenario(t, "error-event")
		s := dial(t, fake.NewServer(t, esc))
		_, _, err := render(t, s, esc)
		wantClass(t, err, openai.ClassInvalidRequest)
		var e *openai.Error
		if errors.As(err, &e); e.Param != "response.audio.output.voice" {
			t.Fatalf("param %q", e.Param)
		}
	})
	t.Run("abrupt close is network", func(t *testing.T) {
		s := dial(t, fake.NewServer(t, sc, fake.WithCloseAfter(4)))
		_, _, err := render(t, s, sc)
		wantClass(t, err, openai.ClassNetwork)
	})
	t.Run("close 1011 is server", func(t *testing.T) {
		s := dialHandler(t, func(c *websocket.Conn) {
			_ = c.Close(websocket.StatusInternalError, "keepalive ping timeout")
		})
		_, err := s.Next(context.Background())
		wantClass(t, err, openai.ClassServer)
		if strings.Contains(err.Error(), "ek_") {
			t.Fatal("secret in error")
		}
	})
	t.Run("handshake 403 is route-denied", func(t *testing.T) {
		hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"no","type":"invalid_request_error","code":"missing_scope","param":null}}`))
		}))
		t.Cleanup(hs.Close)
		c, base := client(t, hs.URL)
		_, err := realtime.Dial(context.Background(), realtime.DialConfig{
			Client: c, URL: realtime.WebSocketURL(base, "m"), Secret: auth.NewClientSecret("ek_canary", time.Unix(1, 0)),
			Route: openai.RouteSpeechMint,
		})
		wantClass(t, err, openai.ClassRouteDenied)
	})
}

// dialHandler opens a session to a test server running fn on the accepted conn.
func dialHandler(t *testing.T, fn func(*websocket.Conn)) *realtime.Session {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		fn(c)
	}))
	t.Cleanup(hs.Close)
	c, base := client(t, hs.URL)
	s, err := realtime.Dial(context.Background(), realtime.DialConfig{
		Client: c, URL: realtime.WebSocketURL(base, "m"), Secret: auth.NewClientSecret("ek_canary", time.Unix(1800000000, 0)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestReadLimitAndSerialisedWrites(t *testing.T) {
	big := strings.Repeat("A", 12<<20) // base64 of 9 MiB, under the 16 MiB limit
	var mu sync.Mutex
	got := 0
	s := dialHandler(t, func(c *websocket.Conn) {
		ctx := context.Background()
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"response.output_audio.delta","response_id":"r","delta":"`+big+`"}`))
		over := `{"type":"x","pad":"` + strings.Repeat("A", realtime.ReadLimit) + `"}`
		for range 8 {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
			mu.Lock()
			got++
			mu.Unlock()
		}
		_ = c.Write(ctx, websocket.MessageText, []byte(over))
		_, _, _ = c.Read(ctx)
	})
	ev, err := s.Next(context.Background())
	if d, ok := ev.(realtime.AudioDelta); err != nil || !ok || len(d.PCM) != 9<<20 {
		t.Fatalf("large message: %T %v", ev, err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if err := s.Send(context.Background(), map[string]any{"type": "input_audio_buffer.append", "n": i}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if _, err := s.Next(context.Background()); err == nil {
		t.Fatal("message over the read limit accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	if got != 8 {
		t.Fatalf("server read %d of 8 concurrent writes", got)
	}
}
