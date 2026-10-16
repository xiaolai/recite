package text

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/spoken"
)

// Boundary is the kind of join that follows a chunk; AUD-03 picks the gap
// from it. Word, Clause and Char joins come from the over-budget fallbacks of
// design §7.2 step 5 and sit inside a sentence.
type Boundary string

// Boundary kinds, weakest first (see boundaryRank).
const (
	BoundaryChar      Boundary = "char" // between two CJK characters, last resort
	BoundaryWord      Boundary = "word"
	BoundaryClause    Boundary = "clause"
	BoundarySentence  Boundary = "sentence"
	BoundaryListItem  Boundary = "list-item"
	BoundaryParagraph Boundary = "paragraph"
	BoundarySection   Boundary = "section"
	BoundaryPause     Boundary = "pause" // a {{pause}} mark
	BoundaryEnd       Boundary = "end"   // after the last chunk
)

var boundaryRank = map[Boundary]int{
	BoundaryChar: 1, BoundaryWord: 2, BoundaryClause: 3, BoundarySentence: 4,
	BoundaryListItem: 5, BoundaryParagraph: 6, BoundarySection: 7, BoundaryPause: 8, BoundaryEnd: 9,
}

// Break forces a chunk boundary at a display byte offset: a block edge
// (paragraph, list item, section, script line) or a {{pause}} mark.
type Break struct {
	Display int
	Kind    Boundary
}

// DefaultChunkSeconds is the --chunk-seconds default.
const DefaultChunkSeconds = 25.0

// OverlongFactor bounds a chunk's estimated duration relative to the budget;
// only a single unbreakable run may exceed it (TXT-03).
const OverlongFactor = 1.3

// SegmentOptions configures Segment.
type SegmentOptions struct {
	Budget float64 // seconds per chunk; 0 means DefaultChunkSeconds
	Speed  float64 // speech speed; 0 means 1.0
	Breaks []Break // forced boundaries; any order
}

// Chunk is one unit of synthesis. Display and Spoken are byte ranges of the
// display text and the spoken form; the separators that follow them (the
// whitespace dropped at the cut) are DisplaySep and SpokenSep.
type Chunk struct {
	Display, DisplaySep spoken.Range
	Spoken, SpokenSep   spoken.Range
	Text                string // the spoken form of the chunk
	DisplayText         string
	Boundary            Boundary // the join after this chunk
	Script              Script
	Seconds             float64 // estimated duration
}

// SegmentWarning reports a chunk cut inside a sentence (Code "S-SPLIT") or a
// chunk over the budget that could not be cut at all ("S-OVERLONG").
type SegmentWarning struct {
	Code    string
	Chunk   int
	Seconds float64
	Msg     string
}

// Segmentation is the result of Segment. Lead is the whitespace before the
// first chunk. Lead + every (chunk, separator) pair reproduces the spoken form
// and, through the Display ranges, the display text exactly.
type Segmentation struct {
	LeadDisplay, LeadSpoken spoken.Range
	Chunks                  []Chunk
	Warnings                []SegmentWarning
}

// ParagraphBreaks returns a paragraph break at every blank-line run of the
// plain-text format (design §7.1).
func ParagraphBreaks(display string) []Break {
	var out []Break
	for i := 0; i < len(display); {
		j := strings.IndexByte(display[i:], '\n')
		if j < 0 {
			break
		}
		nl := i + j
		k := nl + 1
		for k < len(display) && (display[k] == ' ' || display[k] == '\t' || display[k] == '\r') {
			k++
		}
		if k < len(display) && display[k] == '\n' {
			out = append(out, Break{Display: nl, Kind: BoundaryParagraph})
		}
		i = nl + 1
	}
	return out
}

