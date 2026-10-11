package audio

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/xiaolai/recite/internal/exitcode"
)

// FadeDuration is the linear fade applied at every chunk edge (design §9).
const FadeDuration = 5 * time.Millisecond

// MaxGap is the largest gap a range may name (design §9.1).
const MaxGap = 10 * time.Second

// GapClass is the kind of silence at a join (design §9.1).
type GapClass string

// Gap classes. GapNone follows the last chunk; GapPause is an exact pause mark.
const (
	GapNone      GapClass = "none"
	GapSentence  GapClass = "sentence"
	GapList      GapClass = "list"
	GapParagraph GapClass = "paragraph"
	GapSection   GapClass = "section"
	GapPause     GapClass = "pause"
)

// GapRange is a fixed gap (Min == Max) or a range drawn uniformly per join.
type GapRange struct{ Min, Max time.Duration }

// GapError is an invalid gap setting: a usage error, exit 2.
type GapError struct{ msg string }

func (e *GapError) Error() string { return e.msg }

// ExitCode is the CLI exit code of an invalid gap setting.
func (e *GapError) ExitCode() int { return exitcode.Usage }

func gapErrorf(format string, a ...any) error { return &GapError{fmt.Sprintf(format, a...)} }

// ParseGapRange parses "DUR" or "MIN..MAX" (Go durations; a bare "0" is no gap).
func ParseGapRange(s string) (GapRange, error) {
	parse := func(v string) (time.Duration, error) {
		v = strings.TrimSpace(v)
		if v == "0" {
			return 0, nil
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return 0, gapErrorf("gap %q: %q is not a duration (e.g. 300ms, 1.2s)", s, v)
		}
		return d, nil
	}
	var r GapRange
	var err error
	if lo, hi, ok := strings.Cut(s, ".."); ok {
		if r.Min, err = parse(lo); err != nil {
			return GapRange{}, err
		}
		if r.Max, err = parse(hi); err != nil {
			return GapRange{}, err
		}
	} else {
		if r.Min, err = parse(s); err != nil {
			return GapRange{}, err
		}
		r.Max = r.Min
	}
	if err := r.Validate(); err != nil {
		return GapRange{}, err
	}
	return r, nil
}

// Validate enforces 0 ≤ MIN ≤ MAX ≤ 10s.
func (r GapRange) Validate() error {
	switch {
	case r.Min < 0 || r.Max < 0:
		return gapErrorf("gap %s: negative durations are not allowed", r)
	case r.Min > r.Max:
		return gapErrorf("gap %s: MIN is greater than MAX", r)
	case r.Max > MaxGap:
		return gapErrorf("gap %s: over the %s maximum", r, MaxGap)
	}
	return nil
}

func (r GapRange) String() string {
	if r.Min == r.Max {
		return r.Min.String()
	}
	return r.Min.String() + ".." + r.Max.String()
}

// Gaps are the gap settings: one range per class and the seed (design §9.1).
type Gaps struct {
	Sentence, List, Paragraph, Section GapRange
	Seed                               int64
}

// DefaultGaps are the design §9.1 defaults.
func DefaultGaps() Gaps {
	ms := time.Millisecond
	return Gaps{
		Sentence:  GapRange{200 * ms, 350 * ms},
		List:      GapRange{350 * ms, 500 * ms},
		Paragraph: GapRange{550 * ms, 800 * ms},
		Section:   GapRange{1100 * ms, 1500 * ms},
	}
}

