package text

import (
	"math/rand"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/spoken"
)

// segmentText runs the pipeline order of design §7: spoken form, then
// segmentation with plain-text paragraph breaks.
func segmentText(t testing.TB, s string, mode spoken.Mode, o SegmentOptions) (spoken.Result, Segmentation) {
	t.Helper()
	r, err := spoken.Normalize(s, "en", spoken.Options{Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	o.Breaks = append(o.Breaks, ParagraphBreaks(r.Display)...)
	sg, err := Segment(r, o)
	if err != nil {
		t.Fatal(err)
	}
	checkSegmentation(t, r, sg, o)
	return r, sg
}

// checkSegmentation asserts the TXT-03 invariants: exact reconstruction of
// both texts, whitespace-only separators, cuts that agree with the span map,
// and the 1.3x budget bound.
func checkSegmentation(t testing.TB, r spoken.Result, sg Segmentation, o SegmentOptions) {
	t.Helper()
	var sp, dp strings.Builder
	sp.WriteString(r.Spoken[sg.LeadSpoken.Start:sg.LeadSpoken.End])
	dp.WriteString(r.Display[sg.LeadDisplay.Start:sg.LeadDisplay.End])
	m := spanMap{segs: r.Segments}
	type cut struct{ s, d int }
	cuts := []cut{{sg.LeadSpoken.End, sg.LeadDisplay.End}}
	for i, c := range sg.Chunks {
		if c.Text != r.Spoken[c.Spoken.Start:c.Spoken.End] || c.DisplayText != r.Display[c.Display.Start:c.Display.End] {
			t.Fatalf("chunk %d: text fields disagree with ranges", i)
		}
		if strings.TrimSpace(c.Text) == "" {
			t.Fatalf("chunk %d is blank", i)
		}
		sp.WriteString(c.Text)
		sp.WriteString(r.Spoken[c.SpokenSep.Start:c.SpokenSep.End])
		dp.WriteString(c.DisplayText)
		dp.WriteString(r.Display[c.DisplaySep.Start:c.DisplaySep.End])
		for _, sep := range []string{r.Spoken[c.SpokenSep.Start:c.SpokenSep.End], r.Display[c.DisplaySep.Start:c.DisplaySep.End]} {
			if strings.TrimSpace(sep) != "" {
				t.Fatalf("chunk %d: separator %q is not whitespace", i, sep)
			}
		}
		cuts = append(cuts, cut{c.Spoken.End, c.Display.End}, cut{c.SpokenSep.End, c.DisplaySep.End})
		budget := o.Budget
		if budget == 0 {
			budget = DefaultChunkSeconds
		}
		if c.Seconds > OverlongFactor*budget {
			if !slices.ContainsFunc(sg.Warnings, func(w SegmentWarning) bool { return w.Code == "S-OVERLONG" && w.Chunk == i }) {
				t.Fatalf("chunk %d: %.2f s exceeds 1.3 x %.2f s without a warning", i, c.Seconds, budget)
			}
			if p, ok := innerCut(r.Spoken, m, c.Spoken); ok {
				t.Fatalf("chunk %d: overlong %q could have been cut at %d", i, c.Text, p)
			}
		}
	}
	if sp.String() != r.Spoken {
		t.Fatalf("spoken form not reconstructed:\n got %q\nwant %q", sp.String(), r.Spoken)
	}
	if dp.String() != r.Display {
		t.Fatalf("display text not reconstructed:\n got %q\nwant %q", dp.String(), r.Display)
	}
	// Every cut maps through the span map exactly: never inside a rewrite,
	// and at the matching offset inside verbatim text.
	for _, c := range cuts {
		for _, g := range r.Segments {
			if g.Spoken.Start < c.s && c.s < g.Spoken.End {
				if g.Rule != "" {
					t.Fatalf("cut at spoken %d splits rewrite %q", c.s, r.Spoken[g.Spoken.Start:g.Spoken.End])
				}
				if c.d != g.Display.Start+c.s-g.Spoken.Start {
					t.Fatalf("cut at spoken %d maps to display %d, not %d", c.s, c.d, g.Display.Start+c.s-g.Spoken.Start)
				}
			}
		}
	}
}

// innerCut finds a whitespace run or CJK pair inside rg where a cut would
// have been exact: both edges of the dropped separator map to the display.
func innerCut(s string, m spanMap, rg spoken.Range) (int, bool) {
	var prev rune
	for i, r := range s[rg.Start:rg.End] {
		p := rg.Start + i
		if p > rg.Start && m.cuttable(p) {
			if isCJK(prev) && isCJK(r) {
				return p, true
			}
			if unicode.IsSpace(r) && (i == 0 || !unicode.IsSpace(prev)) {
				end := p + len(s[p:rg.End]) - len(strings.TrimLeftFunc(s[p:rg.End], unicode.IsSpace))
				if end < rg.End && m.cuttable(end) {
					return p, true
				}
			}
		}
		prev = r
	}
	return 0, false
}

// accept: TXT-03#1
func FuzzSegment(f *testing.F) {
	for _, s := range []string{"", " ", "\n\n", "Mrs. Okafor arrived. She left.", "Growth hit 3.5% today.",
		"Use e.g. apples.\n\nThen \"go.\" Now!", "我们明天去北京。你去吗？", "The word 北京 means north capital.",
		"The deal was worth $3.5M, or about €3.2 million.", "Dr. Smith lives on St. James St., No. 5.",
		"  lead and trail  \n", "a b　c", strings.Repeat("word ", 50), strings.Repeat("字", 60)} {
		f.Add(s, uint8(16), false)
	}
	f.Fuzz(func(t *testing.T, s string, b uint8, off bool) {
		if !utf8.ValidString(s) {
			return // the decoder (TXT-01) rejects invalid UTF-8 before this stage
		}
		mode := spoken.ModeAuto
		if off {
			mode = spoken.ModeOff
		}
		o := SegmentOptions{Budget: 0.1 + float64(b)/32}
		// A pause mark somewhere in the text, on a rune boundary.
		if len(s) > 0 {
			p := int(b) % (len(s) + 1)
			for p > 0 && !utf8.RuneStart(s[p%len(s)]) && p < len(s) {
				p--
			}
			o.Breaks = []Break{{Display: p, Kind: BoundaryPause}}
		}
		r, err := spoken.Normalize(s, "en", spoken.Options{Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		o.Breaks = append(o.Breaks, ParagraphBreaks(r.Display)...)
		sg, err := Segment(r, o)
		if err != nil {
			t.Fatal(err)
		}
		checkSegmentation(t, r, sg, o)
	})
}

type wantChunk struct {
	spoken, display string
	boundary        Boundary
}

// accept: TXT-03#2
func TestSegmentGolden(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		mode   spoken.Mode
		budget float64
		want   []wantChunk
	}{
		// 1.6 s is 22.4 Latin characters: the sentence must be cut at a word,
		// never after the abbreviation.
		{"Mrs. Okafor", "Ask Mrs. Okafor about the plan now.", spoken.ModeOff, 1.6, []wantChunk{
			{"Ask Mrs. Okafor about", "Ask Mrs. Okafor about", BoundaryWord},
			{"the plan now.", "the plan now.", BoundaryEnd}}},
		{"Mrs. spoken", "Mrs. Okafor arrived. She left.", spoken.ModeAuto, 2, []wantChunk{
			{"Missus Okafor arrived.", "Mrs. Okafor arrived.", BoundarySentence},
			{"She left.", "She left.", BoundaryEnd}}},
		{"3.5% as written", "Rates rose 3.5% over the past year.", spoken.ModeOff, 1.6, []wantChunk{
			{"Rates rose 3.5% over", "Rates rose 3.5% over", BoundaryWord},
			{"the past year.", "the past year.", BoundaryEnd}}},
		// The rewrite is one unit: its inner spaces are not cut points.
		{"3.5% spoken", "Growth hit 3.5% today. Costs fell.", spoken.ModeAuto, 3.2, []wantChunk{
			{"Growth hit three point five percent today.", "Growth hit 3.5% today.", BoundarySentence},
			{"Costs fell.", "Costs fell.", BoundaryEnd}}},
		{"e.g.", "Buy fruit e.g. apples and ripe pears.", spoken.ModeOff, 1.6, []wantChunk{
			{"Buy fruit e.g. apples", "Buy fruit e.g. apples", BoundaryWord},
			{"and ripe pears.", "and ripe pears.", BoundaryEnd}}},
		{"quote after full stop", `He said "Stop." Then he left.`, spoken.ModeOff, 1.6, []wantChunk{
			{`He said "Stop."`, `He said "Stop."`, BoundarySentence},
			{"Then he left.", "Then he left.", BoundaryEnd}}},
		{"curly quote after full stop", "He said “Stop.” Then he left.", spoken.ModeOff, 1.6, []wantChunk{
			{"He said “Stop.”", "He said “Stop.”", BoundarySentence},
			{"Then he left.", "Then he left.", BoundaryEnd}}},
		// 2 s is 8.6 Han characters; CJK marks need no following space.
		{"CJK marks", "我们明天去北京。你去吗？他也去！", spoken.ModeAuto, 2, []wantChunk{
			{"我们明天去北京。", "我们明天去北京。", BoundarySentence},
			{"你去吗？他也去！", "你去吗？他也去！", BoundaryEnd}}},
		{"CJK closing bracket", "他说：「好。」我们走吧。", spoken.ModeAuto, 1.8, []wantChunk{
			{"他说：「好。」", "他说：「好。」", BoundarySentence},
			{"我们走吧。", "我们走吧。", BoundaryEnd}}},
		{"mixed English and Chinese", "The word 北京 means north capital. 我们明天去北京。", spoken.ModeAuto, 3, []wantChunk{
			{"The word 北京 means north capital.", "The word 北京 means north capital.", BoundarySentence},
			{"我们明天去北京。", "我们明天去北京。", BoundaryEnd}}},
		{"clause fallback", "First part goes here, and the second part follows it.", spoken.ModeOff, 2.3, []wantChunk{
			{"First part goes here,", "First part goes here,", BoundaryClause},
			{"and the second part follows it.", "and the second part follows it.", BoundaryEnd}}},
		{"paragraph within budget", "One.\n\nTwo.", spoken.ModeAuto, 25, []wantChunk{
			{"One.", "One.", BoundaryParagraph},
			{"Two.", "Two.", BoundaryEnd}}},
		{"ellipsis", "Wait... Then we go home now.", spoken.ModeOff, 1.5, []wantChunk{
			{"Wait...", "Wait...", BoundarySentence},
			{"Then we go home now.", "Then we go home now.", BoundaryEnd}}},
		{"initial", "Ask J. Smith to call me back soon.", spoken.ModeOff, 1.5, []wantChunk{
			{"Ask J. Smith to call", "Ask J. Smith to call", BoundaryWord},
			{"me back soon.", "me back soon.", BoundaryEnd}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, sg := segmentText(t, c.in, c.mode, SegmentOptions{Budget: c.budget})
			var got []wantChunk
			for _, ch := range sg.Chunks {
				got = append(got, wantChunk{ch.Text, ch.DisplayText, ch.Boundary})
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

// accept: TXT-03#2
// accept: TXT-03#3
func TestSegmentLongSentenceNoPunctuation(t *testing.T) {
	words := []string{"river", "stone", "quiet", "morning", "light", "over", "the", "hills", "and", "far"}
	var b strings.Builder
	for i := 0; b.Len() < 2000; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(words[i%len(words)])
	}
	in := b.String()
	_, sg := segmentText(t, in, spoken.ModeOff, SegmentOptions{})
	if len(sg.Chunks) < 2 {
		t.Fatalf("a %d-character sentence at 25 s became %d chunk(s)", len(in), len(sg.Chunks))
	}
	for i, c := range sg.Chunks {
		// ASCII Latin: every character is 1/14 s, so this checks the estimate too.
		if secs := float64(len(c.Text)) / 14; secs > DefaultChunkSeconds+1e-9 {
			t.Errorf("chunk %d is %.2f s, over the budget", i, secs)
		}
		if i < len(sg.Chunks)-1 && c.Boundary != BoundaryWord {
			t.Errorf("chunk %d boundary %s, want word", i, c.Boundary)
		}
	}
	if len(sg.Warnings) != len(sg.Chunks)-1 || sg.Warnings[0].Code != "S-SPLIT" {
		t.Errorf("want one S-SPLIT warning per cut, got %+v", sg.Warnings)
	}

	han := strings.Repeat("我们明天去北京看看", 223)[:2000*3]
	_, sg = segmentText(t, han, spoken.ModeAuto, SegmentOptions{})
	for i, c := range sg.Chunks {
		if secs := float64(utf8.RuneCountInString(c.Text)) / 4.3; secs > DefaultChunkSeconds+1e-9 {
			t.Errorf("han chunk %d is %.2f s, over the budget", i, secs)
		}
	}
	if len(sg.Chunks) < 2 || sg.Chunks[0].Boundary != BoundaryChar {
		t.Errorf("2,000 Han characters with no punctuation: %d chunks", len(sg.Chunks))
	}
}

// accept: TXT-03#3
func TestSegmentBudgetBound(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	vocab := []string{"a", "the", "Mrs.", "e.g.", "3.5%", "river", "extraordinarily", "stone,", "quiet;",
		"done.", "why?", "\"yes.\"", "42", "$3.5M", "St.", "No.", "\n\n", "北京", "我们。", "好，"}
	for n := 0; n < 300; n++ {
		var b strings.Builder
		for w := rng.Intn(400); w >= 0; w-- {
			b.WriteString(vocab[rng.Intn(len(vocab))])
			b.WriteByte(' ')
		}
		budget := 0.5 + rng.Float64()*10
		_, sg := segmentText(t, b.String(), spoken.ModeAuto, SegmentOptions{Budget: budget})
		for i, c := range sg.Chunks {
			over := c.Seconds > OverlongFactor*budget
			warned := slices.ContainsFunc(sg.Warnings, func(w SegmentWarning) bool { return w.Code == "S-OVERLONG" && w.Chunk == i })
			if over && !warned {
				t.Fatalf("chunk %d %q: %.2f s > 1.3 x %.2f s", i, c.Text, c.Seconds, budget)
			}
		}
	}
}

// accept: TXT-03#3
func TestSegmentUnbreakableRunWarned(t *testing.T) {
	run := strings.Repeat("x", 200) // 14.3 s at 14 chars/s
	_, sg := segmentText(t, "Short one. "+run+" end.", spoken.ModeOff, SegmentOptions{Budget: 2})
	var found bool
	for i, c := range sg.Chunks {
		if c.Text == run {
			found = slices.ContainsFunc(sg.Warnings, func(w SegmentWarning) bool { return w.Code == "S-OVERLONG" && w.Chunk == i })
		}
	}
	if !found {
		t.Fatalf("unbreakable run not isolated and warned: %+v", sg)
	}
}

func TestSegmentPauseAndBoundaries(t *testing.T) {
	in := "Alpha beta. Gamma delta."
	r, err := spoken.Normalize(in, "en", spoken.Options{})
	if err != nil {
		t.Fatal(err)
	}
	o := SegmentOptions{Breaks: []Break{{Display: strings.Index(in, "Gamma"), Kind: BoundaryPause}}}
	sg, err := Segment(r, o)
	if err != nil {
		t.Fatal(err)
	}
	checkSegmentation(t, r, sg, o)
	if len(sg.Chunks) != 2 || sg.Chunks[0].Boundary != BoundaryPause || sg.Chunks[0].Text != "Alpha beta." {
		t.Fatalf("pause did not force a split: %+v", sg.Chunks)
	}
	if sg.Chunks[0].Script != ScriptLatin {
		t.Errorf("script %q", sg.Chunks[0].Script)
	}

	// The strongest boundary wins when breaks coincide around blank text.
	_, sg = segmentText(t, "One.\n\n  \n\nTwo.", spoken.ModeAuto, SegmentOptions{})
	if len(sg.Chunks) != 2 || sg.Chunks[0].Boundary != BoundaryParagraph {
		t.Fatalf("blank paragraphs: %+v", sg.Chunks)
	}
	_, sg = segmentText(t, " \n\n ", spoken.ModeAuto, SegmentOptions{})
	if len(sg.Chunks) != 0 {
		t.Fatalf("whitespace became chunks: %+v", sg.Chunks)
	}
}

func TestSegmentRejectsBadInput(t *testing.T) {
	r, err := spoken.Normalize("Hello there.", "en", spoken.Options{})
	if err != nil {
		t.Fatal(err)
	}
	bad := []struct {
		name string
		r    spoken.Result
		o    SegmentOptions
	}{
		{"no span map", spoken.Result{Display: "x", Spoken: "x"}, SegmentOptions{}},
		{"negative budget", r, SegmentOptions{Budget: -1}},
		{"break out of range", r, SegmentOptions{Breaks: []Break{{Display: 99, Kind: BoundaryPause}}}},
		{"unknown kind", r, SegmentOptions{Breaks: []Break{{Display: 1, Kind: "nope"}}}},
	}
	for _, c := range bad {
		if _, err := Segment(c.r, c.o); err == nil {
			t.Errorf("%s: no error", c.name)
		}
	}
}
