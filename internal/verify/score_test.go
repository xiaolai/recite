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
