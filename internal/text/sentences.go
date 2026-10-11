package text

import (
	"strings"
	"unicode/utf8"
)

// Sentences splits s into its sentences with the segmenter's rules (design §7.2): a terminal
// mark ends a sentence unless it is a decimal point, an abbreviation or an initial, and the
// closing quotes and brackets after it stay with it. Each sentence is trimmed; empty ones are
// dropped, so the result is empty only for blank s.
func Sentences(s string) []string {
	var out []string
	add := func(p string) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	start := 0
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if !isTerminal(r) || !sentenceEnd(s, start, i) {
			i += n
			continue
		}
		j := i + n
		for j < len(s) {
			q, m := utf8.DecodeRuneInString(s[j:])
			if !isTerminal(q) && !isCloser(q) {
				break
			}
			j += m
		}
		// As in the segmenter, a Latin mark ends a sentence only before whitespace
		// ("example.com" is one word); the end of s needs no cut.
		if j < len(s) && punctBreak(s, i, j, len(s), BoundarySentence) == "" {
			i = j
			continue
		}
		add(s[start:j])
		start, i = j, j
	}
	add(s[start:])
	return out
}
