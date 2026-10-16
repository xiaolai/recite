package text

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/spoken"
)

// The spoken script (`--input-format spoken`, design §7.10) is what `prepare`
// writes: one chunk per paragraph, blank lines between paragraphs, and comment
// lines starting with "#|" carrying `key: value` pairs about their paragraph.
const (
	CommentPrefix = "#|"
	KeyRecite     = "recite"  // the header line
	KeyDisplay    = "display" // the paragraph's display text, a Go-quoted string
	KeyJoin       = "join"    // the boundary after the paragraph, when not a paragraph break
	KeyFinding    = "finding" // a text-stage finding, for the reader only
)

// joinKinds are the boundaries a join comment may name; pause comes from a
// {{pause}} mark and end from the end of the script.
var joinKinds = []Boundary{BoundaryChar, BoundaryWord, BoundaryClause, BoundarySentence, BoundaryListItem, BoundaryParagraph, BoundarySection}

// SpokenMark is a speech mark of a spoken script and its range in Spoken.
type SpokenMark struct {
	Mark   Mark
	Spoken spoken.Range
}

// SpokenDoc is a parsed spoken script, segmented exactly as written: one chunk
// per paragraph, and one more at each {{pause}} inside a paragraph.
type SpokenDoc struct {
	Display string // every chunk's display text, joined by blank lines
	Spoken  string // every chunk's spoken form, joined by blank lines
	Seg     Segmentation
	Pauses  []time.Duration // the duration of each pause boundary, in chunk order
	Marks   []SpokenMark
}

// MarkReader returns the spoken form of a text-bearing mark, or a reason it
// cannot be read (spoken.MarkSpoken in the reader's language).
type MarkReader func(Mark) (spokenForm, ambiguous string)

// ScriptError is a spoken script that cannot be read (exit 2).
type ScriptError struct {
	Line int
	Msg  string
}

func (e *ScriptError) Error() string {
	if e.Line == 0 {
		return "spoken script: " + e.Msg
	}
	return fmt.Sprintf("spoken script line %d: %s", e.Line, e.Msg)
}

type scriptPara struct {
	line       int // first line of the paragraph
	bodyLine   int // first text line
	display    string
	hasDisplay bool
	join       Boundary
	body       []string
}

// ParseSpokenScript reads a spoken script. The text is taken as the spoken
// form: the guard and the spoken-form rules do not run. With marks on, speech
// marks apply and a malformed one is an error, since no guard is there to strip
// it. speed scales the duration estimates (0 means 1.0).
func ParseSpokenScript(s string, marks bool, read MarkReader, speed float64) (SpokenDoc, error) {
	paras, err := scriptParas(s)
	if err != nil {
		return SpokenDoc{}, err
	}
	b := spokenBuilder{speed: speed}
	if b.speed == 0 {
		b.speed = 1
	}
	for _, p := range paras {
		if err := b.para(p, marks, read); err != nil {
			return SpokenDoc{}, err
		}
	}
	return b.finish()
}

func scriptParas(s string) ([]scriptPara, error) {
	var out []scriptPara
	var cur *scriptPara
	for i, line := range strings.Split(s, "\n") {
		n := i + 1
		if strings.TrimSpace(line) == "" {
			cur = nil
			continue
		}
		if cur == nil {
			out = append(out, scriptPara{line: n, join: BoundaryParagraph})
			cur = &out[len(out)-1]
		}
		if !strings.HasPrefix(line, CommentPrefix) {
			if len(cur.body) == 0 {
				cur.bodyLine = n
			}
			cur.body = append(cur.body, line)
			continue
		}
		key, val, ok := strings.Cut(strings.TrimPrefix(line, CommentPrefix), ":")
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if !ok {
			return nil, &ScriptError{n, fmt.Sprintf("comment %q: want %s key: value", line, CommentPrefix)}
		}
		switch key {
		case KeyRecite, KeyFinding:
		case KeyDisplay:
			if cur.hasDisplay {
				return nil, &ScriptError{n, "a second display comment in one paragraph"}
			}
			d, err := strconv.Unquote(val)
			if err != nil {
				return nil, &ScriptError{n, fmt.Sprintf("display %s: want a quoted string such as \"Hello.\"", val)}
			}
			cur.display, cur.hasDisplay = d, true
		case KeyJoin:
			k := Boundary(val)
			if !containsBoundary(joinKinds, k) {
				return nil, &ScriptError{n, fmt.Sprintf("join %q: want one of %s", val, boundaryNames(joinKinds))}
			}
			cur.join = k
		default:
			return nil, &ScriptError{n, fmt.Sprintf("unknown comment key %q (keys: %s, %s, %s, %s)", key, KeyRecite, KeyDisplay, KeyJoin, KeyFinding)}
		}
	}
	for _, p := range out {
		if len(p.body) == 0 && (p.hasDisplay || p.join != BoundaryParagraph) {
			return nil, &ScriptError{p.line, "a display or join comment with no text under it"}
		}
	}
	return out, nil
}

