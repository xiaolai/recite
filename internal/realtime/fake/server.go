// Package fake provides test doubles for internal/realtime: Server, an httptest server that
// replays recorded fixture scenarios over the mint endpoint and the WebSocket with fault
// injection (FND-04), and ScriptedSynthesizer, a scripted Synthesizer (FND-06).
package fake

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Step is one recorded wire event.
type Step struct {
	Dir   string          // "client" or "server"
	Type  string          // the event's "type"
	Event json.RawMessage // the event, verbatim
}

// Scenario is one parsed fixture file.
type Scenario struct {
	Name         string
	Model        string
	MintStatus   int
	MintResponse json.RawMessage
	Steps        []Step
}

// FixtureDir returns the absolute path of testdata/realtime.
func FixtureDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("fake: runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "testdata", "realtime")
}

// LoadScenario reads testdata/realtime/<name>.jsonl.
func LoadScenario(name string) (Scenario, error) {
	return ParseScenarioFile(filepath.Join(FixtureDir(), name+".jsonl"))
}

// ParseScenarioFile parses a fixture file: a meta line, then client/server events in wire order.
func ParseScenarioFile(path string) (Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, fmt.Errorf("read fixture: %w", err)
	}
	return ParseScenario(data)
}

// ParseScenario parses fixture bytes.
func ParseScenario(data []byte) (Scenario, error) {
	var sc Scenario
	sr := bufio.NewScanner(bytes.NewReader(data))
	sr.Buffer(make([]byte, 0, 1<<20), 16<<20)
	n := 0
	for sr.Scan() {
		n++
		var line struct {
			Dir          string          `json:"dir"`
			Scenario     string          `json:"scenario"`
			Model        string          `json:"model"`
			MintStatus   int             `json:"mint_status"`
			MintResponse json.RawMessage `json:"mint_response"`
			Event        json.RawMessage `json:"event"`
		}
		if err := json.Unmarshal(sr.Bytes(), &line); err != nil {
			return Scenario{}, fmt.Errorf("fixture line %d: %w", n, err)
		}
		if n == 1 {
			if line.Dir != "meta" {
				return Scenario{}, fmt.Errorf("fixture line 1: dir %q, want meta", line.Dir)
			}
			sc.Name, sc.Model, sc.MintStatus, sc.MintResponse = line.Scenario, line.Model, line.MintStatus, line.MintResponse
			continue
		}
		if line.Dir != "client" && line.Dir != "server" {
			return Scenario{}, fmt.Errorf("fixture line %d: dir %q", n, line.Dir)
		}
		typ, err := eventType(line.Event)
		if err != nil {
			return Scenario{}, fmt.Errorf("fixture line %d: %w", n, err)
		}
		sc.Steps = append(sc.Steps, Step{Dir: line.Dir, Type: typ, Event: line.Event})
	}
	if err := sr.Err(); err != nil {
		return Scenario{}, fmt.Errorf("scan fixture: %w", err)
	}
	if n == 0 {
		return Scenario{}, errors.New("empty fixture")
	}
	return sc, nil
}

func eventType(ev []byte) (string, error) {
	var v struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(ev, &v); err != nil {
		return "", fmt.Errorf("event: %w", err)
	}
	if v.Type == "" {
		return "", errors.New("event has no type")
	}
	return v.Type, nil
}

// MintRequest is one request received on the mint endpoint.
type MintRequest struct {
	Header http.Header
	Body   []byte
}

// Option configures fault injection.
type Option func(*config)

type config struct {
	closeAfter   int // close the socket after this many server events; <0 = never
	errorAfter   int // inject an error event after this many server events; <0 = never
	errorCode    string
	unknownAfter int // inject an unknown event after this many server events; <0 = never
	dropDone     bool
	dropAudio    bool
	deltaDelay   time.Duration
	sleep        func(context.Context, time.Duration)
	mintStatus   int
	mintCode     string // overrides the code of the injected mint error
	mintParam    string // the param of the injected mint error; "" = null
	rejectBearer string // answer 401 invalid_token to mints carrying this bearer
	transcripts  []string
}

