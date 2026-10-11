package cli

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/text"
)

// Input formats of --input-format (design §12.2). markdown and script are later work.
const (
	formatText   = "text"
	formatSpoken = "spoken"
)

// inputMode resolves --input-format, --marks and --no-marks: marks are on by default for
// spoken input and off otherwise (design §7.8).
func (f *textFlags) inputMode() (format string, marks bool, err error) {
	switch f.inputFormat {
	case formatText, formatSpoken:
	case "markdown", "script":
		return "", false, fmt.Errorf("--input-format %s is not available yet (valid: %s, %s)", f.inputFormat, formatText, formatSpoken)
	default:
		return "", false, fmt.Errorf("--input-format must be %s or %s, not %q", formatText, formatSpoken, f.inputFormat)
	}
	if f.marks && f.noMarks {
		return "", false, fmt.Errorf("--marks and --no-marks exclude each other")
	}
	marks = f.inputFormat == formatSpoken
	if f.marks {
		marks = true
	}
	if f.noMarks {
		marks = false
	}
	return f.inputFormat, marks, nil
}

// markReader reads text-bearing marks in the language lang (spoken.MarkSpoken).
func markReader(lang string, style spoken.DateStyle) text.MarkReader {
	return func(m text.Mark) (string, string) {
		return spoken.MarkSpoken(string(m.Kind), m.Value, m.Text, lang, style)
	}
}

// markOptions adds the guard's placed marks to the spoken-form options: each text-bearing
// mark becomes an override, and each pause an empty protected range no rule may cross.
func markOptions(g guard.Result, so spoken.Options, read text.MarkReader) spoken.Options {
	so.Overrides = append([]spoken.Override(nil), so.Overrides...)
	so.Protected = append([]spoken.Range(nil), so.Protected...)
	for _, pm := range g.Placed {
		m := g.Marks[pm.Index]
		r := spoken.Range{Start: pm.Start, End: pm.End}
		if m.Kind == text.MarkPause {
			so.Protected = append(so.Protected, r)
			continue
		}
		say, ambiguous := read(m)
		so.Overrides = append(so.Overrides, spoken.Override{Range: r, Spoken: say, Rule: spoken.MarkRule(string(m.Kind)), Ambiguous: ambiguous})
	}
	return so
}

// placedPauses are the guard's pause marks at their display offsets.
type placedPause struct {
	at int
	d  time.Duration
}

func pausesOf(g guard.Result) []placedPause {
	var out []placedPause
	for _, pm := range g.Placed {
		if m := g.Marks[pm.Index]; m.Kind == text.MarkPause {
			out = append(out, placedPause{pm.Start, m.Pause})
		}
	}
	return out
}

// checkPauses rejects a pause with no text before or after it: there is nothing for its
// silence to separate.
func checkPauses(g guard.Result) error {
	for _, p := range pausesOf(g) {
		if !guard.Speakable(g.Text[:p.at]) || !guard.Speakable(g.Text[p.at:]) {
			return usagef("a {{pause}} must sit between two pieces of text")
		}
	}
	return nil
}

// spokenMarks pairs each applied text-bearing mark with its range in the spoken form, found
// through the segment its override made.
func spokenMarks(g guard.Result, form spoken.Result) []text.SpokenMark {
	var out []text.SpokenMark
	for _, pm := range g.Placed {
		m := g.Marks[pm.Index]
		for _, sg := range form.Segments {
			if sg.Rule == spoken.MarkRule(string(m.Kind)) && sg.Display.Start == pm.Start && sg.Display.End == pm.End {
				out = append(out, text.SpokenMark{Mark: m, Spoken: sg.Spoken})
				break
			}
		}
	}
	return out
}

// markSpans are the planner's view of the marks: instructions and protected ranges.
func markSpans(ms []text.SpokenMark) []pipeline.MarkSpan {
	out := make([]pipeline.MarkSpan, 0, len(ms))
	for _, sm := range ms {
		m := sm.Mark
		span := pipeline.MarkSpan{Spoken: sm.Spoken}
		switch m.Kind {
		case text.MarkSay, text.MarkSpell, text.MarkDigits:
			span.Protect = true
		case text.MarkHint:
			span.Instruction = fmt.Sprintf("Pronounce %s as %s.", strings.TrimSpace(m.Text), strings.TrimSpace(m.Value))
		case text.MarkEmph:
			span.Instruction = fmt.Sprintf("Stress the word(s) '%s'.", strings.TrimSpace(m.Text))
		case text.MarkLang:
			span.Instruction = fmt.Sprintf("Say '%s' in %s.", strings.TrimSpace(m.Text), languageName(m.Value))
			span.Lang = strings.TrimSpace(m.Value)
		}
		out = append(out, span)
	}
	return out
}

// languageName is the English name of a BCP 47 tag ("fr" → "French"), or the tag itself.
func languageName(tag string) string {
	t, err := language.Parse(strings.TrimSpace(tag))
	if err != nil {
		return tag
	}
	if n := display.English.Tags().Name(t); n != "" {
		return n
	}
	return t.String()
}

// pauseDurations gives each pause boundary of seg its duration: the sum of the pause marks
// in the separator after the chunk (display coordinates).
func pauseDurations(seg text.Segmentation, pauses []placedPause) ([]time.Duration, error) {
	var out []time.Duration
	for _, c := range seg.Chunks {
		if c.Boundary != text.BoundaryPause {
			continue
		}
		var d time.Duration
		for _, p := range pauses {
			if p.at >= c.Display.End && p.at <= max(c.DisplaySep.End, c.Display.End) {
				d += p.d
			}
		}
		if d == 0 {
			return nil, fmt.Errorf("plan: a pause boundary at display offset %d has no pause mark", c.Display.End)
		}
		out = append(out, d)
	}
	return out, nil
}

// spokenScriptPlan plans a spoken script (TXT-15): no guard and no spoken-form rules; one
// chunk per paragraph, split only at its pauses.
func spokenScriptPlan(input string, marks bool, so spoken.Options, settings pipeline.Settings, speed float64) (pipeline.Plan, text.SpokenDoc, error) {
	lang := text.Resolve(scriptText(input), text.ResolveOptions{})
	doc, err := text.ParseSpokenScript(input, marks, markReader(lang.Code, so.DateStyle), speed)
	if err != nil {
		return pipeline.Plan{}, doc, err
	}
	p, err := pipeline.Build(pipeline.Input{Display: doc.Display, Seg: doc.Seg, Pauses: doc.Pauses, Marks: markSpans(doc.Marks)}, settings)
	return p, doc, err
}

// scriptText drops the comment lines of a spoken script, for language detection.
func scriptText(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(line, text.CommentPrefix) {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return strings.TrimRightFunc(b.String(), unicode.IsSpace)
}
