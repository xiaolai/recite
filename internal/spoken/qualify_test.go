//go:build qualify

package spoken_test

import (
	"os"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/text"
)

// probeCorpus is the 21 items of dev-docs/probe_textnorm.py, Chinese included.
var probeCorpus = []string{
	"The deal was worth $3.5M, or about €3.2 million.",
	"The meeting is on 2026-10-11 at 3:45pm.",
	"Take 1/2 cup on 1/2.",
	"Growth hit 50% (up from 12.5%).",
	"Read pages 3–5 and chapters 10-12.",
	"Upgrade to v1.2.3 before Friday.",
	"Write it in C++ or C#, not F#.",
	"Visit https://example.com/docs?id=7 for details.",
	"Mail ops@example.com today.",
	"Call +1 (415) 555-0134.",
	"NASA and the FBI met at the UN; the CEO used SQL.",
	"Dr. Smith lives on St. James St., No. 5.",
	"A → B, x ≥ 3, & 2 × 4 = 8.",
	"Henry VIII ruled in the 16th century; see Chapter IV.",
	"Great job 🎉👍 see you soon 😀",
	"This was proven [1] and later disputed.[2]",
	"Ths   is  ,,a  sentnce with typos!!!  and​ zero-width chars",
	"It weighs 5kg, runs at 120km/h, and costs 3¥.",
	"2026年10月11日下午3点45分，价格是¥1,280.50，增长了12.5%。",
	"请在iPhone 17上打开App Store，下载版本v2.0。",
	"第1/2季度，温度是-5℃到30℃。",
}

// Qualification (TXT-11#3): the 51 chunks, normalised, one take each: at least
// 49 verify on the first attempt and all 51 within the attempt limit. The
// render-and-verify half needs verification and assembly (WP-2.9, plan §1.7);
// until then this builds and normalises the corpus only.
//
// accept: TXT-11#3
func TestQualifyEnglishSpokenForm(t *testing.T) {
	raw, err := os.ReadFile("testdata/qualify-en.txt")
	if err != nil {
		t.Fatal(err)
	}
	corpus := append(append([]string(nil), probeCorpus...), strings.Split(strings.TrimRight(string(raw), "\n"), "\n")...)
	if len(corpus) != 51 {
		t.Fatalf("corpus has %d chunks, want 51", len(corpus))
	}
	for _, c := range corpus {
		g, err := guard.Run(c, guard.Options{})
		if err != nil {
			t.Fatalf("%q: guard: %v", c, err)
		}
		lang := text.Resolve(g.Text, text.ResolveOptions{})
		if _, err := spoken.Normalize(g.Text, lang.Code, spoken.Options{}); err != nil {
			t.Fatalf("%q: %v", c, err)
		}
	}
	t.Skip("rendering and verifying the 51 chunks needs the WP-2.9 pipeline")
}
