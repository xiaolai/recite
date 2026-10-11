package spoken

import (
	"slices"
	"strings"
	"unicode"
)

// MarkRule is the rule ID a speech mark of the given kind records ("mark.say").
func MarkRule(kind string) RuleID { return RuleID("mark." + kind) }

// markCategories are the rules a {{num}} or {{date}} mark may use, per language.
var markCategories = map[string]map[string][]RuleID{
	"num": {
		"en": {"en.currency.scale", "en.currency", "en.percent", "en.unit", "en.ordinal", "en.number"},
		"zh": {"zh.currency", "zh.percent", "zh.temp.below-zero", "zh.temp", "zh.unit", "zh.number"},
	},
	"date": {
		"en": {"en.date.iso", "en.date.numeric", "en.date.text"},
		"zh": {"zh.date.iso"},
	},
}

// MarkSpoken is the spoken form of a text-bearing speech mark (design §7.8) in
// language lang. A non-empty ambiguous means the mark cannot be read as asked;
// the text is then left as written and reported as N-AMBIGUOUS.
//
// say speaks its value; spell reads each letter and digit; digits reads each
// digit; num, date and fraction force that category's rules on the text;
// verbatim, hint, emph and lang keep the text as written.
func MarkSpoken(kind, value, text, lang string, style DateStyle) (spokenForm, ambiguous string) {
	base, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(lang)), "-")
	switch kind {
	case "say":
		return strings.TrimSpace(value), ""
	case "spell":
		if w := spell(text); w != "" {
			return w, ""
		}
		return "", "no letter or digit to spell"
	case "digits":
		if !strings.ContainsFunc(text, isASCIIDigit) {
			return "", "no digit to read"
		}
		return digitWords(text, base), ""
	case "fraction":
		if w, ok := forceFraction(strings.TrimSpace(text), base); ok {
			return w, ""
		}
		return "", "not a fraction such as 1/2 or 2 3/4"
	case "date":
		if w, ok := forceShortDate(strings.TrimSpace(text), base, style); ok {
			return w, ""
		}
		return forceCategory(kind, text, base, style)
	case "num":
		return forceCategory(kind, text, base, style)
	}
	return text, ""
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

// spell reads text letter by letter: "API" → "A P I". Spaces and punctuation
// are not read.
func spell(text string) string {
	var out []string
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, string(r))
		}
	}
	return strings.Join(out, " ")
}

// digitWords reads each ASCII digit as a word ("two zero two six", 二零二六);
// letters are read one by one, everything else is skipped.
func digitWords(text, lang string) string {
	var out []string
	for _, r := range text {
		switch {
		case isASCIIDigit(r) && lang == "zh":
			out = append(out, hanDigit[r-'0'])
		case isASCIIDigit(r):
			out = append(out, spellN(uint64(r-'0')))
		case unicode.IsLetter(r):
			out = append(out, string(r))
		}
	}
	if lang == "zh" {
		return strings.Join(out, "")
	}
	return strings.Join(out, " ")
}

// forceFraction reads "a/b" or "w a/b" as a fraction, whatever the context.
func forceFraction(t, lang string) (string, bool) {
	whole := ""
	if w, rest, ok := strings.Cut(t, " "); ok {
		whole, t = w, strings.TrimSpace(rest)
		if !allDigits(whole) {
			return "", false
		}
	}
	as, bs, ok := strings.Cut(t, "/")
	if !ok || !allDigits(as) || !allDigits(bs) || len(as) > 3 || len(bs) > 3 {
		return "", false
	}
	a, _ := atoi(as)
	b, _ := atoi(bs)
	if a < 1 || b < 2 {
		return "", false
	}
	if lang == "zh" {
		w := zhInt(bs) + "分之" + zhInt(as)
		if whole != "" {
			w = zhInt(whole) + "又" + w
		}
		return w, true
	}
	w := fractionWords(a, b)
	if whole != "" {
		w = spellInt(whole) + " and " + w
	}
	return w, true
}

// forceShortDate reads "m/d" (or "d/m" under the uk style) as a date.
func forceShortDate(t, lang string, style DateStyle) (string, bool) {
	as, bs, ok := strings.Cut(t, "/")
	if !ok || !allDigits(as) || !allDigits(bs) || len(as) > 2 || len(bs) > 2 {
		return "", false
	}
	m, _ := atoi(as)
	d, _ := atoi(bs)
	if style == DateUK {
		m, d = d, m
	}
	if !validDate(m, d) {
		return "", false
	}
	if lang == "zh" {
		return zhInt(itoa(m)) + "月" + zhInt(itoa(d)) + "日", true
	}
	return sayDate(m, d, 0, style), true
}

// forceCategory runs only the category's rules over the whole text; it must
// rewrite something and leave nothing ambiguous.
func forceCategory(kind, text, lang string, style DateStyle) (string, string) {
	ids := markCategories[kind][lang]
	if ids == nil {
		return "", "no " + kind + " rules for this language"
	}
	var t Table
	for _, r := range TableFor(lang) {
		if slices.Contains(ids, r.ID) {
			t = append(t, r)
		}
	}
	res, err := Run(text, t, Options{DateStyle: style})
	if err != nil || len(res.Findings) > 0 || len(res.Rewrites()) == 0 {
		return "", "cannot be read as a " + map[string]string{"num": "number", "date": "date"}[kind]
	}
	return res.Spoken, ""
}

func allDigits(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return !isASCIIDigit(r) })
}