func containsBoundary(ks []Boundary, k Boundary) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}

func boundaryNames(ks []Boundary) string {
	names := make([]string, len(ks))
	for i, k := range ks {
		names[i] = string(k)
	}
	return strings.Join(names, ", ")
}

// part is one chunk of a spoken script before assembly.
type part struct {
	spoken, display string
	marks           []SpokenMark // ranges relative to spoken
	boundary        Boundary
	pause           time.Duration
}

type spokenBuilder struct {
	speed float64
	parts []part
}

// para splits a paragraph at its pauses into parts.
func (b *spokenBuilder) para(p scriptPara, marksOn bool, read MarkReader) error {
	if len(p.body) == 0 {
		return nil
	}
	body := strings.Join(p.body, "\n")
	var ms []Mark
	if marksOn {
		var bad []BadMark
		ms, bad = ParseMarks(body)
		if len(bad) > 0 {
			return &ScriptError{p.bodyLine + strings.Count(body[:bad[0].Start], "\n"),
				fmt.Sprintf("G-MARK: %s: %q", bad[0].Reason, body[bad[0].Start:bad[0].End])}
		}
	}
	var sp, disp strings.Builder
	var pm []SpokenMark
	first := true
	flush := func(boundary Boundary, pause time.Duration, line int) error {
		pt, ok := trimPart(sp.String(), disp.String(), pm)
		sp.Reset()
		disp.Reset()
		pm = nil
		if !ok {
			// Nothing to speak before this boundary: a pause joins the previous chunk.
			if pause == 0 {
				return nil
			}
			n := len(b.parts)
			if n == 0 {
				return &ScriptError{line, "a pause before any text"}
			}
			b.parts[n-1].boundary = BoundaryPause
			b.parts[n-1].pause += pause
			return nil
		}
		if p.hasDisplay {
			pt.display = ""
			if first {
				pt.display = p.display
			}
		}
		first = false
		pt.boundary, pt.pause = boundary, pause
		b.parts = append(b.parts, pt)
		return nil
	}
	pos := 0
	for _, m := range ms {
		sp.WriteString(body[pos:m.Start])
		disp.WriteString(body[pos:m.Start])
		pos = m.End
		if m.Kind == MarkPause {
			if err := flush(BoundaryPause, m.Pause, p.bodyLine+strings.Count(body[:m.Start], "\n")); err != nil {
				return err
			}
			continue
		}
		say, ambiguous := read(m)
		if ambiguous != "" {
			return &ScriptError{p.bodyLine + strings.Count(body[:m.Start], "\n"), fmt.Sprintf("%s: %q %s", spoken.AmbiguousCode, m.String(), ambiguous)}
		}
		start := sp.Len()
		sp.WriteString(say)
		disp.WriteString(m.Text)
		pm = append(pm, SpokenMark{Mark: m, Spoken: spoken.Range{Start: start, End: sp.Len()}})
	}
	sp.WriteString(body[pos:])
	disp.WriteString(body[pos:])
	return flush(p.join, 0, p.bodyLine)
}

