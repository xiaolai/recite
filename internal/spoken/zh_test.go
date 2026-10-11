package spoken_test

import (
	"os"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/text"
)

// zhGoldenPairs reads testdata/zh-golden.tsv: one "input<TAB>spoken" per line.
func zhGoldenPairs(t *testing.T) []pair {
	t.Helper()
	raw, err := os.ReadFile("testdata/zh-golden.tsv")
	if err != nil {
		t.Fatal(err)
	}
	var ps []pair
	for n, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		in, want, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("zh-golden.tsv:%d: no tab", n+1)
		}
		ps = append(ps, pair{in, want})
	}
	return ps
}

// accept: TXT-12#1
func TestChineseGolden(t *testing.T) {
	ps := zhGoldenPairs(t)
	if len(ps)+len(zhProbeItems) < 150 {
		t.Fatalf("%d golden pairs, want at least 150", len(ps)+len(zhProbeItems))
	}
	for _, p := range ps {
		r, err := spoken.Normalize(p.in, "zh", spoken.Options{})
		if err != nil {
			t.Fatal(err)
		}
		checkMap(t, r)
		if r.Spoken != p.want || len(r.Findings) != 0 {
			t.Errorf("%q\n got %q %v\nwant %q", p.in, r.Spoken, r.Findings, p.want)
		}
		if again := runZH(t, r.Spoken, spoken.Options{}); again.Spoken != r.Spoken {
			t.Errorf("%q is not idempotent: %q then %q", p.in, r.Spoken, again.Spoken)
		}
	}
}

// zhProbeItems are the zh-* items of dev-docs/probe_textnorm.py (design §7.5), run
// through the text guard and then the spoken form, as `say` does.
var zhProbeItems = []struct {
	name, in, want string
	ambiguous      int
}{
	{"zh-num", "2026年10月11日下午3点45分，价格是¥1,280.50，增长了12.5%。",
		"二〇二六年十月十一日下午三点四十五分，价格是一千二百八十点五元，增长了百分之十二点五。", 0},
	{"zh-mixed", "请在iPhone 17上打开App Store，下载版本v2.0。",
		"请在iPhone 十七上打开App Store，下载版本二点零。", 0},
	{"zh-symb", "第1/2季度，温度是-5℃到30℃。",
		"第1/2季度，温度是零下五摄氏度到三十摄氏度。", 1},
}

// accept: TXT-12#1
func TestChineseProbeItems(t *testing.T) {
	for _, p := range zhProbeItems {
		g, err := guard.Run(p.in, guard.Options{})
		if err != nil {
			t.Fatalf("%s: guard: %v", p.name, err)
		}
		r, err := spoken.Normalize(g.Text, "zh", spoken.Options{})
		if err != nil {
			t.Fatal(err)
		}
		checkMap(t, r)
		if r.Spoken != p.want || len(r.Findings) != p.ambiguous {
			t.Errorf("%s: %q %v\nwant %q (%d ambiguous)", p.name, r.Spoken, r.Findings, p.want, p.ambiguous)
		}
	}
}

// E25: the model read ¥1,280.50 with the currency in the wrong place. The spoken form
// puts 元 after the amount, and drops the trailing zero of the fraction.
//
// accept: TXT-12#2
func TestYuanAmountIsPinned(t *testing.T) {
	for _, lang := range []string{"zh", "zh-CN", "zh-Hans", "ZH-tw"} {
		r, err := spoken.Normalize("¥1,280.50", lang, spoken.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if r.Spoken != "一千二百八十点五元" {
			t.Errorf("%s: ¥1,280.50 → %q, want 一千二百八十点五元", lang, r.Spoken)
		}
		rw := r.Rewrites()
		if len(rw) != 1 || rw[0].Rule != "zh.currency" || rw[0].Before != "¥1,280.50" {
			t.Errorf("%s: rewrites %v", lang, rw)
		}
	}
}

// English words, product names and code terms stay as written; only the numbers
// next to them are read, and a number glued to a term is part of it.
//
// accept: TXT-12#1
func TestChineseKeepsEnglishTerms(t *testing.T) {
	cases := []struct{ in, want string }{
		{"打开App Store和Google Play。", "打开App Store和Google Play。"},
		{"NASA和FBI都用SQL。", "NASA和FBI都用SQL。"},
		{"C++和C#都是编程语言。", "C++和C#都是编程语言。"},
		{"COVID-19疫情期间买了A4纸和MP3。", "COVID-19疫情期间买了A4纸和MP3。"},
		{"请在iPhone 17上打开App Store。", "请在iPhone 十七上打开App Store。"},
		{"Dr. Smith住在St. James St.。", "Dr. Smith住在St. James St.。"},
	}
	for _, c := range cases {
		r := runZH(t, c.in, spoken.Options{})
		if r.Spoken != c.want || len(r.Findings) != 0 {
			t.Errorf("%q → %q %v, want %q", c.in, r.Spoken, r.Findings, c.want)
		}
	}
}

// accept: TXT-12#1
func TestChineseAmbiguityIsReported(t *testing.T) {
	cases := []struct {
		in, text string
		rule     spoken.RuleID
	}{
		{"第1/2季度", "1/2", "zh.fraction"},
		{"编号3-5-7", "3-5", "zh.range"},
		{"2026-10", "2026-10", "zh.range"},
		{"2026-02-30", "2026-02-30", "zh.date.iso"},
		{"2026-02-29", "2026-02-29", "zh.date.iso"}, // 2026 is not a leap year
		{"1/0", "1/0", "zh.fraction"},
	}
	for _, c := range cases {
		r := runZH(t, c.in, spoken.Options{})
		if len(r.Findings) != 1 {
			t.Fatalf("%q: findings %v", c.in, r.Findings)
		}
		f := r.Findings[0]
		if f.Code != spoken.AmbiguousCode || f.Rule != c.rule || f.Text != c.text {
			t.Errorf("%q: %+v", c.in, f)
		}
		if !strings.Contains(r.Spoken, c.text) {
			t.Errorf("%q: the ambiguous span was rewritten: %q", c.in, r.Spoken)
		}
	}
}

func TestChineseRulesCanBeDisabled(t *testing.T) {
	r, err := spoken.Normalize("1999年涨了12.5%。", "zh", spoken.Options{Disabled: []spoken.RuleID{"zh.year"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Spoken != "一千九百九十九年涨了百分之十二点五。" {
		t.Errorf("%q", r.Spoken)
	}
	if _, err := spoken.ParseDisabled("zh.temp.below-zero,en.year"); err != nil {
		t.Error(err)
	}
}

func runZH(t *testing.T, s string, o spoken.Options) spoken.Result {
	t.Helper()
	r, err := spoken.Normalize(s, "zh", o)
	if err != nil {
		t.Fatal(err)
	}
	checkMap(t, r)
	return r
}

// The TXT-12#3 corpus must reach the model fully spoken: no finding, and no ASCII
// digit left, so a live failure is about the voice and not about a missing rule.
func TestChineseQualifyCorpusIsFullySpoken(t *testing.T) {
	raw, err := os.ReadFile("testdata/qualify-zh.txt")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 30 {
		t.Fatalf("%d sentences, want 30", len(lines))
	}
	for _, l := range lines {
		if lang := text.Resolve(l, text.ResolveOptions{}); lang.Code != "zh" {
			t.Errorf("%q resolves to %q", l, lang.Code)
		}
		r := runZH(t, l, spoken.Options{})
		if len(r.Findings) != 0 || strings.ContainsAny(r.Spoken, "0123456789") {
			t.Errorf("%q → %q %v", l, r.Spoken, r.Findings)
		}
	}
}
