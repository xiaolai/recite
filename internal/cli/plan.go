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
	seg, err := text.Segment(form, text.SegmentOptions{Budget: budget, Speed: s.Speed, Breaks: text.ParagraphBreaks(form.Display)})
	if err != nil {
		return pipeline.Plan{}, text.Segmentation{}, fmt.Errorf("segment: %w", err)
	}
	in := pipeline.Input{
		Display:  form.Display,
		Seg:      seg,
		Findings: planFindings(input, g, form),
		// Pause boundaries come only from {{pause}} marks, which the plain-text input of M2
		// cannot carry: only paragraph breaks are passed to Segment, so there is no pause to
		// time. Build fails loudly if one ever appears without a duration.
		Pauses: nil,
	}
	p, err := pipeline.Build(in, pipeline.Settings{
		Model: s.Model, Voice: s.Voice, Speed: s.Speed, Effort: string(s.Effort), InstructionsVersion: s.InstructionsVersion,
	})
	if err != nil {
		return pipeline.Plan{}, seg, err
	}
	return p, seg, nil
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
func tasksFor(p pipeline.Plan, spec realtime.SessionSpec) []pipeline.Task {
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
			},
			Spec:     spec,
			CacheKey: c.CacheKey,
		}
	}
	return tasks
}
