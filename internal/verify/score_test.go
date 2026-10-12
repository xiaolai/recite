package verify

import (
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/text"
)

// accept: VER-04#1
func TestScoreKnownPairs(t *testing.T) {
	cases := []struct {
		ref, hyp     string
		metric       text.Metric
		refLen, errs int
		score        float64
	}{
		{"the cat sat on the mat", "the cat sat on the mat", text.MetricWER, 6, 0, 1},
		{"the cat sat on the mat", "The cat sat on the hat.", text.MetricWER, 6, 1, 1 - 1.0/6},
		{"the cat sat on the mat", "the cat on the mat", text.MetricWER, 6, 1, 1 - 1.0/6},
		{"the cat sat on the mat", "well the cat sat on the mat", text.MetricWER, 6, 1, 1 - 1.0/6},
		{"one two three four", "five six seven eight nine ten", text.MetricWER, 4, 6, 0}, // clamped
		{"Then 42 students arrived.", "Then forty-two students arrived", text.MetricWER, 5, 0, 1},
		{"Mrs. Okafor", "missus okafor", text.MetricWER, 2, 0, 1},
		{"", "", text.MetricWER, 0, 0, 1},
		{"", "hello", text.MetricWER, 0, 1, 0},
		{"你好世界", "你好世界。", text.MetricCER, 4, 0, 1},
		{"你好世界", "你好时界", text.MetricCER, 4, 1, 0.75},
		{"我有四十二本书", "我有42本书", text.MetricCER, 4 + len("fortytwo"), 0, 1},
	}
	for _, c := range cases {
		got := Score(c.ref, c.hyp, c.metric)
		if got.RefLen != c.refLen || got.Errors != c.errs || got.Score != c.score || got.Metric != c.metric {
			t.Errorf("Score(%q, %q, %s) = len %d errors %d score %v; want %d %d %v",
				c.ref, c.hyp, c.metric, got.RefLen, got.Errors, got.Score, c.refLen, c.errs, c.score)
		}
	}
}

func TestScriptMetric(t *testing.T) {
	if ScriptMetric("hello world") != text.MetricWER || ScriptMetric("你好世界") != text.MetricCER {
		t.Fatal("wrong metric by script")
	}
}

// accept: VER-04#2
func TestAlignmentSpansInOriginalCoordinates(t *testing.T) {
	ref := "On Monday, Mrs. Okafor approved the 42 new permits."
	hyp := "On Monday, missus Okafor rejected the forty-two new permits."
	got := Score(ref, hyp, text.MetricWER)
	if len(got.Edits) != 1 {
		t.Fatalf("edits = %+v", got.Edits)
	}
	e := got.Edits[0]
	if e.Kind != Substitution || e.Expected != "approved" || e.Heard != "rejected" ||
		e.RefStart != strings.Index(ref, "approved") || e.HeardStart != strings.Index(hyp, "rejected") {
		t.Fatalf("edit = %+v", e)
	}
	if !strings.Contains(got.Report(), `substitution at 23: expected "approved" / heard "rejected"`) {
		t.Fatalf("report:\n%s", got.Report())
	}
}

func TestAlignmentInsertionAndDeletion(t *testing.T) {
	ref := "read every word here"
	got := Score(ref, "Sure, read every word", text.MetricWER)
	if len(got.Edits) != 2 {
		t.Fatalf("edits = %+v", got.Edits)
	}
	ins, del := got.Edits[0], got.Edits[1]
	if ins.Kind != Insertion || ins.Heard != "Sure," || ins.Expected != "" || ins.RefStart != 0 {
		t.Fatalf("insertion = %+v", ins)
	}
	if del.Kind != Deletion || del.Expected != "here" || del.Heard != "" || del.HeardStart != len("Sure, read every word") {
		t.Fatalf("deletion = %+v", del)
	}
}

