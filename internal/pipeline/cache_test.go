package pipeline_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/cache"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/text"
	"github.com/xiaolai/recite/internal/verify"
)

var cacheNow = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

var baseSettings = pipeline.Settings{Model: "gpt-realtime-2.1", Voice: "marin", Speed: 1, Effort: "low", InstructionsVersion: 1}

// keyed sets each task's cache key under settings s.
func keyed(ts []pipeline.Task, s pipeline.Settings) []pipeline.Task {
	out := make([]pipeline.Task, len(ts))
	for i, t := range ts {
		t.CacheKey = pipeline.CacheKey(s, nil, t.Chunk.Spoken)
		out[i] = t
	}
	return out
}

func newStore(t testing.TB) *cache.Store {
	t.Helper()
	st, err := cache.Open(t.TempDir(), func() time.Time { return cacheNow })
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// hearing transcribes a tagged take as its chunk's text, or as an override, reporting the
// given language; it counts calls per chunk and reports a configurable verifier route.
type hearing struct {
	mu       sync.Mutex
	texts    map[string]string
	override map[string]string
	lang     string
	calls    map[string]int
	route    string
}

func newHearing(ts []pipeline.Task) *hearing {
	h := &hearing{texts: map[string]string{}, override: map[string]string{}, calls: map[string]int{}, route: "http"}
	h.set(ts)
	return h
}

func (h *hearing) set(ts []pipeline.Task) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, t := range ts {
		h.texts[t.Chunk.ID] = t.Chunk.Spoken
	}
}

func (h *hearing) Transcribe(ctx context.Context, pcm []byte, _ stt.Hint) (stt.Transcript, error) {
	if err := ctx.Err(); err != nil {
		return stt.Transcript{}, err
	}
	id, _, ok := rtfake.PCMTag(pcm)
	if !ok {
		return stt.Transcript{}, errors.New("hearing: untagged take")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls[id]++
	s := h.texts[id]
	if o, ok := h.override[id]; ok {
		s = o
	}
	langs := []stt.DetectedLanguage{}
	if h.lang != "" {
		langs = append(langs, stt.DetectedLanguage{Code: h.lang})
	}
	return stt.Transcript{Text: s, Languages: langs}, nil
}

func (h *hearing) Route() (string, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.route, "model-" + h.route
}

func (h *hearing) total() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, c := range h.calls {
		n += c
	}
	return n
}

// cached is one scheduler run with a take cache; out is the assembly of the delivered takes.
type cached struct {
	res     pipeline.Result
	err     error
	code    int
	renders int // synthesizer calls made by this run
	stt     int // transcriptions made by this run
	out     []byte
	takes   [][]byte // the delivered PCM per chunk
}

func runCache(t *testing.T, ts []pipeline.Task, synth *rtfake.ScriptedSynthesizer, h *hearing, tc *pipeline.TakeCache, mut func(*pipeline.Config)) cached {
	t.Helper()
	before, sttBefore := len(synth.Calls()), h.total()
	gaps := make([]int, len(ts))
	for i := range gaps {
		gaps[i] = 240
	}
	var buf bytes.Buffer
	asm := audio.NewAssembler(&buf, gaps)
	c := cached{takes: make([][]byte, len(ts))}
	cfg := pipeline.Config{
		Jobs: 2, Verify: verify.DefaultOptions(), Synth: synth, Transcriber: h,
		Retry: openai.NewRetrier(1, noSleep), Cache: tc,
		Deliver: func(i int, r verify.ChunkResult) error {
			c.takes[i] = append([]byte(nil), r.PCM...)
			return asm.Add(i, r.PCM)
		},
	}
	if mut != nil {
		mut(&cfg)
	}
	c.res, c.err = pipeline.Run(context.Background(), cfg, ts)
	c.code, _ = pipeline.ExitCode(c.res, c.err, verify.OutputPolicy{})
	if c.err == nil {
		if err := asm.Close(); err != nil {
			t.Fatal(err)
		}
	}
	c.out = buf.Bytes()
	c.renders, c.stt = len(synth.Calls())-before, h.total()-sttBefore
	return c
}

