package spoken_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/spoken"
)

// Test-only rules standing in for later stages: a footnote deletion (the
// segment maps a display range to an empty spoken range) and a
// {{say:SPOKEN|TEXT}} mark (§7.8, WP-3.6) rewriting to its spoken part.
var (
	footnoteRe = regexp.MustCompile(`^\[\d+\]`)
	sayMarkRe  = regexp.MustCompile(`^\{\{say:([^|{}]*)\|[^{}]*\}\}`)
)

var testTable = slices.Concat(spoken.Table{
	{ID: "test.footnote", Match: func(s string, i int, _ *spoken.Options) (spoken.Match, bool) {
		if m := footnoteRe.FindString(s[i:]); m != "" {
			return spoken.Match{End: i + len(m)}, true
		}
		return spoken.Match{}, false
	}},
	{ID: "test.say", Match: func(s string, i int, _ *spoken.Options) (spoken.Match, bool) {
		if m := sayMarkRe.FindStringSubmatch(s[i:]); m != nil {
			return spoken.Match{End: i + len(m[0]), Spoken: m[1]}, true
		}
		return spoken.Match{}, false
	}},
}, spoken.English)

// checkMap asserts the TXT-10 span-map invariant: segments in order, not
// overlapping, covering the whole display text and the whole spoken form,
// and verbatim segments copying their display text exactly.
func checkMap(t *testing.T, r spoken.Result) {
	t.Helper()
	d, s := 0, 0
	for k, sg := range r.Segments {
		if sg.Display.Start != d || sg.Spoken.Start != s || sg.Display.End < sg.Display.Start || sg.Spoken.End < sg.Spoken.Start {
			t.Fatalf("segment %d %+v does not continue at display %d / spoken %d", k, sg, d, s)
		}
		if sg.Display.End == sg.Display.Start && sg.Spoken.End == sg.Spoken.Start {
			t.Fatalf("segment %d %+v is empty on both sides", k, sg)
		}
		if sg.Rule == "" && r.Display[sg.Display.Start:sg.Display.End] != r.Spoken[sg.Spoken.Start:sg.Spoken.End] {
			t.Fatalf("verbatim segment %d changed its text", k)
		}
		d, s = sg.Display.End, sg.Spoken.End
	}
	if d != len(r.Display) || s != len(r.Spoken) {
		t.Fatalf("segments cover display %d/%d and spoken %d/%d", d, len(r.Display), s, len(r.Spoken))
	}
}

func run(t *testing.T, s string, o spoken.Options) spoken.Result {
	t.Helper()
	r, err := spoken.Run(s, testTable, o)
	if err != nil {
		t.Fatal(err)
	}
	checkMap(t, r)
	return r
}

type seg struct {
	display, spoken string
	rule            spoken.RuleID
}

func segs(r spoken.Result) []seg {
	var out []seg
	for _, sg := range r.Segments {
		out = append(out, seg{r.Display[sg.Display.Start:sg.Display.End], r.Spoken[sg.Spoken.Start:sg.Spoken.End], sg.Rule})
	}
	return out
}

// The four named cases: an expansion, a currency with a scale, a deletion and
// a mark rewrite, each inside text so the verbatim neighbours are mapped too.
// accept: TXT-10#1
func TestSpanMapCases(t *testing.T) {
	cases := []struct {
		in   string
		want []seg
	}{
		{"42", []seg{{"42", "forty-two", "en.number"}}},
		{"$3.5M", []seg{{"$3.5M", "three point five million dollars", "en.currency.scale"}}},
		{"[1]", []seg{{"[1]", "", "test.footnote"}}},
		{"{{say:twenty twenty-six|2026}}", []seg{{"{{say:twenty twenty-six|2026}}", "twenty twenty-six", "test.say"}}},
		{"Paid 42 of $3.5M.[1] In {{say:twenty twenty-six|2026}}!", []seg{
			{"Paid ", "Paid ", ""}, {"42", "forty-two", "en.number"}, {" of ", " of ", ""},
			{"$3.5M", "three point five million dollars", "en.currency.scale"}, {".", ".", ""},
			{"[1]", "", "test.footnote"}, {" In ", " In ", ""},
			{"{{say:twenty twenty-six|2026}}", "twenty twenty-six", "test.say"}, {"!", "!", ""},
		}},
	}
	for _, c := range cases {
		r := run(t, c.in, spoken.Options{})
		if got := segs(r); !slices.Equal(got, c.want) {
			t.Errorf("%q:\n got %+v\nwant %+v", c.in, got, c.want)
		}
	}
}

