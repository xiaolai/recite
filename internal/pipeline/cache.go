package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/xiaolai/recite/internal/cache"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/verify"
)

// TakeCache is the take cache of a run (RUN-04). A nil *TakeCache in Config is --no-cache.
type TakeCache struct {
	Store           *cache.Store
	Refresh         bool // --refresh: render anew, but still write the takes
	AllowUnverified bool // --allow-unverified: an unverified cached take is kept as is
	// CanonVersion and GuardVersion are the versions verification runs under; 0 means
	// verify.CanonVersion and verify.GuardVersion. Tests set them to simulate a bump.
	CanonVersion, GuardVersion int
}

func (tc *TakeCache) canon() int {
	if tc.CanonVersion != 0 {
		return tc.CanonVersion
	}
	return verify.CanonVersion
}

func (tc *TakeCache) guard() int {
	if tc.GuardVersion != 0 {
		return tc.GuardVersion
	}
	return verify.GuardVersion
}

// Router is implemented by a Transcriber that knows its verifier route and STT model
// (stt.Chain); cached verification records them.
type Router interface {
	Route() (route, model string)
}

// Guard names of the verification record (design §11.1), as verify names their rejections.
var (
	preGuards  = []verify.Reason{verify.ReasonStatus, verify.ReasonEmpty, verify.ReasonSaid, verify.ReasonDuration}
	postGuards = []verify.Reason{verify.ReasonLanguage, verify.ReasonProtected, verify.ReasonEdgeInsertion, verify.ReasonContentWord}
)