// WithTranscripts serves a transcription session interactively instead of replaying the
// scenario's client steps: the scenario's server events before its first client event are
// sent, then every input_audio_buffer.commit is answered with input_audio_buffer.committed
// for a new item and conversation.item.input_audio_transcription.completed carrying the next
// transcript. A commit beyond the last transcript is answered with an error event.
func WithTranscripts(texts ...string) Option {
	return func(c *config) { c.transcripts = append([]string{}, texts...) }
}

// WithCloseAfter closes the WebSocket abruptly after n server events (close mid-response).
func WithCloseAfter(n int) Option { return func(c *config) { c.closeAfter = n } }

// WithErrorEvent sends a server `error` event with the given code after n server events.
func WithErrorEvent(n int, code string) Option {
	return func(c *config) { c.errorAfter, c.errorCode = n, code }
}

// WithoutResponseDone drops every response.done event.
func WithoutResponseDone() Option { return func(c *config) { c.dropDone = true } }

// WithoutAudio drops every audio delta, so a response completes without audio (a protocol
// fault).
func WithoutAudio() Option { return func(c *config) { c.dropAudio = true } }

// WithDeltaDelay waits d before each *.delta event, through sleep (nil = a real timer).
func WithDeltaDelay(d time.Duration, sleep func(context.Context, time.Duration)) Option {
	return func(c *config) { c.deltaDelay, c.sleep = d, sleep }
}

// WithUnknownEvent sends an event of a type the client has never seen after n server events.
func WithUnknownEvent(n int) Option { return func(c *config) { c.unknownAfter = n } }

// WithMintError makes the mint endpoint answer status with an OpenAI-style error body.
func WithMintError(status int) Option { return func(c *config) { c.mintStatus = status } }

// WithMintErrorDetail is WithMintError with the error body's code and param set
// (param "" stays null).
func WithMintErrorDetail(status int, code, param string) Option {
	return func(c *config) { c.mintStatus, c.mintCode, c.mintParam = status, code, param }
}

// WithMintRejectBearer answers 401 invalid_token to every mint whose bearer is
// bearer, and mints normally otherwise: a token that expired server-side.
func WithMintRejectBearer(bearer string) Option {
	return func(c *config) { c.rejectBearer = bearer }
}

// UnknownEventType is the type sent by WithUnknownEvent.
const UnknownEventType = "fake.unknown_event"

// Server replays one scenario. It is safe for concurrent use.
type Server struct {
	URL string // http://127.0.0.1:port; the WebSocket is at ws://…/v1/realtime

	sc  Scenario
	cfg config
	hs  *httptest.Server
	wg  sync.WaitGroup

	// stop ends WebSocket handlers on Close: httptest does not track hijacked connections.
	stopCtx context.Context
	stop    context.CancelFunc

	mu        sync.Mutex
	client    []json.RawMessage
	sent      []string
	mints     []MintRequest
	wsHeaders []http.Header
	pings     int
	errs      []error
}

// NewServer starts a server for sc; it is closed by t.Cleanup.
func NewServer(t interface{ Cleanup(func()) }, sc Scenario, opts ...Option) *Server {
	s := &Server{sc: sc, cfg: config{closeAfter: -1, errorAfter: -1, unknownAfter: -1}}
	for _, o := range opts {
		o(&s.cfg)
	}
	s.stopCtx, s.stop = context.WithCancel(context.Background())
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/realtime/client_secrets", s.mint)
	mux.HandleFunc("GET /v1/realtime", s.ws)
	s.hs = httptest.NewServer(mux)
	s.URL = s.hs.URL
	t.Cleanup(s.Close)
	return s
}

// WebSocketURL is the realtime WebSocket endpoint.
func (s *Server) WebSocketURL() string {
	return "ws" + strings.TrimPrefix(s.URL, "http") + "/v1/realtime"
}

// Close stops the server and waits for WebSocket handlers. It is idempotent.
func (s *Server) Close() {
	s.stop()
	s.hs.Close()
	s.wg.Wait()
}

