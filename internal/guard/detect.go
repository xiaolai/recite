package guard

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// match is one detection in the current text. edit says whether the action
// changes the text; repl is the replacement when it does.
type match struct {
	start, end int
	repl       string
	code       Code
	action     Action
	sev        Severity
	edit       bool
}

// detector finds the matches of one code in s, acting with act.
type detector struct {
	code Code
	find func(s string, act Action, o *Options) []match
}

// detectors run in this order on each pass. URL and e-mail run before the
// path and long-token rules, which would otherwise claim their text; emoji
// run before symbols so a kept emoji is not removed as a symbol.
var detectors = []detector{
	{Mark, findMark},
	{Control, findControl},
	{Markup, findMarkup},
	{URL, findURL},
	{Email, findEmail},
	{LongToken, findLongToken},
	{Path, findPath},
	{CodeToken, findCode},
	{Footnote, findFootnote},
	{Emoji, findEmoji},
	{Homoglyph, findHomoglyph},
	{Symbol, findSymbol},
	{Marker, findMarker},
	{RepeatPunct, findRepeatPunct},
	{Whitespace, findWhitespace},
}

// mk builds a match for code acting with act: fail and keep never edit.
func mk(code Code, act Action, start, end int, repl string, sev Severity) match {
	m := match{start: start, end: end, repl: repl, code: code, action: act, sev: sev}
	switch act {
	case ActFail:
		m.sev = Fatal
	case ActKeep, ActFlag, ActWarn:
	default:
		m.edit = true
	}
	return m
}

// regexMatches turns every match of re into a match, with repl computing the
// replacement (and ok=false to skip the occurrence).
func regexMatches(s string, re *regexp.Regexp, code Code, act Action, sev Severity, repl func(m string) (string, bool)) []match {
	var ms []match
	for _, loc := range re.FindAllStringIndex(s, -1) {
		r, ok := repl(s[loc[0]:loc[1]])
		if !ok {
			continue
		}
		ms = append(ms, mk(code, act, loc[0], loc[1], r, sev))
	}
	return ms
}

func drop(string) (string, bool) { return "", true }

// --- G-CONTROL ---

// isControl is design §7.6 G-CONTROL: control characters other than \n and
// \t, invisible format characters (zero-width, bidi controls, soft hyphen),
// line and paragraph separators, and variation selectors.
func isControl(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case unicode.IsControl(r), unicode.Is(unicode.Cf, r): // Cf: soft hyphen, word joiner, tags
		return true
	case r >= 0x200B && r <= 0x200F, r == 0x2060, r == 0xFEFF, r == 0x061C:
		return true
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		return true
	case r == 0x2028 || r == 0x2029:
		return true
	case r >= 0xFE00 && r <= 0xFE0F:
		return true
	}
	return false
}

// controlRepl keeps a separator where the control separated words: the
// whitespace-like controls become a space or a newline; the rest vanish.
func controlRepl(r rune) string {
	switch r {
	case '\r', '\v', '\f', 0x85, 0x2028, 0x2029:
		return "\n"
	}
	return ""
}

func findControl(s string, act Action, _ *Options) []match {
	var ms []match
	for i, r := range s {
		if isControl(r) {
			ms = append(ms, mk(Control, act, i, i+utf8.RuneLen(r), controlRepl(r), rules[Control].sev))
		}
	}
	return ms
}

// --- G-MARKUP ---

var (
	reTag      = regexp.MustCompile(`<!--[\s\S]*?-->|</?[A-Za-z][A-Za-z0-9-]*(?:\s[^<>]*)?/?>`)
	reBlockTag = regexp.MustCompile(`(?i)^</?(br|p|div|li|ul|ol|tr|td|th|h[1-6]|table|blockquote|hr|section|article)\b`)
	reEntity   = regexp.MustCompile(`&(?:#[0-9]{1,7}|#[xX][0-9A-Fa-f]{1,6}|[A-Za-z][A-Za-z0-9]{1,31});`)
	reMDEmph   = regexp.MustCompile(`\*\*|__|` + "`+")
	reMDHead   = regexp.MustCompile(`(?m)^#{1,6}[ \t]+`)
	reLatex    = regexp.MustCompile(`\\[A-Za-z]+(?:\{[^{}\n]*\})*|\$[^$\s](?:[^$\n]*[^$\s])?\$`)
)

