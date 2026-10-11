package spoken

import (
	"strings"
	"unicode"
)

// Hyphen-joined alphanumeric identifiers, shared by the English and Chinese tables:
// part numbers, serials, tickets and model names ("XJ-4471-B", "K9Q-23M-77ZX", "JIRA-10442",
// "RTX-5090"). Left as written, the model reads them its own way and a rule reading only
// the digit groups leaves half a token behind ("XJ-four thousand …-B").
//
// Each digit run is read as the same digits would be read standing alone, and the
// letters are left as written. That is what keeps the comparison stable: the verifier
// takes the transcript through these rules too, and STT writes the same identifier as
// "XJ-4471-B", "XJ 4471B" or "XJ 4471 B"; reading a run as it reads alone gives all three
// one spoken form. A token glued without a hyphen ("3D", "A4", "B47", "MD5") stays as
// written: the model already reads those, and the canonicaliser reads their digits the
// same way.

// identPieces scans a run of ASCII letters and digits joined by single hyphens at i. It is
// an identifier when it has two or more pieces, a letter and a digit, an upper-case
// letter, and no piece that glues lower-case letters to digits ("mid-2020", "B-52s",
// "4.12.0-rc.3", "12th-century" are not identifiers).
func identPieces(s string, i int, start, end func(string, int) bool) ([]string, int, bool) {
	if !isASCIIAlnum(rune(s[i])) || !start(s, i) || !identBoundary(prevRune(s, i)) {
		return nil, 0, false
	}
	var pieces []string
	j := i
	for {
		e := j
		for e < len(s) && isASCIIAlnum(rune(s[e])) {
			e++
		}
		pieces = append(pieces, s[j:e])
		j = e
		if byteAt(s, j) == '-' && isASCIIAlnum(rune(byteAt(s, j+1))) {
			j++
			continue
		}
		break
	}
	if len(pieces) < 2 || !end(s, j) || !identBoundary(nextRune(s, j)) && isASCIIAlnum(rune(byteAt(s, j+1))) {
		return nil, 0, false
	}
	var letter, digit, upper bool
	for _, p := range pieces {
		pl, pd := strings.IndexFunc(p, unicode.IsLetter) >= 0, strings.IndexFunc(p, unicode.IsDigit) >= 0
		if pl && pd && strings.IndexFunc(p, unicode.IsLower) >= 0 {
			return nil, 0, false
		}
		letter, digit = letter || pl, digit || pd
		upper = upper || strings.IndexFunc(p, unicode.IsUpper) >= 0
	}
	if !letter || !digit || !upper {
		return nil, 0, false
	}
	return pieces, j, true
}

// identBoundary reports a rune that may border an identifier: not one that joins it to a
// path, address, version or longer token.
func identBoundary(r rune) bool {
	return !isASCIIAlnum(r) && !strings.ContainsRune("-._/\\@#:", r)
}

func mixed(p string) bool {
	return strings.IndexFunc(p, unicode.IsLetter) >= 0 && strings.IndexFunc(p, unicode.IsDigit) >= 0
}

// identRuns splits a piece into its letter and digit runs: "77ZX" → "77", "ZX".
func identRuns(p string) []string {
	var runs []string
	k := 0
	for j := 1; j <= len(p); j++ {
		if j == len(p) || isDigit(p[j]) != isDigit(p[j-1]) {
			runs = append(runs, p[k:j])
			k = j
		}
	}
	return runs
}

// enIdent reads "K9Q-23M-77ZX" as "K nine Q twenty-three M seventy-seven ZX".
func enIdent(s string, i int, _ *Options) (Match, bool) {
	pieces, end, ok := identPieces(s, i, wordStart, wordEnd)
	if !ok {
		return Match{}, false
	}
	var ws []string
	for _, p := range pieces {
		for _, r := range identRuns(p) {
			if isDigit(r[0]) {
				r = spellInt(r)
			}
			ws = append(ws, r)
		}
	}
	return Match{End: end, Spoken: strings.Join(ws, " ")}, true
}

// zhIdent reads "A7-2291-ZK" as "A七 二二九一 ZK": digits one by one, as for any code.
func zhIdent(s string, i int, _ *Options) (Match, bool) {
	pieces, end, ok := identPieces(s, i, zhStart, func(s string, j int) bool { return !isASCIIAlnum(nextRune(s, j)) })
	if !ok || len(pieces) == 2 && !mixed(pieces[0]) && !mixed(pieces[1]) {
		return Match{}, false // a name such as COVID-19 is an English term, kept as written
	}
	ws := make([]string, len(pieces))
	for k, p := range pieces {
		var b strings.Builder
		for _, r := range identRuns(p) {
			if isDigit(r[0]) {
				r = zhDigits(r, "零")
			}
			b.WriteString(r)
		}
		ws[k] = b.String()
	}
	return Match{End: end, Spoken: strings.Join(ws, " ")}, true
}