// ClientEvents returns the client events received, in order.
func (s *Server) ClientEvents() []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]json.RawMessage(nil), s.client...)
}

// Sent returns the types of the server events written, injected ones included.
func (s *Server) Sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

// MintRequests returns the mint requests received.
func (s *Server) MintRequests() []MintRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]MintRequest(nil), s.mints...)
}

// WebSocketHeaders returns the headers of each WebSocket upgrade request.
func (s *Server) WebSocketHeaders() []http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]http.Header(nil), s.wsHeaders...)
}

// Pings returns how many ping frames clients have sent (NET-03: recite sends none).
func (s *Server) Pings() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pings
}

// Errors returns script mismatches and protocol errors seen by the fake.
func (s *Server) Errors() []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]error(nil), s.errs...)
}

func (s *Server) fail(err error) {
	s.mu.Lock()
	s.errs = append(s.errs, err)
	s.mu.Unlock()
}

// mintErrorBodies mirror the OpenAI error envelope for each injectable status.
var mintErrorBodies = map[int][2]string{ // status → {type, code}
	400: {"invalid_request_error", "invalid_value"},
	401: {"invalid_request_error", "invalid_api_key"},
	403: {"invalid_request_error", "unsupported_country_region_territory"},
	429: {"insufficient_quota", "rate_limit_exceeded"},
	500: {"server_error", "server_error"},
}

func (s *Server) mint(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.fail(fmt.Errorf("mint: read body: %w", err))
	}
	s.mu.Lock()
	s.mints = append(s.mints, MintRequest{Header: r.Header.Clone(), Body: body})
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if s.cfg.rejectBearer != "" && r.Header.Get("Authorization") == "Bearer "+s.cfg.rejectBearer {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"message": "token expired", "type": "invalid_request_error", "code": "invalid_token", "param": nil,
		}})
		return
	}
	if st := s.cfg.mintStatus; st != 0 {
		tc, ok := mintErrorBodies[st]
		if !ok {
			tc = [2]string{"server_error", "server_error"}
		}
		if s.cfg.mintCode != "" {
			tc[1] = s.cfg.mintCode
		}
		var param any
		if s.cfg.mintParam != "" {
			param = s.cfg.mintParam
		}
		w.WriteHeader(st)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"message": fmt.Sprintf("fake mint error %d", st), "type": tc[0], "code": tc[1], "param": param,
		}})
		return
	}
	status := s.sc.MintStatus
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(s.sc.MintResponse)
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	s.wg.Add(1)
	defer s.wg.Done()
	s.mu.Lock()
	s.wsHeaders = append(s.wsHeaders, r.Header.Clone())
	s.mu.Unlock()
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OnPingReceived: func(context.Context, []byte) bool {
		s.mu.Lock()
		s.pings++
		s.mu.Unlock()
		return true
	}})
	if err != nil {
		s.fail(fmt.Errorf("ws accept: %w", err))
		return
	}
	defer func() { _ = c.CloseNow() }()
	c.SetReadLimit(16 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(s.stopCtx, cancel)()
	if s.cfg.transcripts != nil {
		if err := s.transcribe(ctx, c); err != nil && ctx.Err() == nil {
			s.fail(err)
		}
		return
	}
	if err := s.replay(ctx, c); err != nil {
		if !errors.Is(err, errAborted) {
			s.fail(err)
		}
		return
	}
	// Script finished: drain until the client goes away so it can read everything sent.
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		s.record(data)
	}
}

var errAborted = errors.New("aborted by fault injection")

func (s *Server) record(data []byte) {
	s.mu.Lock()
	s.client = append(s.client, json.RawMessage(bytes.Clone(data)))
	s.mu.Unlock()
}