func findMarkup(s string, act Action, o *Options) []match {
	sev := rules[Markup].sev
	var ms []match
	ms = append(ms, regexMatches(s, reTag, Markup, act, sev, func(m string) (string, bool) {
		if reBlockTag.MatchString(m) {
			return " ", true
		}
		return "", true
	})...)
	ms = append(ms, regexMatches(s, reEntity, Markup, act, sev, func(m string) (string, bool) {
		d := html.UnescapeString(m)
		return d, d != m
	})...)
	if !o.Markdown {
		ms = append(ms, regexMatches(s, reMDEmph, Markup, act, sev, drop)...)
		ms = append(ms, regexMatches(s, reMDHead, Markup, act, sev, drop)...)
	}
	// LaTeX is kept with a warning unless the action drops markup outright.
	latexAct := ActWarn
	if act == ActDrop || act == ActFail {
		latexAct = act
	}
	for _, loc := range reLatex.FindAllStringIndex(s, -1) {
		m := s[loc[0]:loc[1]]
		if strings.HasPrefix(m, "$") && !strings.ContainsAny(m, `\^_{}=`) {
			continue // "$3 and $5" is money, not maths
		}
		if prev, _ := utf8.DecodeLastRuneInString(s[:loc[0]]); m[0] == '\\' && (prev == ':' || unicode.IsLetter(prev) || unicode.IsDigit(prev)) {
			continue // C:\Users and dir\file are paths
		}
		ms = append(ms, mk(Markup, latexAct, loc[0], loc[1], "", Warn))
	}
	return resolve(ms)
}

// --- G-URL and G-EMAIL: the only rules that emit words ---

