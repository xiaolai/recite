package verify

import (
	"strings"
	"testing"
)

// accept: VER-03#1
func TestEquivalencePairs(t *testing.T) {
	pairs := [][2]string{
		{"Mrs. Okafor", "Missus Okafor"},
		{"42", "forty-two"},
		{"1999", "nineteen ninety-nine"},
		{"3.5%", "three point five percent"},
		{"四十二", "42"},
		{"two thousand", "2,000"},
		// Further cases from design §8.3.
		{"the 16th", "the sixteenth"},
		{"1999", "one thousand nine hundred ninety-nine"},
		{"1905", "nineteen oh five"},
		{"一千二百八十", "1280"},
		{"两万", "20,000"},
		{"-7", "minus seven"},
		{"one hundred and five", "105"},
		{"e-mail", "email"},
		{"e.g. this", "for example this"},
		{"Dr. Who vs. Mr. Smith", "doctor who versus mister smith"},
		{"St. Louis", "Saint Louis"},
		{"Main St.", "main street"},
		{"Don't", "dont"},
		{"ＨＥＬＬＯ，　ｗｏｒｌｄ！", "hello world"},
	}
	for _, p := range pairs {
		if !Equivalent(p[0], p[1]) {
			t.Errorf("Equivalent(%q, %q) = false; canon %q vs %q", p[0], p[1], Canon(p[0]), Canon(p[1]))
		}
	}
	for _, p := range [][2]string{{"42", "forty-three"}, {"1999", "nineteen ninety-eight"}, {"3.5", "35"}, {"四十二", "24"}} {
		if Equivalent(p[0], p[1]) {
			t.Errorf("Equivalent(%q, %q) = true", p[0], p[1])
		}
	}
}

func TestCanonForms(t *testing.T) {
	cases := map[string]string{
		"1,234,567":        "one million two hundred thirty four thousand five hundred sixty seven",
		"007":              "zero zero seven",
		"3.14":             "three point one four",
		"1st 2nd 3rd 21st": "first second third twenty first",
		"20th":             "twentieth",
		"1,2":              "one two",
		"mp3":              "mp three",
		"etc.":             "et cetera",
		"i.e. it":          "that is it",
		"一九九九":             "one thousand nine hundred ninety nine",
		"一亿两千万":            "one hundred twenty million",
		"Hello—world":      "hello world",
		"msð":              "msð", // "ms" is not a whole word here, though regexp's \b says so
	}
	for in, want := range cases {
		if got := strings.Join(Canon(in), " "); got != want {
			t.Errorf("Canon(%q) = %q, want %q", in, got, want)
		}
	}
}

// accept: VER-03#2
func FuzzCanon(f *testing.F) {
	for _, s := range []string{"Mrs. Okafor", "3.5%", "四十二", "2,000", "-1st", "e.g."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c := Canon(s)
		if again := Canon(strings.Join(c, " ")); !equalTokens(again, c) {
			t.Fatalf("not idempotent: %q -> %q -> %q", s, c, again)
		}
		if !Equivalent(s, s) {
			t.Fatalf("not reflexive: %q", s)
		}
	})
}
