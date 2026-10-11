package spoken_test

import (
	"os"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/spoken"
)

type pair struct{ in, want string }

// goldenPairs reads testdata/en-golden.tsv: one "input<TAB>spoken" per line.
func goldenPairs(t *testing.T) []pair {
	t.Helper()
	raw, err := os.ReadFile("testdata/en-golden.tsv")
	if err != nil {
		t.Fatal(err)
	}
	var ps []pair
	for n, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		in, want, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("en-golden.tsv:%d: no tab", n+1)
		}
		ps = append(ps, pair{in, want})
	}
	return ps
}

// accept: TXT-11#1
func TestEnglishGolden(t *testing.T) {
	ps := goldenPairs(t)
	if len(ps)+len(probeItems) < 200 {
		t.Fatalf("%d golden pairs, want at least 200", len(ps)+len(probeItems))
	}
	for _, p := range ps {
		r, err := spoken.Normalize(p.in, "en", spoken.Options{})
		if err != nil {
			t.Fatal(err)
		}
		checkMap(t, r)
		if r.Spoken != p.want {
			t.Errorf("%q\n got %q\nwant %q", p.in, r.Spoken, p.want)
		}
	}
}

// probeItems are the English items of dev-docs/probe_textnorm.py (design
// §7.5), run through the text guard and then the spoken form, as `say` does.
var probeItems = []struct {
	name, in, want string
	ambiguous      int
}{
	{"money", "The deal was worth $3.5M, or about €3.2 million.",
		"The deal was worth three point five million dollars, or about three point two million euros.", 0},
	{"date", "The meeting is on 2026-10-11 at 3:45pm.",
		"The meeting is on October eleventh, twenty twenty-six at three forty-five p m.", 0},
	{"ambig", "Take 1/2 cup on 1/2.", "Take one half cup on 1/2.", 1},
	{"percent", "Growth hit 50% (up from 12.5%).", "Growth hit fifty percent (up from twelve point five percent).", 0},
	{"range", "Read pages 3–5 and chapters 10-12.", "Read pages three to five and chapters ten to twelve.", 0},
	{"version", "Upgrade to v1.2.3 before Friday.", "Upgrade to version one point two point three before Friday.", 0},
	{"code", "Write it in C++ or C#, not F#.", "Write it in C plus plus or C sharp, not F sharp.", 0},
	{"url", "Visit https://example.com/docs?id=7 for details.", "Visit example dot com for details.", 0},
	{"email", "Mail ops@example.com today.", "Mail ops at example dot com today.", 0},
	{"phone", "Call +1 (415) 555-0134.", "Call plus one, four one five, five five five, oh one three four.", 0},
	{"acronym", "NASA and the FBI met at the UN; the CEO used SQL.", "NASA and the FBI met at the UN; the CEO used SQL.", 0},
	{"abbrev", "Dr. Smith lives on St. James St., No. 5.", "Doctor Smith lives on Saint James Street, number five.", 0},
	{"symbols", "A → B, x ≥ 3, & 2 × 4 = 8.", "A to B, x greater than or equal to three, and two times four equals eight.", 0},
	{"roman", "Henry VIII ruled in the 16th century; see Chapter IV.", "Henry the Eighth ruled in the sixteenth century; see Chapter four.", 0},
	{"emoji", "Great job 🎉👍 see you soon 😀", "Great job see you soon", 0},
	{"footnote", "This was proven [1] and later disputed.[2]", "This was proven and later disputed.", 0},
	{"dirty", "Ths   is  ,,a  sentnce with typos!!!  and\u200b zero-width chars", "Ths is,a sentnce with typos! and zero-width chars", 0},
	{"units", "It weighs 5kg, runs at 120km/h, and costs 3¥.", "It weighs five kilograms, runs at one hundred twenty kilometers per hour, and costs three yen.", 0},
}

// accept: TXT-11#1
func TestProbeItems(t *testing.T) {
	for _, p := range probeItems {
		g, err := guard.Run(p.in, guard.Options{})
		if err != nil {
			t.Fatalf("%s: guard: %v", p.name, err)
		}
		r, err := spoken.Normalize(g.Text, "en", spoken.Options{})
		if err != nil {
			t.Fatal(err)
		}
		checkMap(t, r)
		if r.Spoken != p.want || len(r.Findings) != p.ambiguous {
			t.Errorf("%s: %q (%d ambiguous)\nwant %q (%d)", p.name, r.Spoken, len(r.Findings), p.want, p.ambiguous)
		}
	}
}