func (s *Server) replay(ctx context.Context, c *websocket.Conn) error {
	sent := 0
	write := func(typ string, ev []byte) error {
		if err := c.Write(ctx, websocket.MessageText, ev); err != nil {
			return fmt.Errorf("write %s: %w", typ, err)
		}
		s.mu.Lock()
		s.sent = append(s.sent, typ)
		s.mu.Unlock()
		return nil
	}
	// afterSend applies the count-based faults once sent server events have gone out.
	afterSend := func() error {
		if sent == s.cfg.unknownAfter {
			ev := fmt.Sprintf(`{"type":%q,"event_id":"event_9998"}`, UnknownEventType)
			if err := write(UnknownEventType, []byte(ev)); err != nil {
				return err
			}
		}
		if sent == s.cfg.errorAfter {
			ev, _ := json.Marshal(map[string]any{"type": "error", "event_id": "event_9999", "error": map[string]any{
				"type": "server_error", "code": s.cfg.errorCode, "message": "injected by fake", "param": nil, "event_id": nil,
			}})
			if err := write("error", ev); err != nil {
				return err
			}
		}
		if sent == s.cfg.closeAfter {
			_ = c.CloseNow()
			return errAborted
		}
		return nil
	}
	if err := afterSend(); err != nil {
		return err
	}
	for i, st := range s.sc.Steps {
		if st.Dir == "client" {
			_, data, err := c.Read(ctx)
			if err != nil {
				return fmt.Errorf("step %d: read client %s: %w", i, st.Type, err)
			}
			s.record(data)
			got, terr := eventType(data)
			if terr != nil || got != st.Type {
				s.fail(fmt.Errorf("step %d: client sent %q (%v), want %q", i, got, terr, st.Type))
			}
			continue
		}
		if s.cfg.dropDone && st.Type == "response.done" {
			continue
		}
		if s.cfg.dropAudio && st.Type == "response.output_audio.delta" {
			continue
		}
		if s.cfg.deltaDelay > 0 && strings.HasSuffix(st.Type, ".delta") {
			sleep := s.cfg.sleep
			if sleep == nil {
				sleep = realSleep
			}
			sleep(ctx, s.cfg.deltaDelay)
		}
		if err := write(st.Type, st.Event); err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		sent++
		if err := afterSend(); err != nil {
			return err
		}
	}
	return nil
}

// transcribe serves WithTranscripts until the client goes away.
func (s *Server) transcribe(ctx context.Context, c *websocket.Conn) error {
	write := func(typ string, ev []byte) error {
		if err := c.Write(ctx, websocket.MessageText, ev); err != nil {
			return fmt.Errorf("write %s: %w", typ, err)
		}
		s.mu.Lock()
		s.sent = append(s.sent, typ)
		s.mu.Unlock()
		return nil
	}
	for _, st := range s.sc.Steps {
		if st.Dir == "client" {
			break
		}
		if err := write(st.Type, st.Event); err != nil {
			return err
		}
	}
	commits := 0
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return nil // the client closed the session
		}
		s.record(data)
		typ, err := eventType(data)
		if err != nil {
			return err
		}
		switch typ {
		case "input_audio_buffer.append":
			continue
		case "input_audio_buffer.commit":
		default:
			s.fail(fmt.Errorf("transcription session: unexpected client event %q", typ))
			continue
		}
		commits++
		if commits > len(s.cfg.transcripts) {
			ev, _ := json.Marshal(map[string]any{"type": "error", "event_id": fmt.Sprintf("event_%04d", 9000+commits), "error": map[string]any{
				"type": "invalid_request_error", "code": "fake_no_transcript", "message": "no scripted transcript left", "param": nil, "event_id": nil,
			}})
			if err := write("error", ev); err != nil {
				return err
			}
			continue
		}
		item := fmt.Sprintf("item_%04d", commits)
		committed, _ := json.Marshal(map[string]any{"type": "input_audio_buffer.committed", "event_id": fmt.Sprintf("event_%04d", 2*commits), "previous_item_id": nil, "item_id": item})
		completed, _ := json.Marshal(map[string]any{"type": "conversation.item.input_audio_transcription.completed", "event_id": fmt.Sprintf("event_%04d", 2*commits+1),
			"item_id": item, "content_index": 0, "transcript": s.cfg.transcripts[commits-1]})
		if err := write("input_audio_buffer.committed", committed); err != nil {
			return err
		}
		if err := write("conversation.item.input_audio_transcription.completed", completed); err != nil {
			return err
		}
	}
}

func realSleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
