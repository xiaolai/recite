package spoken

import (
	"regexp"
	"strings"

	"github.com/xiaolai/recite/internal/numwords"
)

// English is the English column of design §7.7. Order matters where two
// rules can start at the same position: the more specific rule comes first
// (a phone number before a range, a date before a range, a unit or currency
// before a bare cardinal).
var English = Table{
	{"en.tech", enTech},
	{"en.phone", enPhone},
	{"en.date.iso", enDateISO},
	{"en.date.numeric", enDateNumeric},
	{"en.date.text", enDateText},
	{"en.digitgroups", enDigitGroups},
	{"en.elapsed", enElapsed},
	{"en.time", enTime},
	{"en.ratio", enRatio},
	{"en.duration", enDuration},
	{"en.currency.scale", enCurrencyScale},
	{"en.currency", enCurrency},
	{"en.ipv4", enIPv4},
	{"en.version", enVersion},
	{"en.ident", enIdent},
	{"en.year", enYear},
	{"en.range", enRange},
	{"en.fraction", enFraction},
	{"en.percent", enPercent},
	{"en.unit", enUnit},
	{"en.ordinal", enOrdinal},
	{"en.number", enNumber},
	{"en.roman", enRoman},
	{"en.abbrev", enAbbrev},
	{"en.symbol", enSymbol},
}

// --- tech terms ---

// techTerms is the built-in lexicon; the user lexicon (§7.8) overrides it.
var techTerms = []struct{ term, say string }{
	{"C++", "C plus plus"}, {"C#", "C sharp"}, {"F#", "F sharp"}, {".NET", "dot net"},
	{"iPadOS", "iPad O S"}, {"iOS", "i O S"}, {"macOS", "mac O S"}, {"watchOS", "watch O S"},
	{"tvOS", "T V O S"}, {"visionOS", "vision O S"}, {"24/7", "twenty-four seven"},
}

func enTech(s string, i int, _ *Options) (Match, bool) {
	for _, t := range techTerms {
		if !strings.HasPrefix(s[i:], t.term) {
			continue
		}
		j := i + len(t.term)
		if t.term[0] != '.' && !wordStart(s, i) || !wordEnd(s, j) || nextRune(s, j) == '+' || nextRune(s, j) == '#' {
			continue
		}
		return Match{End: j, Spoken: pad(s, i, j, t.say)}, true
	}
	return Match{}, false
}

// --- phone numbers ---

// The anchor sits outside the alternation: with one ^ per branch, Go's regexp does not treat
// the pattern as anchored and scans the whole remainder, making Run quadratic on long input.
var phoneRe = regexp.MustCompile(`^(?:(?:\+(\d{1,3})[ -]?)?(?:\((\d{3})\) ?|(\d{3})[-.])(\d{3})[-.](\d{4})|(\d{3})-(\d{4}))`)

func enPhone(s string, i int, _ *Options) (Match, bool) {
	if c := s[i]; c != '+' && c != '(' && !isDigit(c) || !wordStart(s, i) || prevRune(s, i) == '-' {
		return Match{}, false
	}
	m := phoneRe.FindStringSubmatchIndex(s[i:])
	if m == nil {
		return Match{}, false
	}
	j := i + m[1]
	if !wordEnd(s, j) || nextRune(s, j) == '-' {
		return Match{}, false
	}
	var groups []string
	for g := 1; g < len(m)/2; g++ {
		if m[2*g] < 0 {
			continue
		}
		ds := s[i+m[2*g] : i+m[2*g+1]]
		w := spellDigits(ds)
		if g == 1 {
			w = "plus " + w
		}
		groups = append(groups, w)
	}
	return Match{End: j, Spoken: strings.Join(groups, ", ")}, true
}

// --- currency ---

var currencies = map[string][3]string{ // singular, plural, minor unit
	"$": {"dollar", "dollars", "cents"}, "€": {"euro", "euros", "cents"},
	"£": {"pound", "pounds", "pence"}, "¥": {"yen", "yen", ""},
}

var scaleRe = regexp.MustCompile(`^(?: (thousand|million|billion|trillion)\b|(bn|tn|[kKmMbBT])\b)`)