func hashOf(parts ...any) string {
	b, err := json.Marshal(parts)
	if err != nil {
		panic("pipeline: guard context encoding: " + err.Error()) // strings and numbers only
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// guardContexts hashes the inputs each guard judges for task t: the spoken form for said,
// edge-insertion, content-word and protected-token (with the lexicon keys and protected spans),
// the expected language and its certainty for the language guard, the estimate and speed for
// the duration guard.
func guardContexts(t Task) map[string]string {
	c := t.Chunk
	lex := slices.Clone(c.Lexicon)
	slices.Sort(lex)
	spans := make([][2]int, len(c.Protected))
	for i, r := range c.Protected {
		spans[i] = [2]int{r.Start, r.End}
	}
	return map[string]string{
		string(verify.ReasonStatus):        hashOf("status"),
		string(verify.ReasonEmpty):         hashOf("empty", verify.MinTakeDuration.Nanoseconds()),
		string(verify.ReasonSaid):          hashOf("said", c.Spoken),
		string(verify.ReasonDuration):      hashOf("duration", c.Estimate.Nanoseconds(), strconv.FormatFloat(t.Spec.Speed, 'g', -1, 64)),
		string(verify.ReasonLanguage):      hashOf("language", c.Language.Code, c.Language.Certain),
		string(verify.ReasonProtected):     hashOf("protected", c.Spoken, lex, spans),
		string(verify.ReasonEdgeInsertion): hashOf("edge", c.Spoken),
		string(verify.ReasonContentWord):   hashOf("content", c.Spoken, c.Language.Code),
	}
}

// requiredGuards are the guards required now, with their context hashes: every guard, except
// the language guard when the chunk's language is not certain.
func requiredGuards(t Task) map[string]string {
	ctx := guardContexts(t)
	if !t.Chunk.Language.Certain || t.Chunk.Language.Code == "" {
		delete(ctx, string(verify.ReasonLanguage))
	}
	return ctx
}

func (s *sched) route() (string, string) {
	if r, ok := s.cfg.Transcriber.(Router); ok {
		return r.Route()
	}
	return "", ""
}

// lookup consults the cache for chunk i. It returns a finished result when the stored take is
// reused, or the stored take to re-verify in full; hit is false on a miss.
func (s *sched) lookup(i int) (take realtime.Take, reuse *verify.ChunkResult, hit bool) {
	tc, t := s.cfg.Cache, s.tasks[i]
	if tc == nil || tc.Refresh || t.CacheKey == "" || t.Render != "" {
		return realtime.Take{}, nil, false
	}
	e, pcm, ok, err := tc.Store.Get(t.CacheKey)
	if err != nil {
		s.logger.Warn("cache entry ignored", "chunk", t.Chunk.ID, "err", err)
	}
	if !ok {
		return realtime.Take{}, nil, false
	}
	take = realtime.Take{PCM: pcm, Said: e.Said, Status: realtime.ResponseStatus(e.Status)}
	state := verify.StateVerified
	if s.cfg.Verify.Mode == verify.ModeOff {
		state = verify.StateUnverified // nothing verifies under off; the audio is still valid
	} else {
		route, model := s.route()
		d := e.Verification.Decide(cache.Current{
			Accept: s.cfg.Verify.Mode.Thresholds().Accept, Route: route, STTModel: model,
			CanonVersion: tc.canon(), GuardVersion: tc.guard(),
			Required: requiredGuards(t), AllowUnverified: tc.AllowUnverified,
		})
		switch d {
		case cache.ReuseUnverified:
			state = verify.StateUnverified
		case cache.Reverify:
			s.logger.Debug("cache: re-verifying", "chunk", t.Chunk.ID)
			return take, nil, true
		}
	}
	// The pre-STT guards cost nothing: they re-run on every reuse.
	pcmT, rej := verify.PreSTT(t.Chunk, take, s.cfg.Verify.NoTrim)
	if rej != nil {
		return take, nil, true
	}
	a := verify.Attempt{N: 1, Status: take.Status, Eligible: true, Score: e.Score, PCM: pcmT}
	r := verify.ChunkResult{ChunkID: t.Chunk.ID, State: state, Attempts: []verify.Attempt{a}, Final: 1, PCM: pcmT, Score: e.Score, Cached: true}
	s.logger.Debug("cache: reused", "chunk", t.Chunk.ID, "state", state)
	return take, &r, true
}

// store writes the kept take of a finished chunk with its verification record. A failure is
// logged: the cache only saves work, it never decides a result.
func (s *sched) store(i int, r verify.ChunkResult, take realtime.Take) {
	tc, t := s.cfg.Cache, s.tasks[i]
	if tc == nil || t.CacheKey == "" || t.Render != "" || s.cfg.Verify.BypassSaid ||
		(r.State != verify.StateVerified && r.State != verify.StateUnverified) {
		return
	}
	a := r.Attempts[r.Final-1]
	route, model := s.route()
	v := cache.Verification{
		Verdict: cache.VerdictUnverified, Guards: guardVerdicts(t, a), Score: a.Score,
		Route: route, STTModel: model, CanonVersion: tc.canon(), GuardVersion: tc.guard(),
		Mode: string(s.cfg.Verify.Mode), AcceptThreshold: s.cfg.Verify.Mode.Thresholds().Accept,
	}
	if r.State == verify.StateVerified {
		v.Verdict = cache.VerdictAccepted
	}
	var heard string
	if n := len(a.Transcriptions); n > 0 {
		heard = a.Transcriptions[n-1].Text
	}
	e := cache.Entry{Key: t.CacheKey, ChunkID: t.Chunk.ID, Said: take.Said, Status: string(take.Status),
		Transcript: heard, Score: a.Score, Attempts: len(r.Attempts), Verification: v}
	if err := tc.Store.Put(e, take.PCM); err != nil {
		s.logger.Warn("cache write failed", "chunk", t.Chunk.ID, "err", err)
	}
}

// guardVerdicts records what each guard concluded about attempt a. A guard records pass only
// when it actually judged the take and passed it; a guard not reached is left out.
func guardVerdicts(t Task, a verify.Attempt) map[string]cache.GuardVerdict {
	ctx := guardContexts(t)
	out := map[string]cache.GuardVerdict{}
	set := func(g verify.Reason, verdict string) {
		out[string(g)] = cache.GuardVerdict{Verdict: verdict, ContextHash: ctx[string(g)]}
	}
	var reason verify.Reason
	if a.Rejection != nil {
		reason = a.Rejection.Reason
	}
	if !a.Eligible {
		set(reason, cache.GuardFail)
		return out
	}
	for _, g := range preGuards {
		set(g, cache.GuardPass)
	}
	if a.SaidMismatch {
		set(verify.ReasonSaid, cache.GuardFail)
	}
	if len(a.Transcriptions) == 0 {
		for _, g := range postGuards {
			set(g, cache.GuardSkipped)
		}
		return out
	}
	if slices.Contains(postGuards, reason) {
		set(reason, cache.GuardFail)
		return out
	}
	for _, g := range postGuards[1:] {
		set(g, cache.GuardPass)
	}
	// The language guard judges only a certain language, and only a transcript that reports one.
	judged := t.Chunk.Language.Certain && t.Chunk.Language.Code != ""
	for _, tr := range a.Transcriptions {
		judged = judged && tr.Language != ""
	}
	if judged {
		set(verify.ReasonLanguage, cache.GuardPass)
	} else {
		set(verify.ReasonLanguage, cache.GuardSkipped)
	}
	return out
}