// Segment cuts the spoken form of r into chunks of at most opts.Budget
// estimated seconds, following design §7.2: forced breaks first, then
// sentences packed greedily, then clause punctuation, then whitespace, then
// (for CJK) between characters. Cuts are made only where the span map is
// exact (inside text copied as written, or at segment edges), so every chunk
// carries its display text along.
func Segment(r spoken.Result, opts SegmentOptions) (Segmentation, error) {
	if err := validateResult(r); err != nil {
		return Segmentation{}, err
	}
	if opts.Budget == 0 {
		opts.Budget = DefaultChunkSeconds
	}
	if opts.Speed == 0 {
		opts.Speed = 1
	}
	if !(opts.Budget > 0) || !(opts.Speed > 0) {
		return Segmentation{}, fmt.Errorf("segment: budget %v and speed %v must be positive", opts.Budget, opts.Speed)
	}
	m := spanMap{segs: r.Segments}
	// Forced breaks in spoken coordinates, strongest kind per position.
	forced := map[int]Boundary{}
	for _, b := range opts.Breaks {
		if b.Display < 0 || b.Display > len(r.Display) {
			return Segmentation{}, fmt.Errorf("segment: break at %d is outside the text", b.Display)
		}
		if _, ok := boundaryRank[b.Kind]; !ok || b.Kind == BoundaryEnd {
			return Segmentation{}, fmt.Errorf("segment: unknown break kind %q", b.Kind)
		}
		p := m.toSpoken(b.Display)
		if boundaryRank[b.Kind] > boundaryRank[forced[p]] {
			forced[p] = b.Kind
		}
	}
	cuts := make([]int, 0, len(forced)+2)
	cuts = append(cuts, 0)
	for p := range forced {
		cuts = append(cuts, p)
	}
	cuts = append(cuts, len(r.Spoken))
	slices.Sort(cuts)
	cuts = slices.Compact(cuts)

	sg := segmenter{s: r.Spoken, m: m, budget: opts.Budget, speed: opts.Speed}
	for i := 0; i+1 < len(cuts); i++ {
		sg.block(cuts[i], cuts[i+1], forced[cuts[i+1]])
	}
	return sg.finish(r), nil
}

func validateResult(r spoken.Result) error {
	d, s := 0, 0
	for _, g := range r.Segments {
		if g.Display.Start != d || g.Spoken.Start != s || g.Display.End < d || g.Spoken.End < s {
			return errors.New("segment: span map segments are not contiguous")
		}
		d, s = g.Display.End, g.Spoken.End
	}
	if d != len(r.Display) || s != len(r.Spoken) {
		return errors.New("segment: span map does not cover the display text and the spoken form")
	}
	return nil
}

// spanMap converts offsets between the spoken form and the display text.
type spanMap struct{ segs []spoken.Segment }

// at is the first segment whose spoken range ends at or after p, so an offset
// on a segment edge maps leftmost (before any empty spoken segment).
func (m spanMap) at(p int) (spoken.Segment, bool) {
	i, _ := slices.BinarySearchFunc(m.segs, p, func(g spoken.Segment, p int) int {
		if g.Spoken.End < p {
			return -1
		}
		return 1
	})
	if i == len(m.segs) {
		return spoken.Segment{}, false
	}
	return m.segs[i], true
}

// cuttable reports whether spoken offset p maps to exactly one display offset:
// it lies in verbatim text or on a segment edge.
func (m spanMap) cuttable(p int) bool {
	g, ok := m.at(p)
	if !ok {
		return p == 0
	}
	return g.Rule == "" || p == g.Spoken.Start || p == g.Spoken.End
}

// toDisplay maps a cuttable spoken offset to the display text.
func (m spanMap) toDisplay(p int) int {
	g, ok := m.at(p)
	switch {
	case !ok:
		return 0
	case g.Rule == "":
		return g.Display.Start + p - g.Spoken.Start
	case p == g.Spoken.Start:
		return g.Display.Start
	}
	return g.Display.End
}

// toSpoken maps a display offset to the spoken form; an offset inside a
// rewritten span moves to the span's end, since a rewrite cannot be cut.
func (m spanMap) toSpoken(d int) int {
	// The first segment ending at or after d, skipping a non-empty one that
	// merely ends at d (d belongs to whatever starts there).
	i, _ := slices.BinarySearchFunc(m.segs, d, func(g spoken.Segment, d int) int {
		if g.Display.End < d {
			return -1
		}
		return 1
	})
	for i < len(m.segs) && m.segs[i].Display.End == d && m.segs[i].Display.Start < d {
		i++
	}
	if i == len(m.segs) {
		if n := len(m.segs); n > 0 {
			return m.segs[n-1].Spoken.End
		}
		return 0
	}
	g := m.segs[i]
	switch {
	case g.Rule == "":
		return g.Spoken.Start + d - g.Display.Start
	case d == g.Display.Start:
		return g.Spoken.Start
	}
	return g.Spoken.End
}

// piece is a chunk in spoken coordinates before the display ranges are filled in.
type piece struct {
	start, end, next int
	kind             Boundary
	seconds          float64
	split, overlong  bool
}

type segmenter struct {
	s             string
	m             spanMap
	budget, speed float64
	pieces        []piece
	lead          int // end of the leading whitespace
}