var scaleWords = map[string]string{"k": "thousand", "K": "thousand", "m": "million", "M": "million",
	"b": "billion", "B": "billion", "bn": "billion", "T": "trillion", "tn": "trillion"}

// money is a currency amount at i: [-]SYM number [scale] or number SYM.
type money struct {
	n     number
	sym   string
	scale string
	end   int
}

func scanMoney(s string, i int) (money, bool) {
	var mo money
	j := i
	neg := false
	if s[j] == '-' && wordStart(s, j) {
		neg, j = true, j+1
	}
	for sym := range currencies {
		if strings.HasPrefix(s[j:], sym) {
			if j == i && !wordStart(s, i) {
				return mo, false
			}
			n, ok := scanNumber(s, j+len(sym), false)
			if !ok {
				return mo, false
			}
			n.Neg = neg
			mo = money{n: n, sym: sym, end: n.End}
			// scaleRe's \b knows only ASCII letters; wordEnd applies it to Unicode ("$5 bñ").
			if m := scaleRe.FindStringSubmatch(s[n.End:]); m != nil && wordEnd(s, n.End+len(m[0])) {
				mo.scale = m[1] + scaleWords[m[2]]
				mo.end = n.End + len(m[0])
			}
			if !wordEnd(s, mo.end) {
				return mo, false
			}
			return mo, true
		}
	}
	if neg || !isDigit(s[i]) || !wordStart(s, i) {
		return mo, false
	}
	n, ok := scanNumber(s, i, false)
	if !ok {
		return mo, false
	}
	k := n.End
	if k < len(s) && s[k] == ' ' {
		k++
	}
	for sym := range currencies {
		if strings.HasPrefix(s[k:], sym) && wordEnd(s, k+len(sym)) {
			return money{n: n, sym: sym, end: k + len(sym)}, true
		}
	}
	return mo, false
}

func (mo money) words() string {
	c := currencies[mo.sym]
	if mo.scale != "" {
		return mo.n.words() + " " + mo.scale + " " + c[1]
	}
	if mo.n.Frac == "" {
		if mo.n.isOne() {
			return "one " + c[0]
		}
		return mo.n.words() + " " + c[1]
	}
	if len(mo.n.Frac) == 2 && c[2] != "" {
		whole := mo.n
		whole.Frac = ""
		unit := c[1]
		if whole.Int == "1" {
			unit = c[0]
		}
		w := whole.words() + " " + unit
		if mo.n.Frac != "00" {
			cents := strings.TrimPrefix(mo.n.Frac, "0")
			w += " and " + spellInt(cents) + " " + c[2]
			if cents == "1" && c[2] == "cents" {
				w = strings.TrimSuffix(w, "s")
			}
		}
		return w
	}
	return mo.n.words() + " " + c[1]
}

func enCurrencyScale(s string, i int, _ *Options) (Match, bool) {
	mo, ok := scanMoney(s, i)
	if !ok || mo.scale == "" {
		return Match{}, false
	}
	return Match{End: mo.end, Spoken: mo.words()}, true
}

func enCurrency(s string, i int, _ *Options) (Match, bool) {
	mo, ok := scanMoney(s, i)
	if !ok || mo.scale != "" {
		return Match{}, false
	}
	return Match{End: mo.end, Spoken: mo.words()}, true
}

// --- IPv4 addresses ---

// enIPv4 reads a dotted quad digit by digit ("one nine two dot zero dot two dot
// one"), the way addresses are said aloud. Anything that is not exactly four
// groups of 0-255 without leading zeros is left to the version rule.
func enIPv4(s string, i int, _ *Options) (Match, bool) {
	if !wordStart(s, i) {
		return Match{}, false
	}
	var groups []string
	j := i
	for {
		e := digits(s, j)
		n, ok := atoi(s[j:e])
		if !ok || n > 255 || e-j > 1 && s[j] == '0' {
			return Match{}, false
		}
		groups = append(groups, strings.Join(numwords.Digits(s[j:e]), " "))
		j = e
		if j+1 < len(s) && s[j] == '.' && isDigit(s[j+1]) {
			j++
			continue
		}
		break
	}
	if len(groups) != 4 || !wordEnd(s, j) {
		return Match{}, false
	}
	return Match{End: j, Spoken: strings.Join(groups, " dot ")}, true
}