func stateOf(r pipeline.Result, id string) verify.State {
	for _, c := range r.Chunks {
		if c.ChunkID == id {
			return c.State
		}
	}
	return ""
}

// accept: RUN-04#1
func TestCacheSecondIdenticalRunMakesNoSynthCalls(t *testing.T) {
	defer goleak.VerifyNone(t)
	ts := keyed(tasks(12), baseSettings)
	st := newStore(t)
	synth, h := rtfake.NewScriptedSynthesizer(), newHearing(ts)
	first := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil)
	if first.code != exitcode.OK || first.renders != 12 {
		t.Fatalf("first run: exit %d, %d renders (%v)", first.code, first.renders, first.err)
	}
	second := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil)
	if second.code != exitcode.OK || second.renders != 0 || second.stt != 0 {
		t.Fatalf("second run: exit %d, %d renders, %d transcriptions; want 0 and 0", second.code, second.renders, second.stt)
	}
	for _, r := range first.res.Chunks {
		if r.Cached {
			t.Fatalf("%s marked cached on the first run", r.ChunkID)
		}
	}
	for _, r := range second.res.Chunks {
		if r.State != verify.StateVerified || !r.Cached {
			t.Fatalf("%s reused as %s, cached %v", r.ChunkID, r.State, r.Cached)
		}
	}
	if !bytes.Equal(first.out, second.out) {
		t.Fatal("the reused output differs from the rendered one")
	}
	// --refresh renders anew but still writes; --no-cache (nil) neither reads nor writes.
	if r := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st, Refresh: true}, nil); r.renders != 12 {
		t.Fatalf("--refresh: %d renders, want 12", r.renders)
	}
	if r := runCache(t, ts, synth, h, nil, nil); r.renders != 12 {
		t.Fatalf("--no-cache: %d renders, want 12", r.renders)
	}
}

// The 3 s target for 100 cached chunks is a benchmark, not an assertion.
// accept: RUN-04#1
func BenchmarkCacheReuse100Chunks(b *testing.B) {
	ts := keyed(tasks(100), baseSettings)
	st, err := cache.Open(b.TempDir(), nil)
	if err != nil {
		b.Fatal(err)
	}
	cfg := func(synth realtime.Synthesizer) pipeline.Config {
		return pipeline.Config{Verify: verify.DefaultOptions(), Synth: synth, Transcriber: newEcho(ts),
			Retry: openai.NewRetrier(1, noSleep), Cache: &pipeline.TakeCache{Store: st},
			Deliver: func(int, verify.ChunkResult) error { return nil }}
	}
	if _, err := pipeline.Run(context.Background(), cfg(rtfake.NewScriptedSynthesizer()), ts); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		synth := rtfake.NewScriptedSynthesizer()
		if _, err := pipeline.Run(context.Background(), cfg(synth), ts); err != nil {
			b.Fatal(err)
		}
		if n := len(synth.Calls()); n != 0 {
			b.Fatalf("%d renders from a warm cache", n)
		}
	}
}

// accept: RUN-04#2
func TestCacheOneChangedChunkRerendersOnlyIt(t *testing.T) {
	ts := keyed(tasks(6), baseSettings)
	st := newStore(t)
	synth, h := rtfake.NewScriptedSynthesizer(), newHearing(ts)
	if r := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil); r.code != exitcode.OK {
		t.Fatalf("first run: %v", r.err)
	}
	edited := tasks(6)
	edited[3].Chunk.Spoken = "An entirely rewritten fourth paragraph says something else now."
	edited = keyed(edited, baseSettings)
	h.set(edited)
	before := len(synth.Calls())
	r := runCache(t, edited, synth, h, &pipeline.TakeCache{Store: st}, nil)
	if r.code != exitcode.OK || r.renders != 1 {
		t.Fatalf("exit %d, %d renders; want only the changed chunk", r.code, r.renders)
	}
	if got := synth.Calls()[before].Request.ChunkID; got != "c0004" {
		t.Fatalf("re-rendered %s, want c0004", got)
	}
}

