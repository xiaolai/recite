package guard

import (
	"regexp"
	"unicode"
	"unicode/utf8"
)

// Go's regexp \b knows only ASCII word characters, so a pattern bounded by \b still matches
// inside "ñyou" or "wwwé". bounded applies the boundary to Unicode letters and digits at a
// match's outer edges: the rune before it, and the rune after it when the match ends in a word
// rune (where the pattern's trailing \b sits).
func bounded(s string, start, end int) bool {
	if r, _ := utf8.DecodeLastRuneInString(s[:start]); wordRune(r) {
		return false
	}
	if last, _ := utf8.DecodeLastRuneInString(s[start:end]); wordRune(last) {
		if r, _ := utf8.DecodeRuneInString(s[end:]); wordRune(r) {
			return false
		}
	}
	return true
}

func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r) || r == '_'
}

// boundedIndex is FindAllStringIndex keeping only bounded matches.
func boundedIndex(re *regexp.Regexp, s string) [][]int {
	var out [][]int
	for _, loc := range re.FindAllStringIndex(s, -1) {
		if bounded(s, loc[0], loc[1]) {
			out = append(out, loc)
		}
	}
	return out
}

// matchBounded is MatchString with Unicode word boundaries at the match's edges.
func matchBounded(re *regexp.Regexp, s string) bool { return len(boundedIndex(re, s)) > 0 }