// --- versions ---

// enVersion reads v1.2.3 ("version one point two point three") and bare
// dotted numbers with two or more dots.
func enVersion(s string, i int, _ *Options) (Match, bool) {
	if !wordStart(s, i) {
		return Match{}, false
	}
	j, prefix := i, ""
	if s[i] == 'v' {
		j, prefix = i+1, "version "
	}
	var parts []string
	for {
		e := digits(s, j)
		if e == j {
			return Match{}, false
		}
		parts = append(parts, s[j:e])
		j = e
		if j+1 < len(s) && s[j] == '.' && isDigit(s[j+1]) {
			j++
			continue
		}
		break
	}
	if prefix == "" && len(parts) < 3 {
		return Match{}, false
	}
	ws := make([]string, len(parts))
	for k, p := range parts {
		if p == "0" && k > 0 {
			ws[k] = "oh"
		} else {
			ws[k] = spellInt(p)
		}
	}
	w := prefix + strings.Join(ws, " point ")
	if end, suf, ok := versionSuffix(s, j); ok {
		return Match{End: end, Spoken: w + suf}, true
	}
	if !wordEnd(s, j) {
		return Match{}, false
	}
	return Match{End: j, Spoken: w}, true
}

// versionSuffix reads a semver pre-release or build suffix at j ("-rc.3", "+build.5"), and
// the spoken "dash" a transcript writes for the hyphen ("4.12.0 dash RC.3"), so every
// variant reads the same: "dash R C point three". The first identifier must start with a
// letter, so "1.2.3-4" stays a hyphenated number.
func versionSuffix(s string, j int) (int, string, bool) {
	var w string
	switch {
	case strings.HasPrefix(s[j:], "-"):
		w, j = " dash", j+1
	case strings.HasPrefix(s[j:], "+"):
		w, j = " plus", j+1
	case strings.HasPrefix(s[j:], " dash "):
		w, j = " dash", j+len(" dash ")
	default:
		return 0, "", false
	}
	if !isASCIILetter(byteAt(s, j)) {
		return 0, "", false
	}
	for first := true; ; first = false {
		e := j
		for e < len(s) && (isASCIILetter(s[e]) || isDigit(s[e])) {
			e++
		}
		if e == j {
			return 0, "", false
		}
		if !first {
			w += " point"
		}
		id, ok := versionIdent(s[j:e])
		if !ok {
			return 0, "", false
		}
		w += " " + id
		j = e
		if byteAt(s, j) == '.' && (isASCIILetter(byteAt(s, j+1)) || isDigit(byteAt(s, j+1))) {
			j++
			continue
		}
		break
	}
	if !wordEnd(s, j) || byteAt(s, j) == '-' || byteAt(s, j) == '+' {
		return 0, "", false
	}
	return j, w, true
}

// releaseTags are pre-release words said as words; any other letter run of up to three
// letters is spelled ("rc" is "R C"), and a longer unknown word rejects the suffix.
var releaseTags = map[string]bool{"alpha": true, "beta": true, "build": true, "dev": true, "pre": true,
	"preview": true, "nightly": true, "snapshot": true, "canary": true, "final": true, "stable": true}

// versionIdent reads one dot-separated identifier: letter runs as tags or spelled
// capitals, digit runs as cardinals ("rc2" is "R C two").
func versionIdent(id string) (string, bool) {
	var out []string
	for k := 0; k < len(id); {
		e := k
		if isDigit(id[k]) {
			e = digits(id, k)
			out = append(out, spellInt(id[k:e]))
		} else {
			for e < len(id) && isASCIILetter(id[e]) {
				e++
			}
			low := strings.ToLower(id[k:e])
			switch {
			case releaseTags[low]:
				out = append(out, low)
			case e-k <= 3:
				out = append(out, strings.Join(strings.Split(strings.ToUpper(low), ""), " "))
			default:
				return "", false
			}
		}
		k = e
	}
	return strings.Join(out, " "), true
}

func isASCIILetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

// --- years ---

var yearCues = map[string]bool{"in": true, "since": true, "year": true, "until": true, "till": true,
	"from": true, "during": true, "circa": true, "by": true}

func isMonthName(w string) bool {
	for _, m := range months {
		if w == m || len(w) >= 3 && strings.HasPrefix(m, w) && len(w) <= 4 {
			return true
		}
	}
	return false
}

// enYear reads a four-digit year only next to a year cue, and a decade
// ("1990s") anywhere.
func enYear(s string, i int, _ *Options) (Match, bool) {
	if !isDigit(s[i]) || !wordStart(s, i) || prevRune(s, i) == '-' {
		return Match{}, false
	}
	e := digits(s, i)
	if e-i != 4 {
		return Match{}, false
	}
	y, _ := atoi(s[i:e])
	if y < 1000 || y > 2099 {
		return Match{}, false
	}
	if e < len(s) && s[e] == 's' && y%10 == 0 && wordEnd(s, e+1) {
		return Match{End: e + 1, Spoken: sayDecade(y)}, true
	}
	if !wordEnd(s, e) || e < len(s) && (s[e] == '-' || s[e] == '%') || strings.HasPrefix(s[e:], "–") {
		return Match{}, false
	}
	// "from 1013.25 to", "from 1013 km": a decimal or a measure is not a year.
	if c := byteAt(s, e); (c == '.' || c == ',' || c == ':') && isDigit(byteAt(s, e+1)) {
		return Match{}, false
	}
	if _, ok := enUnit(s, i, nil); ok {
		return Match{}, false
	}
	w := prevWord(s, i)
	if !yearCues[strings.ToLower(w)] && !isMonthName(w) {
		return Match{}, false
	}
	return Match{End: e, Spoken: sayYear(y)}, true
}

// --- ranges ---

// enRange reads 3–5 and 10-12 as "three to five". With a plain hyphen both
// ends must look like a range (ascending, no leading zero); two years read
// as years.
func enRange(s string, i int, _ *Options) (Match, bool) {
	if !isDigit(s[i]) || !wordStart(s, i) || prevRune(s, i) == '-' {
		return Match{}, false
	}
	a, ok := scanNumber(s, i, false)
	if !ok {
		return Match{}, false
	}
	k := a.End
	dash := ""
	for _, d := range []string{"–", "—", "-", " – ", " — "} {
		if strings.HasPrefix(s[k:], d) {
			dash = d
			break
		}
	}
	if dash == "" {
		return Match{}, false
	}
	b, ok := scanNumber(s, k+len(dash), false)
	if !ok || !wordEnd(s, b.End) || nextRune(s, b.End) == '-' || strings.HasPrefix(s[b.End:], "–") {
		return Match{}, false
	}
	if strings.HasPrefix(s[b.End:], ".") && isDigit(byteAt(s, b.End+1)) {
		return Match{}, false
	}
	av, aok := a.value()
	bv, bok := b.value()
	if !aok || !bok {
		return Match{}, false
	}
	if dash == "-" && (av >= bv || len(b.Int) > 1 && b.Int[0] == '0' || len(a.Int) > 1 && a.Int[0] == '0') {
		return Match{}, false
	}
	aw, bw := a.words(), b.words()
	if a.plain(s, i) && b.plain(s, k+len(dash)) && av >= 1000 && av <= 2099 && bv >= 1000 && bv <= 2099 && len(a.Int) == 4 && len(b.Int) == 4 {
		aw, bw = sayYear(int(av)), sayYear(int(bv))
	}
	return Match{End: b.End, Spoken: aw + " to " + bw}, true
}

// --- fractions ---