// brk is a candidate cut inside a block: the chunk text ends at end, the
// separator runs to next.
type brk struct {
	end, next int
	kind      Boundary
	cum       float64 // seconds from the block start to end
	cumNext   float64 // seconds from the block start to next
}

// block segments the spoken range [a, b); after is the forced boundary at b.
func (sg *segmenter) block(a, b int, after Boundary) {
	if after == "" {
		after = BoundaryEnd
	}
	a, b = sg.trim(a, b)
	if a >= b {
		// Nothing to speak: the range joins the previous separator, which keeps
		// the stronger of the two boundaries.
		if n := len(sg.pieces); n > 0 && boundaryRank[after] > boundaryRank[sg.pieces[n-1].kind] && after != BoundaryEnd {
			sg.pieces[n-1].kind = after
		}
		return
	}
	if len(sg.pieces) == 0 {
		sg.lead = a
	}
	brks, total := sg.breaks(a, b)
	pos, base := a, 0.0
	for total-base > sg.budget {
		k, ok := sg.pick(brks, pos, base)
		if !ok {
			break
		}
		cut := brks[k]
		brks = brks[k+1:] // consumed: keeps the whole block linear
		p := piece{start: pos, end: cut.end, next: cut.next, kind: cut.kind, seconds: cut.cum - base}
		p.split = boundaryRank[cut.kind] < boundaryRank[BoundarySentence]
		p.overlong = p.seconds > sg.budget
		sg.pieces = append(sg.pieces, p)
		pos, base = cut.next, cut.cumNext
	}
	p := piece{start: pos, end: b, next: b, kind: after, seconds: total - base}
	p.overlong = p.seconds > sg.budget
	sg.pieces = append(sg.pieces, p)
}

// pick chooses the cut after pos: the last break of the strongest tier that
// keeps the chunk within budget, or else the first break at all (an
// unbreakable run, reported as overlong). It returns an index into brks.
func (sg *segmenter) pick(brks []brk, pos int, base float64) (int, bool) {
	best := [4]int{-1, -1, -1, -1} // sentence, clause, word, char
	first := -1
	for i, c := range brks {
		if c.end <= pos {
			continue
		}
		if first < 0 {
			first = i
		}
		if c.cum-base > sg.budget {
			break
		}
		best[tier(c.kind)] = i
	}
	for _, i := range best {
		if i >= 0 {
			return i, true
		}
	}
	return first, first >= 0
}

func tier(k Boundary) int {
	switch k {
	case BoundarySentence:
		return 0
	case BoundaryClause:
		return 1
	case BoundaryWord:
		return 2
	}
	return 3
}

// trim narrows [a, b) past leading and trailing whitespace, stopping where the
// span map would not be exact.
func (sg *segmenter) trim(a, b int) (int, int) {
	for a < b {
		r, n := utf8.DecodeRuneInString(sg.s[a:])
		if !unicode.IsSpace(r) || !sg.m.cuttable(a+n) {
			break
		}
		a += n
	}
	for b > a {
		r, n := utf8.DecodeLastRuneInString(sg.s[:b])
		if !unicode.IsSpace(r) || !sg.m.cuttable(b-n) {
			break
		}
		b -= n
	}
	return a, b
}

// breaks lists the candidate cuts in [a, b) with their cumulative duration,
// and returns the block's total duration.
func (sg *segmenter) breaks(a, b int) ([]brk, float64) {
	s := sg.s[:b]
	dom := DetectScript(s[a:]).Script
	var out []brk
	cum := 0.0
	add := func(end, next int, kind Boundary, cumEnd, cumNext float64) {
		if end <= a || next >= b || !sg.m.cuttable(end) || !sg.m.cuttable(next) {
			return
		}
		if n := len(out); n > 0 && out[n-1].end == end {
			if tier(kind) < tier(out[n-1].kind) {
				out[n-1].kind = kind
			}
			return
		}
		out = append(out, brk{end: end, next: next, kind: kind, cum: cumEnd, cumNext: cumNext})
	}
	var prev rune
	for i := a; i < b; {
		r, n := utf8.DecodeRuneInString(s[i:])
		if i > a && isCJK(prev) && isCJK(r) {
			add(i, i, BoundaryChar, cum, cum)
		}
		before := cum
		cum += sg.seconds(r, dom) + sg.spelled(s, i, n, r)
		j := i + n
		switch {
		case unicode.IsSpace(r):
			k, c := sg.skipSpace(s, j, cum, dom)
			add(i, k, BoundaryWord, before, c)
			// Whitespace is part of the separator, not of either chunk.
			cum, j = c, k
		case isTerminal(r) || isClause(r):
			var kind Boundary
			switch {
			case isClause(r):
				kind = BoundaryClause
			case sentenceEnd(s, a, i):
				kind = BoundarySentence
			}
			// An abbreviation or decimal stop is no cut; a following space
			// still gives a word break on the next iteration.
			// Absorb further terminal marks and closing quotes or brackets.
			for j < b {
				q, qn := utf8.DecodeRuneInString(s[j:])
				if !isTerminal(q) && !isCloser(q) {
					break
				}
				cum += sg.seconds(q, dom)
				j += qn
			}
			if kind != "" {
				kind = punctBreak(s, i, j, b, kind)
			}
			if kind != "" {
				k, c := sg.skipSpace(s, j, cum, dom)
				add(j, k, kind, cum, c)
				if k > j {
					cum, j = c, k
				}
			}
		}
		prev, _ = utf8.DecodeLastRuneInString(s[:j])
		i = j
	}
	return out, cum
}