// accept: TXT-11#2
func TestAmbiguityIsReportedNotGuessed(t *testing.T) {
	cases := []struct {
		in, text string
		rule     spoken.RuleID
		line     int
		col      int
	}{
		{"Add 1/2.", "1/2", "en.fraction", 1, 5},
		{"1/2", "1/2", "en.fraction", 1, 1},
		{"Meet me at St. and wait.", "St.", "en.abbrev", 1, 12},
		{"St.", "St.", "en.abbrev", 1, 1},
		{"Then\nIV happened.", "IV", "en.roman", 2, 1},
		{"Grade XII", "", "", 0, 0}, // a numbering context decides it
		{"Due 10/11/2026.", "", "", 0, 0},
	}
	for _, c := range cases {
		r, err := spoken.Normalize(c.in, "en", spoken.Options{})
		if err != nil {
			t.Fatal(err)
		}
		checkMap(t, r)
		if c.text == "" {
			if len(r.Findings) != 0 {
				t.Errorf("%q: unexpected %v", c.in, r.Findings)
			}
			continue
		}
		if len(r.Findings) != 1 {
			t.Fatalf("%q: findings %v", c.in, r.Findings)
		}
		f := r.Findings[0]
		if f.Code != spoken.AmbiguousCode || f.Rule != c.rule || f.Text != c.text || f.Line != c.line || f.Col != c.col {
			t.Errorf("%q: %+v", c.in, f)
		}
		if !strings.Contains(r.Spoken, c.text) {
			t.Errorf("%q: the ambiguous span was rewritten: %q", c.in, r.Spoken)
		}
	}
	r, _ := spoken.Normalize("Take 1/2.", "en", spoken.Options{})
	if got := r.Findings[0].String(); got != `1:6 N-AMBIGUOUS en.fraction: "1/2" left as written (fraction or date)` {
		t.Errorf("finding line %q", got)
	}
}

// --date-style sets the reading order, and how a numeric date is read.
// accept: TXT-11#1
func TestDateStyles(t *testing.T) {
	cases := []struct {
		style     spoken.DateStyle
		in, want  string
		ambiguous bool
	}{
		{spoken.DateUS, "2026-10-11", "October eleventh, twenty twenty-six", false},
		{spoken.DateUK, "2026-10-11", "the eleventh of October, twenty twenty-six", false},
		{spoken.DateISO, "2026-10-11", "twenty twenty-six, October eleventh", false},
		{spoken.DateUS, "10/11/2026", "October eleventh, twenty twenty-six", false},
		{spoken.DateUK, "10/11/2026", "the tenth of November, twenty twenty-six", false},
		{spoken.DateISO, "10/11/2026", "10/11/2026", true},
		{spoken.DateISO, "25/12/2026", "twenty twenty-six, December twenty-fifth", false},
		{spoken.DateUS, "25/12/2026", "December twenty-fifth, twenty twenty-six", false},
		{spoken.DateUK, "October 11", "October eleventh", false},
		{spoken.DateUS, "2026-02-30", "2026-02-30", true},
	}
	for _, c := range cases {
		r, err := spoken.Normalize(c.in, "en", spoken.Options{DateStyle: c.style})
		if err != nil {
			t.Fatal(err)
		}
		if r.Spoken != c.want || (len(r.Findings) == 1) != c.ambiguous {
			t.Errorf("%s %q: %q %v", c.style, c.in, r.Spoken, r.Findings)
		}
	}
}

// accept: TXT-11#2
func TestSaintStreetBothSidesIsAmbiguous(t *testing.T) {
	cases := []struct {
		in, want  string
		ambiguous int
	}{
		{"He lives on Main St. The shop is closed.", "He lives on Main St. The shop is closed.", 1},
		{"Visit St. Paul today.", "Visit Saint Paul today.", 0},
		{"She lives on Elm St. near the park.", "She lives on Elm Street near the park.", 0},
	}
	for _, c := range cases {
		r, err := spoken.Normalize(c.in, "en", spoken.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if r.Spoken != c.want || len(r.Findings) != c.ambiguous {
			t.Errorf("%q → %q (%d ambiguous); want %q (%d)", c.in, r.Spoken, len(r.Findings), c.want, c.ambiguous)
		}
	}
}