var (
	reURL   = regexp.MustCompile(`(?i)\b(?:https?://|ftp://|www\.)[^\s<>"'‹›]+`)
	reEmail = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+`)
)

// trimURL drops trailing sentence punctuation that is not part of the URL.
func trimURL(u string) string {
	return strings.TrimRight(u, ".,;:!?)]}")
}

// dotted speaks a host: "docs.example.com" → "docs dot example dot com".
func dotted(host string) string {
	parts := strings.FieldsFunc(host, func(r rune) bool { return r == '.' })
	return strings.Join(parts, " dot ")
}

func findURL(s string, act Action, _ *Options) []match {
	var ms []match
	for _, loc := range reURL.FindAllStringIndex(s, -1) {
		raw := trimURL(s[loc[0]:loc[1]])
		end := loc[0] + len(raw)
		full := raw
		if !strings.Contains(full, "://") {
			full = "http://" + full
		}
		u, err := url.Parse(full)
		host := ""
		if err == nil {
			host = strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		}
		if host == "" {
			ms = append(ms, mk(URL, act, loc[0], end, "", rules[URL].sev))
			continue
		}
		repl := dotted(host)
		if act == ActFull && u != nil {
			for _, seg := range strings.Split(strings.Trim(u.EscapedPath(), "/"), "/") {
				if seg = spokenSegment(seg); seg != "" {
					repl += " slash " + seg
				}
			}
		}
		if act == ActDrop {
			repl = ""
		}
		ms = append(ms, mk(URL, act, loc[0], end, repl, rules[URL].sev))
	}
	return ms
}

// spokenSegment keeps the letters and digits of a path segment as words.
func spokenSegment(seg string) string {
	return strings.Join(strings.FieldsFunc(seg, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}

var emailWords = strings.NewReplacer(".", " dot ", "@", " at ", "_", " underscore ", "+", " plus ", "%", " percent ")

func findEmail(s string, act Action, _ *Options) []match {
	return regexMatches(s, reEmail, Email, act, rules[Email].sev, func(m string) (string, bool) {
		if act == ActDrop {
			return "", true
		}
		return strings.Join(strings.Fields(emailWords.Replace(m)), " "), true
	})
}

// --- G-LONG-TOKEN, G-PATH, G-CODE ---

// A long token is a run of more than 32 characters of the alphabets hashes,
// base64 and keys use. Pure-letter runs are words (or German), so a digit or
// one of +/=_ is required.
var reLongToken = regexp.MustCompile(`[A-Za-z0-9+/=_.\-]{33,}`)

func findLongToken(s string, act Action, _ *Options) []match {
	return regexMatches(s, reLongToken, LongToken, act, rules[LongToken].sev, func(m string) (string, bool) {
		return "", strings.ContainsAny(m, "0123456789+/=_")
	})
}

// A path is a token with two or more '/' separators holding a letter, or a
// backslash path (C:\x, dir\file). One slash is "km/h" or "and/or".
var (
	rePathSlash = regexp.MustCompile(`(?:~|\.{1,2})?/?[\w.\-]+(?:/[\w.\-]+){2,}/?|(?:~|\.{1,2})?/[\w.\-]+/[\w.\-]+/?|~/[\w.\-]+`)
	rePathBack  = regexp.MustCompile(`\b[A-Za-z]:\\[\w.\-\\]*|\b\w+(?:\\[\w.\-]+)+`)
)

func findPath(s string, act Action, _ *Options) []match {
	repl := func(m string) (string, bool) {
		if !strings.ContainsFunc(m, unicode.IsLetter) {
			return "", false // 10/11/2026 is a date
		}
		if act == ActSpeak {
			return m, true
		}
		return "", true
	}
	ms := regexMatches(s, rePathSlash, Path, act, rules[Path].sev, repl)
	ms = append(ms, regexMatches(s, rePathBack, Path, act, rules[Path].sev, repl)...)
	// speak keeps the text; the spoken-form stage reads it.
	for i := range ms {
		if act == ActSpeak {
			ms[i].edit, ms[i].sev = false, Info
		}
	}
	return resolve(ms)
}

var reCode = regexp.MustCompile(`\b[A-Za-z_]\w*\(\)|\b[A-Za-z_]\w*(?:\.\w+){2,}\b|\b0[xX][0-9A-Fa-f]+\b|\b[A-Za-z]\w*_\w+\b`)

func findCode(s string, act Action, _ *Options) []match {
	return regexMatches(s, reCode, CodeToken, act, rules[CodeToken].sev, drop)
}

// --- G-FOOTNOTE ---

var (
	reFootnote    = regexp.MustCompile(`\[\d{1,3}\]`)
	reSuperscript = regexp.MustCompile(`[¹²³⁰⁴⁵⁶⁷⁸⁹]+`)
)

func findFootnote(s string, act Action, _ *Options) []match {
	ms := regexMatches(s, reFootnote, Footnote, act, rules[Footnote].sev, drop)
	// A superscript is a citation after punctuation or a word of four or more
	// letters; after a short word it is more likely a unit power (m², km²).
	for _, loc := range reSuperscript.FindAllStringIndex(s, -1) {
		if citationContext(s[:loc[0]]) {
			ms = append(ms, mk(Footnote, act, loc[0], loc[1], "", rules[Footnote].sev))
		}
	}
	return resolve(ms)
}

func citationContext(before string) bool {
	r, _ := utf8.DecodeLastRuneInString(before)
	if strings.ContainsRune(".,;:)]\"”’", r) {
		return true
	}
	n := 0
	for before != "" {
		r, size := utf8.DecodeLastRuneInString(before)
		if !unicode.IsLetter(r) {
			break
		}
		n++
		before = before[:len(before)-size]
	}
	return n >= 4
}

// --- G-EMOJI ---

func isEmoji(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF:
		return true
	case r >= 0x2600 && r <= 0x27BF:
		return true
	case r == 0x2B50 || r == 0x2B55 || r == 0x2B1B || r == 0x2B1C:
		return true
	case r >= 0x231A && r <= 0x231B, r >= 0x23E9 && r <= 0x23FA:
		return true
	}
	return false
}

func findEmoji(s string, act Action, _ *Options) []match {
	return findRuns(s, isEmoji, Emoji, act, "")
}

// findRuns turns each maximal run of runes satisfying in into one match.
func findRuns(s string, in func(rune) bool, code Code, act Action, repl string) []match {
	var ms []match
	start := -1
	for i, r := range s {
		switch {
		case in(r) && start < 0:
			start = i
		case !in(r) && start >= 0:
			ms = append(ms, mk(code, act, start, i, repl, rules[code].sev))
			start = -1
		}
	}
	if start >= 0 {
		ms = append(ms, mk(code, act, start, len(s), repl, rules[code].sev))
	}
	return ms
}

// --- G-SYMBOL ---

// spokenSymbols are the symbols the spoken-form stage (§7.7) is expected to
// read: currency, arithmetic, comparison, arrows, degrees, and the ASCII
// symbols.
// Every other symbol has no spoken rule yet and is removed with a warning.
const spokenSymbols = "+-×÷=≠<>≤≥±≈~^°℃℉%‰&#@*|$€£¥¢₩₹₽→"

func isUnspokenSymbol(r rune) bool {
	if !unicode.IsSymbol(r) || isEmoji(r) {
		return false
	}
	return !unicode.Is(unicode.Sc, r) && !strings.ContainsRune(spokenSymbols, r)
}

func findSymbol(s string, act Action, _ *Options) []match {
	return findRuns(s, isUnspokenSymbol, Symbol, act, "")
}

// --- G-HOMOGLYPH ---

// lookalikes maps Cyrillic and Greek letters to the Latin letter they are
// visually indistinguishable from.
var lookalikes = map[rune]rune{
	'а': 'a', 'е': 'e', 'о': 'o', 'р': 'p', 'с': 'c', 'у': 'y', 'х': 'x', 'і': 'i', 'ј': 'j', 'ѕ': 's', 'ԁ': 'd', 'ӏ': 'l', 'һ': 'h', 'ԛ': 'q', 'ԝ': 'w',
	'А': 'A', 'В': 'B', 'Е': 'E', 'К': 'K', 'М': 'M', 'Н': 'H', 'О': 'O', 'Р': 'P', 'С': 'C', 'Т': 'T', 'Х': 'X', 'У': 'Y', 'І': 'I', 'Ј': 'J', 'Ѕ': 'S',
	'ο': 'o', 'ν': 'v', 'Α': 'A', 'Β': 'B', 'Ε': 'E', 'Ζ': 'Z', 'Η': 'H', 'Ι': 'I', 'Κ': 'K', 'Μ': 'M', 'Ν': 'N', 'Ο': 'O', 'Ρ': 'P', 'Τ': 'T', 'Υ': 'Y', 'Χ': 'X',
}

var reWord = regexp.MustCompile(`[\p{L}\p{M}]+`)

func findHomoglyph(s string, act Action, _ *Options) []match {
	var ms []match
	for _, loc := range reWord.FindAllStringIndex(s, -1) {
		w := s[loc[0]:loc[1]]
		latin, other, mappable := 0, 0, true
		for _, r := range w {
			switch {
			case unicode.Is(unicode.Latin, r):
				latin++
			case unicode.Is(unicode.Cyrillic, r), unicode.Is(unicode.Greek, r):
				other++
				if _, ok := lookalikes[r]; !ok {
					mappable = false
				}
			}
		}
		if latin == 0 || other == 0 {
			continue
		}
		if !mappable {
			// Ambiguous: warn and keep (fail still fails).
			a := ActWarn
			if act == ActFail {
				a = ActFail
			}
			ms = append(ms, mk(Homoglyph, a, loc[0], loc[1], "", Warn))
			continue
		}
		fixed := strings.Map(func(r rune) rune {
			if l, ok := lookalikes[r]; ok {
				return l
			}
			return r
		}, w)
		ms = append(ms, mk(Homoglyph, act, loc[0], loc[1], fixed, Warn))
	}
	return ms
}

// --- G-MARKER ---

var reMarker = regexp.MustCompile(`<{3,}|>{3,}`)

func findMarker(s string, _ Action, _ *Options) []match {
	return regexMatches(s, reMarker, Marker, ActEscape, rules[Marker].sev, func(m string) (string, bool) {
		if m[0] == '<' {
			return strings.Repeat("‹", len(m)), true
		}
		return strings.Repeat("›", len(m)), true
	})
}

// --- G-REPEAT-PUNCT ---

// Two commas, three or more '!' or '?', four or more '.', or three or more '…'
// are beyond natural use. "!!!" → "!" follows the §7.6 example; "……" is the
// normal Chinese ellipsis and is kept.
var reRepeat = regexp.MustCompile(`,{2,}|，{2,}|!{3,}|！{3,}|\?{3,}|？{3,}|\.{4,}|…{3,}|。{2,}`)

func findRepeatPunct(s string, act Action, _ *Options) []match {
	return regexMatches(s, reRepeat, RepeatPunct, act, rules[RepeatPunct].sev, func(m string) (string, bool) {
		r, _ := utf8.DecodeRuneInString(m)
		if r == '.' {
			return "...", true
		}
		return string(r), true
	})
}

// --- G-WHITESPACE ---

var (
	reSpaceRun     = regexp.MustCompile(`[ \t]{2,}|\t`)
	reSpaceBefore  = regexp.MustCompile(`[ \t]+[,.;:!?，。；：！？、]`)
	reBlankLines   = regexp.MustCompile(`\n(?:[ \t]*\n){3,}`)
	reTrailingLine = regexp.MustCompile(`(?m)[ \t]+$`)
	reLeadingLine  = regexp.MustCompile(`(?m)^[ \t]+`)
)

func findWhitespace(s string, act Action, _ *Options) []match {
	sev := rules[Whitespace].sev
	var ms []match
	ms = append(ms, regexMatches(s, reBlankLines, Whitespace, act, sev, func(string) (string, bool) { return "\n\n", true })...)
	ms = append(ms, regexMatches(s, reSpaceBefore, Whitespace, act, sev, func(m string) (string, bool) {
		return strings.TrimLeft(m, " \t"), true
	})...)
	ms = append(ms, regexMatches(s, reTrailingLine, Whitespace, act, sev, drop)...)
	ms = append(ms, regexMatches(s, reLeadingLine, Whitespace, act, sev, drop)...)
	ms = append(ms, regexMatches(s, reSpaceRun, Whitespace, act, sev, func(m string) (string, bool) {
		return " ", m != " "
	})...)
	return resolve(ms)
}

// resolve sorts ms and drops any match overlapping an earlier-starting (or,
// at the same start, longer) one, so a pass's matches can be applied together.
func resolve(ms []match) []match {
	sortMatches(ms)
	out := ms[:0]
	end := -1
	for _, m := range ms {
		if m.start < end {
			continue
		}
		out = append(out, m)
		end = m.end
	}
	return out
}