var measureWords = map[string]bool{
	"cup": true, "cups": true, "teaspoon": true, "teaspoons": true, "tablespoon": true, "tablespoons": true,
	"tsp": true, "tbsp": true, "inch": true, "inches": true, "mile": true, "miles": true, "pound": true,
	"pounds": true, "lb": true, "ounce": true, "ounces": true, "oz": true, "hour": true, "hours": true,
	"minute": true, "minutes": true, "second": true, "seconds": true, "day": true, "days": true,
	"week": true, "weeks": true, "month": true, "months": true, "year": true, "years": true,
	"liter": true, "liters": true, "litre": true, "litres": true, "gallon": true, "gallons": true,
	"pint": true, "pints": true, "quart": true, "quarts": true, "kg": true, "kilo": true, "kilos": true,
	"gram": true, "grams": true, "meter": true, "meters": true, "metre": true, "metres": true,
	"km": true, "foot": true, "feet": true, "yard": true, "yards": true, "acre": true, "acres": true,
	"of": true, "mile's": true, "point": true, "points": true, "share": true, "shares": true,
}

// fractionWords reads a/b: "one half", "three quarters", "two thirds".
func fractionWords(a, b int) string {
	var den string
	switch b {
	case 2:
		den = "half"
		if a != 1 {
			den = "halves"
		}
	case 4:
		den = "quarter"
		if a != 1 {
			den = "quarters"
		}
	default:
		den = ordinalWords(uint64(b))
		if a != 1 {
			den += "s"
		}
	}
	return spellN(uint64(a)) + " " + den
}

func enFraction(s string, i int, _ *Options) (Match, bool) {
	if !isDigit(s[i]) || !wordStart(s, i) || prevRune(s, i) == '/' {
		return Match{}, false
	}
	e := digits(s, i)
	whole := ""
	start := i
	// A mixed number "2 1/2".
	if e < len(s) && s[e] == ' ' && isDigit(byteAt(s, e+1)) {
		e2 := digits(s, e+1)
		if e2 < len(s) && s[e2] == '/' {
			whole, start, e = s[i:e], e+1, e2
		}
	}
	if e >= len(s) || s[e] != '/' || e-start > 3 {
		return Match{}, false
	}
	e3 := digits(s, e+1)
	if e3 == e+1 || e3-e-1 > 3 || !wordEnd(s, e3) || nextRune(s, e3) == '/' {
		return Match{}, false
	}
	a, _ := atoi(s[start:e])
	b, _ := atoi(s[e+1 : e3])
	if b < 2 || a < 1 || s[start] == '0' || s[e+1] == '0' || a >= b && b > 10 {
		return Match{}, false
	}
	w := fractionWords(a, b)
	if whole != "" {
		if a >= b {
			return Match{}, false
		}
		return Match{End: e3, Spoken: spellInt(whole) + " and " + w}, true
	}
	if validDate(a, b) && !measureWords[strings.ToLower(nextWord(s, e3))] {
		return Match{End: e3, Ambiguous: "fraction or date"}, true
	}
	return Match{End: e3, Spoken: w}, true
}

// --- percentages, units, ordinals, cardinals ---

func enPercent(s string, i int, _ *Options) (Match, bool) {
	n, ok := scanNumber(s, i, true)
	if !ok || !wordStart(s, i) || n.End >= len(s) || s[n.End] != '%' {
		return Match{}, false
	}
	return Match{End: n.End + 1, Spoken: n.words() + " percent"}, true
}

type unit struct {
	abbr, one, many string
	spaced          bool // may follow the number after one space
}

