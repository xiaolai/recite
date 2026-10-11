package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

var scenarios = []string{"speak", "oob-three", "answer-instead-of-read", "error-event", "incomplete", "transcription"}

// accept: FND-04#2
func TestFixturesPresentAndParse(t *testing.T) {
	for _, name := range scenarios {
		sc, err := LoadScenario(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sc.Name != name || len(sc.Steps) == 0 || len(sc.MintResponse) == 0 {
			t.Fatalf("%s: incomplete scenario", name)
		}
	}
	oob, _ := LoadScenario("oob-three")
	if n := countType(oob, "response.done"); n != 3 {
		t.Errorf("oob-three has %d response.done, want 3", n)
	}
	errEv, _ := LoadScenario("error-event")
	if countType(errEv, "error") == 0 {
		t.Error("error-event has no error event")
	}
	tr, _ := LoadScenario("transcription")
	if countType(tr, "conversation.item.input_audio_transcription.completed") == 0 {
		t.Error("transcription has no completed transcript")
	}
}

func serverTypes(sc Scenario) []string {
	var out []string
	for _, st := range sc.Steps {
		if st.Dir == "server" {
			out = append(out, st.Type)
		}
	}
	return out
}

func countType(sc Scenario, typ string) int {
	n := 0
	for _, st := range sc.Steps {
		if st.Type == typ {
			n++
		}
	}
	return n
}

var (
	placeholderID = regexp.MustCompile(`^[a-z]+_\d{4}$`)
	secretLike    = regexp.MustCompile(`ek_[A-Za-z0-9_]*|eyJ[A-Za-z0-9_-]*|sk-[A-Za-z0-9_-]+`)
	emailLike     = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
)

// accept: FND-04#3
func TestFixtureLint(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(FixtureDir(), "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseScenario(data); err != nil { // every non-meta line has a type
			t.Errorf("%s: %v", f, err)
		}
		for _, prefix := range []string{"ek_", "eyJ", "sk-"} {
			rest := data
			for i := bytes.Index(rest, []byte(prefix)); i >= 0; i = bytes.Index(rest, []byte(prefix)) {
				if m := secretLike.Find(rest[i:]); string(m) != "ek_fixture" {
					t.Errorf("%s: secret-like string %.12q…", filepath.Base(f), m)
				}
				rest = rest[i+len(prefix):]
			}
		}
		for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var v any
			if err := json.Unmarshal([]byte(line), &v); err != nil {
				t.Fatalf("%s:%d: %v", f, i+1, err)
			}
			walkIDs(t, filepath.Base(f), i+1, v)
		}
	}
}

// walkIDs checks every request, session, item, response, event and account identifier.
func walkIDs(t *testing.T, file string, line int, v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			lk := strings.ToLower(k)
			if strings.Contains(lk, "account") || strings.Contains(lk, "organization") || lk == "user" || lk == "user_id" {
				t.Errorf("%s:%d: account identifier key %q", file, line, k)
			}
			if s, ok := val.(string); ok {
				if (k == "id" || strings.HasSuffix(k, "_id")) && !placeholderID.MatchString(s) {
					t.Errorf("%s:%d: %s = %q is not a placeholder", file, line, k, s)
				}
				// Audio payloads are generated base64 PCM; scanning them for addresses is waste.
				if k != "delta" && k != "audio" && emailLike.MatchString(s) {
					t.Errorf("%s:%d: %s contains an email address", file, line, k)
				}
			}
			walkIDs(t, file, line, val)
		}
	case []any:
		for _, e := range x {
			walkIDs(t, file, line, e)
		}
	}
}

