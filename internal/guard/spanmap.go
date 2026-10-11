package guard

import (
	"slices"
	"strings"

	"github.com/xiaolai/recite/internal/text"
)

// piece is a run of the current text. An unedited piece is a verbatim slice
// of the original, orig[o0:o1]; an edited piece replaced orig[o0:o1] and is
// atomic: later edits that touch it absorb it whole, so every edit maps back
// to one original range.
type piece struct {
	text   string
	o0, o1 int
	edited bool
	codes  []Code
	// mark is 1 + the index of the speech mark this piece holds; such a piece
	// is protected: no detector match may touch it (design §7.8).
	mark int
}

// spanDoc is the current text as pieces over the original.
type spanDoc struct {
	orig   string
	pieces []piece
}

func newSpanDoc(s string) *spanDoc {
	return &spanDoc{orig: s, pieces: []piece{{text: s, o0: 0, o1: len(s)}}}
}

// newMarkedDoc replaces each mark with its display text as a protected piece.
func newMarkedDoc(s string, marks []text.Mark) *spanDoc {
	d := &spanDoc{orig: s}
	pos := 0
	for i, m := range marks {
		if m.Start > pos {
			d.pieces = append(d.pieces, piece{text: s[pos:m.Start], o0: pos, o1: m.Start})
		}
		d.pieces = append(d.pieces, piece{text: m.Text, o0: m.Start, o1: m.End, edited: true, mark: i + 1})
		pos = m.End
	}
	if pos < len(s) || len(d.pieces) == 0 {
		d.pieces = append(d.pieces, piece{text: s[pos:], o0: pos, o1: len(s)})
	}
	return d
}

// touchesMark reports whether match m overlaps a protected piece, or strictly
// contains an empty one (a pause).
func (d *spanDoc) touchesMark(m match) bool {
	pos := 0
	for _, p := range d.pieces {
		pend := pos + len(p.text)
		if p.mark > 0 && overlaps(pos, pend, m) {
			return true
		}
		pos = pend
	}
	return false
}

// marks lists the protected pieces as (mark index, current range).
func (d *spanDoc) marks() []PlacedMark {
	var out []PlacedMark
	pos := 0
	for _, p := range d.pieces {
		pend := pos + len(p.text)
		if p.mark > 0 {
			out = append(out, PlacedMark{Index: p.mark - 1, Start: pos, End: pend})
		}
		pos = pend
	}
	return out
}

func (d *spanDoc) String() string {
	var b strings.Builder
	for _, p := range d.pieces {
		b.WriteString(p.text)
	}
	return b.String()
}

// origRange maps the current byte range [a, b) to the original range it came from.
func (d *spanDoc) origRange(a, b int) (int, int) {
	return d.origPos(a, false), d.origPos(b, b > a)
}

// origPos maps a current offset. end selects the piece holding x-1, so an
// end offset maps to the end of an edited piece rather than its start.
func (d *spanDoc) origPos(x int, end bool) int {
	pos := 0
	for _, p := range d.pieces {
		pend := pos + len(p.text)
		in := pos <= x && x < pend
		if end {
			in = pos < x && x <= pend
		}
		if in {
			if !p.edited {
				return p.o0 + (x - pos)
			}
			if end {
				return p.o1
			}
			return p.o0
		}
		pos = pend
	}
	return len(d.orig)
}

// splitAt splits the unedited piece strictly containing current offset x.
func (d *spanDoc) splitAt(x int) {
	pos := 0
	for i, p := range d.pieces {
		pend := pos + len(p.text)
		if pos < x && x < pend {
			if p.edited {
				return
			}
			k := x - pos
			left := piece{text: p.text[:k], o0: p.o0, o1: p.o0 + k}
			right := piece{text: p.text[k:], o0: p.o0 + k, o1: p.o1}
			d.pieces = slices.Replace(d.pieces, i, i+1, left, right)
			return
		}
		pos = pend
	}
}

// apply replaces each non-empty current range ms[i].start:end with ms[i].repl.
// ms must be sorted and non-overlapping.
func (d *spanDoc) apply(ms []match) {
	for _, m := range ms {
		d.splitAt(m.start)
		d.splitAt(m.end)
	}
	cur := d.String()
	out := make([]piece, 0, len(d.pieces))
	pos, mi := 0, 0
	for i := 0; i < len(d.pieces); {
		p := d.pieces[i]
		pend := pos + len(p.text)
		if mi >= len(ms) || !overlaps(pos, pend, ms[mi]) {
			out = append(out, p)
			pos = pend
			i++
			continue
		}
		m := ms[mi]
		first, start := p, pos
		last, end := p, pend
		var codes []Code
		for i < len(d.pieces) {
			q := d.pieces[i]
			qend := pos + len(q.text)
			if !overlaps(pos, qend, m) {
				break
			}
			codes = append(codes, q.codes...)
			last, end = q, qend
			pos = qend
			i++
		}
		if !slices.Contains(codes, m.code) {
			codes = append(codes, m.code)
		}
		text := cur[start:m.start] + m.repl + cur[m.end:end]
		out = append(out, piece{text: text, o0: first.o0, o1: last.o1, edited: true, codes: codes})
		mi++
	}
	d.pieces = mergeUnedited(out)
}

func overlaps(pos, pend int, m match) bool {
	if pos == pend {
		return m.start < pos && pos < m.end
	}
	return pos < m.end && pend > m.start
}

// mergeUnedited joins adjacent unedited pieces, so the piece list stays short.
func mergeUnedited(ps []piece) []piece {
	out := ps[:0]
	for _, p := range ps {
		if n := len(out); n > 0 && !p.edited && !out[n-1].edited && out[n-1].o1 == p.o0 {
			out[n-1].text += p.text
			out[n-1].o1 = p.o1
			continue
		}
		out = append(out, p)
	}
	return out
}

// edits lists the edited pieces as span edits over the original.
func (d *spanDoc) edits() []Edit {
	var es []Edit
	for _, p := range d.pieces {
		if p.edited {
			es = append(es, Edit{Start: p.o0, End: p.o1, Replacement: p.text, Codes: slices.Clone(p.codes)})
		}
	}
	return es
}

// curExpand widens the current range [a, b) to the edited pieces it touches,
// the same widening origRange applies.
func (d *spanDoc) curExpand(a, b int) (int, int) {
	pos := 0
	na, nb := a, b
	for _, p := range d.pieces {
		pend := pos + len(p.text)
		if p.edited && pos <= a && a < pend {
			na = pos
		}
		if p.edited && b > a && pos < b && b <= pend {
			nb = pend
		}
		pos = pend
	}
	return na, nb
}
