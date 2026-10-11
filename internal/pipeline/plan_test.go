package pipeline

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/text"
)

func testSettings() Settings {
	return Settings{Model: "gpt-realtime-2.1", Voice: "cedar", Speed: 1, Effort: "minimal",
		InstructionsVersion: 2, Emotion: "neutral"}
}

// planText runs the design §7 order: guard, spoken form, segmentation, plan.
func planText(t *testing.T, raw string, s Settings, budget float64) Plan {
	t.Helper()
	g, err := guard.Run(raw, guard.Options{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := spoken.Normalize(g.Text, "en", spoken.Options{Mode: spoken.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	seg, err := text.Segment(r, text.SegmentOptions{Budget: budget, Breaks: text.ParagraphBreaks(r.Display)})
	if err != nil {
		t.Fatal(err)
	}
	var fs []Finding
	for _, f := range g.Findings {
		fs = append(fs, Finding{Code: string(f.Code), Action: string(f.Action)})
	}
	p, err := Build(Input{Display: r.Display, Seg: seg, Findings: fs}, s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

const sample = "Dr. Okafor paid $3.5M on 2026-10-11.\n\nThe second paragraph is here, and it goes on for a while so that it splits.\n\nThird."

// accept: RUN-01#1
func TestPlanJSONByteIdentical(t *testing.T) {
	first, err := planText(t, sample, testSettings(), 2).JSON()
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, err := planText(t, sample, testSettings(), 2).JSON()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("plan JSON differs between runs:\n%s\n---\n%s", first, again)
		}
	}
	// Platform independence: no computed float reaches the JSON, and the IDs are fixed-width.
	p := planText(t, sample, testSettings(), 2)
	if len(p.Chunks) < 3 || p.Chunks[0].ID != "c0001" || p.Chunks[1].ID != "c0002" {
		t.Fatalf("chunk IDs: %+v", p.Chunks)
	}
	if !strings.Contains(string(first), `"est_ms":`) || strings.Contains(string(first), `"est_ms": 0.`) {
		t.Fatalf("estimate encoding: %s", first)
	}
	c := p.Chunks[0]
	if c.SessionKey != "gpt-realtime-2.1|cedar|1" || c.Effort != "minimal" || c.Delivery.Emotion != "neutral" {
		t.Fatalf("chunk settings: %+v", c)
	}
	if c.Gap != audio.GapSentence && c.Gap != audio.GapParagraph {
		t.Fatalf("gap class %q", c.Gap)
	}
	if last := p.Chunks[len(p.Chunks)-1]; last.Boundary != text.BoundaryEnd || last.Gap != audio.GapNone {
		t.Fatalf("last chunk: %+v", last)
	}
}

// accept: RUN-01#2
func TestCacheKeyIgnoresDisplayOnlyEdits(t *testing.T) {
	a := planText(t, "The result was clear.[1] Nobody argued.", testSettings(), 25)
	b := planText(t, "The result was clear. Nobody argued.", testSettings(), 25)
	if len(a.Chunks) != 1 || len(b.Chunks) != 1 {
		t.Fatalf("chunks: %d, %d", len(a.Chunks), len(b.Chunks))
	}
	if len(a.Chunks[0].Findings) == 0 {
		t.Fatalf("the footnote marker should be a finding: %+v", a.Chunks[0])
	}
	if a.Chunks[0].CacheKey != b.Chunks[0].CacheKey {
		t.Fatalf("display-only edit changed the cache key: %s vs %s", a.Chunks[0].CacheKey, b.Chunks[0].CacheKey)
	}
	// Anything that shapes the audio does change it.
	s := testSettings()
	base := CacheKey(s, nil, "Hello.")
	for name, mut := range map[string]func(*Settings){
		"voice":  func(s *Settings) { s.Voice = "marin" },
		"speed":  func(s *Settings) { s.Speed = 1.25 },
		"effort": func(s *Settings) { s.Effort = "low" },
		"model":  func(s *Settings) { s.Model = "gpt-realtime-2" },
		"style":  func(s *Settings) { s.Style = "Warm." },
		"instr":  func(s *Settings) { s.InstructionsVersion = 3 },
		"emo":    func(s *Settings) { s.Emotion = "storyteller" },
	} {
		m := testSettings()
		mut(&m)
		if CacheKey(m, nil, "Hello.") == base {
			t.Errorf("%s does not change the cache key", name)
		}
	}
	if CacheKey(s, nil, "Hello!") == base || CacheKey(s, []Hint{{Term: "Hello", Hint: "heh-LOW"}}, "Hello.") == base {
		t.Error("spoken text or hints do not change the cache key")
	}
	h := []Hint{{Term: "b", Hint: "2"}, {Term: "a", Say: "1"}, {Term: "c", Say: "3"}}
	if CacheKey(s, h, "x") != CacheKey(s, []Hint{h[2], h[1], h[0]}, "x") {
		t.Error("hint order changes the cache key")
	}
	// A say pair's rewrite is in the spoken text already; the pair adds nothing to the key.
	if CacheKey(s, []Hint{{Term: "a", Say: "1"}}, "x") != CacheKey(s, nil, "x") {
		t.Error("a say pair changes the key of the same spoken text")
	}
}

// accept: AUD-03#6
func TestGapSettingsCauseNoRerender(t *testing.T) {
	p := planText(t, sample, testSettings(), 2)
	cache := map[string]audio.PCM{}
	for _, c := range p.Chunks {
		cache[c.CacheKey] = make(audio.PCM, 2400)
	}
	other := audio.DefaultGaps()
	other.Seed = 9
	other.Paragraph = audio.GapRange{Min: time.Second, Max: 2 * time.Second}
	for _, g := range []audio.Gaps{audio.DefaultGaps(), other} {
		// Gaps are not a planner input, so the keys and the cache hits cannot change.
		q := planText(t, sample, testSettings(), 2)
		renders := 0
		for _, c := range q.Chunks {
			if _, ok := cache[c.CacheKey]; !ok {
				renders++
			}
		}
		if renders != 0 {
			t.Fatalf("gap settings %+v caused %d re-render(s)", g, renders)
		}
		if _, err := audio.GapSamples(g, q.Joins()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGapClassFor(t *testing.T) {
	want := map[text.Boundary]audio.GapClass{
		text.BoundaryChar: audio.GapSentence, text.BoundaryWord: audio.GapSentence,
		text.BoundaryClause: audio.GapSentence, text.BoundarySentence: audio.GapSentence,
		text.BoundaryListItem: audio.GapList, text.BoundaryParagraph: audio.GapParagraph,
		text.BoundarySection: audio.GapSection, text.BoundaryPause: audio.GapPause, text.BoundaryEnd: audio.GapNone,
	}
	for b, w := range want {
		if got, err := GapClassFor(b); err != nil || got != w {
			t.Errorf("%s → %s, %v; want %s", b, got, err, w)
		}
	}
	if _, err := GapClassFor("bogus"); err == nil {
		t.Error("unknown boundary accepted")
	}
}

func TestBuildPausesAndValidation(t *testing.T) {
	seg := text.Segmentation{Chunks: []text.Chunk{
		{Text: "One.", DisplayText: "One.", Boundary: text.BoundaryPause, Seconds: 0.4},
		{Text: "Two.", DisplayText: "Two.", Boundary: text.BoundaryEnd, Seconds: 0.4},
	}}
	p, err := Build(Input{Seg: seg, Pauses: []time.Duration{1234 * time.Millisecond}}, testSettings())
	if err != nil {
		t.Fatal(err)
	}
	if j := p.Joins(); j[0].Class != audio.GapPause || j[0].Pause != 1234*time.Millisecond {
		t.Fatalf("pause join %+v", j[0])
	}
	if _, err := Build(Input{Seg: seg}, testSettings()); err == nil {
		t.Error("pause without duration accepted")
	}
	if _, err := Build(Input{Seg: seg, Pauses: []time.Duration{1, 2}}, testSettings()); err == nil {
		t.Error("extra pause durations accepted")
	}
	bad := testSettings()
	bad.Speed = 0
	if _, err := Build(Input{Seg: seg}, bad); err == nil {
		t.Error("zero speed accepted")
	}
}
