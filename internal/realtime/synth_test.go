package realtime_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/realtime/fake"
)

// stepClock advances by step on every reading, so durations are exact.
type stepClock struct {
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(c.step)
	return c.t
}

// expected is one response of a fixture, rebuilt from its recorded events.
type expected struct {
	chunk  string
	pcm    []byte
	said   string
	status realtime.ResponseStatus
	usage  realtime.TokenUsage
}

func expectedTakes(t *testing.T, sc fake.Scenario) []expected {
	t.Helper()
	var out []expected
	var cur expected
	for _, st := range sc.Steps {
		if st.Dir != "server" {
			continue
		}
		var ev struct {
			Delta    string `json:"delta"`
			Response struct {
				Status   string            `json:"status"`
				Metadata map[string]string `json:"metadata"`
				Usage    struct {
					InputTokens       int64 `json:"input_tokens"`
					OutputTokens      int64 `json:"output_tokens"`
					InputTokenDetails *struct {
						TextTokens int64 `json:"text_tokens"`
					} `json:"input_token_details"`
					OutputTokenDetails *struct {
						TextTokens  int64 `json:"text_tokens"`
						AudioTokens int64 `json:"audio_tokens"`
					} `json:"output_token_details"`
				} `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal(st.Event, &ev); err != nil {
			t.Fatal(err)
		}
		switch st.Type {
		case realtime.TypeAudioDelta:
			b, err := base64.StdEncoding.DecodeString(ev.Delta)
			if err != nil {
				t.Fatal(err)
			}
			cur.pcm = append(cur.pcm, b...)
		case realtime.TypeTranscriptDelta:
			cur.said += ev.Delta
		case realtime.TypeResponseDone:
			cur.chunk = ev.Response.Metadata["chunk"]
			cur.status = realtime.ResponseStatus(ev.Response.Status)
			u := ev.Response.Usage
			cur.usage = realtime.TokenUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}
			if u.InputTokenDetails != nil {
				cur.usage.InputTextTokens = u.InputTokenDetails.TextTokens
			}
			if d := u.OutputTokenDetails; d != nil {
				cur.usage.OutputTextTokens, cur.usage.OutputAudioTokens, cur.usage.Detailed = d.TextTokens, d.AudioTokens, true
			}
			out = append(out, cur)
			cur = expected{}
		}
	}
	return out
}

func newSynth(t *testing.T, srv *fake.Server, creds realtime.Credentials) *realtime.Synth {
	t.Helper()
	c, base := client(t, srv.URL)
	return &realtime.Synth{
		Minter:  &realtime.Minter{Client: c, BaseURL: base, Creds: creds},
		Client:  c,
		BaseURL: base,
		Now:     (&stepClock{t: time.Unix(1700000000, 0), step: 10 * time.Millisecond}).Now,
	}
}

func open(t *testing.T, s *realtime.Synth) realtime.SpeechSession {
	t.Helper()
	ss, err := s.Open(context.Background(), speechSpec("minimal", 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	return ss
}

func checkTake(t *testing.T, got realtime.Take, want expected) {
	t.Helper()
	if !bytes.Equal(got.PCM, want.pcm) || len(got.PCM) == 0 {
		t.Errorf("PCM: %d bytes, want the %d bytes of the concatenated deltas", len(got.PCM), len(want.pcm))
	}
	if got.Said != want.said {
		t.Errorf("Said = %q, want %q", got.Said, want.said)
	}
	if got.Usage != want.usage || got.Usage.InputTokens == 0 {
		t.Errorf("Usage = %+v, want %+v", got.Usage, want.usage)
	}
	if got.Status != want.status {
		t.Errorf("Status = %q, want %q", got.Status, want.status)
	}
}

// accept: SPK-01#1
func TestRenderSpeak(t *testing.T) {
	sc := loadScenario(t, "speak")
	srv := fake.NewServer(t, sc)
	ss := open(t, newSynth(t, srv, newStatic()))
	take, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "Hello from recite."})
	if err != nil {
		t.Fatal(err)
	}
	checkTake(t, take, expectedTakes(t, sc)[0])
	// One clock step from start to the first delta, two to response.done.
	if take.FirstAudio != 10*time.Millisecond || take.Wall != 20*time.Millisecond {
		t.Errorf("FirstAudio, Wall = %v, %v; want 10ms, 20ms", take.FirstAudio, take.Wall)
	}
	if age := ss.Age(); age <= 0 {
		t.Errorf("Age = %v", age)
	}
	var sent struct {
		Response struct {
			Conversation string            `json:"conversation"`
			Metadata     map[string]string `json:"metadata"`
			Reasoning    any               `json:"reasoning"`
		} `json:"response"`
	}
	if err := json.Unmarshal(srv.ClientEvents()[0], &sent); err != nil {
		t.Fatal(err)
	}
	r := sent.Response
	if r.Conversation != "none" || r.Metadata["chunk"] != "c0001" || r.Metadata["attempt"] != "1" || r.Reasoning != nil {
		t.Errorf("response.create = %+v", r)
	}
	if errs := srv.Errors(); len(errs) > 0 {
		t.Fatal(errs)
	}
}

// accept: SPK-01#1
func TestRenderThreeOnOneSession(t *testing.T) {
	sc := loadScenario(t, "oob-three")
	srv := fake.NewServer(t, sc)
	s := newSynth(t, srv, newStatic())
	var limits []realtime.RateLimitsUpdated
	s.OnRateLimits = func(e realtime.RateLimitsUpdated) { limits = append(limits, e) }
	ss := open(t, s)
	want := expectedTakes(t, sc)
	if len(want) != 3 {
		t.Fatalf("fixture has %d responses", len(want))
	}
	for i, w := range want {
		take, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: w.chunk, Text: "text", Attempt: 1})
		if err != nil {
			t.Fatalf("chunk %s: %v", w.chunk, err)
		}
		checkTake(t, take, want[i])
	}
	if len(limits) != 2 {
		t.Errorf("rate_limits.updated delivered %d times, want 2", len(limits))
	}
	if n := len(srv.MintRequests()); n != 1 {
		t.Errorf("%d mints, want 1", n)
	}
	if errs := srv.Errors(); len(errs) > 0 {
		t.Fatal(errs)
	}
}

// accept: SPK-01#1
func TestRenderAnswerFixtureCollectsWhatWasSaid(t *testing.T) {
	sc := loadScenario(t, "answer-instead-of-read")
	ss := open(t, newSynth(t, fake.NewServer(t, sc), newStatic()))
	take, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "What is two plus two?"})
	if err != nil {
		t.Fatal(err)
	}
	checkTake(t, take, expectedTakes(t, sc)[0])
}

func TestRenderIncompleteStatus(t *testing.T) {
	sc := loadScenario(t, "incomplete")
	ss := open(t, newSynth(t, fake.NewServer(t, sc), newStatic()))
	take, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "long text"})
	if err != nil {
		t.Fatal(err)
	}
	if take.Status != realtime.StatusIncomplete {
		t.Fatalf("Status = %q, want incomplete", take.Status)
	}
	checkTake(t, take, expectedTakes(t, sc)[0])
}

func TestRenderErrorEventClassified(t *testing.T) {
	ss := open(t, newSynth(t, fake.NewServer(t, loadScenario(t, "error-event")), newStatic()))
	_, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "x"})
	wantClass(t, err, openai.ClassInvalidRequest)
}

// accept: SPK-01#2
func TestRenderMismatchedChunkIsProtocol(t *testing.T) {
	sc, err := fake.ParseScenarioFile(filepath.Join("testdata", "render-mismatch.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	ss := open(t, newSynth(t, fake.NewServer(t, sc), newStatic()))
	_, err = ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "Hello."})
	wantClass(t, err, openai.ClassProtocol)
}

// accept: SPK-01#3
func TestRenderHardCap(t *testing.T) {
	if got := openai.ResponseHardCap(10 * time.Second); got != 60*time.Second {
		t.Fatalf("ResponseHardCap(10s) = %v, want 3 × 10 s + 30 s", got)
	}
	// Every delta is delayed 20 ms, so response.done is at least 200 ms away;
	// the cap of 30 ms always fires first, while the idle limit never does.
	srv := fake.NewServer(t, loadScenario(t, "speak"), fake.WithDeltaDelay(20*time.Millisecond, nil))
	s := newSynth(t, srv, newStatic())
	s.IdleTimeout = time.Hour
	var estimates []time.Duration
	s.HardCap = func(est time.Duration) time.Duration {
		estimates = append(estimates, est)
		return 30 * time.Millisecond
	}
	ss := open(t, s)
	_, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "Hello from recite.", Estimate: 7 * time.Second})
	wantClass(t, err, openai.ClassProtocol)
	if len(estimates) != 1 || estimates[0] != 7*time.Second {
		t.Errorf("HardCap saw %v, want the request's 7s estimate", estimates)
	}
	// The abandoned response may still be streaming: the session refuses more work.
	if _, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0002", Text: "x"}); err == nil {
		t.Fatal("Render after an abandoned response succeeded")
	}
}

func TestRenderIdleTimeoutIsNetwork(t *testing.T) {
	srv := fake.NewServer(t, loadScenario(t, "speak"), fake.WithoutResponseDone())
	s := newSynth(t, srv, newStatic())
	s.IdleTimeout = 50 * time.Millisecond
	ss := open(t, s)
	_, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "Hello from recite."})
	wantClass(t, err, openai.ClassNetwork)
}

func TestRenderCallerCancel(t *testing.T) {
	srv := fake.NewServer(t, loadScenario(t, "speak"), fake.WithoutResponseDone())
	ss := open(t, newSynth(t, srv, newStatic()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ss.Render(ctx, realtime.RenderRequest{ChunkID: "c0001", Text: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestRenderEffortOverrideAndAttempt(t *testing.T) {
	srv := fake.NewServer(t, loadScenario(t, "speak"))
	ss := open(t, newSynth(t, srv, newStatic()))
	// Only metadata.chunk is matched on response.done, so the fixture's attempt 1 does not matter.
	if _, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "x", Effort: "low", Attempt: 2}); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Response struct {
			Metadata  map[string]string `json:"metadata"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		} `json:"response"`
	}
	if err := json.Unmarshal(srv.ClientEvents()[0], &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Response.Metadata["attempt"] != "2" || sent.Response.Reasoning.Effort != "low" {
		t.Errorf("response.create = %+v", sent.Response)
	}
}

// refreshCreds hands out a stale token first and a fresh one on refresh.
type refreshCreds struct{ refreshed int }

var (
	staleTok = auth.NewToken("eyJcanary.stale.canary", time.Unix(1800000000, 0), "", "test")
	freshTok = auth.NewToken("eyJcanary.canary.canary", time.Unix(1800000000, 0), "", "test")
)

func (r *refreshCreds) Token(context.Context) (auth.Token, error) { return staleTok, nil }
func (r *refreshCreds) RefreshAfter(context.Context, auth.Token) (auth.Token, error) {
	r.refreshed++
	return freshTok, nil
}

func TestOpenRefreshesOnceOnAuthExpired(t *testing.T) {
	srv := fake.NewServer(t, loadScenario(t, "speak"), fake.WithMintRejectBearer(staleTok.Bearer()))
	creds := &refreshCreds{}
	ss := open(t, newSynth(t, srv, creds))
	if creds.refreshed != 1 || len(srv.MintRequests()) != 2 {
		t.Fatalf("refreshed %d, mints %d; want 1, 2", creds.refreshed, len(srv.MintRequests()))
	}
	if _, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenMintFailure(t *testing.T) {
	srv := fake.NewServer(t, loadScenario(t, "speak"), fake.WithMintError(400))
	_, err := newSynth(t, srv, newStatic()).Open(context.Background(), speechSpec("minimal", 1))
	wantClass(t, err, openai.ClassInvalidRequest)
	if _, err := (&realtime.Synth{}).Open(context.Background(), speechSpec("minimal", 1)); err == nil {
		t.Fatal("Open without configuration succeeded")
	}
}

func TestEstimateDuration(t *testing.T) {
	if got := realtime.EstimateDuration("abcdefghijklmn", 1); got != time.Second {
		t.Errorf("14 Latin chars = %v, want 1s", got)
	}
	if a, b := realtime.EstimateDuration("你好世界", 1), realtime.EstimateDuration("abcd", 1); a <= 3*b {
		t.Errorf("CJK %v not slower than Latin %v", a, b)
	}
	if a, b := realtime.EstimateDuration("abcdefghijklmn", 1.5), realtime.EstimateDuration("abcdefghijklmn", 1); a >= b {
		t.Errorf("speed 1.5 estimate %v not shorter than %v", a, b)
	}
}

// A connection that drops mid-response fails the render and the session; it is never an empty
// successful take.
func TestRenderDisconnectMidResponseFails(t *testing.T) {
	ss := open(t, newSynth(t, fake.NewServer(t, loadScenario(t, "speak"), fake.WithCloseAfter(2)), newStatic()))
	take, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "Hello from recite."})
	if err == nil {
		t.Fatalf("a dropped connection returned a take of %d bytes and no error", len(take.PCM))
	}
	if _, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "again"}); err == nil {
		t.Fatal("the session stayed usable after its connection dropped")
	}
}