// punctBreak decides whether punctuation ending at j (mark at i) is a cut: a
// Latin mark needs whitespace after it; a CJK mark does not.
func punctBreak(s string, i, j, b int, kind Boundary) Boundary {
	if j >= b {
		return ""
	}
	mark, _ := utf8.DecodeRuneInString(s[i:])
	next, _ := utf8.DecodeRuneInString(s[j:])
	if unicode.IsSpace(next) || isCJKPunct(mark) {
		return kind
	}
	return ""
}

// skipSpace returns the end of the whitespace run at j and the duration up to it.
func (sg *segmenter) skipSpace(s string, j int, cum float64, dom Script) (int, float64) {
	for j < len(s) {
		r, n := utf8.DecodeRuneInString(s[j:])
		if !unicode.IsSpace(r) {
			break
		}
		cum += sg.seconds(r, dom)
		j += n
	}
	return j, cum
}

// spelledLetterChars is how long a spelled letter is spoken, in characters: its name ("you" for U,
// "ar" for R, "eff" for F), whose English lengths average three.
const spelledLetterChars = 3

// spelled is the time a capital Latin letter standing alone takes beyond its one character: it is
// spelled, so spoken as its name. Not A or I, which are words; not a letter beside another letter
// or a digit, which is part of a word or a token. 0 for any other rune.
func (sg *segmenter) spelled(s string, i, n int, r rune) float64 {
	if r > unicode.MaxASCII || !unicode.IsUpper(r) || r == 'A' || r == 'I' {
		return 0
	}
	before, _ := utf8.DecodeLastRuneInString(s[:i])
	after, _ := utf8.DecodeRuneInString(s[i+n:])
	if i > 0 && (unicode.IsLetter(before) || unicode.IsDigit(before)) ||
		i+n < len(s) && (unicode.IsLetter(after) || unicode.IsDigit(after)) {
		return 0
	}
	return (spelledLetterChars - 1) / SpeechRate(ScriptLatin) / sg.speed
}

// seconds is one rune's estimated duration: letters at their own script's
// rate, everything else at the block's dominant script's rate.
func (sg *segmenter) seconds(r rune, dom Script) float64 {
	sc := dom
	if unicode.IsLetter(r) {
		sc = runeScript(r)
	}
	return 1 / SpeechRate(sc) / sg.speed
}

func runeScript(r rune) Script {
	for _, name := range scriptOrder {
		if unicode.Is(unicode.Scripts[name], r) {
			return Script(name)
		}
	}
	return ScriptUnknown
}