// accept: RUN-04#3
func TestCacheRenderSettingsRerenderEverything(t *testing.T) {
	ts := tasks(4)
	st := newStore(t)
	synth, h := rtfake.NewScriptedSynthesizer(), newHearing(ts)
	if r := runCache(t, keyed(ts, baseSettings), synth, h, &pipeline.TakeCache{Store: st}, nil); r.renders != 4 {
		t.Fatalf("first run: %d renders", r.renders)
	}
	variants := map[string]func(*pipeline.Settings){
		"voice":    func(s *pipeline.Settings) { s.Voice = "cedar" },
		"speed":    func(s *pipeline.Settings) { s.Speed = 1.1 },
		"effort":   func(s *pipeline.Settings) { s.Effort = "high" },
		"model":    func(s *pipeline.Settings) { s.Model = "gpt-realtime-mini" },
		"emotion":  func(s *pipeline.Settings) { s.Emotion = "calm" },
		"style":    func(s *pipeline.Settings) { s.Style = "whisper" },
		"instrver": func(s *pipeline.Settings) { s.InstructionsVersion = 2 },
	}
	for _, name := range []string{"voice", "speed", "effort", "model", "emotion", "style", "instrver"} {
		s := baseSettings
		variants[name](&s)
		if r := runCache(t, keyed(ts, s), synth, h, &pipeline.TakeCache{Store: st}, nil); r.renders != 4 {
			t.Errorf("%s changed: %d renders, want all 4", name, r.renders)
		}
	}
	// The unchanged settings still hit.
	if r := runCache(t, keyed(ts, baseSettings), synth, h, &pipeline.TakeCache{Store: st}, nil); r.renders != 0 {
		t.Errorf("unchanged settings: %d renders", r.renders)
	}
}

// grayText is a chunk whose transcript, missing one function word, scores between the normal
// (0.95) and strict (0.98) accept thresholds.
const grayText = "we walked along the quiet road and then we sat down by the small river for a while to rest before we went on to the old town at the end of the day"

// accept: RUN-04#4
func TestCacheNormalTakeNotReusedAsVerifiedUnderStrict(t *testing.T) {
	ts := tasks(1)
	ts[0].Chunk.Spoken = grayText
	ts[0].Chunk.Language = text.Language{Code: "en"} // "the" is an English function word
	ts = keyed(ts, baseSettings)
	st := newStore(t)
	synth, h := rtfake.NewScriptedSynthesizer(), newHearing(ts)
	h.override["c0001"] = strings.Replace(grayText, "by the small", "by small", 1)
	first := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil)
	if stateOf(first.res, "c0001") != verify.StateVerified {
		t.Fatalf("normal: %+v", first.res.Chunks)
	}
	e, _, ok, err := st.Get(ts[0].CacheKey)
	if !ok || err != nil || e.Score < 0.95 || e.Score >= 0.98 || e.Verification.Mode != "normal" {
		t.Fatalf("stored entry %+v (%v); want a normal take scored in [0.95, 0.98)", e.Verification, err)
	}
	strict := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, func(c *pipeline.Config) { c.Verify.Mode = verify.ModeStrict })
	if strict.stt == 0 {
		t.Fatal("the normal take was reused under strict without verification")
	}
	if a := strict.res.Chunks[0].Attempts[0]; a.Rejection == nil || a.Rejection.Reason != verify.ReasonScore {
		t.Fatalf("re-verification of the cached take: %+v, want a score rejection", a.Rejection)
	}
	if strict.renders == 0 {
		t.Fatal("the rejected cached take was not re-rendered")
	}
}

// protText scores about 0.983 when one number is misheard, which the protected-token guard
// rejects.
const protText = "the meeting of the committee on the future of the city park will take place in the main hall of the library on the morning of day 42 and every member of the public is welcome to attend and to speak about the plans for the new playground and the garden"