// accept: TXT-10#1
// accept: TXT-10#3
func FuzzSpoken(f *testing.F) {
	for _, s := range []string{"42", "$3.5M", "[1]", "{{say:twenty twenty-six|2026}}", "",
		"The deal was worth $3.5M, or about €3.2 million.", "Take 1/2 cup on 1/2.",
		"Dr. Smith lives on St. James St., No. 5.", "A → B, x ≥ 3, & 2 × 4 = 8.",
		"Henry VIII ruled in the 16th century; see Chapter IV.", "Call +1 (415) 555-0134.",
		"The meeting is on 2026-10-11 at 3:45pm.", "It weighs 5kg, runs at 120km/h, and costs 3¥.",
		"in 1999 -5 1,280.50 v1.2.3 3–5 10-12 12.5% C++ C# .NET iOS e.g. IV 3/4 #1",
		"2026年10月11日下午3点45分，价格是¥1,280.50，增长了12.5%。", "请在iPhone 17上打开App Store，下载版本v2.0。",
		"第1/2季度，温度是-5℃到30℃。", "¥10-20 -5~30℃ 10-20% 2026-10 3-5-7 1/0 第IV章 2个 2年级 v10.0.1版本",
		"+86 13800138000 尾号1234 12:00:05 比分3:1 1+1=2 x ≥ 3 COVID-19 120km/h 1400000000人",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return // the decoder (TXT-01) rejects invalid UTF-8 before this stage
		}
		run(t, s, spoken.Options{})
		// Idempotence is a property of the English and Chinese rules; the test mark rule
		// speaks arbitrary text by design (marks are protected later, §7.8).
		r, err := spoken.Normalize(s, "en", spoken.Options{})
		if err != nil {
			t.Fatal(err)
		}
		checkMap(t, r)
		again, err := spoken.Normalize(r.Spoken, "en", spoken.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if again.Spoken != r.Spoken {
			t.Fatalf("not idempotent:\n in  %q\n one %q\n two %q", s, r.Spoken, again.Spoken)
		}
		zh, err := spoken.Normalize(s, "zh", spoken.Options{})
		if err != nil {
			t.Fatal(err)
		}
		checkMap(t, zh)
		zhAgain, err := spoken.Normalize(zh.Spoken, "zh", spoken.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if zhAgain.Spoken != zh.Spoken {
			t.Fatalf("zh not idempotent:\n in  %q\n one %q\n two %q", s, zh.Spoken, zhAgain.Spoken)
		}
	})
}

// accept: TXT-10#2
func TestDisabledRuleHasNoEffect(t *testing.T) {
	in := "Paid $3.5M in 1999 for 42 units."
	all := run(t, in, spoken.Options{})
	if all.Spoken != "Paid three point five million dollars in nineteen ninety-nine for forty-two units." {
		t.Fatalf("baseline %q", all.Spoken)
	}
	cases := []struct {
		disabled []spoken.RuleID
		want     string
	}{
		{[]spoken.RuleID{"en.year"}, "Paid three point five million dollars in one thousand nine hundred ninety-nine for forty-two units."},
		{[]spoken.RuleID{"en.currency.scale"}, "Paid $3.5M in nineteen ninety-nine for forty-two units."},
		{[]spoken.RuleID{"en.year", "en.number"}, "Paid three point five million dollars in 1999 for 42 units."},
	}
	for _, c := range cases {
		r := run(t, in, spoken.Options{Disabled: c.disabled})
		if r.Spoken != c.want {
			t.Errorf("disabled %v: %q, want %q", c.disabled, r.Spoken, c.want)
		}
		for _, sg := range r.Segments {
			if slices.Contains(c.disabled, sg.Rule) {
				t.Errorf("disabled %v: a segment still names %s", c.disabled, sg.Rule)
			}
		}
	}
}

// accept: TXT-10#2
func TestReportListsRulePerRewrite(t *testing.T) {
	r := run(t, "Dr. Lee paid $5\non 2026-10-11.", spoken.Options{})
	var got []string
	for _, rw := range r.Rewrites() {
		got = append(got, rw.String())
	}
	want := []string{
		`1:1 en.abbrev: "Dr." → "Doctor"`,
		`1:14 en.currency: "$5" → "five dollars"`,
		`2:4 en.date.iso: "2026-10-11" → "October eleventh, twenty twenty-six"`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("rewrites\n got %q\nwant %q", got, want)
	}
}

