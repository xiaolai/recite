package verify

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
	"golang.org/x/text/width"
)

// CanonVersion identifies the canonicaliser's behaviour. Bump it on any change
// to Canon's output: cache keys and verification provenance record it.
const CanonVersion = 1

// Canon canonicalises s into comparison tokens (design §8.3): NFKC and width
// folding, abbreviation expansion, lower case, numbers to English words (one
// canonical form for digits and words), apostrophes removed, hyphens to a
// space, every other punctuation or symbol removed. Canon is idempotent:
// Canon(strings.Join(Canon(x), " ")) equals Canon(x).
func Canon(s string) []string { return canon(s, false) }

// Equivalent reports whether a and b canonicalise equal, also trying the
// alternatives Canon cannot decide alone: hyphenated compounds joined
// ("e-mail" = "email") and years read as pairs ("nineteen ninety-nine" =
// "1999", whose canonical form is the cardinal).
func Equivalent(a, b string) bool {
	for _, join := range []bool{false, true} {
		ta, tb := canon(a, join), canon(b, join)
		if equalTokens(ta, tb) || equalTokens(yearPairsToCardinal(ta), yearPairsToCardinal(tb)) {
			return true
		}
	}
	return false
}

func equalTokens(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func canon(s string, joinHyphens bool) []string {
	s = width.Fold.String(norm.NFKC.String(s))
	// Numbers first: digits glued to letters ("Mr00") must not hide a word
	// boundary from the abbreviation pass, or a second pass would differ.
	s = chineseNumbers(englishNumbers(s))
	s = expandAbbreviations(s)
	s = norm.NFKC.String(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case isApostrophe(r):
		case isHyphen(r):
			if !joinHyphens {
				b.WriteByte(' ')
			}
		case unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return expandTokens(dropNumberAnd(strings.Fields(b.String())))
}

// expandTokens expands abbreviations that only became whole tokens after
// punctuation removal ("M's" -> "ms"). Without it a second Canon pass would
// expand them, breaking idempotence. Case is gone here, so "st" is "street".
func expandTokens(toks []string) []string {
	var out []string
	for _, t := range toks {
		switch exp := abbreviations[t]; {
		case t == "st":
			out = append(out, "street")
		case exp != "":
			out = append(out, strings.Fields(exp)...)
		default:
			out = append(out, t)
		}
	}
	return out
}

func isApostrophe(r rune) bool { return r == '\'' || r == '’' || r == 'ʼ' || r == '‘' }

func isHyphen(r rune) bool {
	return r == '-' || r == '‐' || r == '‑' || r == '‒' || r == '–' || r == '—' || r == '−'
}

// --- abbreviations ---

var (
	latinAbbrev = regexp.MustCompile(`(?i)\b(e\.g|i\.e)\b\.?`)
	wordAbbrev  = regexp.MustCompile(`(?i)\b(mrs|mr|ms|dr|st|vs|etc|jr|sr)\b\.?`)
)

var abbreviations = map[string]string{
	"mrs": "missus", "mr": "mister", "ms": "miz", "dr": "doctor", "vs": "versus",
	"etc": "et cetera", "jr": "junior", "sr": "senior",
}

// expandAbbreviations runs before lower-casing because "st" needs case:
// "St" followed by a capitalised word is "saint" (St. Louis), otherwise
// "street" (Main St.). "dr" is always "doctor": the Drive reading is rare
// enough in spoken text that a context rule would misfire more than it helps.
func expandAbbreviations(s string) string {
	s = latinAbbrev.ReplaceAllStringFunc(s, func(m string) string {
		if strings.EqualFold(m[:1], "e") {
			return " for example "
		}
		return " that is "
	})
	var b strings.Builder
	last := 0
	for _, loc := range wordAbbrev.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(s[last:loc[0]])
		word := strings.ToLower(s[loc[2]:loc[3]])
		exp := abbreviations[word]
		if word == "st" {
			exp = "street"
			if r, _ := utf8.DecodeRuneInString(strings.TrimLeft(s[loc[1]:], " \t")); unicode.IsUpper(r) {
				exp = "saint"
			}
		}
		b.WriteString(exp)
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// --- English numbers ---

var (
	ones = []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine",
		"ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen",
		"eighteen", "nineteen"}
	tens   = []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}
	scales = []struct {
		v    uint64
		name string
	}{{1e12, "trillion"}, {1e9, "billion"}, {1e6, "million"}, {1e3, "thousand"}}
)

// maxCardinal bounds numbers read as cardinals; longer digit strings are read
// digit by digit, as are strings with a leading zero ("007").
const maxCardinal = 999_999_999_999_999

func cardinal(n uint64) []string {
	if n < 20 {
		return []string{ones[n]}
	}
	if n < 100 {
		w := []string{tens[n/10]}
		if n%10 != 0 {
			w = append(w, ones[n%10])
		}
		return w
	}
	if n < 1000 {
		w := []string{ones[n/100], "hundred"}
		if n%100 != 0 {
			w = append(w, cardinal(n%100)...)
		}
		return w
	}
	for _, sc := range scales {
		if n >= sc.v {
			w := append(cardinal(n/sc.v), sc.name)
			if n%sc.v != 0 {
				w = append(w, cardinal(n%sc.v)...)
			}
			return w
		}
	}
	panic("unreachable")
}

func digitWords(ds string) []string {
	w := make([]string, 0, len(ds))
	for _, d := range ds {
		w = append(w, ones[d-'0'])
	}
	return w
}

func integerWords(ds string) []string {
	if len(ds) > 1 && ds[0] == '0' || len(ds) > 15 {
		return digitWords(ds)
	}
	var n uint64
	for _, d := range ds {
		n = n*10 + uint64(d-'0')
	}
	if n > maxCardinal {
		return digitWords(ds)
	}
	return cardinal(n)
}

var ordinalIrregular = map[string]string{
	"one": "first", "two": "second", "three": "third", "five": "fifth", "eight": "eighth",
	"nine": "ninth", "twelve": "twelfth",
}

func ordinal(words []string) []string {
	last := words[len(words)-1]
	switch {
	case ordinalIrregular[last] != "":
		last = ordinalIrregular[last]
	case strings.HasSuffix(last, "y"):
		last = last[:len(last)-1] + "ieth"
	default:
		last += "th"
	}
	out := append([]string(nil), words[:len(words)-1]...)
	return append(out, last)
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// englishNumbers rewrites every ASCII digit run as English words, so no ASCII
// digit survives (which keeps Canon idempotent). It reads thousands
// separators, a sign after a non-word character, decimals, a % suffix and an
// ordinal suffix.
func englishNumbers(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		neg := false
		start := i
		if c == '-' && i+1 < len(s) && isASCIIDigit(s[i+1]) {
			prev, _ := utf8.DecodeLastRuneInString(s[:i])
			if i == 0 || !isWordRune(prev) {
				neg, start = true, i+1
			}
		}
		if !neg && !isASCIIDigit(c) {
			b.WriteByte(c)
			i++
			continue
		}
		j := start
		var digits strings.Builder
		for j < len(s) && isASCIIDigit(s[j]) {
			digits.WriteByte(s[j])
			j++
		}
		// Thousands groups: ",ddd" not followed by another digit, after 1-3 digits.
		if digits.Len() <= 3 {
			for j+3 < len(s) && s[j] == ',' && isASCIIDigit(s[j+1]) && isASCIIDigit(s[j+2]) && isASCIIDigit(s[j+3]) &&
				(j+4 >= len(s) || !isASCIIDigit(s[j+4])) {
				digits.WriteString(s[j+1 : j+4])
				j += 4
			}
		}
		words := integerWords(digits.String())
		if j+1 < len(s) && s[j] == '.' && isASCIIDigit(s[j+1]) {
			k := j + 1
			for k < len(s) && isASCIIDigit(s[k]) {
				k++
			}
			words = append(append(words, "point"), digitWords(s[j+1:k])...)
			j = k
		} else if suf := ordinalSuffix(s[j:]); suf > 0 {
			words = ordinal(words)
			j += suf
		}
		if j < len(s) && s[j] == '%' {
			words = append(words, "percent")
			j++
		}
		if neg {
			words = append([]string{"minus"}, words...)
		}
		b.WriteByte(' ')
		b.WriteString(strings.Join(words, " "))
		b.WriteByte(' ')
		i = j
	}
	return b.String()
}

// ordinalSuffix returns the length of an ordinal suffix (st, nd, rd, th) at
// the start of s when no letter follows it, else 0.
func ordinalSuffix(s string) int {
	if len(s) < 2 {
		return 0
	}
	switch strings.ToLower(s[:2]) {
	case "st", "nd", "rd", "th":
	default:
		return 0
	}
	if r, _ := utf8.DecodeRuneInString(s[2:]); len(s) > 2 && unicode.IsLetter(r) {
		return 0
	}
	return 2
}

// dropNumberAnd removes "and" inside a number reading ("one hundred and
// five" = "one hundred five"), the form numbers are canonicalised to.
func dropNumberAnd(toks []string) []string {
	out := toks[:0:0]
	for i, t := range toks {
		if t == "and" && i > 0 && i+1 < len(toks) && isScale(toks[i-1]) && isNumberWord(toks[i+1]) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func isScale(w string) bool {
	return w == "hundred" || w == "thousand" || w == "million" || w == "billion" || w == "trillion"
}

func isNumberWord(w string) bool { _, ok := smallValue(w); return ok }

func smallValue(w string) (int, bool) {
	for i, o := range ones {
		if o == w {
			return i, true
		}
	}
	for i, t := range tens {
		if t != "" && t == w {
			return i * 10, true
		}
	}
	return 0, false
}

// twoDigit parses a number word reading of 0..99 at toks[i:], returning the
// value and the tokens consumed (0 when none).
func twoDigit(toks []string, i int) (int, int) {
	if i >= len(toks) {
		return 0, 0
	}
	v, ok := smallValue(toks[i])
	if !ok {
		return 0, 0
	}
	if v >= 20 && i+1 < len(toks) {
		if u, ok := smallValue(toks[i+1]); ok && u >= 1 && u <= 9 {
			return v + u, 2
		}
	}
	return v, 1
}

// yearPairsToCardinal rewrites year-pair readings for 1000..2099 ("nineteen
// ninety nine", "nineteen oh five", "nineteen hundred", "twenty twenty")
// into the cardinal words Canon gives the digits.
func yearPairsToCardinal(toks []string) []string {
	var out []string
	for i := 0; i < len(toks); {
		hi, n := twoDigit(toks, i)
		if n == 1 && hi >= 10 && hi <= 20 && i+1 < len(toks) {
			j := i + 1
			lo, m := -1, 0
			switch {
			case toks[j] == "hundred":
				lo, m = 0, 1
			case toks[j] == "oh" && j+1 < len(toks):
				if v, ok := smallValue(toks[j+1]); ok && v >= 1 && v <= 9 {
					lo, m = v, 2
				}
			default:
				if v, k := twoDigit(toks, j); k > 0 && v >= 10 {
					lo, m = v, k
				}
			}
			if lo >= 0 {
				out = append(out, cardinal(uint64(hi*100+lo))...)
				i = j + m
				continue
			}
		}
		out = append(out, toks[i])
		i++
	}
	return out
}

// --- Chinese numbers ---

var hanDigits = map[rune]uint64{'〇': 0, '零': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4,
	'五': 5, '六': 6, '七': 7, '八': 8, '九': 9}

var hanUnits = map[rune]uint64{'十': 10, '百': 100, '千': 1000, '万': 1e4, '亿': 1e8}

func isHanNumeral(r rune) bool {
	_, d := hanDigits[r]
	_, u := hanUnits[r]
	return d || u
}

// chineseNumbers rewrites each run of 汉字 numerals as English words, so
// "四十二" and "42" share one canonical form.
func chineseNumbers(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); {
		if !isHanNumeral(rs[i]) {
			b.WriteRune(rs[i])
			i++
			continue
		}
		j := i
		for j < len(rs) && isHanNumeral(rs[j]) {
			j++
		}
		b.WriteByte(' ')
		b.WriteString(strings.Join(hanRunWords(rs[i:j]), " "))
		b.WriteByte(' ')
		i = j
	}
	return b.String()
}

func hanRunWords(run []rune) []string {
	positional := false
	for _, r := range run {
		if _, ok := hanUnits[r]; ok {
			positional = true
		}
	}
	if !positional {
		// 一九九九: a digit string.
		var ds strings.Builder
		for _, r := range run {
			ds.WriteByte(byte('0' + hanDigits[r]))
		}
		return integerWords(ds.String())
	}
	var total, section, number uint64
	for _, r := range run {
		if d, ok := hanDigits[r]; ok {
			number = d
			continue
		}
		u := hanUnits[r]
		if total+section+number > maxCardinal/u {
			return digitWordsHan(run)
		}
		switch u {
		case 1e8:
			total = (total + section + number) * u
			section, number = 0, 0
		case 1e4:
			total += (section + number) * u
			section, number = 0, 0
		default:
			if number == 0 {
				number = 1 // 十二 = 12
			}
			section += number * u
			number = 0
		}
	}
	return cardinal(total + section + number)
}

// digitWordsHan is the fallback for out-of-range runs: each rune read alone.
func digitWordsHan(run []rune) []string {
	var w []string
	for _, r := range run {
		if d, ok := hanDigits[r]; ok {
			w = append(w, ones[d])
		} else {
			w = append(w, cardinal(hanUnits[r])...)
		}
	}
	return w
}