// accept: RUN-04#5
func TestCacheProtectedFailureNeverReusedAsVerified(t *testing.T) {
	ts := tasks(1)
	ts[0].Chunk.Spoken = protText
	ts = keyed(ts, baseSettings)
	st := newStore(t)
	synth, h := rtfake.NewScriptedSynthesizer(), newHearing(ts)
	h.override["c0001"] = strings.Replace(protText, "42", "43", 1)
	first := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil)
	if stateOf(first.res, "c0001") != verify.StateUnverified {
		t.Fatalf("first run: %+v", first.res.Chunks)
	}
	e, _, ok, _ := st.Get(ts[0].CacheKey)
	if !ok || e.Verification.Verdict != cache.VerdictUnverified || e.Verification.Guards["protected-token"].Verdict != cache.GuardFail || e.Score < 0.98 {
		t.Fatalf("stored %+v score %.4f", e.Verification, e.Score)
	}
	for _, mode := range []verify.Mode{verify.ModeNormal, verify.ModeStrict} {
		r := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, func(c *pipeline.Config) { c.Verify.Mode = mode })
		if r.stt == 0 || stateOf(r.res, "c0001") == verify.StateVerified {
			t.Fatalf("%s: %d transcriptions, state %s; the take was reused as verified", mode, r.stt, stateOf(r.res, "c0001"))
		}
	}
	// Even a decision with a lower threshold and every guard hash matching never promotes it.
	for _, accept := range []float64{0, 0.95, 0.98} {
		if d := e.Verification.Decide(cache.Current{Accept: accept, CanonVersion: e.Verification.CanonVersion, GuardVersion: e.Verification.GuardVersion,
			Route: e.Verification.Route, STTModel: e.Verification.STTModel}); d == cache.ReuseVerified {
			t.Fatalf("accept %.2f: unverified take promoted", accept)
		}
	}
}

// accept: RUN-04#6
func TestCacheCanonBumpOrRouteSwitchReverifiesWithoutRendering(t *testing.T) {
	ts := keyed(tasks(5), baseSettings)
	st := newStore(t)
	synth, h := rtfake.NewScriptedSynthesizer(), newHearing(ts)
	if r := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st, CanonVersion: verify.CanonVersion - 1}, nil); r.renders != 5 {
		t.Fatalf("first run: %d renders", r.renders)
	}
	bump := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil)
	if bump.code != exitcode.OK || bump.renders != 0 || bump.stt != 5 {
		t.Fatalf("canonicaliser bump: exit %d, %d renders, %d transcriptions; want 0 and 5", bump.code, bump.renders, bump.stt)
	}
	if r := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil); r.stt != 0 || r.renders != 0 {
		t.Fatalf("after re-verification: %d renders, %d transcriptions; the new provenance was not stored", r.renders, r.stt)
	}
	h.route = "realtime"
	sw := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil)
	if sw.code != exitcode.OK || sw.renders != 0 || sw.stt != 5 {
		t.Fatalf("route switch: exit %d, %d renders, %d transcriptions; want 0 and 5", sw.code, sw.renders, sw.stt)
	}
}

// accept: RUN-04#7
func TestCacheUncertainLanguageThenLangFrReverifiesAndRejects(t *testing.T) {
	ts := tasks(1)
	ts[0].Chunk.Language = text.Language{Code: "en", Certain: false}
	ts = keyed(ts, baseSettings)
	st := newStore(t)
	synth, h := rtfake.NewScriptedSynthesizer(), newHearing(ts)
	h.lang = "en"
	if r := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil); stateOf(r.res, "c0001") != verify.StateVerified {
		t.Fatalf("first run: %+v", r.res.Chunks)
	}
	if e, _, _, _ := st.Get(ts[0].CacheKey); e.Verification.Guards["language"].Verdict != cache.GuardSkipped {
		t.Fatalf("language verdict %+v, want skipped", e.Verification.Guards["language"])
	}
	fr := append([]pipeline.Task(nil), ts...)
	fr[0].Chunk.Language = text.Language{Code: "fr", Certain: true}
	r := runCache(t, fr, synth, h, &pipeline.TakeCache{Store: st}, func(c *pipeline.Config) { c.Verify.MaxAttempts = 1 })
	if r.stt == 0 {
		t.Fatal("the take was reused without the newly required language guard")
	}
	if a := r.res.Chunks[0].Attempts[0]; a.Rejection == nil || a.Rejection.Reason != verify.ReasonLanguage || r.renders != 0 {
		t.Fatalf("attempt %+v, %d renders; want the cached take rejected by the language guard", a.Rejection, r.renders)
	}
}

