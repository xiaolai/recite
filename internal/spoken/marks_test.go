package spoken

import (
	"testing"
)

// Each text-bearing mark kind has a fixed spoken form (design §7.8).
// accept: TXT-13#1
func TestMarkSpokenGolden(t *testing.T) {
	cases := []struct {
		kind, value, text, lang string
		style                   DateStyle
		want, ambiguous         string
	}{
		{"say", "twenty twenty-six", "2026", "en", "", "twenty twenty-six", ""},
		{"spell", "", "API", "en", "", "A P I", ""},
		{"spell", "", "R2-D2", "en", "", "R 2 D 2", ""},
		{"spell", "", "--", "en", "", "", "no letter or digit to spell"},
		{"digits", "", "2026", "en", "", "two zero two six", ""},
		{"digits", "", "2026", "zh", "", "二零二六", ""},
		{"digits", "", "abc", "en", "", "", "no digit to read"},
		{"num", "", "1,280.50", "en", "", "one thousand two hundred eighty point five oh", ""},
		{"num", "", "12.5%", "zh", "", "百分之十二点五", ""},
		{"num", "", "hello", "en", "", "", "cannot be read as a number"},
		{"date", "", "10/11", "en", DateUS, "October eleventh", ""},
		{"date", "", "10/11", "en", DateUK, "the tenth of November", ""},
		{"date", "", "10/11", "en", DateISO, "October eleventh", ""},
		{"date", "", "2026-10-11", "en", "", "October eleventh, twenty twenty-six", ""},
		{"date", "", "1/2", "zh", "", "一月二日", ""},
		{"date", "", "13/40", "en", "", "", "cannot be read as a date"},
		{"fraction", "", "1/2", "en", "", "one half", ""},
		{"fraction", "", "2 3/4", "en", "", "two and three quarters", ""},
		{"fraction", "", "1/2", "zh", "", "二分之一", ""},
		{"fraction", "", "x/2", "en", "", "", "not a fraction such as 1/2 or 2 3/4"},
		{"verbatim", "", "R2-D2", "en", "", "R2-D2", ""},
		{"hint", "oh-KAH-for", "Okafor", "en", "", "Okafor", ""},
		{"emph", "", "never", "en", "", "never", ""},
		{"lang", "fr", "bonjour", "en", "", "bonjour", ""},
	}
	for _, c := range cases {
		got, amb := MarkSpoken(c.kind, c.value, c.text, c.lang, c.style)
		if got != c.want || amb != c.ambiguous {
			t.Errorf("%s|%s (%s %s): %q / %q, want %q / %q", c.kind, c.text, c.lang, c.style, got, amb, c.want, c.ambiguous)
		}
	}
}

// An override replaces its range under its rule, no rule rewrites inside a protected or an
// overridden range, an undecidable override is N-AMBIGUOUS, and the result is a fixed point.
func TestOverridesInRun(t *testing.T) {
	s := "Take 1/2 of 5kg on 2026-10-11, X."
	o := Options{
		Overrides: []Override{
			{Range: Range{5, 8}, Spoken: "one half", Rule: MarkRule("fraction")},
			{Range: Range{30, 31}, Rule: MarkRule("num"), Ambiguous: "cannot be read as a number"},
		},
		Protected: []Range{{19, 29}},
	}
	res, err := Normalize(s, "en", o)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Take one half of five kilograms on 2026-10-11, X."; res.Spoken != want {
		t.Fatalf("spoken %q, want %q", res.Spoken, want)
	}
	if len(res.Findings) != 1 || res.Findings[0].Rule != "mark.num" || res.Findings[0].Col != 31 {
		t.Fatalf("findings %+v", res.Findings)
	}
	var found bool
	for _, sg := range res.Segments {
		if sg.Rule == "mark.fraction" && sg.Display == (Range{5, 8}) && res.Spoken[sg.Spoken.Start:sg.Spoken.End] == "one half" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no mark.fraction segment: %+v", res.Segments)
	}
	// Overrides apply with the rules off as well.
	off, err := Normalize(s, "en", Options{Mode: ModeOff, Overrides: o.Overrides[:1]})
	if err != nil || off.Spoken != "Take one half of 5kg on 2026-10-11, X." {
		t.Fatalf("off: %q %v", off.Spoken, err)
	}
	// Overlapping protected and overridden ranges are rejected.
	if _, err := Normalize(s, "en", Options{Overrides: o.Overrides[:1], Protected: []Range{{6, 9}}}); err == nil {
		t.Fatal("overlap accepted")
	}
}
