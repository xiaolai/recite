package spoken

import "strings"

// --- Roman numerals ---

var regnalNames = map[string]bool{}

func init() {
	for _, n := range strings.Fields(`Henry Edward George William Richard Charles James Louis Elizabeth
		Mary Anne John Paul Pius Leo Benedict Gregory Innocent Clement Urban Boniface Philip Frederick
		Peter Alexander Nicholas Ivan Ramesses Rameses Victor Emmanuel Juan Carlos Ferdinand Isabella
		Constantine Otto Wilhelm Friedrich Ludwig Gustav Harald Olaf Christian Haakon Rama Selim Mehmed
		Murad Ahmed Napoleon Francis Joseph Felipe Alfonso Sancho Pedro Manuel Umberto Carl Willem
		Margrethe Frederik Oscar Sixtus Julius Adrian Martin Stephen Malcolm David Robert Edmund
		Harold Canute Ptolemy Antiochus Darius Xerxes Cyrus Thutmose Amenhotep Seti Sobhuza`) {
		regnalNames[n] = true
	}
}

var numberingWords = map[string]bool{
	"Chapter": true, "Part": true, "Volume": true, "Vol": true, "Book": true, "Act": true,
	"Scene": true, "Section": true, "Article": true, "Appendix": true, "Phase": true,
	"Episode": true, "Season": true, "Stage": true, "Level": true, "Class": true, "Type": true,
	"Psalm": true, "Canto": true, "Title": true, "Schedule": true, "Grade": true, "Round": true,
	"chapter": true, "part": true, "volume": true, "book": true, "act": true, "scene": true,
	"section": true, "article": true, "appendix": true, "phase": true, "episode": true, "season": true,
}

var romanValues = map[byte]int{'I': 1, 'V': 5, 'X': 10, 'L': 50, 'C': 100, 'D': 500, 'M': 1000}

// parseRoman returns the value of a canonical Roman numeral, or 0.
func parseRoman(t string) int {
	v := 0
	for k := 0; k < len(t); k++ {
		c := romanValues[t[k]]
		if k+1 < len(t) && c < romanValues[t[k+1]] {
			v -= c
		} else {
			v += c
		}
	}
	if v <= 0 || v >= 4000 || toRoman(v) != t {
		return 0
	}
	return v
}

