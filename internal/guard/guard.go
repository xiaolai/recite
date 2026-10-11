package guard

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/text"
)

// Options configures one guard run.
type Options struct {
	Policy Policy // "" means fix
	// Actions overrides the default action per code (config [guard]).
	Actions map[Code]Action
	// Markdown says the input is Markdown, so ** ## ` are syntax, not residue.
	Markdown bool
	// Marks parses speech marks (§7.8) before the guard: each becomes its
	// display text, which no detector touches, and a malformed one is G-MARK.
	Marks bool
}

func (o *Options) action(c Code) Action {
	if a, ok := o.Actions[c]; ok {
		return a
	}
	return rules[c].def
}

// Result is the guard's output: the cleaned display text, the findings in
// text order, and the span edits that turned the input into Text.
type Result struct {
	Text     string
	Findings []Finding
	Edits    []Edit
	// Marks are the speech marks in text order and Placed their ranges in Text,
	// one per mark (Options.Marks).
	Marks  []text.Mark
	Placed []PlacedMark
}

// PlacedMark is where mark Marks[Index] ended up: the byte range [Start, End)
// of Result.Text holding its display text (empty for a pause).
type PlacedMark struct {
	Index      int
	Start, End int
}

// maxPasses bounds the fixpoint loop. Each pass only shrinks or rewrites to
// text the detectors no longer match, so the loop settles in a few passes.
const maxPasses = 16

// Run guards s, the decoded and normalised input (text.Read).
//
// Under fix the rules run to a fixpoint, so Run(Run(x).Text) leaves the text
// unchanged. The guard never adds words, with two exceptions: G-URL and
// G-EMAIL speak a host or an address as words ("example dot com"), because
// §7.6 defines those outputs as the guard's actions. Every other rewrite is a
// removal, a collapse, an entity decode, a letter swap or a marker escape.
//
// G-MARKER escaping applies under every policy, including warn and off: an
// unescaped marker would let the input close the script early (§6.4), so it
// is a hard rule like G-ENCODING and G-EMPTY.
func Run(s string, o Options) (Result, error) {
	if o.Policy == "" {
		o.Policy = PolicyFix
	}
	d := newSpanDoc(s)
	var marks []text.Mark
	if o.Marks {
		marks, _ = parseMarks(s)
		d = newMarkedDoc(s, marks)
	}
	fs := newFindingSet(d)
	switch o.Policy {
	case PolicyFix, PolicyStrict:
		for pass := 0; pass < maxPasses; pass++ {
			changed := false
			for _, det := range detectors {
				if runDetector(d, fs, det, &o, true) {
					changed = true
				}
			}
			if !changed {
				break
			}
		}
	case PolicyWarn:
		// Report on the text as written; only markers are escaped.
		for _, det := range detectors {
			runDetector(d, fs, det, &o, det.code == Marker)
		}
	case PolicyOff:
		runDetector(d, fs, detector{Marker, findMarker}, &o, true)
	default:
		return Result{}, fmt.Errorf("guard: unknown policy %q", o.Policy)
	}
	out := d.String()
	if o.Policy != PolicyOff {
		for _, m := range findRequest(out) {
			fs.add(m, out)
		}
	}
	res := Result{Text: out, Findings: fs.list(), Edits: d.edits(), Marks: marks, Placed: d.marks()}
	if !Speakable(out) {
		res.Findings = append(res.Findings, Finding{Code: Empty, Line: 1, Col: 1, Severity: Fatal, Action: ActFail, Before: s, After: out})
		return res, &Error{Reason: "nothing speakable is left after the guard (G-EMPTY)", Findings: res.Findings}
	}
	var fatal, warn int
	for _, f := range res.Findings {
		switch {
		case f.Severity >= Fatal:
			fatal++
		case f.Severity == Warn:
			warn++
		}
	}
	if fatal > 0 {
		return res, &Error{Reason: fmt.Sprintf("%d finding(s) set to fail", fatal), Findings: res.Findings}
	}
	if o.Policy == PolicyStrict && warn > 0 {
		return res, &Error{Reason: fmt.Sprintf("--guard strict: %d finding(s) at warn level", warn), Findings: res.Findings}
	}
	return res, nil
}

// runDetector runs det on the current text, records its findings, and, when
// apply is set, applies its edits. It reports whether the text changed.
func runDetector(d *spanDoc, fs *findingSet, det detector, o *Options, apply bool) bool {
	cur := d.String()
	ms := det.find(cur, o.action(det.code), o)
	var edits []match
	for _, m := range ms {
		if m.edit && cur[m.start:m.end] == m.repl {
			continue // a no-op rewrite is not a finding
		}
		if d.touchesMark(m) {
			continue // a speech mark's text is the user's explicit choice
		}
		fs.add(m, cur)
		if m.edit && apply {
			edits = append(edits, m)
		}
	}
	if len(edits) == 0 {
		return false
	}
	d.apply(edits)
	return true
}

