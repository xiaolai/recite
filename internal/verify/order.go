package verify

import (
	"strings"

	"github.com/xiaolai/recite/internal/text"
)

// Transposition window: the most matched units a moved unit may jump over and still count as
// a swap of neighbours. A Han word is two or three runes, so CER needs a wider window.
const (
	transposeWordsWER = 3
	transposeRunesCER = 4
)

// transposition finds neighbouring units heard in the wrong order: two substitutions that
// cross, adjacent ("of the" heard "the of") or over at most a few matched units ("to him from"
// heard "from him to"), or a unit deleted and the same unit inserted after at
// most a few matched units, or inserted and then deleted ("kind of" heard "of kind"; 红了眼眶
// heard 红眼眶了). The aligner prices such a take at one or two edits, often on function words,
// so neither the score nor the content-word guard notices it. Every unit counts, function
// words included. It returns the reference and heard units involved, joined for the report.
//
// A recogniser that restores the order itself (it hears "four the of nine" and writes "four of
// the nine") leaves nothing here to find; that limit is the recogniser's.
func transposition(al alignment, metric text.Metric) (expected, heard string, ok bool) {
	window, sep := transposeWordsWER, " "
	if metric == text.MetricCER {
		window, sep = transposeRunesCER, ""
	}
	st := al.steps
	refText := func(s step) string { return al.ref[s.ref].text }
	hypText := func(s step) string { return al.hyp[s.hyp].text }
	report := func(seg []step) (string, string) {
		var r, h []string
		for _, s := range seg {
			if s.ref >= 0 {
				r = append(r, refText(s))
			}
			if s.hyp >= 0 {
				h = append(h, hypText(s))
			}
		}
		return strings.Join(r, sep), strings.Join(h, sep)
	}
	isSub := func(s step) bool { return !s.match && s.ref >= 0 && s.hyp >= 0 }
	isDel := func(s step) bool { return s.hyp < 0 }
	isIns := func(s step) bool { return s.ref < 0 }
	for i := range st {
		if isSub(st[i]) {
			// The next unmatched step, past at most window matched units ("to him from" heard
			// "from him to" crosses over "him").
			j := i + 1
			for j < len(st) && st[j].match && j-i-1 < window {
				j++
			}
			if j < len(st) && isSub(st[j]) {
				a, b := refText(st[i]), refText(st[j])
				if a != b && hypText(st[i]) == b && hypText(st[j]) == a {
					e, h := report(st[i : j+1])
					return e, h, true
				}
			}
		}
		var moved string
		switch {
		case isDel(st[i]):
			moved = refText(st[i])
		case isIns(st[i]):
			moved = hypText(st[i])
		default:
			continue
		}
		// Look past at least one and at most window matched units for the other half of the move.
		for j := i + 1; j < len(st) && j-i-1 <= window; j++ {
			if st[j].match {
				continue
			}
			closes := (isDel(st[i]) && isIns(st[j]) && hypText(st[j]) == moved) ||
				(isIns(st[i]) && isDel(st[j]) && refText(st[j]) == moved)
			if closes && j > i+1 {
				// A unit moved past copies of itself leaves the order as it was.
				if e, h := report(st[i : j+1]); e != h {
					return e, h, true
				}
			}
			break
		}
	}
	return "", "", false
}
