package subtitle

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/text"
)

// Line limits of a cue (OUT-03): two lines of 42 characters, or 16 for CJK text.
const (
	MaxLines   = 2
	MaxLineLen = 42
	MaxLineCJK = 16
)

// Chunk is one chunk on the assembly timeline: its display text and where its audio lies, in
// samples from the start of the assembly at rate Hz: [Start, End). The gap after it is silence
// and carries no cue.
type Chunk struct {
	Text       string
	Start, End int64
}

// Cue is one subtitle cue.
type Cue struct {
	Start, End time.Duration
	Lines      []string
}

// Cues times one cue per sentence from the chunk spans (OUT-03). The timeline knows chunks
// only, so the sentences of a chunk share its span in proportion to their character counts:
// an approximation inside a chunk, exact at its edges. A sentence longer than one cue's
// lines becomes several cues, sharing its time the same way.
func Cues(chunks []Chunk, rate int) ([]Cue, error) {
	if rate <= 0 {
		return nil, fmt.Errorf("subtitle: sample rate %d", rate)
	}
	var out []Cue
	var prevEnd int64
	for i, c := range chunks {
		if c.Start < prevEnd || c.End < c.Start {
			return nil, fmt.Errorf("subtitle: chunk %d span [%d, %d) is not after %d", i+1, c.Start, c.End, prevEnd)
		}
		prevEnd = c.End
		var pieces [][]string
		for _, s := range text.Sentences(c.Text) {
			lines := wrap(s)
			for len(lines) > 0 {
				n := min(MaxLines, len(lines))
				pieces = append(pieces, lines[:n])
				lines = lines[n:]
			}
		}
		if len(pieces) == 0 {
			continue
		}
		weights := make([]int64, len(pieces))
		var total int64
		for k, p := range pieces {
			for _, l := range p {
				weights[k] += int64(utf8.RuneCountInString(l))
			}
			total += weights[k]
		}
		span := c.End - c.Start
		var cum int64
		start := c.Start
		for k, p := range pieces {
			cum += weights[k]
			// Rounded cumulative shares: the first cue starts and the last one ends exactly
			// on the chunk's edges, and no cue goes backwards.
			end := c.Start + (span*cum+total/2)/total
			out = append(out, Cue{Start: at(start, rate), End: at(end, rate), Lines: p})
			start = end
		}
	}
	return out, nil
}

func textSentences(s string) []string { return text.Sentences(s) }

func at(samples int64, rate int) time.Duration {
	return audio.SampleDuration(samples, rate)
}

// wrap breaks s into lines within the line limit: at spaces for word-spaced text, between any
// two characters for CJK text, and inside a word only when the word alone is too long.
func wrap(s string) []string {
	limit := MaxLineLen
	if isCJKText(s) {
		limit = MaxLineCJK
	}
	var lines []string
	var cur []rune
	flush := func() {
		if l := strings.TrimSpace(string(cur)); l != "" {
			lines = append(lines, l)
		}
		cur = cur[:0]
	}
	for _, w := range strings.Fields(s) {
		r := []rune(w)
		if limit == MaxLineCJK {
			// CJK text breaks anywhere; a space between runs is kept when it fits.
			if len(cur) > 0 && len(cur)+1 < limit {
				cur = append(cur, ' ')
			} else if len(cur) > 0 {
				flush()
			}
		} else if len(cur) > 0 && len(cur)+1+len(r) <= limit {
			cur = append(cur, ' ')
		} else if len(cur) > 0 {
			flush()
		}
		for len(r) > 0 {
			room := limit - len(cur)
			if room <= 0 {
				flush()
				room = limit
			}
			n := min(room, len(r))
			cur = append(cur, r[:n]...)
			r = r[n:]
		}
	}
	flush()
	return lines
}

// isCJKText reports text that is mostly Han, kana or Hangul, which has no spaces to wrap at.
func isCJKText(s string) bool {
	var cjk, other int
	for _, r := range s {
		switch {
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
			cjk++
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			other++
		}
	}
	return cjk > 0 && cjk >= other
}

// Shift moves cues by -offset, for a file that starts at offset on the assembly timeline.
func Shift(cues []Cue, offset time.Duration) []Cue {
	out := make([]Cue, len(cues))
	for i, c := range cues {
		out[i] = Cue{Start: c.Start - offset, End: c.End - offset, Lines: c.Lines}
	}
	return out
}