// units is ordered so a longer abbreviation is tried before its prefix.
var units = []unit{
	{"hPa", "hectopascal", "hectopascals", true}, {"kPa", "kilopascal", "kilopascals", true},
	{"MPa", "megapascal", "megapascals", true}, {"Pa", "pascal", "pascals", true},
	{"mbar", "millibar", "millibars", true},
	{"N·m", "newton meter", "newton meters", true}, {"N⋅m", "newton meter", "newton meters", true},
	{"Nm", "newton meter", "newton meters", true}, {"kN", "kilonewton", "kilonewtons", true},
	{"dB", "decibel", "decibels", true},
	{"µm", "micrometer", "micrometers", true}, {"μm", "micrometer", "micrometers", true},
	{"km/h", "kilometer per hour", "kilometers per hour", true},
	{"km/s", "kilometer per second", "kilometers per second", true},
	{"m/s", "meter per second", "meters per second", true},
	{"mph", "mile per hour", "miles per hour", true},
	{"kWh", "kilowatt hour", "kilowatt hours", true},
	{"mAh", "milliamp hour", "milliamp hours", true},
	{"Gbps", "gigabit per second", "gigabits per second", true},
	{"Mbps", "megabit per second", "megabits per second", true},
	{"GHz", "gigahertz", "gigahertz", true}, {"MHz", "megahertz", "megahertz", true},
	{"kHz", "kilohertz", "kilohertz", true}, {"Hz", "hertz", "hertz", true},
	{"kW", "kilowatt", "kilowatts", true}, {"MW", "megawatt", "megawatts", true},
	{"GW", "gigawatt", "gigawatts", true}, {"mW", "milliwatt", "milliwatts", true},
	{"kV", "kilovolt", "kilovolts", true}, {"mV", "millivolt", "millivolts", true},
	{"mA", "milliamp", "milliamps", true},
	{"TB", "terabyte", "terabytes", true}, {"GB", "gigabyte", "gigabytes", true},
	{"MB", "megabyte", "megabytes", true}, {"KB", "kilobyte", "kilobytes", true},
	{"kB", "kilobyte", "kilobytes", true},
	{"kg", "kilogram", "kilograms", true}, {"mg", "milligram", "milligrams", true},
	{"lbs", "pound", "pounds", true}, {"lb", "pound", "pounds", true},
	{"oz", "ounce", "ounces", true},
	{"tbsp", "tablespoon", "tablespoons", true}, {"tsp", "teaspoon", "teaspoons", true},
	{"km", "kilometer", "kilometers", true}, {"cm", "centimeter", "centimeters", true},
	{"mm", "millimeter", "millimeters", true}, {"nm", "nanometer", "nanometers", true},
	{"mi", "mile", "miles", false}, {"ft", "foot", "feet", true}, {"yd", "yard", "yards", true},
	{"ml", "milliliter", "milliliters", true}, {"mL", "milliliter", "milliliters", true},
	{"℃", "degree Celsius", "degrees Celsius", true}, {"°C", "degree Celsius", "degrees Celsius", true},
	{"℉", "degree Fahrenheit", "degrees Fahrenheit", true}, {"°F", "degree Fahrenheit", "degrees Fahrenheit", true},
	{"°", "degree", "degrees", false},
	{"ms", "millisecond", "milliseconds", true}, {"min", "minute", "minutes", true},
	{"hrs", "hour", "hours", true}, {"hr", "hour", "hours", true},
	{"g", "gram", "grams", false}, {"m", "meter", "meters", false}, {"L", "liter", "liters", false},
	// A spaced single capital is a unit only when no number follows: "455 W 57th St" is West.
	// A (amp), N (newton) and J (joule) stay out: "5 A" is a grade or a flat, "45 N" a latitude.
	{"W", "watt", "watts", true}, {"V", "volt", "volts", true},
	{"h", "hour", "hours", false},
	{"x", "times", "times", false},
}

func enUnit(s string, i int, _ *Options) (Match, bool) {
	if !wordStart(s, i) {
		return Match{}, false
	}
	n, ok := scanNumber(s, i, true)
	if !ok {
		return Match{}, false
	}
	for _, u := range units {
		k := n.End
		// "a 12.7-mm shaft": the hyphenated compound modifies a noun.
		hyphen := k < len(s) && s[k] == '-' && u.abbr != "x" && u.abbr != "°"
		if hyphen || u.spaced && k < len(s) && s[k] == ' ' {
			k++
		}
		if !strings.HasPrefix(s[k:], u.abbr) {
			continue
		}
		j := k + len(u.abbr)
		if !wordEnd(s, j) || nextRune(s, j) == '/' && !strings.Contains(u.abbr, "/") {
			continue
		}
		if k > n.End && len(u.abbr) == 1 && singleCapital(u.abbr) {
			if w := nextWord(s, j); byteAt(s, j) == ' ' && w != "" && isDigit(w[0]) {
				continue
			}
		}
		name := u.many
		if n.isOne() || hyphen || adjectival(s, i, j) {
			name = u.one
		}
		return Match{End: j, Spoken: n.words() + " " + name}, true
	}
	return Match{}, false
}