func dial(t *testing.T, s *Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := http.Header{"Authorization": {"Bearer ek_canary"}}
	c, _, err := websocket.Dial(ctx, s.WebSocketURL()+"?model=gpt-realtime-2.1", &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadLimit(16 << 20)
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

// drive plays the client side of sc: it sends each recorded client event and reads server
// events, returning the server event types read until the stream ends or until want scenario
// events (not counting injected ones) have arrived; want < 0 means every server event.
func drive(t *testing.T, s *Server, sc Scenario, want int) (types []string, readErr error) {
	t.Helper()
	c := dial(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var clients []json.RawMessage
	for _, st := range sc.Steps {
		if st.Dir == "client" {
			clients = append(clients, st.Event)
		}
	}
	// The client sends its next event once the server has sent everything before it.
	serverBefore := map[int]int{} // client index → server events preceding it
	n, ci := 0, 0
	for _, st := range sc.Steps {
		if st.Dir == "client" {
			serverBefore[ci] = n
			ci++
		} else {
			n++
		}
	}
	if want < 0 {
		want = n
	}
	recorded := 0
	ci = 0
	for recorded < want {
		for ci < len(clients) && serverBefore[ci] <= recorded {
			if err := c.Write(ctx, websocket.MessageText, clients[ci]); err != nil {
				return types, err
			}
			ci++
		}
		_, data, err := c.Read(ctx)
		if err != nil {
			return types, err
		}
		typ, err := eventType(data)
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, typ)
		if typ != UnknownEventType && (typ != "error" || !strings.Contains(string(data), "event_9999")) {
			recorded++
		}
	}
	return types, nil
}

// accept: FND-04#1
// accept: FND-04#2
func TestReplayEveryScenario(t *testing.T) {
	for _, name := range scenarios {
		t.Run(name, func(t *testing.T) {
			sc, err := LoadScenario(name)
			if err != nil {
				t.Fatal(err)
			}
			s := NewServer(t, sc)
			types, err := drive(t, s, sc, -1)
			if err != nil {
				t.Fatalf("read: %v (after %v)", err, types)
			}
			want := serverTypes(sc)
			if !slices.Equal(types, want) {
				t.Fatalf("server types differ: got %d events, want %d", len(types), len(want))
			}
			s.Close()
			if errs := s.Errors(); len(errs) != 0 {
				t.Fatalf("fake errors: %v", errs)
			}
			if got, want := len(s.ClientEvents()), len(sc.Steps)-len(want); got != want {
				t.Fatalf("client events = %d, want %d", got, want)
			}
			if h := s.WebSocketHeaders(); len(h) != 1 || h[0].Get("Authorization") != "Bearer ek_canary" {
				t.Fatal("upgrade headers not recorded")
			}
		})
	}
}

// accept: FND-04#1
func TestClientMismatchIsRecorded(t *testing.T) {
	sc, _ := LoadScenario("speak")
	s := NewServer(t, sc)
	c := dial(t, s)
	ctx := context.Background()
	if _, _, err := c.Read(ctx); err != nil { // session.created
		t.Fatal(err)
	}
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"session.update"}`)); err != nil {
		t.Fatal(err)
	}
	for { // the script continues after a mismatch
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ, _ := eventType(data); typ == "response.done" {
			break
		}
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
	s.Close()
	if errs := s.Errors(); len(errs) != 1 || !strings.Contains(errs[0].Error(), `"session.update"`) {
		t.Fatalf("errors = %v", errs)
	}
}

// accept: FND-04#1
func TestFaultCloseMidResponse(t *testing.T) {
	sc, _ := LoadScenario("speak")
	s := NewServer(t, sc, WithCloseAfter(5))
	types, err := drive(t, s, sc, -1)
	if err == nil {
		t.Fatal("stream did not break")
	}
	if len(types) != 5 {
		t.Fatalf("read %d events before close, want 5", len(types))
	}
}

// accept: FND-04#1
func TestFaultErrorEvent(t *testing.T) {
	sc, _ := LoadScenario("speak")
	s := NewServer(t, sc, WithErrorEvent(3, "server_error"))
	types, err := drive(t, s, sc, -1)
	if err != nil {
		t.Fatal(err)
	}
	if types[3] != "error" {
		t.Fatalf("event 4 = %q, want error", types[3])
	}
}

// accept: FND-04#1
func TestFaultNoResponseDone(t *testing.T) {
	sc, _ := LoadScenario("speak")
	s := NewServer(t, sc, WithoutResponseDone())
	total := countType(sc, "response.done")
	if _, err := drive(t, s, sc, len(serverTypes(sc))-total); err != nil {
		t.Fatal(err)
	}
	s.Close() // the script has ended; Sent is final
	sent := s.Sent()
	if slices.Contains(sent, "response.done") || len(sent) != len(serverTypes(sc))-total {
		t.Fatalf("sent %v", sent)
	}
}

// accept: FND-04#1
func TestFaultSlowDeltas(t *testing.T) {
	sc, _ := LoadScenario("speak")
	var mu sync.Mutex
	var slept []time.Duration
	sleep := func(_ context.Context, d time.Duration) {
		mu.Lock()
		slept = append(slept, d)
		mu.Unlock()
	}
	s := NewServer(t, sc, WithDeltaDelay(250*time.Millisecond, sleep))
	if _, err := drive(t, s, sc, -1); err != nil {
		t.Fatal(err)
	}
	deltas := 0
	for _, st := range sc.Steps {
		if st.Dir == "server" && strings.HasSuffix(st.Type, ".delta") {
			deltas++
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(slept) != deltas || deltas == 0 || slept[0] != 250*time.Millisecond {
		t.Fatalf("slept %d times, want %d", len(slept), deltas)
	}
}

// accept: FND-04#1
func TestFaultUnknownEvent(t *testing.T) {
	sc, _ := LoadScenario("speak")
	s := NewServer(t, sc, WithUnknownEvent(2))
	types, err := drive(t, s, sc, -1)
	if err != nil {
		t.Fatal(err)
	}
	if types[2] != UnknownEventType {
		t.Fatalf("event 3 = %q, want %q", types[2], UnknownEventType)
	}
}

// accept: FND-04#1
func TestMint(t *testing.T) {
	sc, _ := LoadScenario("speak")
	post := func(s *Server) (*http.Response, []byte) {
		req, _ := http.NewRequest(http.MethodPost, s.URL+"/v1/realtime/client_secrets", strings.NewReader(`{"session":{"type":"realtime"}}`))
		req.Header.Set("Authorization", "Bearer eyJcanary.canary.canary")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp, b
	}
	s := NewServer(t, sc)
	resp, body := post(s)
	var ok struct {
		Value string `json:"value"`
	}
	if resp.StatusCode != 200 || json.Unmarshal(body, &ok) != nil || ok.Value != "ek_fixture" {
		t.Fatalf("mint = %d", resp.StatusCode)
	}
	m := s.MintRequests()
	if len(m) != 1 || m[0].Header.Get("Authorization") != "Bearer eyJcanary.canary.canary" || !strings.Contains(string(m[0].Body), "realtime") {
		t.Fatal("mint request not recorded")
	}
	for _, st := range []int{400, 401, 403, 429, 500} {
		s := NewServer(t, sc, WithMintError(st))
		resp, body := post(s)
		var e struct {
			Error struct {
				Message, Type, Code string
			} `json:"error"`
		}
		if resp.StatusCode != st || json.Unmarshal(body, &e) != nil || e.Error.Type == "" || e.Error.Code == "" || e.Error.Message == "" {
			t.Errorf("status %d: got %d %s", st, resp.StatusCode, body)
		}
	}
	// Detail and per-bearer rejection.
	resp, body = post(NewServer(t, sc, WithMintErrorDetail(400, "invalid_value", "session.audio.output.voice")))
	if resp.StatusCode != 400 || !strings.Contains(string(body), `"param":"session.audio.output.voice"`) || !strings.Contains(string(body), `"code":"invalid_value"`) {
		t.Errorf("detail: %d %s", resp.StatusCode, body)
	}
	if resp, body := post(NewServer(t, sc, WithMintRejectBearer("eyJcanary.canary.canary"))); resp.StatusCode != 401 || !strings.Contains(string(body), "invalid_token") {
		t.Errorf("reject bearer: %d %s", resp.StatusCode, body)
	}
	if resp, _ := post(NewServer(t, sc, WithMintRejectBearer("eyJother.other.other"))); resp.StatusCode != 200 {
		t.Errorf("other bearer refused: %d", resp.StatusCode)
	}
}

func TestPingsCounted(t *testing.T) {
	sc, err := LoadScenario("speak")
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(t, sc)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, s.WebSocketURL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// A reader is needed to receive the pong; cancelling ctx ends it.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}()
	defer func() {
		cancel()
		<-readDone
		_ = c.CloseNow()
	}()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if n := s.Pings(); n != 1 {
		t.Fatalf("Pings() = %d, want 1", n)
	}
}
