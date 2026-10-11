package spoken

import "strings"

// Durations, elapsed times and hyphenated digit identifiers, shared by the
// English and Chinese tables. The scanners find the shape; each language
// says it.

// durUnit ranks the components of a compound duration: days, hours, minutes,
// seconds. A compound must name them in strictly falling order.
type durUnit int

const (
	durDay durUnit = iota
	durHour
	durMinute
	durSecond
)

// durAbbrevs is ordered so a longer abbreviation is tried before its prefix.
var durAbbrevs = []struct {
	abbr string
	unit durUnit
}{
	{"hrs", durHour}, {"hr", durHour}, {"h", durHour},
	{"mins", durMinute}, {"min", durMinute}, {"m", durMinute},
	{"secs", durSecond}, {"sec", durSecond}, {"s", durSecond},
	{"d", durDay},
}

type durPart struct {
	n    number
	unit durUnit
}

// scanDuration reads a compound duration at i: two or more number+unit parts
// in falling unit order, glued or one space apart ("2h 37m", "5m 30s",
// "1h30m"). The compound is what makes "m" minutes: a lone "37m" is left to
// the unit rule (meters), and a lone "30s" may be a decade. Only the last part
// may carry a fraction. A unit may follow its number after one space ("2 h 3 m").
func scanDuration(s string, i int, start func(string, int) bool) ([]durPart, int, bool) {
	if !isDigit(s[i]) || !start(s, i) || prevRune(s, i) == '-' {
		return nil, 0, false
	}
	var parts []durPart
	j, end := i, i
	for {
		n, ok := scanNumber(s, j, false)
		if !ok {
			break
		}
		u, e, ok := durAbbrevAt(s, n.End)
		if !ok && byteAt(s, n.End) == ' ' {
			u, e, ok = durAbbrevAt(s, n.End+1)
		}
		if !ok || len(parts) > 0 && (u <= parts[len(parts)-1].unit || parts[len(parts)-1].n.Frac != "") {
			break
		}
		parts = append(parts, durPart{n, u})
		end = e
		if e < len(s) && s[e] == ' ' && isDigit(byteAt(s, e+1)) {
			j = e + 1
		} else if isDigit(byteAt(s, e)) {
			j = e
		} else {
			break
		}
	}
	// One part is enough for decimal seconds ("9.58s"): an integer "30s" may be a decade.
	single := len(parts) == 1 && parts[0].unit == durSecond && parts[0].n.Frac != "" && end == parts[0].n.End+1
	if len(parts) < 2 && !single || isWordRune(nextRune(s, end)) {
		return nil, 0, false
	}
	return parts, end, true
}

// durAbbrevAt reads a unit abbreviation at j that ends a word or is glued to
// the next part's digits.
func durAbbrevAt(s string, j int) (durUnit, int, bool) {
	for _, a := range durAbbrevs {
		if !strings.HasPrefix(s[j:], a.abbr) {
			continue
		}
		e := j + len(a.abbr)
		if r := nextRune(s, e); isWordRune(r) && !isDigit(byteAt(s, e)) || r == '/' {
			continue
		}
		return a.unit, e, true
	}
	return 0, 0, false
}

// elapsed is a stopwatch time with a fraction of a second: m:ss.f or
// h:mm:ss.f ("1:40.91", "1:02:03.5"). The fraction is what makes it a
// duration: no clock time is written with one, so the last field is seconds.
type elapsed struct {
	h, m int
	hasH bool
	sec  number // seconds with their fraction
	end  int
}