func TestAlignmentCERSpan(t *testing.T) {
	got := Score("你好世界", "你好时界", text.MetricCER)
	if len(got.Edits) != 1 || got.Edits[0].Expected != "世" || got.Edits[0].Heard != "时" {
		t.Fatalf("edits = %+v", got.Edits)
	}
}

func TestCanonTokensMatchCanon(t *testing.T) {
	for _, s := range []string{
		"St. Louis is on Main St.", "one hundred and five", "Then 42 students arrived.",
		"我有四十二本书", "e.g. the 16th", "", "  ", "Dr. Who vs. Mr. Smith, etc.", "两万人",
	} {
		toks := CanonTokens(s)
		want := Canon(s)
		if len(toks) != len(want) {
			t.Fatalf("%q: %d tokens, Canon has %d", s, len(toks), len(want))
		}
		for i, tk := range toks {
			if tk.Text != want[i] || tk.Start < 0 || tk.End > len(s) || tk.Start > tk.End {
				t.Fatalf("%q: token %d = %+v, want %q", s, i, tk, want[i])
			}
		}
	}
}

func FuzzCanonTokens(f *testing.F) {
	for _, s := range []string{"St. Louis", "one hundred and five", "四十二", "Mr00", "e-mail 3.5%"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		toks := CanonTokens(s)
		want := Canon(s)
		if len(toks) != len(want) {
			t.Fatalf("%d tokens, Canon has %d", len(toks), len(want))
		}
		last := 0
		for i, tk := range toks {
			if tk.Text != want[i] || tk.Start > tk.End || tk.End > len(s) || tk.Start < 0 {
				t.Fatalf("token %d = %+v, want %q", i, tk, want[i])
			}
			if tk.End < last {
				t.Fatalf("token %d goes backwards: %+v", i, tk)
			}
			last = tk.End
		}
	})
}

// The 50 ms target is a benchmark, never a timed test (tests run under -race).
//
// accept: VER-04#3
func BenchmarkScore2000Words(b *testing.B) {
	words := strings.Fields("the quick brown fox jumps over the lazy dog while forty two students watch")
	var ref, hyp strings.Builder
	for i := range 2000 {
		w := words[i%len(words)]
		ref.WriteString(w + " ")
		if i%97 == 0 {
			w = "zebra"
		}
		hyp.WriteString(w + " ")
	}
	r, h := ref.String(), hyp.String()
	b.ResetTimer()
	for b.Loop() {
		Score(r, h, text.MetricWER)
	}
}

// naiveDistance is the textbook Levenshtein distance, the oracle for the banded alignment.
func naiveDistance(a, b []string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := prev[j-1]
			if a[i-1] != b[j-1] {
				c++
			}
			cur[j] = min(c, prev[j]+1, cur[j-1]+1)
		}
		prev = cur
	}
	return prev[len(b)]
}

func FuzzAlignDistance(f *testing.F) {
	f.Add("abcabcabcabcabcabcabcabcabcabcabcabcabc", "xbcabcabcabcaycabcabcabbcabcabcabcabzzz")
	f.Add("", "abc")
	f.Add("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	f.Fuzz(func(t *testing.T, x, y string) {
		a, b := strings.Split(x, ""), strings.Split(y, "")
		errs, i, j := 0, 0, 0
		for _, st := range align(a, b) {
			if !st.match {
				errs++
			}
			if st.ref >= 0 {
				if st.ref != i {
					t.Fatalf("ref step %d out of order", st.ref)
				}
				i++
			}
			if st.hyp >= 0 {
				if st.hyp != j {
					t.Fatalf("hyp step %d out of order", st.hyp)
				}
				j++
			}
			if st.match && a[st.ref] != b[st.hyp] {
				t.Fatalf("false match at %d/%d", st.ref, st.hyp)
			}
		}
		if i != len(a) || j != len(b) {
			t.Fatalf("alignment covers %d/%d of %d/%d", i, j, len(a), len(b))
		}
		if want := naiveDistance(a, b); errs != want {
			t.Fatalf("distance %d, want %d", errs, want)
		}
	})
}

