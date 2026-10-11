package verify

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// CanonToken is one canonical token with the byte span of the original text it came from.
// Several tokens may share a span ("42" yields "forty" and "two", both spanning "42").
type CanonToken struct {
	Text       string
	Start, End int // byte offsets into the original string
}

// CanonTokens is Canon with provenance: the token texts always equal Canon(s), and each token
// carries the span of s it was derived from, so an alignment over canonical tokens can be
// reported in the coordinates of the text the user wrote (VER-04).
//
// The spans come from canonicalising s unit by unit (whitespace-separated runs, with every
// Han, Kana, Hangul and Thai letter its own unit). Where that differs from canonicalising the
// whole text (rules that look across units, such as "St" before a capitalised word or "and"
// inside a number reading), the whole-text tokens win and take their spans from an alignment
// with the per-unit tokens.
func CanonTokens(s string) []CanonToken {
	want := Canon(s)
	var got []CanonToken
	for _, u := range canonUnits(s) {
		for _, t := range Canon(s[u[0]:u[1]]) {
			got = append(got, CanonToken{Text: t, Start: u[0], End: u[1]})
		}
	}
	if sameTexts(want, got) {
		return got
	}
	return reconcile(want, got)
}

func sameTexts(want []string, got []CanonToken) bool {
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i] != got[i].Text {
			return false
		}
	}
	return true
}

// reconcile gives each whole-text token the span of the per-unit tokens it came from. Usually
// they differ only by unit boundaries inside a word (a Han run is one token but several units),
// which a linear merge resolves; otherwise each whole-text token takes the span of the per-unit
// token aligned with it, and a token with no counterpart a zero-width span after the previous.
func reconcile(want []string, got []CanonToken) []CanonToken {
	if out, ok := mergeUnits(want, got); ok {
		return out
	}
	gotTexts := make([]string, len(got))
	for i, g := range got {
		gotTexts[i] = g.Text
	}
	out := make([]CanonToken, len(want))
	pos := 0
	for _, st := range align(want, gotTexts) {
		if st.ref < 0 {
			continue // a per-unit token the whole text does not have
		}
		tok := CanonToken{Text: want[st.ref], Start: pos, End: pos}
		if st.hyp >= 0 {
			tok.Start, tok.End = got[st.hyp].Start, got[st.hyp].End
		}
		pos = tok.End
		out[st.ref] = tok
	}
	return out
}

// mergeUnits succeeds when every whole-text token is the concatenation of consecutive
// per-unit tokens.
func mergeUnits(want []string, got []CanonToken) ([]CanonToken, bool) {
	out := make([]CanonToken, len(want))
	j := 0
	for i, w := range want {
		k, acc := j, ""
		for k < len(got) && len(acc) < len(w) && strings.HasPrefix(w, acc+got[k].Text) {
			acc += got[k].Text
			k++
		}
		if k == j || acc != w {
			return nil, false
		}
		out[i] = CanonToken{Text: w, Start: got[j].Start, End: got[k-1].End}
		j = k
	}
	return out, j == len(got)
}

// canonUnits splits s into the byte ranges canonicalised one by one.
func canonUnits(s string) [][2]int {
	var units [][2]int
	start := -1
	flush := func(end int) {
		if start >= 0 {
			units = append(units, [2]int{start, end})
			start = -1
		}
	}
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case unicode.IsSpace(r):
			flush(i)
		case isHanNumeral(r):
			// A numeral run reads as one number, so it stays one unit.
			if start >= 0 {
				prev, _ := utf8.DecodeLastRuneInString(s[:i])
				if !isHanNumeral(prev) {
					flush(i)
				}
			}
			if start < 0 {
				start = i
			}
		case isSyllabicLetter(r):
			flush(i)
			units = append(units, [2]int{i, i + size})
		default:
			if start >= 0 {
				prev, _ := utf8.DecodeLastRuneInString(s[:i])
				if isHanNumeral(prev) {
					flush(i)
				}
			}
			if start < 0 {
				start = i
			}
		}
		i += size
	}
	flush(len(s))
	return units
}

// isSyllabicLetter reports letters of scripts written without spaces, where one rune is the
// smallest unit worth reporting.
func isSyllabicLetter(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Thai)
}