// singularDeterminers only ever precede a singular noun, so after them a measure is part
// of a compound modifier: "a 2 TB disk" is "a two terabyte disk". After "the" or with no
// determiner the measure may be the noun itself ("the 5 kg were lost"), so it stays plural.
var singularDeterminers = map[string]bool{"a": true, "an": true, "each": true, "every": true, "another": true}

// notNouns are words after a measure that show it is not modifying a noun ("a 2 TB of data").
var notNouns = map[string]bool{"of": true, "and": true, "or": true, "to": true, "in": true, "at": true,
	"for": true, "per": true, "from": true, "with": true, "by": true, "on": true, "than": true}

// adjectival reports a measure s[i:j] between a singular determiner and a following word.
func adjectival(s string, i, j int) bool {
	if !singularDeterminers[strings.ToLower(prevWord(s, i))] || byteAt(s, j) != ' ' {
		return false
	}
	w := nextWord(s, j)
	return w != "" && !isDigit(w[0]) && !notNouns[strings.ToLower(w)]
}

// enRatio reads "3:2" and "1:100" as "three to two" and "one to one hundred". Two digits
// after the colon are a clock time (en.time); a capitalised word before it may name a
// book and make it a reference ("Genesis 1:3"), which is not guessed.
func enRatio(s string, i int, _ *Options) (Match, bool) {
	if !isDigit(s[i]) || !wordStart(s, i) || prevRune(s, i) == ':' || prevRune(s, i) == '.' {
		return Match{}, false
	}
	a := digits(s, i)
	if a-i > 6 || byteAt(s, a) != ':' {
		return Match{}, false
	}
	b := digits(s, a+1)
	if r := b - a - 1; r == 0 || r == 2 || r > 6 || !wordEnd(s, b) ||
		(byteAt(s, b) == ':' || byteAt(s, b) == '.') && isDigit(byteAt(s, b+1)) {
		return Match{}, false
	}
	if p := prevWord(s, i); p != "" && capitalised(p) && !sentenceInitial(s, i) {
		return Match{End: b, Ambiguous: "a ratio or a reference"}, true
	}
	return Match{End: b, Spoken: spellInt(s[i:a]) + " to " + spellInt(s[a+1:b])}, true
}

// ordinalSuffixLen is the length of st/nd/rd/th at j when no letter follows.
func ordinalSuffixLen(s string, j int) int {
	if j+2 > len(s) {
		return 0
	}
	switch strings.ToLower(s[j : j+2]) {
	case "st", "nd", "rd", "th":
		if wordEnd(s, j+2) {
			return 2
		}
	}
	return 0
}

func enOrdinal(s string, i int, _ *Options) (Match, bool) {
	if !isDigit(s[i]) || !wordStart(s, i) {
		return Match{}, false
	}
	n, ok := scanNumber(s, i, false)
	if !ok || n.Frac != "" {
		return Match{}, false
	}
	suf := ordinalSuffixLen(s, n.End)
	v, vok := n.value()
	if suf == 0 || !vok || len(n.Int) > 1 && n.Int[0] == '0' {
		return Match{}, false
	}
	return Match{End: n.End + suf, Spoken: ordinalWords(v)}, true
}

// enNumber reads cardinals, decimals and negatives. A number glued to
// letters ("3D", "A4") or to a further ".digit" is left as written.
func enNumber(s string, i int, _ *Options) (Match, bool) {
	if !wordStart(s, i) {
		return Match{}, false
	}
	n, ok := scanNumber(s, i, true)
	if !ok || !wordEnd(s, n.End) {
		return Match{}, false
	}
	// Further ".digits" groups (an address-like "10,000.1.2") are read on as
	// "point" groups, so no digit is left for a second pass to find.
	w, j := n.words(), n.End
	for j+1 < len(s) && s[j] == '.' && isDigit(s[j+1]) {
		e := digits(s, j+1)
		w += " point " + spellDigits(s[j+1:e])
		j = e
	}
	if !wordEnd(s, j) || j+1 < len(s) && (s[j] == '/' || s[j] == ':') && isDigit(s[j+1]) {
		return Match{}, false
	}
	return Match{End: j, Spoken: w}, true
}
