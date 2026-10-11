package verify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/text"
)

// Mode is the --verify setting (VER-09).
type Mode string

// Verify modes (design §8.4).
const (
	ModeNormal Mode = "normal"
	ModeStrict Mode = "strict"
	ModeOff    Mode = "off" // guards only; every chunk is unverified
)

// ParseMode parses --verify.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeNormal, ModeStrict, ModeOff:
		return m, nil
	}
	return "", fmt.Errorf("--verify %q: want strict, normal or off", s)
}

// Thresholds of the score (design §8.4, Q3): accept at or above Accept, reject below Reject,
// gray zone between.
type Thresholds struct{ Accept, Reject float64 }

// Thresholds returns the mode's thresholds.
func (m Mode) Thresholds() Thresholds {
	if m == ModeStrict {
		return Thresholds{Accept: 0.98, Reject: 0.85}
	}
	return Thresholds{Accept: 0.95, Reject: 0.85}
}

// Defaults of the attempt limits (design §8.5, TXT-14).
const (
	DefaultMaxAttempts          = 3
	DefaultRequestExtraAttempts = 1
)

// Chunk is what adjudication needs to know about one chunk.
type Chunk struct {
	ID          string
	Spoken      string        // the spoken form: what is rendered, and the reference
	Language    text.Language // a certain language sends the STT hint and arms the language guard
	Estimate    time.Duration // estimated speech duration at the session speed; must be > 0
	RequestLike bool          // flagged G-REQUEST: one extra attempt
	Lexicon     []string
	Protected   []spoken.Range // {{spell}}, {{digits}} and {{say}} spans, in Spoken coordinates
}

// Options are the adjudication settings.
type Options struct {
	Mode                 Mode
	MaxAttempts          int // renders per chunk, ≥ 1
	RequestExtraAttempts int // extra renders for a request-like chunk, ≥ 0
	NoTrim               bool
	// BypassSaid skips the said guard so every take reaches STT. Only the calibration harness
	// sets it (VER-07): a corrupted take would otherwise be caught by its own transcript and
	// the post-render verifier would never be measured. The CLI never sets it.
	BypassSaid bool
}

// DefaultOptions are the CLI defaults.
func DefaultOptions() Options {
	return Options{Mode: ModeNormal, MaxAttempts: DefaultMaxAttempts, RequestExtraAttempts: DefaultRequestExtraAttempts}
}

// Render produces one take of the chunk; attempt is 1-based.
type Render func(ctx context.Context, attempt int) (realtime.Take, error)

// State is a chunk's final verdict.
type State string

// Final states (VER-08).
const (
	StateVerified   State = "verified"
	StateUnverified State = "unverified"  // the best eligible take is kept
	StateNoEligible State = "no-eligible" // no take passed the pre-STT guards: no output
)

// Transcription is one STT pass over a take.
type Transcription struct {
	Text       string
	Language   string // first detected language, "" when none was reported
	Result     Result
	Confidence []SpanConfidence // per mismatched heard span
}

// Attempt records one render and its verdict.
type Attempt struct {
	N              int
	Status         realtime.ResponseStatus
	Eligible       bool       // passed every pre-STT guard
	SaidMismatch   bool       // the said guard would have rejected, but Options.BypassSaid skipped it
	Rejection      *Rejection // nil when accepted
	Transcriptions []Transcription
	Score          float64 // the best transcription's score; 0 when not transcribed
	PCM            audio.PCM
}

// ChunkResult is the run-result record of one chunk (VER-08).
type ChunkResult struct {
	ChunkID  string
	State    State
	Attempts []Attempt
	STTCalls int
	Final    int       // 1-based attempt whose take is kept; 0 for StateNoEligible
	PCM      audio.PCM // the kept take, trimmed; nil for StateNoEligible
	Score    float64
}

// Adjudicate renders and verifies one chunk until a take is accepted or attempts run out
// (design §8, VER-08). A render or transcription error ends adjudication and is returned as
// is, for the caller to classify; a take that fails a guard is re-rendered instead.
func Adjudicate(ctx context.Context, c Chunk, o Options, render Render, tr stt.Transcriber) (ChunkResult, error) {
	res := ChunkResult{ChunkID: c.ID}
	if err := o.validate(c); err != nil {
		return res, err
	}
	for n := 1; ; n++ {
		take, err := render(ctx, n)
		if err != nil {
			return res, fmt.Errorf("chunk %s attempt %d: %w", c.ID, n, err)
		}
		a, calls, err := Check(ctx, c, o, n, take, tr)
		res.STTCalls += calls
		if err != nil {
			return res, err
		}
		if res.Fold(a, o, c) {
			return res, nil
		}
	}
}

// Limit is the number of renders chunk c may have (TXT-14: one extra for a request-like chunk).
func (o Options) Limit(c Chunk) int {
	if c.RequestLike {
		return o.MaxAttempts + o.RequestExtraAttempts
	}
	return o.MaxAttempts
}

// Validate checks the options against chunk c.
func (o Options) Validate(c Chunk) error { return o.validate(c) }