// Cases from the live TXT-11#3 corpus where the audio was right and the score was not
// (design §8.3: years read as pairs and as cardinals are both accepted; §8.4: Han is compared
// by character even inside a word-scored chunk).
func TestScoreEquivalentForms(t *testing.T) {
	for _, c := range []struct{ ref, hyp string }{
		{"the years nineteen fourteen to nineteen eighteen", "the years 1914 to 1918"},
		{"in twenty twenty-six", "in 2026"},
		{"in nineteen oh five", "in 1905"},
		{"请在iPhone 17上打开App Store，下载版本v2.0。", "请在 iPhone 17 上打开 App Store，下载版本 V2.0。"},
		{"Visit example dot com for details.", "Visit example.com for details."},
		{"mail ops at example dot co dot uk", "mail ops at example.co.uk"},
	} {
		got := Score(c.ref, c.hyp, ScriptMetric(c.ref))
		if got.Errors != 0 || got.Score != 1 {
			t.Errorf("Score(%q, %q) = %d errors, score %.3f; want a match", c.ref, c.hyp, got.Errors, got.Score)
		}
	}
	for _, c := range []struct{ ref, hyp string }{
		{"in nineteen fourteen", "in 1940"},
		{"in twenty twenty-six", "in 2025"},
		{"请在iPhone 17上打开App", "请在 iPhone 18 上打开 App"},
		{"grew three point five percent", "grew 35 percent"},
		{"see e.g. this", "see eg this"},
	} {
		if got := Score(c.ref, c.hyp, ScriptMetric(c.ref)); got.Errors == 0 {
			t.Errorf("Score(%q, %q) found no error", c.ref, c.hyp)
		}
	}
}

// A spelled-out code scores the same however STT spaces its letters, and a wrong letter in it
// still costs exactly one error.
func TestSpelledCodeScoresWithoutSpacingErrors(t *testing.T) {
	ref := "Order part XJ four thousand four hundred seventy-one B today."
	for _, hyp := range []string{"Order part X J 4471 B today.", "Order part XJ 4471 B today.", "Order part XJ-4471-B today."} {
		if r := Score(ref, hyp, text.MetricWER); r.Errors != 0 || r.Score != 1 {
			t.Errorf("%q: %d errors, score %v", hyp, r.Errors, r.Score)
		}
	}
	if r := Score(ref, "Order part X K 4471 B today.", text.MetricWER); r.Errors != 1 {
		t.Errorf("wrong letter: %d errors, want 1", r.Errors)
	}
}

// A long reference against an empty or one-word transcript is all deletions, and the band
// stores at most m+1 cells a row, so the table stays linear in the reference: 100,000 units
// against nothing once allocated about 10 GB.
func TestAlignOneSidedStaysLinear(t *testing.T) {
	const n = 100_000
	ref := make([]int32, n)
	for i := range ref {
		ref[i] = int32(i%7 + 1)
	}
	for _, hyp := range [][]int32{nil, {1}} {
		steps := alignCore(ref, hyp)
		dels := 0
		for _, s := range steps {
			if s.hyp < 0 {
				dels++
			}
		}
		if len(steps) != n || dels != n-len(hyp) {
			t.Errorf("m=%d: %d steps, %d deletions", len(hyp), len(steps), dels)
		}
	}
	for k := 16; k <= n; k *= 2 {
		if w := bandWidth(n, 1, k); w > 2 {
			t.Errorf("k=%d: %d cells a row against one unit, want at most 2", k, w)
		}
	}
	if allocs := testing.AllocsPerRun(5, func() { alignCore(ref, nil) }); allocs > 1 {
		t.Errorf("empty transcript: %v allocations, want only the steps", allocs)
	}
}

func BenchmarkAlignLongAgainstShort(b *testing.B) {
	ref := make([]int32, 100_000)
	for i := range ref {
		ref[i] = int32(i % 7)
	}
	b.ReportAllocs()
	for b.Loop() {
		alignCore(ref, []int32{99})
	}
}