// An error event while the response streams leaves the session unusable, so a retry on it
// cannot read the abandoned response's events.
func TestRenderErrorMidResponseRetiresTheSession(t *testing.T) {
	ss := open(t, newSynth(t, fake.NewServer(t, loadScenario(t, "speak"), fake.WithErrorEvent(3, "rate_limit_exceeded")), newStatic()))
	_, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "Hello from recite."})
	wantClass(t, err, openai.ClassRateLimited)
	if _, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "again"}); err == nil ||
		!strings.Contains(err.Error(), "unusable") {
		t.Fatalf("second render on the same session: %v", err)
	}
}

// An empty audio delta is not audio: a response that completes with only empty deltas is the
// "completed without audio" protocol fault.
func TestRenderEmptyAudioDeltaIsNoAudio(t *testing.T) {
	sc, err := fake.ParseScenarioFile(filepath.Join("testdata", "render-empty-audio.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	ss := open(t, newSynth(t, fake.NewServer(t, sc), newStatic()))
	_, err = ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "Hello."})
	wantClass(t, err, openai.ClassProtocol)
}

// An event of another response inside a render is a protocol fault, not audio of this one.
func TestRenderOtherResponseIsProtocol(t *testing.T) {
	sc, err := fake.ParseScenarioFile(filepath.Join("testdata", "render-other-response.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	ss := open(t, newSynth(t, fake.NewServer(t, sc), newStatic()))
	_, err = ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "Hello."})
	wantClass(t, err, openai.ClassProtocol)
}

// Without response.created, the first event's response id is the render's: a take of audio from
// one response, a transcript from another and response.done from a third is a protocol fault.
func TestRenderMixedResponsesWithoutCreatedIsProtocol(t *testing.T) {
	sc, err := fake.ParseScenarioFile(filepath.Join("testdata", "render-mixed-without-created.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	ss := open(t, newSynth(t, fake.NewServer(t, sc), newStatic()))
	_, err = ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "Hello."})
	wantClass(t, err, openai.ClassProtocol)
	if u, ok := ss.(realtime.Unusable); !ok || !u.Unusable() {
		t.Fatal("a session that mixed responses was not reported unusable")
	}
}
