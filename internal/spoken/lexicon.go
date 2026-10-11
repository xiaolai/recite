package spoken

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/language"
)

// Rule IDs of the two lexicon modes (design §7.8). Their segments mark where an entry
// matched, so the planner can find the spans again.
const (
	RuleLexiconSay  RuleID = "lexicon.say"
	RuleLexiconHint RuleID = "lexicon.hint"
)

// Entry is one lexicon entry: exactly one of Say (rewrite the spoken form) and Hint (keep
// the text, add a pronunciation instruction). Lang, when set, limits the entry to text in
// that language.
type Entry struct {
	Term string `json:"-"`
	Say  string `json:"say,omitempty"`
	Hint string `json:"hint,omitempty"`
	Lang string `json:"lang,omitempty"`
}

// Lexicon is a validated pronunciation table, sorted by term, terms unique.
type Lexicon []Entry

// ParseLexicon validates a decoded TOML [lexicon] table: term → { say = … } or
// { hint = …, lang = … }. Problems are joined in term order.
func ParseLexicon(table map[string]any) (Lexicon, error) {
	terms := make([]string, 0, len(table))
	for t := range table {
		terms = append(terms, t)
	}
	slices.Sort(terms)
	var out Lexicon
	var errs []error
	for _, term := range terms {
		body, ok := table[term].(map[string]any)
		if !ok {
			errs = append(errs, fmt.Errorf("lexicon %q: want a table such as { say = \"…\" } or { hint = \"…\" }", term))
			continue
		}
		e := Entry{Term: term}
		for _, k := range sortedFields(body) {
			s, ok := body[k].(string)
			if !ok {
				errs = append(errs, fmt.Errorf("lexicon %q: %s must be a string", term, k))
				continue
			}
			switch k {
			case "say":
				e.Say = s
			case "hint":
				e.Hint = s
			case "lang":
				e.Lang = s
			default:
				errs = append(errs, fmt.Errorf("lexicon %q: unknown field %q (fields: say, hint, lang)", term, k))
			}
		}
		if err := e.validate(); err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, e)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

func sortedFields(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (e Entry) validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("lexicon %q: %s", e.Term, fmt.Sprintf(format, a...))
	}
	if err := checkLexText(e.Term); err != "" {
		return bad("the term %s", err)
	}
	if !strings.ContainsFunc(e.Term, lexWordRune) {
		return bad("the term needs a letter or digit")
	}
	switch {
	case (e.Say == "") == (e.Hint == ""):
		return bad("set exactly one of say and hint")
	case e.Say != "":
		if err := checkLexText(e.Say); err != "" {
			return bad("say %s", err)
		}
		if !strings.ContainsFunc(e.Say, lexWordRune) {
			return bad("say needs a letter or digit to speak")
		}
	default:
		if err := checkLexText(e.Hint); err != "" {
			return bad("hint %s", err)
		}
		// The hint is quoted inside the instruction; a quote would end it early.
		if strings.ContainsAny(e.Term+e.Hint, `"\`) {
			return bad("a hint entry cannot hold \" or \\")
		}
	}
	if e.Lang != "" {
		if _, err := language.Parse(e.Lang); err != nil {
			return bad("lang %q: want a BCP 47 language tag such as en or zh", e.Lang)
		}
	}
	return nil
}

// checkLexText rejects text that cannot be a lexicon term or value: empty, invalid UTF-8,
// surrounding space, line breaks or other control characters, and speech-mark braces.
func checkLexText(s string) string {
	switch {
	case s == "":
		return "is empty"
	case !utf8.ValidString(s):
		return "is not valid UTF-8"
	case strings.TrimSpace(s) != s:
		return "has leading or trailing space"
	case strings.ContainsFunc(s, unicode.IsControl):
		return "holds a line break or control character"
	case strings.Contains(s, "{{") || strings.Contains(s, "}}"):
		return "holds speech-mark braces"
	}
	return ""
}

// Instruction is the line a hint entry adds to its chunk's instructions (design §7.8).
func (e Entry) Instruction() string {
	return `Pronounce "` + e.Term + `" as "` + e.Hint + `".`
}

// lexiconJSON is the canonical text form: an object keyed by term, keys sorted.
type lexiconJSON map[string]Entry

// String is the canonical text form of the lexicon (sorted JSON), the value config layers
// carry; ParseLexiconText reads it back.
func (l Lexicon) String() string {
	m := make(lexiconJSON, len(l))
	for _, e := range l {
		m[e.Term] = e
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(fmt.Sprintf("lexicon: encode: %v", err)) // only strings
	}
	return string(b)
}

// ParseLexiconText reads the canonical text form; "" is the empty lexicon.
func ParseLexiconText(s string) (Lexicon, error) {
	if s == "" {
		return nil, nil
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("lexicon: %w", err)
	}
	table := make(map[string]any, len(raw))
	for k, v := range raw {
		table[k] = v
	}
	return ParseLexicon(table)
}

// Lookup returns the entry of a term.
func (l Lexicon) Lookup(term string) (Entry, bool) {
	i, ok := slices.BinarySearchFunc(l, term, func(e Entry, t string) int { return strings.Compare(e.Term, t) })
	if !ok {
		return Entry{}, false
	}
	return l[i], true
}

// LexMatch is one place a lexicon entry matched.
type LexMatch struct {
	Range
	Entry Entry
}

// Match finds the entries of the lexicon in s (design §7.8, SPK-08). A term matches as a
// whole word where it is written in a spaced script, and as a plain substring where it is
// Chinese or Japanese: each edge of the term that is a letter, mark or digit of a spaced
// script needs a neighbour that is not one. Matching is case-sensitive. When matches
// overlap, the longest wins, then the earliest. A match never overlaps a skip range (speech
// marks) or crosses an empty one (a pause). The result is in text order.
func (l Lexicon) Match(s, lang string, skip []Range) []LexMatch {
	if len(l) == 0 {
		return nil
	}
	byFirst := map[byte][]int{}
	for i, e := range l {
		if e.applies(lang) {
			byFirst[e.Term[0]] = append(byFirst[e.Term[0]], i)
		}
	}
	type cand struct{ start, end, entry int }
	var cs []cand
	for i := 0; i < len(s); {
		for _, k := range byFirst[s[i]] {
			t := l[k].Term
			j := i + len(t)
			if strings.HasPrefix(s[i:], t) && lexBounded(s, i, j) && !overlapsAny(skip, i, j) {
				cs = append(cs, cand{i, j, k})
			}
		}
		_, n := utf8.DecodeRuneInString(s[i:])
		i += n
	}
	slices.SortStableFunc(cs, func(a, b cand) int {
		if d := (b.end - b.start) - (a.end - a.start); d != 0 {
			return d
		}
		return a.start - b.start
	})
	// taken marks the bytes of chosen matches, so each overlap test costs the term's length,
	// not the number of matches so far: a lexicon matching everywhere in a large text stays
	// linear.
	taken := make([]bool, len(s))
	var out []LexMatch
	for _, c := range cs {
		if slices.Contains(taken[c.start:c.end], true) {
			continue
		}
		for k := c.start; k < c.end; k++ {
			taken[k] = true
		}
		out = append(out, LexMatch{Range: Range{c.start, c.end}, Entry: l[c.entry]})
	}
	slices.SortFunc(out, func(a, b LexMatch) int { return a.Start - b.Start })
	return out
}

// applies reports whether the entry is used for text in language lang ("" when unknown):
// an entry with a lang only for text of that base language.
func (e Entry) applies(lang string) bool {
	if e.Lang == "" {
		return true
	}
	return baseLang(e.Lang) == baseLang(lang)
}

func baseLang(tag string) string {
	b, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
	return b
}

// Overrides are the matches of the lexicon as spoken-form overrides: a say entry speaks its
// value, a hint entry keeps its text (and so is protected from the rules).
func (l Lexicon) Overrides(s, lang string, skip []Range) []Override {
	var out []Override
	for _, m := range l.Match(s, lang, skip) {
		ov := Override{Range: m.Range, Spoken: m.Entry.Term, Rule: RuleLexiconHint}
		if m.Entry.Say != "" {
			ov.Spoken, ov.Rule = m.Entry.Say, RuleLexiconSay
		}
		out = append(out, ov)
	}
	return out
}

// lexWordRune is a rune that continues a word: a letter, a combining mark or a digit.
func lexWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r) }

// unspaced is a script written without spaces between words, where a term matches as a
// substring.
func unspaced(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana)
}

// WordBounded reports whether s[i:j] stands as a whole word by the lexicon's rule: each edge
// that is a letter, mark or digit of a spaced script needs a neighbour that is not one.
func WordBounded(s string, i, j int) bool { return lexBounded(s, i, j) }

// lexBounded reports whether s[i:j] stands as a whole word at each edge that needs one.
func lexBounded(s string, i, j int) bool {
	first, _ := utf8.DecodeRuneInString(s[i:j])
	last, _ := utf8.DecodeLastRuneInString(s[i:j])
	if lexWordRune(first) && !unspaced(first) && i > 0 {
		if p, _ := utf8.DecodeLastRuneInString(s[:i]); lexWordRune(p) && !unspaced(p) {
			return false
		}
	}
	if lexWordRune(last) && !unspaced(last) && j < len(s) {
		if n, _ := utf8.DecodeRuneInString(s[j:]); lexWordRune(n) && !unspaced(n) {
			return false
		}
	}
	return true
}

// overlapsAny reports whether [i, j) overlaps a range, or strictly contains an empty one.
func overlapsAny(rs []Range, i, j int) bool {
	for _, r := range rs {
		if i < r.End && r.Start < j {
			return true
		}
	}
	return false
}