func toRoman(v int) string {
	syms := []struct {
		v int
		s string
	}{{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"}, {100, "C"}, {90, "XC"}, {50, "L"}, {40, "XL"},
		{10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"}}
	var b strings.Builder
	for _, x := range syms {
		for v >= x.v {
			b.WriteString(x.s)
			v -= x.v
		}
	}
	return b.String()
}

func enRoman(s string, i int, _ *Options) (Match, bool) {
	if _, ok := romanValues[s[i]]; !ok || !wordStart(s, i) {
		return Match{}, false
	}
	j := i
	for j < len(s) && romanValues[s[j]] != 0 {
		j++
	}
	if !wordEnd(s, j) {
		return Match{}, false
	}
	v := parseRoman(s[i:j])
	if v == 0 {
		return Match{}, false
	}
	prev := prevWord(s, i)
	switch {
	case regnalNames[prev]:
		w := ordinalWords(uint64(v))
		return Match{End: j, Spoken: "the " + strings.ToUpper(w[:1]) + w[1:]}, true
	case numberingWords[strings.TrimSuffix(prev, ".")]:
		return Match{End: j, Spoken: spellN(uint64(v))}, true
	case prev == "War" && strings.HasSuffix(strings.TrimRight(s[:i], " "), "World War"):
		return Match{End: j, Spoken: spellN(uint64(v))}, true
	case j-i >= 2 && strings.Trim(s[i:j], "IVX") == "":
		return Match{End: j, Ambiguous: "Roman numeral outside a numbering or regnal context"}, true
	}
	return Match{}, false
}

// --- abbreviations ---

var abbreviations = []struct{ abbr, say string }{
	{"e.g.", "for example"}, {"i.e.", "that is"}, {"Mrs.", "Missus"}, {"Mr.", "Mister"},
	{"Ms.", "Miz"}, {"Prof.", "Professor"}, {"Jr.", "Junior"}, {"Sr.", "Senior"},
	{"Mt.", "Mount"}, {"Vol.", "Volume"}, {"vs.", "versus"}, {"approx.", "approximately"},
}

func enAbbrev(s string, i int, _ *Options) (Match, bool) {
	if !wordStart(s, i) || prevRune(s, i) == '.' {
		return Match{}, false
	}
	rest := s[i:]
	// The sentence keeps its full stop when the abbreviation ends it.
	keepStop := func(j int, w string) Match {
		if j >= len(s) || s[j] == '\n' {
			return Match{End: j, Spoken: w + "."}
		}
		return Match{End: j, Spoken: w}
	}
	switch {
	case strings.HasPrefix(rest, "St."):
		j := i + 3
		if capitalised(nextWord(s, j)) && j < len(s) && s[j] == ' ' {
			return Match{End: j, Spoken: "Saint"}, true
		}
		if p := prevWord(s, i); p != "" && (capitalised(p) || isDigit(p[0])) {
			return keepStop(j, "Street"), true
		}
		return Match{End: j, Ambiguous: "Saint or Street, with no name next to it"}, true
	case strings.HasPrefix(rest, "Dr."):
		j := i + 3
		if j < len(s) && s[j] == ' ' && capitalised(nextWord(s, j)) {
			return Match{End: j, Spoken: "Doctor"}, true
		}
		if p := prevWord(s, i); p != "" && capitalised(p) {
			return keepStop(j, "Drive"), true
		}
		return Match{End: j, Spoken: "Doctor"}, true
	case strings.HasPrefix(rest, "No. ") && isDigit(byteAt(s, i+4)), strings.HasPrefix(rest, "no. ") && isDigit(byteAt(s, i+4)):
		return Match{End: i + 3, Spoken: "number"}, true
	case strings.HasPrefix(rest, "etc") && wordEnd(s, i+3):
		return Match{End: i + 3, Spoken: "et cetera"}, true
	}
	for _, a := range abbreviations {
		if strings.HasPrefix(rest, a.abbr) {
			j := i + len(a.abbr)
			if wordEnd(s, j) {
				return keepStop(j, a.say), true
			}
		}
	}
	return Match{}, false
}

// --- math and symbols ---

var symbols = map[rune]string{
	'≥': "greater than or equal to", '≤': "less than or equal to", '×': "times", '÷': "divided by",
	'=': "equals", '≠': "not equal to", '≈': "approximately", '±': "plus or minus", '→': "to",
	'&': "and",
}

func enSymbol(s string, i int, _ *Options) (Match, bool) {
	r := nextRune(s, i)
	j := i + len(string(r))
	spaced := func() bool { return (i == 0 || s[i-1] == ' ') && (j >= len(s) || s[j] == ' ') }
	var w string
	switch {
	case symbols[r] != "":
		if r == '=' && (prevRune(s, i) == '=' || nextRune(s, j) == '=' || prevRune(s, i) == '!' ||
			prevRune(s, i) == '<' || prevRune(s, i) == '>') {
			return Match{}, false
		}
		w = symbols[r]
	case r == '+' && (spaced() || isDigit(byteAt(s, j)) && (!isWordRune(prevRune(s, i)) || isDigit(s[i-1]))):
		w = "plus"
	case r == '#' && isDigit(byteAt(s, j)) && !isWordRune(prevRune(s, i)):
		w = "number"
	case r == '%' && i > 0 && isDigit(s[i-1]):
		w = "percent"
	case r == '<' && spaced():
		w = "less than"
	case r == '>' && spaced():
		w = "greater than"
	default:
		return Match{}, false
	}
	return Match{End: j, Spoken: pad(s, i, j, w)}, true
}