// accept: TXT-10#3
func TestIdempotentOnGolden(t *testing.T) {
	for _, p := range goldenPairs(t) {
		r := run(t, p.in, spoken.Options{})
		if again := run(t, r.Spoken, spoken.Options{}); again.Spoken != r.Spoken {
			t.Errorf("%q: %q then %q", p.in, r.Spoken, again.Spoken)
		}
	}
}

func TestModeOffAndOtherLanguages(t *testing.T) {
	in := "Paid $5 on 2026-10-11."
	for _, c := range []struct {
		lang string
		o    spoken.Options
	}{{"en", spoken.Options{Mode: spoken.ModeOff}}, {"fr", spoken.Options{}}, {"ja", spoken.Options{}}, {"", spoken.Options{}}} {
		r, err := spoken.Normalize(in, c.lang, c.o)
		if err != nil {
			t.Fatal(err)
		}
		checkMap(t, r)
		if r.Spoken != in || len(r.Segments) != 1 || len(r.Rewrites()) != 0 {
			t.Errorf("%q %+v: %+v", c.lang, c.o, r)
		}
	}
	r, err := spoken.Normalize(in, "en-GB", spoken.Options{})
	if err != nil || r.Spoken == in {
		t.Fatalf("en-GB should use the English rules: %q %v", r.Spoken, err)
	}
	if r, err := spoken.Normalize("", "en", spoken.Options{}); err != nil || r.Spoken != "" || len(r.Segments) != 0 {
		t.Fatalf("empty: %+v %v", r, err)
	}
}

// Protected ranges (speech marks, WP-3.6) are copied as written, and no rule
// match may run into one.
func TestProtectedRanges(t *testing.T) {
	in := "Pay $5 and 42 now"
	p := strings.Index(in, "42")
	r := run(t, in, spoken.Options{Protected: []spoken.Range{{p, p + 2}}})
	if r.Spoken != "Pay five dollars and 42 now" {
		t.Fatalf("%q", r.Spoken)
	}
	// A match may not run into a protected range: with "M" protected the
	// scaled amount cannot be rewritten, and no shorter rule claims "$3.5".
	in = "$3.5M"
	r = run(t, in, spoken.Options{Protected: []spoken.Range{{4, 5}}})
	if r.Spoken != "$3.5M" || len(r.Rewrites()) != 0 {
		t.Fatalf("%q", r.Spoken)
	}
	for _, bad := range [][]spoken.Range{{{3, 2}}, {{0, 99}}, {{2, 4}, {1, 3}}} {
		if _, err := spoken.Run(in, testTable, spoken.Options{Protected: bad}); err == nil {
			t.Errorf("protected %v accepted", bad)
		}
	}
	if _, err := spoken.Run("é", testTable, spoken.Options{Protected: []spoken.Range{{1, 2}}}); err == nil {
		t.Error("a range splitting a character was accepted")
	}
}

func TestOptionErrors(t *testing.T) {
	if _, err := spoken.Run("x", testTable, spoken.Options{Mode: "maybe"}); err == nil {
		t.Error("bad mode accepted")
	}
	if _, err := spoken.Run("x", testTable, spoken.Options{DateStyle: "mars"}); err == nil {
		t.Error("bad date style accepted")
	}
	if m, err := spoken.ParseMode("off"); err != nil || m != spoken.ModeOff {
		t.Error(m, err)
	}
	if _, err := spoken.ParseMode("on"); err == nil {
		t.Error("--normalize on accepted")
	}
	if d, err := spoken.ParseDateStyle("uk"); err != nil || d != spoken.DateUK {
		t.Error(d, err)
	}
	if _, err := spoken.ParseDateStyle("eu"); err == nil {
		t.Error("--date-style eu accepted")
	}
	ids, err := spoken.ParseDisabled(" en.year, en.currency.scale ,")
	if err != nil || !slices.Equal(ids, []spoken.RuleID{"en.year", "en.currency.scale"}) {
		t.Error(ids, err)
	}
	if _, err := spoken.ParseDisabled("en.yaer"); err == nil || !strings.Contains(err.Error(), "en.year") {
		t.Errorf("unknown rule: %v", err)
	}
}