// trimPart trims the whitespace around a part's spoken form and display text;
// ok is false when nothing is left to speak.
func trimPart(sp, disp string, ms []SpokenMark) (part, bool) {
	lead := len(sp) - len(strings.TrimLeftFunc(sp, unicode.IsSpace))
	t := strings.TrimSpace(sp)
	if t == "" {
		return part{}, false
	}
	out := part{spoken: t, display: strings.TrimSpace(disp)}
	for _, m := range ms {
		m.Spoken.Start = min(max(m.Spoken.Start-lead, 0), len(t))
		m.Spoken.End = min(max(m.Spoken.End-lead, 0), len(t))
		out.marks = append(out.marks, m)
	}
	return out, true
}

func (b *spokenBuilder) finish() (SpokenDoc, error) {
	var doc SpokenDoc
	if len(b.parts) == 0 {
		return doc, ErrNothingToSpeak
	}
	last := &b.parts[len(b.parts)-1]
	if last.boundary == BoundaryPause {
		return doc, &ScriptError{0, "a pause after the last text has nothing to separate"}
	}
	last.boundary = BoundaryEnd
	var disp, sp strings.Builder
	const sep = "\n\n"
	for i, p := range b.parts {
		d0, s0 := disp.Len(), sp.Len()
		disp.WriteString(p.display)
		sp.WriteString(p.spoken)
		d1, s1 := disp.Len(), sp.Len()
		if i+1 < len(b.parts) {
			disp.WriteString(sep)
			sp.WriteString(sep)
		}
		for _, m := range p.marks {
			doc.Marks = append(doc.Marks, SpokenMark{Mark: m.Mark, Spoken: spoken.Range{Start: s0 + m.Spoken.Start, End: s0 + m.Spoken.End}})
		}
		doc.Seg.Chunks = append(doc.Seg.Chunks, Chunk{
			Display: spoken.Range{Start: d0, End: d1}, DisplaySep: spoken.Range{Start: d1, End: disp.Len()},
			Spoken: spoken.Range{Start: s0, End: s1}, SpokenSep: spoken.Range{Start: s1, End: sp.Len()},
			Text: p.spoken, DisplayText: p.display, Boundary: p.boundary,
			Script: DetectScript(p.spoken).Script, Seconds: EstimateSeconds(p.spoken, b.speed),
		})
		if p.boundary == BoundaryPause {
			doc.Pauses = append(doc.Pauses, p.pause)
		}
	}
	doc.Display, doc.Spoken = disp.String(), sp.String()
	return doc, nil
}

// EstimateSeconds is the estimated duration of s spoken as one unsplit block
// at speed: the estimate Segment gives a block it does not cut (design §7.2).
func EstimateSeconds(s string, speed float64) float64 {
	if speed == 0 {
		speed = 1
	}
	sg := segmenter{speed: speed}
	dom := DetectScript(s).Script
	total := 0.0
	for i, r := range s {
		total += sg.seconds(r, dom) + sg.spelled(s, i, utf8.RuneLen(r), r)
	}
	return total
}

// validScriptLine reports whether a spoken form line can be written to a
// spoken script without being read back as a comment.
func validScriptLine(line string) bool { return !strings.HasPrefix(line, CommentPrefix) }

// CheckScriptText reports why spoken text t cannot be written as the text of a
// spoken script paragraph, or "" when it can: a line that would read as a
// comment, a blank line that would split the paragraph, or (with marks on) a
// "{{" that would read as a mark.
func CheckScriptText(t string, marks bool) string {
	if !utf8.ValidString(t) {
		return "invalid UTF-8"
	}
	for _, line := range strings.Split(t, "\n") {
		switch {
		case !validScriptLine(line):
			return fmt.Sprintf("a line starts with %q", CommentPrefix)
		case strings.TrimSpace(line) == "":
			return "a blank line inside one chunk"
		}
	}
	if marks && strings.Contains(t, "{{") {
		return `"{{" would read as a speech mark`
	}
	return ""
}