// accept: RUN-04#8
func TestCacheChatEnThenFrContextHashDiffers(t *testing.T) {
	ts := tasks(1)
	ts[0].Chunk.Spoken = "Chat"
	ts[0].Chunk.Estimate = 400 * time.Millisecond
	ts[0].Chunk.Language = text.Language{Code: "en", Certain: true}
	ts = keyed(ts, baseSettings)
	st := newStore(t)
	synth, h := rtfake.NewScriptedSynthesizer(), newHearing(ts)
	synth.Default = rtfake.Pass(400 * time.Millisecond)
	h.lang = "en"
	if r := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil); stateOf(r.res, "c0001") != verify.StateVerified {
		t.Fatalf("first run: %+v", r.res.Chunks)
	}
	e, _, _, _ := st.Get(ts[0].CacheKey)
	if e.Score != 1 || e.Verification.Guards["language"].Verdict != cache.GuardPass {
		t.Fatalf("stored score %v, language %+v", e.Score, e.Verification.Guards["language"])
	}
	fr := append([]pipeline.Task(nil), ts...)
	fr[0].Chunk.Language = text.Language{Code: "fr", Certain: true}
	if fr[0].CacheKey != pipeline.CacheKey(baseSettings, nil, "Chat") {
		t.Fatal("the audio key changed")
	}
	r := runCache(t, fr, synth, h, &pipeline.TakeCache{Store: st}, func(c *pipeline.Config) { c.Verify.MaxAttempts = 1 })
	if r.stt == 0 {
		t.Fatal("the take was reused although the language guard judged other inputs")
	}
	if a := r.res.Chunks[0].Attempts[0]; a.Rejection == nil || a.Rejection.Reason != verify.ReasonLanguage {
		t.Fatalf("attempt %+v, want a language rejection", a.Rejection)
	}
}

// accept: RUN-04#9
func TestCacheGuardVersionChangeDurationFailRerenders(t *testing.T) {
	ts := keyed(tasks(1), baseSettings)
	st := newStore(t)
	// A take an older guard set accepted, which the current duration guard fails: a third of
	// the estimate.
	pcm := rtfake.TaggedPCM("c0001", 7, 300*time.Millisecond)
	e := cache.Entry{Key: ts[0].CacheKey, ChunkID: "c0001", Said: ts[0].Chunk.Spoken, Status: string(realtime.StatusCompleted), Score: 1,
		Verification: cache.Verification{Verdict: cache.VerdictAccepted, Score: 1, Route: "http", STTModel: "model-http",
			CanonVersion: verify.CanonVersion, GuardVersion: verify.GuardVersion - 1, Mode: "normal", AcceptThreshold: 0.95}}
	if err := st.Put(e, pcm); err != nil {
		t.Fatal(err)
	}
	synth, h := rtfake.NewScriptedSynthesizer(), newHearing(ts)
	r := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil)
	a := r.res.Chunks[0].Attempts
	if len(a) < 2 || a[0].Rejection == nil || a[0].Rejection.Reason != verify.ReasonDuration || r.renders != 1 || stateOf(r.res, "c0001") != verify.StateVerified {
		t.Fatalf("attempts %+v, %d renders; want the cached take rejected for duration, then one render", a, r.renders)
	}
}