func scanElapsed(s string, i int, start func(string, int) bool) (elapsed, bool) {
	var el elapsed
	if !isDigit(s[i]) || !start(s, i) || prevRune(s, i) == ':' || prevRune(s, i) == '.' {
		return el, false
	}
	var fields []int
	j := i
	for {
		e := digits(s, j)
		if e == j || len(fields) == 0 && e-j > 2 || len(fields) > 0 && e-j != 2 {
			return el, false
		}
		v, _ := atoi(s[j:e])
		fields = append(fields, v)
		j = e
		if len(fields) < 3 && byteAt(s, j) == ':' && isDigit(byteAt(s, j+1)) {
			j++
			continue
		}
		break
	}
	if len(fields) < 2 || byteAt(s, j) != '.' || !isDigit(byteAt(s, j+1)) {
		return el, false
	}
	f := digits(s, j+1)
	if isWordRune(nextRune(s, f)) || (byteAt(s, f) == '.' || byteAt(s, f) == ':') && isDigit(byteAt(s, f+1)) {
		return el, false
	}
	for _, v := range fields[1:] {
		if v > 59 {
			return el, false
		}
	}
	last := fields[len(fields)-1]
	el.sec = number{Int: itoa(last), Frac: s[j+1 : f]}
	el.m = fields[len(fields)-2]
	if len(fields) == 3 {
		el.h, el.hasH = fields[0], true
	}
	el.end = f
	return el, true
}

// scanDigitGroups reads three or more digit groups joined by single hyphens,
// an identifier such as an ISBN ("978-0-13-468599-1", "0-8044-2957-X") or a
// part number. An ISBN-10 check character X may end it. Such a run is not a
// quantity, so it is read digit by digit; dates and phone numbers of the same
// shape are claimed first by their own rules.
func scanDigitGroups(s string, i int, start func(string, int) bool) ([]string, int, bool) {
	if !isDigit(s[i]) || !start(s, i) || prevRune(s, i) == '-' {
		return nil, 0, false
	}
	var groups []string
	j := i
	for {
		e := digits(s, j)
		if e == j {
			if len(groups) >= 3 && (s[j] == 'X' || s[j] == 'x') {
				groups, e = append(groups, "X"), j+1
				j = e
			}
			break
		}
		groups = append(groups, s[j:e])
		j = e
		if byteAt(s, j) == '-' && (isDigit(byteAt(s, j+1)) || byteAt(s, j+1) == 'X' || byteAt(s, j+1) == 'x') {
			j++
			continue
		}
		break
	}
	// Two groups only as a long serial and its check digit ("482917-3", ISO 6346).
	checkDigit := len(groups) == 2 && len(groups[0]) >= 6 && len(groups[1]) == 1
	if len(groups) < 3 && !checkDigit || isWordRune(nextRune(s, j)) || byteAt(s, j) == '-' && isDigit(byteAt(s, j+1)) ||
		byteAt(s, j) == '.' && isDigit(byteAt(s, j+1)) {
		return nil, 0, false
	}
	return groups, j, true
}

// scanSpacedGroups reads four or more digit groups of four separated by single spaces, the
// last of one to four digits: an account or card number ("DE89 3704 0044 0532 0130 00",
// "4111 1111 1111 1111"). Like a hyphenated run it is not a quantity.
func scanSpacedGroups(s string, i int, start func(string, int) bool) ([]string, int, bool) {
	if !isDigit(s[i]) || !start(s, i) {
		return nil, 0, false
	}
	var groups []string
	j := i
	for {
		e := digits(s, j)
		groups = append(groups, s[j:e])
		j = e
		if len(s[j-len(groups[len(groups)-1]):j]) != 4 || byteAt(s, j) != ' ' || !isDigit(byteAt(s, j+1)) {
			break
		}
		j++
	}
	last := len(groups[len(groups)-1])
	if len(groups) < 4 || last > 4 || isWordRune(nextRune(s, j)) ||
		(byteAt(s, j) == '.' || byteAt(s, j) == ',' || byteAt(s, j) == '-') && isDigit(byteAt(s, j+1)) {
		return nil, 0, false
	}
	return groups, j, true
}

// singleDigits reports a run like "3-5-7": a code, a list or a score, with no
// longer group to show it is an identifier, so it is left as written (N-AMBIGUOUS).
func singleDigits(groups []string) bool {
	for _, g := range groups {
		if len(g) > 1 {
			return false
		}
	}
	return true
}

// --- English ---