// Speakable reports whether s holds anything to say: a letter or a digit.
// A chunk without one is skipped (G-EMPTY); an input without one fails.
func Speakable(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
}

// findingSet collects findings in original coordinates. The fixpoint loop
// sees a kept span again on every pass, and a chained rewrite (an entity that
// decodes to a marker) touches the same original span twice; one finding per
// code and original range is kept, with the latest replacement.
type findingSet struct {
	d     *spanDoc
	byKey map[findingKey]int
	items []item
}

type findingKey struct {
	code   Code
	o0, o1 int
}

type item struct {
	o0, o1 int
	f      Finding
}

func newFindingSet(d *spanDoc) *findingSet {
	return &findingSet{d: d, byKey: map[findingKey]int{}}
}

func (fs *findingSet) add(m match, cur string) {
	o0, o1 := fs.d.origRange(m.start, m.end)
	before := fs.d.orig[o0:o1]
	after := before
	if m.edit {
		// The replacement as the original span reads once the match is
		// applied: an edited piece the match touches keeps its other text.
		a, b := fs.d.curExpand(m.start, m.end)
		after = cur[a:m.start] + m.repl + cur[m.end:b]
	}
	line, col := position(fs.d.orig, o0)
	f := Finding{Code: m.code, Line: line, Col: col, Severity: m.sev, Action: m.action, Before: before, After: after}
	k := findingKey{m.code, o0, o1}
	if i, ok := fs.byKey[k]; ok {
		fs.items[i].f = f
		return
	}
	fs.byKey[k] = len(fs.items)
	fs.items = append(fs.items, item{o0, o1, f})
}

func (fs *findingSet) list() []Finding {
	sort.SliceStable(fs.items, func(i, j int) bool {
		a, b := fs.items[i], fs.items[j]
		if a.o0 != b.o0 {
			return a.o0 < b.o0
		}
		return codeOrder(a.f.Code) < codeOrder(b.f.Code)
	})
	out := make([]Finding, 0, len(fs.items))
	for _, it := range fs.items {
		out = append(out, it.f)
	}
	return out
}

func codeOrder(c Code) int {
	for i, x := range Codes() {
		if x == c {
			return i
		}
	}
	return len(Codes())
}

// position is the 1-based line and column (in runes) of byte offset off in s.
func position(s string, off int) (line, col int) {
	line, col = 1, 1
	for _, r := range s[:off] {
		if r == '\n' {
			line, col = line+1, 1
			continue
		}
		col++
	}
	return line, col
}

func sortMatches(ms []match) {
	sort.SliceStable(ms, func(i, j int) bool {
		if ms[i].start != ms[j].start {
			return ms[i].start < ms[j].start
		}
		return ms[i].end > ms[j].end
	})
}

// EncodingFinding maps a decode error (text.Decode) on raw to its G-ENCODING
// finding, so reports list it with the other findings.
func EncodingFinding(raw []byte, err error) (Finding, bool) {
	var ie *text.InputError
	if !errors.As(err, &ie) || ie.Offset < 0 || ie.Offset > int64(len(raw)) {
		return Finding{}, false
	}
	off := int(ie.Offset)
	prefix := raw[:off]
	line := 1 + strings.Count(string(prefix), "\n")
	last := strings.LastIndexByte(string(prefix), '\n')
	col := 1 + utf8.RuneCount(prefix[last+1:])
	end := off + 1
	before := string(raw[off:end])
	return Finding{Code: Encoding, Line: line, Col: col, Severity: Fatal, Action: ActFail, Before: before, After: ""}, true
}

// parseMarks parses the speech marks of s. A mark holding a control character
// or a <<< >>> marker is malformed: its text would reach the model unguarded.
func parseMarks(s string) ([]text.Mark, []text.BadMark) {
	marks, bad := text.ParseMarks(s)
	ok := marks[:0:0]
	for _, m := range marks {
		raw := s[m.Start:m.End]
		if strings.ContainsFunc(raw, isControl) || reMarker.MatchString(raw) {
			bad = append(bad, text.BadMark{Start: m.Start, End: m.End, Reason: "contains a control character or a <<< >>> marker", Plain: m.Text})
			continue
		}
		ok = append(ok, m)
	}
	return ok, bad
}

// findMark reports each malformed speech mark (G-MARK) of the current text and
// strips its syntax, so no brace reaches the model.
func findMark(s string, act Action, o *Options) []match {
	if !o.Marks {
		return nil
	}
	_, bad := parseMarks(s)
	ms := make([]match, 0, len(bad))
	for _, b := range bad {
		ms = append(ms, mk(Mark, act, b.Start, b.End, b.Plain, rules[Mark].sev))
	}
	sortMatches(ms)
	return ms
}