// accept: RUN-04#10
func TestCacheGapChangesRenderAndVerifyNothing(t *testing.T) {
	ts := keyed(tasks(4), baseSettings)
	st := newStore(t)
	synth, h := rtfake.NewScriptedSynthesizer(), newHearing(ts)
	first := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil)
	// Gaps are applied at assembly; the plan's keys do not depend on them (plan_test.go), so a
	// gap change reaches the scheduler as the same tasks.
	second := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil)
	if second.renders != 0 || second.stt != 0 {
		t.Fatalf("%d renders, %d transcriptions after a gap change", second.renders, second.stt)
	}
	for i := range ts {
		if !bytes.Equal(first.takes[i], second.takes[i]) {
			t.Fatalf("chunk %d audio changed", i)
		}
	}
}

// accept: RUN-05#1
func TestCacheInterruptAndResume(t *testing.T) {
	defer goleak.VerifyNone(t)
	const n = 20
	ts := keyed(tasks(n), baseSettings)
	st := newStore(t)
	synth, h := rtfake.NewScriptedSynthesizer(), newHearing(ts)
	stop := make(chan struct{})
	var once sync.Once
	synth.Hook = func(_ context.Context, c rtfake.RenderCall) error {
		if c.Request.ChunkID == chunkID(n/2) {
			once.Do(func() { close(stop) })
		}
		return nil
	}
	first := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, func(c *pipeline.Config) {
		c.Jobs = 1
		c.Stop = stop
		c.After = func(time.Duration) <-chan time.Time { return make(chan time.Time) } // the drain never times out
	})
	if first.code != exitcode.Interrupted {
		t.Fatalf("first run: exit %d (%v), want 130", first.code, first.err)
	}
	verified := map[int][]byte{}
	for i, p := range first.takes {
		if p != nil {
			verified[i] = p
		}
	}
	cachedN := 0
	for _, tk := range ts {
		if _, _, ok, _ := st.Get(tk.CacheKey); ok {
			cachedN++
		}
	}
	if len(verified) == 0 || len(verified) >= n || cachedN < len(verified) {
		t.Fatalf("%d chunks delivered and %d cached before the interrupt", len(verified), cachedN)
	}
	synth.Hook = nil
	second := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil)
	if second.code != exitcode.OK {
		t.Fatalf("resume: exit %d (%v)", second.code, second.err)
	}
	// Every chunk cached before the interrupt is reused; the in-flight ones may be either.
	if second.renders != n-cachedN {
		t.Fatalf("resume rendered %d chunks; %d of %d were cached", second.renders, cachedN, n)
	}
	for i, p := range verified {
		if !bytes.Equal(second.takes[i], p) {
			t.Fatalf("chunk %d was verified before the interrupt but not reused byte for byte", i)
		}
	}
	// The output holds the takes actually used, in chunk order.
	for i, p := range second.takes {
		if id, _, ok := rtfake.PCMTag(p); !ok || id != chunkID(i) {
			t.Fatalf("position %d holds %q", i, id)
		}
	}
	third := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil)
	if third.renders != 0 || !bytes.Equal(third.out, second.out) {
		t.Fatalf("an unchanged re-run rendered %d chunks; identical output: %v", third.renders, bytes.Equal(third.out, second.out))
	}
}

// Under --allow-unverified an unverified cached take is kept as is; otherwise it is
// re-verified (design §11.1).
func TestCacheUnverifiedTakeReattemptedUnlessAllowed(t *testing.T) {
	ts := tasks(1)
	ts[0].Chunk.Spoken = protText
	ts = keyed(ts, baseSettings)
	st := newStore(t)
	synth, h := rtfake.NewScriptedSynthesizer(), newHearing(ts)
	h.override["c0001"] = strings.Replace(protText, "42", "43", 1)
	runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil)
	r := runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st, AllowUnverified: true}, nil)
	if r.renders != 0 || r.stt != 0 || stateOf(r.res, "c0001") != verify.StateUnverified {
		t.Fatalf("allow-unverified: %d renders, %d transcriptions, %s", r.renders, r.stt, stateOf(r.res, "c0001"))
	}
	delete(h.override, "c0001")
	r = runCache(t, ts, synth, h, &pipeline.TakeCache{Store: st}, nil)
	if r.stt == 0 {
		t.Fatal("the unverified take was not re-attempted")
	}
}
