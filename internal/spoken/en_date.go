package spoken

import (
	"regexp"
	"strings"
)

// --- dates ---

var months = []string{"January", "February", "March", "April", "May", "June", "July", "August",
	"September", "October", "November", "December"}

var monthDays = []int{31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

func validDate(m, d int) bool { return m >= 1 && m <= 12 && d >= 1 && d <= monthDays[m-1] }

// sayDate reads a date in the requested style; year 0 means no year.
func sayDate(m, d, y int, style DateStyle) string {
	day := ordinalWords(uint64(d))
	var md string
	if style == DateUK {
		md = "the " + day + " of " + months[m-1]
	} else {
		md = months[m-1] + " " + day
	}
	switch {
	case y == 0:
		return md
	case style == DateISO:
		return sayYear(y) + ", " + md
	}
	return md + ", " + sayYear(y)
}

// sayYear reads a year as a speaker would: 1999 "nineteen ninety-nine", 1905
// "nineteen oh five", 1900 "nineteen hundred", 2000-2009 "two thousand …".
func sayYear(y int) string {
	hi, lo := y/100, y%100
	switch {
	case y < 1000 || y%1000 < 10:
		return spellN(uint64(y))
	case lo == 0:
		return spellN(uint64(hi)) + " hundred"
	case lo < 10:
		return spellN(uint64(hi)) + " oh " + spellN(uint64(lo))
	}
	return spellN(uint64(hi)) + " " + spellN(uint64(lo))
}

// sayDecade reads 1990s as "nineteen nineties".
func sayDecade(y int) string {
	hi, lo := y/100, y%100
	if lo == 0 {
		if hi%10 == 0 {
			return spellN(uint64(y/1000)) + " thousands"
		}
		return spellN(uint64(hi)) + " hundreds"
	}
	t := spellN(uint64(lo))
	return spellN(uint64(hi)) + " " + t[:len(t)-1] + "ies"
}

func enDateISO(s string, i int, o *Options) (Match, bool) {
	if !isDigit(s[i]) || !wordStart(s, i) || prevRune(s, i) == '-' || len(s)-i < 10 {
		return Match{}, false
	}
	t := s[i : i+10]
	if digits(t, 0) != 4 || t[4] != '-' || digits(t, 5) != 7 || t[7] != '-' || digits(t, 8) != 10 {
		return Match{}, false
	}
	y, _ := atoi(t[:4])
	m, _ := atoi(t[5:7])
	d, _ := atoi(t[8:10])
	j := i + 10
	if !wordEnd(s, j) || nextRune(s, j) == '-' {
		return Match{}, false
	}
	if !validDate(m, d) {
		return Match{End: j, Ambiguous: "date-shaped but not a calendar date"}, true
	}
	return Match{End: j, Spoken: sayDate(m, d, y, o.DateStyle)}, true
}

// enDateNumeric reads a/b/yyyy. Which of a and b is the month follows the
// date style; under iso a date valid both ways is ambiguous.
func enDateNumeric(s string, i int, o *Options) (Match, bool) {
	if !isDigit(s[i]) || !wordStart(s, i) || prevRune(s, i) == '/' {
		return Match{}, false
	}
	e1 := digits(s, i)
	if e1-i > 2 || e1 >= len(s) || s[e1] != '/' {
		return Match{}, false
	}
	e2 := digits(s, e1+1)
	if e2-e1-1 < 1 || e2-e1-1 > 2 || e2 >= len(s) || s[e2] != '/' {
		return Match{}, false
	}
	e3 := digits(s, e2+1)
	if e3-e2-1 != 4 || !wordEnd(s, e3) || nextRune(s, e3) == '/' {
		return Match{}, false
	}
	a, _ := atoi(s[i:e1])
	b, _ := atoi(s[e1+1 : e2])
	y, _ := atoi(s[e2+1 : e3])
	md, dm := validDate(a, b), validDate(b, a)
	switch {
	case !md && !dm:
		return Match{}, false
	case md && dm && a != b && o.DateStyle == DateISO:
		return Match{End: e3, Ambiguous: "month/day or day/month"}, true
	case md && (o.DateStyle != DateUK || !dm):
		return Match{End: e3, Spoken: sayDate(a, b, y, o.DateStyle)}, true
	}
	return Match{End: e3, Spoken: sayDate(b, a, y, o.DateStyle)}, true
}

var monthRe = regexp.MustCompile(`^(January|February|March|April|May|June|July|August|September|October|November|December|Jan\.|Feb\.|Mar\.|Apr\.|Jun\.|Jul\.|Aug\.|Sept?\.|Oct\.|Nov\.|Dec\.)`)

func monthAt(s string, i int) (int, int) {
	loc := monthRe.FindStringIndex(s[i:])
	if loc == nil {
		return 0, 0
	}
	name := s[i : i+loc[1]]
	for k, m := range months {
		if strings.HasPrefix(m, strings.TrimSuffix(name, ".")[:3]) {
			return k + 1, i + loc[1]
		}
	}
	return 0, 0
}

// dayAt reads a day of the month, with an optional ordinal suffix, at i.
func dayAt(s string, i int) (int, int) {
	e := digits(s, i)
	if e == i || e-i > 2 {
		return 0, 0
	}
	d, _ := atoi(s[i:e])
	if suf := ordinalSuffixLen(s, e); suf > 0 {
		e += suf
	}
	if !wordEnd(s, e) {
		return 0, 0
	}
	return d, e
}

// yearAfter reads ", 2026" or " 2026" at j, returning the year and its end.
func yearAfter(s string, j int, comma bool) (int, int) {
	k := j
	if comma && k < len(s) && s[k] == ',' {
		k++
	}
	if k >= len(s) || s[k] != ' ' {
		return 0, j
	}
	k++
	e := digits(s, k)
	if e-k != 4 || !wordEnd(s, e) {
		return 0, j
	}
	y, _ := atoi(s[k:e])
	return y, e
}

// enDateText reads "October 11, 2026", "Oct. 11" and "11 October 2026".
func enDateText(s string, i int, o *Options) (Match, bool) {
	if !wordStart(s, i) {
		return Match{}, false
	}
	style := o.DateStyle
	if m, e := monthAt(s, i); m > 0 && e < len(s) && s[e] == ' ' {
		d, e2 := dayAt(s, e+1)
		if d == 0 || !validDate(m, d) {
			return Match{}, false
		}
		y, e3 := yearAfter(s, e2, true)
		if style == DateUK {
			style = DateUS // keep the order as written
		}
		return Match{End: e3, Spoken: sayDate(m, d, y, style)}, true
	}
	if isDigit(s[i]) {
		d, e := dayAt(s, i)
		if d == 0 || e >= len(s) || s[e] != ' ' {
			return Match{}, false
		}
		m, e2 := monthAt(s, e+1)
		if m == 0 || !validDate(m, d) || !wordEnd(s, e2) && s[e2-1] != '.' {
			return Match{}, false
		}
		y, e3 := yearAfter(s, e2, false)
		return Match{End: e3, Spoken: sayDate(m, d, y, DateUK)}, true
	}
	return Match{}, false
}

// --- times ---

var ampmRe = regexp.MustCompile(`^ ?(?i:(a|p)(\.?)m)`)

// ampmAt reads am/pm (any case, one space before) at j. "p.m." consumes its
// final dot, and keeps it as a full stop when it ends a sentence; "pm." leaves
// the dot to the sentence.
func ampmAt(s string, j int) (string, int) {
	m := ampmRe.FindStringSubmatchIndex(s[j:])
	if m == nil {
		return "", j
	}
	e := j + m[1]
	w := strings.ToLower(s[j+m[2]:j+m[3]]) + " m"
	if m[5] > m[4] && e < len(s) && s[e] == '.' {
		e++
		if e == len(s) || s[e] == '\n' || s[e] == ' ' && capitalised(nextWord(s, e)) {
			w += "."
		}
		return w, e
	}
	if !wordEnd(s, e) {
		return "", j
	}
	return w, e
}

func enTime(s string, i int, _ *Options) (Match, bool) {
	if !isDigit(s[i]) || !wordStart(s, i) || prevRune(s, i) == ':' || prevRune(s, i) == '.' {
		return Match{}, false
	}
	e := digits(s, i)
	if e-i > 2 {
		return Match{}, false
	}
	h, _ := atoi(s[i:e])
	if e+2 < len(s) && s[e] == ':' && digits(s, e+1) == e+3 {
		mm, _ := atoi(s[e+1 : e+3])
		j := e + 3
		if h > 23 || mm > 59 || nextRune(s, j) == ':' {
			return Match{}, false
		}
		ap, j2 := ampmAt(s, j)
		if ap == "" && !wordEnd(s, j) || ap != "" && (h < 1 || h > 12) {
			return Match{}, false
		}
		w := spellN(uint64(h))
		switch {
		case mm == 0 && ap == "" && (h == 0 || h > 12):
			w += " hundred"
		case mm == 0 && ap == "":
			w += " o'clock"
		case mm == 0:
		case mm < 10:
			w += " oh " + spellN(uint64(mm))
		default:
			w += " " + spellN(uint64(mm))
		}
		if ap != "" {
			w += " " + ap
		}
		return Match{End: j2, Spoken: w}, true
	}
	ap, j := ampmAt(s, e)
	if ap == "" || h < 1 || h > 12 {
		return Match{}, false
	}
	return Match{End: j, Spoken: spellN(uint64(h)) + " " + ap}, true
}
