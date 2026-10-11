package cli

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/text"
	"github.com/xiaolai/recite/internal/verify"
)

// planChunks segments the spoken form and builds the chunk plan (RUN-01). input is the text
// the guard ran on, g its result, and form the spoken form of g.Text. Findings are passed to
// the planner at byte offsets of the display text (form.Display).
func planChunks(input string, g guard.Result, form spoken.Result, budget float64, s realtime.Settings) (pipeline.Plan, text.Segmentation, error) {
	return planDoc(plainDoc(input), g, form, nil, budget, s)
}

// planDoc is planChunks for a text document: a Markdown document brings its own block breaks.
// lex is the lexicon form was normalised with, so its spans become hints and protected ranges.
func planDoc(doc textDoc, g guard.Result, form spoken.Result, lex spoken.Lexicon, budget float64, s realtime.Settings) (pipeline.Plan, text.Segmentation, error) {
	input := doc.text
	breaks := docBreaks(doc, form.Display, func(off int) int { return displayOffset(g.Edits, off) })
	pauses := pausesOf(g)
	for _, p := range pauses {
		breaks = append(breaks, text.Break{Display: p.at, Kind: text.BoundaryPause})
	}
	seg, err := text.Segment(form, text.SegmentOptions{Budget: budget, Speed: s.Speed, Breaks: breaks})
	if err != nil {
		return pipeline.Plan{}, text.Segmentation{}, fmt.Errorf("segment: %w", err)
	}
	// A {{pause}} mark is an exact silence and a chunk boundary (TXT-13).
	durations, err := pauseDurations(seg, pauses)
	if err != nil {
		return pipeline.Plan{}, seg, err
	}
	in := pipeline.Input{
		Display:  form.Display,
		Seg:      seg,
		Findings: planFindings(input, g, form),
		Pauses:   durations,
		Marks:    append(markSpans(spokenMarks(g, form)), lexiconSpans(form, lex)...),
	}
	p, err := pipeline.Build(in, planSettings(s))
	if err != nil {
		return pipeline.Plan{}, seg, err
	}
	return p, seg, nil
}

// planSettings are the render settings that shape the plan and its cache keys.
func planSettings(s realtime.Settings) pipeline.Settings {
	return pipeline.Settings{
		Model: s.Model, Voice: s.Voice, Speed: s.Speed, Effort: string(s.Effort), InstructionsVersion: s.InstructionsVersion,
		Emotion: realtime.EmotionCacheID(s.Emotion), Style: s.Style,
	}
}

// planFindings converts the guard findings (line:col of input) and the spoken-form findings
// (line:col of the display text) to byte offsets of the display text.
func planFindings(input string, g guard.Result, form spoken.Result) []pipeline.Finding {
	out := make([]pipeline.Finding, 0, len(g.Findings)+len(form.Findings))
	for _, f := range g.Findings {
		at := displayOffset(g.Edits, byteOffset(input, f.Line, f.Col))
		out = append(out, pipeline.Finding{Code: string(f.Code), At: min(at, len(form.Display)), Action: string(f.Action)})
	}
	for _, f := range form.Findings {
		out = append(out, pipeline.Finding{Code: f.Code, At: byteOffset(form.Display, f.Line, f.Col), Action: "keep"})
	}
	return out
}

// byteOffset is the byte offset of the 1-based line and rune column in s, clamped to s.
func byteOffset(s string, line, col int) int {
	off := 0
	for l := 1; l < line; l++ {
		i := strings.IndexByte(s[off:], '\n')
		if i < 0 {
			return len(s)
		}
		off += i + 1
	}
	for c := 1; c < col && off < len(s) && s[off] != '\n'; c++ {
		_, n := utf8.DecodeRuneInString(s[off:])
		off += n
	}
	return off
}

// displayOffset maps a byte offset of the guard's input to the guard's output through its
// edits (sorted, non-overlapping): an offset inside an edited span, or at an insertion, maps
// to the start of its replacement.
func displayOffset(edits []guard.Edit, orig int) int {
	delta := 0
	for _, e := range edits {
		if orig < e.Start {
			break
		}
		if orig < e.End || orig == e.Start {
			return e.Start + delta
		}
		delta += len(e.Replacement) - (e.End - e.Start)
	}
	return orig + delta
}

// tasksFor turns the plan into scheduler tasks. A chunk holding a G-REQUEST finding gets the
// extra attempt of TXT-14.
func tasksFor(p pipeline.Plan, spec realtime.SessionSpec, delivery string, ladder []realtime.Effort) []pipeline.Task {
	tasks := make([]pipeline.Task, len(p.Chunks))
	for i, c := range p.Chunks {
		requestLike := false
		for _, f := range c.Findings {
			if f.Code == string(guard.Request) {
				requestLike = true
			}
		}
		tasks[i] = pipeline.Task{
			Chunk: verify.Chunk{
				ID:          c.ID,
				Spoken:      c.Spoken,
				Language:    text.Resolve(c.Spoken, text.ResolveOptions{}),
				Estimate:    time.Duration(max(c.EstMillis, 1)) * time.Millisecond,
				RequestLike: requestLike,
				Protected:   c.Protected,
				LangSpans:   langSpans(c.LangSpans),
				Lexicon:     lexiconWords(c.Hints),
				Equivalents: equivalents(c.Hints),
			},
			Spec:   spec,
			Ladder: ladder,
			// The delivery paragraph, then the speech-mark instructions, ride after the rules
			// (realtime.ResponseInstructions).
			Style:    chunkStyle(delivery, c.Instructions),
			CacheKey: c.CacheKey,
		}
	}
	return tasks
}

// lexiconWords are the words the lexicon pairs of a chunk put in its spoken form: the term
// of a hint (the text is unchanged, so the transcript should spell it) and the rewrite of a
// say entry. Verification protects them and offers them to STT as keywords.
func lexiconWords(hs []pipeline.Hint) []string {
	var out []string
	for _, h := range hs {
		if h.Say != "" {
			out = append(out, h.Say)
		} else {
			out = append(out, h.Term)
		}
	}
	return out
}

// equivalents are the say pairs of a chunk, for verification.
func equivalents(hs []pipeline.Hint) []verify.SayPair {
	var out []verify.SayPair
	for _, h := range hs {
		if h.Say != "" {
			out = append(out, verify.SayPair{Written: h.Term, Spoken: h.Say})
		}
	}
	return out
}

// langSpans converts the plan's {{lang}} spans for verification.
func langSpans(ls []pipeline.LangSpan) []verify.LangSpan {
	var out []verify.LangSpan
	for _, l := range ls {
		out = append(out, verify.LangSpan{Spoken: l.Spoken, Lang: l.Lang})
	}
	return out
}

// chunkStyle is the per-response text after the rules: the "Delivery:" paragraph (SPK-07),
// then the chunk's speech-mark instructions (SPK-12, design §7.8), each block a paragraph.
func chunkStyle(delivery string, instructions []string) string {
	marks := strings.Join(instructions, "\n")
	switch {
	case delivery == "":
		return marks
	case marks == "":
		return delivery
	}
	return delivery + "\n\n" + marks
}