// Check judges take n of chunk c: the pre-STT guards, then transcription unless verification is
// off. It returns the attempt and the number of STT calls made. It is one step of Adjudicate,
// exposed so a scheduler can render and verify on separate workers (design §10).
func Check(ctx context.Context, c Chunk, o Options, n int, take realtime.Take, tr stt.Transcriber) (Attempt, int, error) {
	calls := 0
	a := Attempt{N: n, Status: take.Status}
	pcm, rej := preSTT(c, take, o.NoTrim, o.BypassSaid)
	a.PCM, a.Rejection, a.Eligible = pcm, rej, rej == nil
	a.SaidMismatch = o.BypassSaid && !Equivalent(take.Said, c.Spoken)
	if a.Eligible && o.Mode != ModeOff {
		hint := stt.Hint{Languages: text.STTHint(c.Language), Keywords: Keywords(c.Spoken, c.Lexicon)}
		if err := verifyTake(ctx, c, o.Mode.Thresholds(), hint, tr, &a, &calls); err != nil {
			return a, calls, fmt.Errorf("chunk %s attempt %d: %w", c.ID, n, err)
		}
	}
	return a, calls, nil
}

// Fold adds attempt a to r and reports whether adjudication of chunk c is over; when it is, r
// holds the final verdict.
func (r *ChunkResult) Fold(a Attempt, o Options, c Chunk) bool {
	r.ChunkID = c.ID
	r.Attempts = append(r.Attempts, a)
	if a.Eligible && o.Mode == ModeOff {
		// Nothing can verify the take; the guards passed, so it is kept unverified.
		*r = r.keep(StateUnverified, a.N)
		return true
	}
	if a.Eligible && a.Rejection == nil {
		*r = r.keep(StateVerified, a.N)
		return true
	}
	if len(r.Attempts) < o.Limit(c) {
		return false
	}
	best := 0
	for _, a := range r.Attempts {
		if a.Eligible && (best == 0 || a.Score > r.Attempts[best-1].Score) {
			best = a.N
		}
	}
	if best == 0 {
		r.State = StateNoEligible
		return true
	}
	*r = r.keep(StateUnverified, best)
	return true
}

func (o Options) validate(c Chunk) error {
	if _, err := ParseMode(string(o.Mode)); err != nil {
		return err
	}
	if o.MaxAttempts < 1 || o.RequestExtraAttempts < 0 {
		return fmt.Errorf("verify: attempts %d (+%d): want at least 1 (+0)", o.MaxAttempts, o.RequestExtraAttempts)
	}
	if c.Estimate <= 0 {
		return errors.New("verify: chunk " + c.ID + " has no duration estimate")
	}
	return nil
}

func (r ChunkResult) keep(s State, n int) ChunkResult {
	a := r.Attempts[n-1]
	r.State, r.Final, r.PCM, r.Score = s, n, a.PCM, a.Score
	return r
}

// verifyTake transcribes an eligible take once, and a second time in the gray zone or on a
// content-word mismatch; the higher score counts, and a content-word mismatch heard twice at
// the same position rejects. Every guard applies to every transcription.
func verifyTake(ctx context.Context, c Chunk, th Thresholds, hint stt.Hint, tr stt.Transcriber, a *Attempt, calls *int) error {
	var first []mismatch
	for pass := 1; pass <= 2; pass++ {
		t, err := tr.Transcribe(ctx, a.PCM, hint) // the STT client adds the silence padding (design §8.2)
		*calls++
		if err != nil {
			return fmt.Errorf("transcribe: %w", err)
		}
		j := judge(c, t)
		rec := Transcription{Text: t.Text, Result: j.result, Confidence: confidences(t, j.result.Edits)}
		if len(t.Languages) > 0 {
			rec.Language = t.Languages[0].Code
		}
		a.Transcriptions = append(a.Transcriptions, rec)
		a.Score = max(a.Score, j.result.Score)
		if j.rejection != nil {
			a.Rejection = j.rejection
			return nil
		}
		if pass == 2 {
			if repeated(first, j.content) {
				a.Rejection = &Rejection{ReasonContentWord, "the same content-word mismatch was heard twice"}
				return nil
			}
			break
		}
		if j.result.Score < th.Reject {
			break
		}
		if j.result.Score >= th.Accept && len(j.content) == 0 {
			break
		}
		first = j.content
	}
	if a.Score < th.Accept {
		a.Rejection = &Rejection{ReasonScore, fmt.Sprintf("score %.4f below %.2f", a.Score, th.Accept)}
	}
	return nil
}

// OutputPolicy is --allow-unverified and --strict-output (VER-09).
type OutputPolicy struct {
	AllowUnverified bool
	StrictOutput    bool
}

// Outcome decides the exit code and whether the output file is written (design §8.5):
// a chunk with no eligible take means no file and exit 5 whatever the flags; an unverified
// chunk means exit 5 and a file, unless --allow-unverified (exit 0) or --strict-output (no
// file). With both flags, --strict-output wins: no file is written, so the run cannot succeed.
func Outcome(results []ChunkResult, p OutputPolicy) (code int, write bool) {
	unverified := false
	for _, r := range results {
		switch r.State {
		case StateNoEligible:
			return exitcode.VerifyFailed, false
		case StateUnverified:
			unverified = true
		}
	}
	switch {
	case !unverified:
		return exitcode.OK, true
	case p.StrictOutput:
		return exitcode.VerifyFailed, false
	case p.AllowUnverified:
		return exitcode.OK, true
	}
	return exitcode.VerifyFailed, true
}