// Validate checks every range.
func (g Gaps) Validate() error {
	for _, r := range []GapRange{g.Sentence, g.List, g.Paragraph, g.Section} {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (g Gaps) rangeOf(c GapClass) (GapRange, bool) {
	switch c {
	case GapSentence:
		return g.Sentence, true
	case GapList:
		return g.List, true
	case GapParagraph:
		return g.Paragraph, true
	case GapSection:
		return g.Section, true
	}
	return GapRange{}, false
}

// samplesOf rounds d to the nearest whole sample.
func samplesOf(d time.Duration) int {
	return int((int64(d)*SampleRate + int64(time.Second)/2) / int64(time.Second))
}

// DrawGap draws a gap of r in samples, uniformly over the whole samples of the range, from a
// PRNG seeded by sha256(seed, the cache keys either side, class). The draw is stable: the same
// join always gives the same gap, and it depends on nothing else.
func DrawGap(seed int64, before, after string, class GapClass, r GapRange) int {
	lo, hi := samplesOf(r.Min), samplesOf(r.Max)
	if lo == hi {
		return lo
	}
	h := sha256.New()
	for _, f := range []string{"recite/gap@1", strconv.FormatInt(seed, 10), before, after, string(class)} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write([]byte(f))
	}
	sum := h.Sum(nil)
	rng := rand.New(rand.NewPCG(binary.BigEndian.Uint64(sum[:8]), binary.BigEndian.Uint64(sum[8:16])))
	return lo + rng.IntN(hi-lo+1)
}

// Join describes the join after one chunk: its cache key, the class of the gap that
// follows, and the exact duration when the class is GapPause.
type Join struct {
	CacheKey string
	Class    GapClass
	Pause    time.Duration
}

// GapSamples returns the silence after each chunk, in samples. The last chunk is followed by
// none, whatever its class.
func GapSamples(g Gaps, joins []Join) ([]int, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	out := make([]int, len(joins))
	for i := 0; i+1 < len(joins); i++ {
		j := joins[i]
		switch j.Class {
		case GapNone:
		case GapPause:
			if j.Pause < 0 || j.Pause > MaxGap {
				return nil, gapErrorf("pause %s after chunk %d: outside 0..%s", j.Pause, i+1, MaxGap)
			}
			out[i] = samplesOf(j.Pause)
		default:
			r, ok := g.rangeOf(j.Class)
			if !ok {
				return nil, fmt.Errorf("audio: unknown gap class %q after chunk %d", j.Class, i+1)
			}
			out[i] = DrawGap(g.Seed, j.CacheKey, joins[i+1].CacheKey, j.Class, r)
		}
	}
	return out, nil
}

// Fade returns a copy of p with a linear fade in and out of FadeDuration (or half of p when
// p is shorter than two fades): the first and last samples become zero.
func Fade(p PCM) PCM {
	out := make(PCM, len(p)-len(p)%BytesPerSample)
	copy(out, p)
	n := len(out) / BytesPerSample
	f := min(samplesOf(FadeDuration), n/2)
	for i := range f {
		for _, k := range []int{i, n - 1 - i} {
			v := int64(int16(binary.LittleEndian.Uint16(out[k*BytesPerSample:])))
			binary.LittleEndian.PutUint16(out[k*BytesPerSample:], uint16(int16(v*int64(i)/int64(f))))
		}
	}
	return out
}

// Assembler joins chunk PCM in index order, with Fade at every chunk edge and the given
// silence after each chunk. Chunks may arrive in any order; each complete prefix is written
// to w as soon as it exists (design §10), so the output bytes do not depend on arrival order.
type Assembler struct {
	w       io.Writer
	gaps    []int
	pending map[int]PCM
	next    int
	written int64
	spans   []Span
}

// Span is where one chunk's audio lies in the assembly, in samples from the start: [Start, End).
// The gap after the chunk begins at End.
type Span struct{ Start, End int64 }

// NewAssembler assembles len(gaps) chunks; gaps[i] is the silence after chunk i in samples.
func NewAssembler(w io.Writer, gaps []int) *Assembler {
	return &Assembler{w: w, gaps: gaps, pending: map[int]PCM{}}
}

// Add delivers chunk i (0-based) and writes every chunk now in order.
func (a *Assembler) Add(i int, p PCM) error {
	if i < 0 || i >= len(a.gaps) {
		return fmt.Errorf("audio: chunk %d outside 0..%d", i, len(a.gaps)-1)
	}
	if _, dup := a.pending[i]; dup || i < a.next {
		return fmt.Errorf("audio: chunk %d delivered twice", i)
	}
	if err := p.Validate(); err != nil {
		return fmt.Errorf("audio: chunk %d: %w", i, err)
	}
	a.pending[i] = p
	for {
		q, ok := a.pending[a.next]
		if !ok {
			return nil
		}
		delete(a.pending, a.next)
		start := a.written / BytesPerSample
		if err := a.write(Fade(q)); err != nil {
			return err
		}
		a.spans = append(a.spans, Span{Start: start, End: a.written / BytesPerSample})
		if err := a.write(make(PCM, a.gaps[a.next]*BytesPerSample)); err != nil {
			return err
		}
		a.next++
	}
}

func (a *Assembler) write(p PCM) error {
	n, err := a.w.Write(p)
	a.written += int64(n)
	if err != nil {
		return fmt.Errorf("audio: assembly write: %w", err)
	}
	return nil
}

// Written is the number of bytes written so far.
func (a *Assembler) Written() int64 { return a.written }

// Spans returns the place of every chunk written so far, in index order: the timeline the
// manifest records (OUT-02).
func (a *Assembler) Spans() []Span { return append([]Span(nil), a.spans...) }

// Close reports an error unless every chunk was delivered.
func (a *Assembler) Close() error {
	if a.next != len(a.gaps) {
		return errors.New("audio: assembly incomplete: chunk " + strconv.Itoa(a.next) + " missing")
	}
	return nil
}