var enDurNames = [...][2]string{durDay: {"day", "days"}, durHour: {"hour", "hours"},
	durMinute: {"minute", "minutes"}, durSecond: {"second", "seconds"}}

func enCount(n number, u durUnit, adj bool) string {
	if n.isOne() || adj {
		return "one " + enDurNames[u][0]
	}
	return n.words() + " " + enDurNames[u][1]
}

func enDuration(s string, i int, _ *Options) (Match, bool) {
	parts, end, ok := scanDuration(s, i, wordStart)
	if !ok {
		return Match{}, false
	}
	adj := adjectival(s, i, end)
	ws := make([]string, len(parts))
	for k, p := range parts {
		ws[k] = enCount(p.n, p.unit, adj)
	}
	return Match{End: end, Spoken: strings.Join(ws, " ")}, true
}

// enElapsed reads "1:40.91" as "one minute forty point nine one seconds".
// Zero hours or minutes are not said.
func enElapsed(s string, i int, _ *Options) (Match, bool) {
	el, ok := scanElapsed(s, i, wordStart)
	if !ok {
		return Match{}, false
	}
	var ws []string
	if el.hasH && el.h > 0 {
		ws = append(ws, enCount(number{Int: itoa(el.h)}, durHour, false))
	}
	if el.m > 0 {
		ws = append(ws, enCount(number{Int: itoa(el.m)}, durMinute, false))
	}
	ws = append(ws, el.sec.words()+" seconds")
	return Match{End: el.end, Spoken: strings.Join(ws, " ")}, true
}

// enDigitGroups reads "978-0-13-468599-1" as "nine seven eight, oh, one three, …".
func enDigitGroups(s string, i int, _ *Options) (Match, bool) {
	groups, end, ok := scanDigitGroups(s, i, wordStart)
	if !ok {
		if groups, end, ok = scanSpacedGroups(s, i, wordStart); !ok {
			return Match{}, false
		}
	}
	if singleDigits(groups) {
		return Match{End: end, Ambiguous: "a code or a list"}, true
	}
	ws := make([]string, len(groups))
	for k, g := range groups {
		ws[k] = g
		if g != "X" {
			ws[k] = spellDigits(g)
		}
	}
	return Match{End: end, Spoken: strings.Join(ws, ", ")}, true
}

// --- Chinese ---

var zhDurNames = [...]string{durDay: "天", durHour: "小时", durMinute: "分钟", durSecond: "秒"}

func zhCount(n number, unit string) string {
	if n.Int == "2" && n.Frac == "" {
		return "两" + unit
	}
	return zhNum(n, false) + unit
}

func zhDuration(s string, i int, _ *Options) (Match, bool) {
	parts, end, ok := scanDuration(s, i, zhStart)
	if !ok {
		return Match{}, false
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(zhCount(p.n, zhDurNames[p.unit]))
	}
	return Match{End: end, Spoken: b.String()}, true
}

// zhElapsed reads "1:40.91" as 一分四十点九一秒.
func zhElapsed(s string, i int, _ *Options) (Match, bool) {
	el, ok := scanElapsed(s, i, zhStart)
	if !ok {
		return Match{}, false
	}
	w := ""
	if el.hasH && el.h > 0 {
		w += zhCount(number{Int: itoa(el.h)}, "小时")
	}
	if el.m > 0 {
		w += zhInt(itoa(el.m)) + "分"
	}
	return Match{End: el.end, Spoken: w + zhNum(el.sec, false) + "秒"}, true
}

func zhDigitGroups(s string, i int, _ *Options) (Match, bool) {
	groups, end, ok := scanDigitGroups(s, i, zhStart)
	if !ok {
		groups, end, ok = scanSpacedGroups(s, i, zhStart)
	}
	if !ok || singleDigits(groups) {
		return Match{}, false // "3-5-7" is left to zh.range, which reports it
	}
	ws := make([]string, len(groups))
	for k, g := range groups {
		ws[k] = g
		if g != "X" {
			ws[k] = zhDigits(g, "零")
		}
	}
	return Match{End: end, Spoken: strings.Join(ws, " ")}, true
}
