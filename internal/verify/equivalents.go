package verify

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/spoken"
)

// applyEquivalents rewrites each whole-word occurrence of a Written term in the transcript
// to its Spoken form, so a take that says the rewrite verifies whether STT spelled the
// rewrite or wrote the term back. Matching ignores case (STT case varies) and uses the
// lexicon's word rule: whole words in spaced scripts, substrings in Chinese and Japanese.
// The longest term wins at a position.
func applyEquivalents(s string, eqs []SayPair) string {
	if len(eqs) == 0 {
		return s
	}
	type term struct {
		runes int
		eq    SayPair
	}
	var ts []term
	for _, e := range eqs {
		if e.Written != "" && e.Written != e.Spoken {
			ts = append(ts, term{utf8.RuneCountInString(e.Written), e})
		}
	}
	slices.SortStableFunc(ts, func(a, b term) int { return b.runes - a.runes })
	var b strings.Builder
	for i := 0; i < len(s); {
		matched := false
		for _, t := range ts {
			j := runeEnd(s, i, t.runes)
			if j < 0 || !strings.EqualFold(s[i:j], t.eq.Written) || !spoken.WordBounded(s, i, j) {
				continue
			}
			b.WriteString(t.eq.Spoken)
			i, matched = j, true
			break
		}
		if !matched {
			_, n := utf8.DecodeRuneInString(s[i:])
			b.WriteString(s[i : i+n])
			i += n
		}
	}
	return b.String()
}

// sayPairsFor drops the pairs whose written term the reference itself holds (a {{verbatim}}
// or {{say}} mark kept it): there the term in the transcript may be meant as written, so the
// pair cannot tell which reading is right and the take is judged as is.
func sayPairsFor(ref string, eqs []SayPair) []SayPair {
	var out []SayPair
	for _, e := range eqs {
		probe := []SayPair{{Written: e.Written, Spoken: "\x00"}}
		if !strings.Contains(applyEquivalents(ref, probe), "\x00") {
			out = append(out, e)
		}
	}
	return out
}

// runeEnd is the byte offset n runes after i, or -1 past the end.
func runeEnd(s string, i, n int) int {
	for ; n > 0; n-- {
		if i >= len(s) {
			return -1
		}
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return i
}
