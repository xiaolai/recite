package spoken

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/numwords"
)

// Scanning helpers shared by the rules. Offsets are byte offsets; rules only
// look at ASCII digits, so a digit is always one byte.

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// prevRune is the rune before offset i, or 0 at the start.
func prevRune(s string, i int) rune {
	if i == 0 {
		return 0
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return r
}

// nextRune is the rune at offset j, or 0 at the end.
func nextRune(s string, j int) rune {
	if j >= len(s) {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(s[j:])
	return r
}

// wordStart reports whether i starts a word: no letter or digit before it,
// and no '.', ':' or '/' glued to one ("v0.5x", "12:30:45", "50/50"). The
// glue check treats letters and digits alike, so a rewrite to the left (digits
// becoming words) cannot change the outcome on a second pass.
func wordStart(s string, i int) bool {
	p := prevRune(s, i)
	if isWordRune(p) {
		return false
	}
	if p == '.' || p == ':' || p == '/' {
		return !isWordRune(prevRune(s, i-1))
	}
	return true
}

// wordEnd reports whether j ends a word: no letter or digit after it.
func wordEnd(s string, j int) bool { return !isWordRune(nextRune(s, j)) }

// digits returns the end of the ASCII digit run at i.
func digits(s string, i int) int {
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return i
}

// number is a scanned decimal number: Int holds the integer digits with any
// thousands separators removed, Frac the digits after the point.
type number struct {
	Int, Frac string
	Neg       bool
	End       int
}

// scanNumber reads [sign] digits [,ddd]* [.digits] at i. A sign is read only
// where a minus cannot be a hyphen: not after a letter or digit.
func scanNumber(s string, i int, signed bool) (number, bool) {
	var n number
	j := i
	if signed && j < len(s) && wordStart(s, j) {
		switch {
		case s[j] == '-':
			n.Neg, j = true, j+1
		case strings.HasPrefix(s[j:], "−"):
			n.Neg, j = true, j+len("−")
		}
	}
	k := digits(s, j)
	if k == j {
		return n, false
	}
	var ib strings.Builder
	ib.WriteString(s[j:k])
	if k-j <= 3 {
		for k+3 < len(s) && s[k] == ',' && digits(s, k+1) == k+4 {
			ib.WriteString(s[k+1 : k+4])
			k += 4
		}
	}
	n.Int = ib.String()
	if k+1 < len(s) && s[k] == '.' && isDigit(s[k+1]) {
		e := digits(s, k+1)
		n.Frac = s[k+1 : e]
		k = e
	}
	n.End = k
	return n, true
}

// plain reports a number with no sign, fraction or separators.
func (n number) plain(s string, i int) bool {
	return !n.Neg && n.Frac == "" && n.End-i == len(n.Int)
}

func (n number) isOne() bool { return !n.Neg && n.Frac == "" && n.Int == "1" }

// value parses the integer part; ok is false past MaxCardinal.
func (n number) value() (uint64, bool) {
	if len(n.Int) > 15 {
		return 0, false
	}
	var v uint64
	for _, d := range n.Int {
		v = v*10 + uint64(d-'0')
	}
	return v, v <= numwords.MaxCardinal
}

// words spells n: "minus twelve point five oh".
func (n number) words() string {
	w := spellInt(n.Int)
	if n.Frac != "" {
		w += " point " + spellDigits(n.Frac)
	}
	if n.Neg {
		w = "minus " + w
	}
	return w
}

func spellInt(ds string) string { return numwords.Join(numwords.Integer(ds)) }

func spellN(v uint64) string { return numwords.Join(numwords.Cardinal(v)) }

// spellDigits reads digits one by one, zero as "oh" as after a decimal point.
func spellDigits(ds string) string {
	w := numwords.Digits(ds)
	for i := range w {
		if w[i] == "zero" {
			w[i] = "oh"
		}
	}
	return strings.Join(w, " ")
}

func ordinalWords(v uint64) string { return numwords.Join(numwords.Ordinal(numwords.Cardinal(v))) }

// atoi parses a short ASCII digit string; ok is false for anything else.
func atoi(ds string) (int, bool) {
	if ds == "" || len(ds) > 9 {
		return 0, false
	}
	v := 0
	for i := 0; i < len(ds); i++ {
		if !isDigit(ds[i]) {
			return 0, false
		}
		v = v*10 + int(ds[i]-'0')
	}
	return v, true
}

// prevWord returns the word before i, skipping spaces (not other punctuation).
func prevWord(s string, i int) string {
	j := i
	for j > 0 && s[j-1] == ' ' {
		j--
	}
	k := j
	for k > 0 {
		r, n := utf8.DecodeLastRuneInString(s[:k])
		if !isWordRune(r) && r != '.' {
			break
		}
		k -= n
	}
	return strings.TrimLeft(s[k:j], ".")
}

// nextWord returns the word after j, skipping spaces.
func nextWord(s string, j int) string {
	for j < len(s) && s[j] == ' ' {
		j++
	}
	k := j
	for k < len(s) {
		r, n := utf8.DecodeRuneInString(s[k:])
		if !isWordRune(r) {
			break
		}
		k += n
	}
	return s[j:k]
}

func capitalised(w string) bool {
	r, _ := utf8.DecodeRuneInString(w)
	return unicode.IsUpper(r)
}

// pad surrounds a word-like replacement with spaces where it touches a letter
// or digit, so "2×4" becomes "2 times 4" and not "2times4".
func pad(s string, i, j int, words string) string {
	if isWordRune(prevRune(s, i)) {
		words = " " + words
	}
	if isWordRune(nextRune(s, j)) {
		words += " "
	}
	return words
}