func (sg *segmenter) finish(r spoken.Result) Segmentation {
	out := Segmentation{
		LeadSpoken:  spoken.Range{Start: 0, End: sg.lead},
		LeadDisplay: spoken.Range{Start: 0, End: sg.m.toDisplay(sg.lead)},
	}
	if len(sg.pieces) == 0 {
		// Whitespace only: everything is lead.
		out.LeadSpoken.End, out.LeadDisplay.End = len(r.Spoken), len(r.Display)
		return out
	}
	// The separator of each chunk runs to the start of the next one.
	for i := range sg.pieces {
		if i+1 < len(sg.pieces) {
			sg.pieces[i].next = sg.pieces[i+1].start
		} else {
			sg.pieces[i].next = len(r.Spoken)
			sg.pieces[i].kind = BoundaryEnd
		}
	}
	for i, p := range sg.pieces {
		d0, d1, d2 := sg.m.toDisplay(p.start), sg.m.toDisplay(p.end), sg.m.toDisplay(p.next)
		if i+1 == len(sg.pieces) {
			d2 = len(r.Display)
		}
		c := Chunk{
			Display: spoken.Range{Start: d0, End: d1}, DisplaySep: spoken.Range{Start: d1, End: d2},
			Spoken: spoken.Range{Start: p.start, End: p.end}, SpokenSep: spoken.Range{Start: p.end, End: p.next},
			Text: r.Spoken[p.start:p.end], DisplayText: r.Display[d0:d1],
			Boundary: p.kind, Script: DetectScript(r.Spoken[p.start:p.end]).Script, Seconds: p.seconds,
		}
		out.Chunks = append(out.Chunks, c)
		switch {
		case p.overlong:
			out.Warnings = append(out.Warnings, SegmentWarning{Code: "S-OVERLONG", Chunk: i, Seconds: p.seconds,
				Msg: fmt.Sprintf("chunk %d is an unbreakable run of %.1f s, over the %.1f s budget", i+1, p.seconds, sg.budget)})
		case p.split:
			out.Warnings = append(out.Warnings, SegmentWarning{Code: "S-SPLIT", Chunk: i, Seconds: p.seconds,
				Msg: fmt.Sprintf("chunk %d ends inside a sentence (%s cut): the sentence is over the %.1f s budget", i+1, p.kind, sg.budget)})
		}
	}
	return out
}

// abbreviations end with a full stop that does not end a sentence (lower case,
// without the final stop).
var abbreviations = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "prof": true, "st": true, "jr": true, "sr": true,
	"vs": true, "etc": true, "e.g": true, "i.e": true, "eg": true, "ie": true, "cf": true, "inc": true,
	"ltd": true, "co": true, "corp": true, "mt": true, "ft": true, "no": true, "nos": true, "approx": true,
	"dept": true, "fig": true, "figs": true, "gen": true, "gov": true, "lt": true, "col": true, "sgt": true,
	"capt": true, "rev": true, "hon": true, "al": true, "ave": true, "blvd": true, "rd": true, "vol": true,
	"p": true, "pp": true, "ch": true, "sec": true, "jan": true, "feb": true, "mar": true, "apr": true,
	"jun": true, "jul": true, "aug": true, "sep": true, "sept": true, "oct": true, "nov": true, "dec": true,
	"a.m": true, "p.m": true, "u.s": true, "u.k": true,
}

// sentenceEnd reports whether the terminal mark at i ends a sentence: a full
// stop between digits is a decimal point, and one after an abbreviation or a
// single-letter initial is not an end.
func sentenceEnd(s string, a, i int) bool {
	if s[i] != '.' {
		return true
	}
	if i+1 < len(s) && s[i+1] == '.' {
		return true // an ellipsis
	}
	prev, _ := utf8.DecodeLastRuneInString(s[a:i])
	if next, _ := utf8.DecodeRuneInString(s[i+1:]); unicode.IsDigit(prev) && unicode.IsDigit(next) {
		return false
	}
	w := i
	for w > a {
		r, n := utf8.DecodeLastRuneInString(s[:w])
		if !unicode.IsLetter(r) && r != '.' {
			break
		}
		w -= n
	}
	word := strings.ToLower(s[w:i])
	if abbreviations[word] {
		return false
	}
	if utf8.RuneCountInString(word) == 1 {
		r, _ := utf8.DecodeRuneInString(s[w:i])
		return !unicode.IsUpper(r) // "J. Smith": an initial
	}
	return true
}

func isTerminal(r rune) bool {
	switch r {
	case '.', '!', '?', '…', '。', '！', '？', '｡', '‼', '⁇', '⁈', '⁉':
		return true
	}
	return false
}

// PauseMarks counts the marks a reader pauses at in s: the clause and terminal marks Segment
// cuts at. A run of terminals ("?!", "...") is one pause.
func PauseMarks(s string) int {
	n := 0
	prev := false
	for _, r := range s {
		mark := isTerminal(r) || isClause(r)
		if mark && !prev {
			n++
		}
		prev = mark
	}
	return n
}

func isClause(r rune) bool {
	switch r {
	case ',', ';', ':', '，', '；', '：', '、', '—', '–':
		return true
	}
	return false
}

func isCJKPunct(r rune) bool {
	switch r {
	case '。', '！', '？', '｡', '，', '；', '：', '、':
		return true
	}
	return false
}

func isCloser(r rune) bool {
	switch r {
	case '"', '\'', '”', '’', '»', '›', ')', ']', '}', '」', '』', '）', '》', '〉', '】', '〕':
		return true
	}
	return false
}

// isCJK reports runes of scripts written without spaces between words.
func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}
