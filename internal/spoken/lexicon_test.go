package spoken

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func mustLexicon(t testing.TB, table map[string]any) Lexicon {
	t.Helper()
	l, err := ParseLexicon(table)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func say(v string) map[string]any  { return map[string]any{"say": v} }
func hint(v string) map[string]any { return map[string]any{"hint": v} }

// matched lists each match as "term@start".
func matched(l Lexicon, s, lang string, skip []Range) string {
	var out []string
	for _, m := range l.Match(s, lang, skip) {
		out = append(out, s[m.Start:m.End]+"@"+itoa(m.Start))
	}
	return strings.Join(out, " ")
}

// Matching rules (design §7.8): whole words in spaced scripts with Unicode word edges,
// substrings in Chinese and Japanese, case-sensitive, longest match wins.
// accept: SPK-08#1
func TestLexiconMatching(t *testing.T) {
	l := mustLexicon(t, map[string]any{
		"SQL": say("sequel"), "Ana": hint("AH-na"), "Jose": hint("ho-SAY"), "C++": say("see plus plus"),
		"重庆": hint("chóng qìng"), "AI": say("A I"), "New York": say("NY"), "York City Hall": say("hall"),
		"New York City": say("NYC"), "東京": hint("toh-kyoh"),
	})
	for _, c := range []struct{ in, want string }{
		{"SQL and MySQL and SQLite", "SQL@0"},
		{"sql is not SQL.", "SQL@11"},              // case-sensitive
		{"Anaís and Ana.", "Ana@11"},               // í is a letter: no ASCII \b split
		{"José and Jose", "Jose@11"},              // a combining mark continues the word
		{"C++ daily, C+++", "C++@0 C++@11"},        // an edge that is not a word rune needs no boundary
		{"我去重庆市看看重庆", "重庆@6 重庆@21"},                // substring in Chinese
		{"用AI做的，不是AIM", "AI@3"},                    // Han neighbours are a word edge
		{"在東京都", "東京@3"},                           // Japanese kanji
		{"New York City", "New York City@0"},       // the longest entry at a start wins
		{"New York City Hall", "York City Hall@4"}, // longer than "New York City"
		{"Old York City Hall", "York City Hall@4"}, // no competitor
		{"New York Hall", "New York@0"},
		{"SQL_x 9SQL SQL9", "SQL@0"}, // "_" is punctuation; a digit continues the word
	} {
		if got := matched(l, c.in, "", nil); got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

// Entries inside speech marks are ignored, and no match crosses a pause.
// accept: SPK-08#1
func TestLexiconSkipsMarks(t *testing.T) {
	l := mustLexicon(t, map[string]any{"SQL": say("sequel"), "New York": say("NY")})
	s := "SQL New York SQL"
	if got := matched(l, s, "", []Range{{0, 3}}); got != "New York@4 SQL@13" {
		t.Errorf("protected: %q", got)
	}
	if got := matched(l, s, "", []Range{{7, 7}}); got != "SQL@0 SQL@13" {
		t.Errorf("pause inside a term: %q", got)
	}
	if got := matched(l, s, "", []Range{{4, 4}}); got != "SQL@0 New York@4 SQL@13" {
		t.Errorf("pause at an edge: %q", got)
	}

	// Through the spoken-form stage: a mark's override wins over the lexicon.
	r, err := Normalize("Use SQL, then SQL.", "en", Options{Lexicon: l,
		Overrides: []Override{{Range: Range{4, 7}, Spoken: "S Q L", Rule: MarkRule("spell")}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Spoken != "Use S Q L, then sequel." {
		t.Errorf("spoken %q", r.Spoken)
	}
}

// A lang entry applies only to text of that language.
func TestLexiconLang(t *testing.T) {
	l := mustLexicon(t, map[string]any{"Joaquin": map[string]any{"hint": "wah-KEEN", "lang": "en"}})
	if got := matched(l, "Joaquin", "en-US", nil); got != "Joaquin@0" {
		t.Errorf("en: %q", got)
	}
	if got := matched(l, "Joaquin", "zh", nil); got != "" {
		t.Errorf("zh: %q", got)
	}
}

// say rewrites the spoken form and takes precedence over the built-in tech terms and the
// rules; hint keeps the text and protects it from the rules.
func TestLexiconNormalize(t *testing.T) {
	l := mustLexicon(t, map[string]any{"C++": say("cee plus plus"), "R2D2": hint("are-two-dee-two"), "SQL": say("SQL server")})
	r, err := Normalize("C++ and C# meet R2D2 on SQL.", "en", Options{Lexicon: l})
	if err != nil {
		t.Fatal(err)
	}
	if r.Spoken != "cee plus plus and C sharp meet R2D2 on SQL server." {
		t.Errorf("spoken %q", r.Spoken)
	}
	var rules []string
	for _, rw := range r.Rewrites() {
		rules = append(rules, string(rw.Rule))
	}
	if strings.Join(rules, " ") != "lexicon.say en.tech lexicon.hint lexicon.say" {
		t.Errorf("rules %q", rules)
	}
}

func TestParseLexiconErrors(t *testing.T) {
	for _, c := range []struct {
		table map[string]any
		want  string
	}{
		{map[string]any{"SQL": "sequel"}, "want a table"},
		{map[string]any{"SQL": map[string]any{}}, "exactly one of say and hint"},
		{map[string]any{"SQL": map[string]any{"say": "a", "hint": "b"}}, "exactly one"},
		{map[string]any{"SQL": map[string]any{"sya": "a"}}, `unknown field "sya"`},
		{map[string]any{"SQL": map[string]any{"say": 1}}, "must be a string"},
		{map[string]any{" SQL": say("a")}, "leading or trailing space"},
		{map[string]any{"S\nQL": say("a")}, "line break"},
		{map[string]any{"{{x}}": say("a")}, "speech-mark braces"},
		{map[string]any{"...": say("a")}, "needs a letter or digit"},
		{map[string]any{"SQL": say("...")}, "say needs a letter or digit"},
		{map[string]any{"Okafor": hint(`oh "KAH"`)}, `cannot hold "`},
		{map[string]any{"Xi": map[string]any{"hint": "shee", "lang": "??"}}, "BCP 47"},
	} {
		if _, err := ParseLexicon(c.table); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.table, err, c.want)
		}
	}
	l := mustLexicon(t, map[string]any{"b": say("x"), "a": hint("y")})
	back, err := ParseLexiconText(l.String())
	if err != nil || back.String() != `{"a":{"hint":"y"},"b":{"say":"x"}}` {
		t.Errorf("round trip %q %v", back.String(), err)
	}
}

// applySay rewrites the say matches of s.
func applySay(l Lexicon, s string) string {
	var b strings.Builder
	pos := 0
	for _, ov := range l.Overrides(s, "", nil) {
		b.WriteString(s[pos:ov.Start])
		b.WriteString(ov.Spoken)
		pos = ov.End
	}
	return b.String() + s[pos:]
}

// The matcher never panics, returns sorted, non-overlapping, rune-aligned matches of the
// terms, and keeps valid UTF-8 valid. A say entry whose term is a single spaced-script word
// and whose value does not hold the term is idempotent: a rewrite can neither hold nor
// border a new match. (Other lexicons can form new matches across a rewrite, which is why
// the spoken-form stage applies the lexicon once and protects the result.)
func FuzzLexiconMatch(f *testing.F) {
	for _, s := range [][3]string{
		{"SQL and MySQL", "SQL", "sequel"}, {"我去重庆", "重庆", "chongqing"}, {"Jose\u0301", "Jose", "ho"},
		{"New York City", "New York", "NY"}, {"\xff\xfeSQL", "SQL", "x"}, {"aaaa", "aa", "b"},
	} {
		f.Add(s[0], s[1], s[2])
	}
	f.Fuzz(func(t *testing.T, s, term, value string) {
		l, err := ParseLexicon(map[string]any{term: say(value), "New York": say("NY"), "重庆": hint("chóng qìng")})
		if err != nil {
			return
		}
		ms := l.Match(s, "", nil)
		prev := 0
		for _, m := range ms {
			// Rune alignment is checked on valid input; a stray byte is not a rune to split.
			aligned := !utf8.ValidString(s) || utf8.RuneStart(byteAt(s, m.Start)) && utf8.RuneStart(byteAt(s, m.End))
			if m.Start < prev || m.End > len(s) || s[m.Start:m.End] != m.Entry.Term || !aligned {
				t.Fatalf("bad match %+v in %q (%+v)", m, s, ms)
			}
			prev = m.End
		}
		if once := applySay(l, s); utf8.ValidString(s) && !utf8.ValidString(once) {
			t.Fatalf("invalid UTF-8 from %q: %q", s, once)
		}
		one, err := ParseLexicon(map[string]any{term: say(value)})
		if err != nil || strings.ContainsFunc(term, func(r rune) bool { return !lexWordRune(r) || unspaced(r) }) ||
			len(one.Match(value, "", nil)) > 0 {
			return
		}
		once := applySay(one, s)
		if twice := applySay(one, once); twice != once {
			t.Fatalf("not idempotent: %q → %q → %q", s, once, twice)
		}
	})
}

// A one-letter entry matching at every word of a large text: the overlap check was quadratic
// in the number of matches.
func BenchmarkLexiconMatchDense(b *testing.B) {
	l, err := ParseLexicon(map[string]any{"x": map[string]any{"say": "ex"}, "x x": map[string]any{"say": "double ex"}})
	if err != nil {
		b.Fatal(err)
	}
	s := strings.Repeat("x ", 200_000)
	for b.Loop() {
		if got := l.Match(s, "", nil); len(got) != 100_000 {
			b.Fatalf("%d matches, want 100000", len(got))
		}
	}
}
