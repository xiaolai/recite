package verify

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/text"
)

// EditKind names one kind of alignment difference.
type EditKind string

// Alignment differences (VER-04).
const (
	Substitution EditKind = "substitution"
	Insertion    EditKind = "insertion" // heard but not in the reference
	Deletion     EditKind = "deletion"  // in the reference but not heard
)

// Edit is one contiguous run of differences between the reference and what was heard, in the
// coordinates of the original strings. An insertion has an empty reference span at the point
// it was heard; a deletion has an empty heard span.
type Edit struct {
	Kind                 EditKind
	Expected, Heard      string // the original text of each span
	RefStart, RefEnd     int    // byte offsets into the reference
	HeardStart, HeardEnd int    // byte offsets into the transcript
}

// Result is the score of one transcript against its reference (design §8.4).
type Result struct {
	Metric text.Metric
	RefLen int // canonical words (WER) or runes (CER) in the reference
	Errors int // Levenshtein distance
	Score  float64
	Edits  []Edit
}

// Score compares hypothesis (the STT transcript) with reference (the chunk's spoken form) after
// canonicalising both: Levenshtein over canonical words for MetricWER, over canonical runes for
// MetricCER. score = 1 − errors / reference length, clamped to [0, 1]; an empty reference
// scores 1 against an empty transcript and 0 against anything else.
func Score(reference, hypothesis string, metric text.Metric) Result {
	ref, hyp := units(reference, metric), units(hypothesis, metric)
	steps := align(texts(ref), texts(hyp))
	res := Result{Metric: metric, RefLen: len(ref)}
	for _, st := range steps {
		if !st.match {
			res.Errors++
		}
	}
	switch {
	case res.RefLen == 0 && res.Errors == 0:
		res.Score = 1
	case res.RefLen == 0:
		res.Score = 0
	default:
		res.Score = max(0, min(1, 1-float64(res.Errors)/float64(res.RefLen)))
	}
	res.Edits = edits(steps, ref, hyp, reference, hypothesis)
	return res
}

// ScriptMetric chooses the metric for a chunk from its dominant script.
func ScriptMetric(chunk string) text.Metric { return text.Scorer(text.DetectScript(chunk).Script) }

// Report renders the edits as "expected X / heard Y" lines for the manifest and
// --keep-rejected.
func (r Result) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s score %.4f (%d errors / %d)\n", strings.ToUpper(string(r.Metric)), r.Score, r.Errors, r.RefLen)
	for _, e := range r.Edits {
		fmt.Fprintf(&b, "%s at %d: expected %q / heard %q\n", e.Kind, e.RefStart, e.Expected, e.Heard)
	}
	return b.String()
}

// unit is one compared element: a canonical word, or one rune of one, with its original span.
type unit struct {
	text       string
	start, end int
}

func units(src string, metric text.Metric) []unit {
	toks := CanonTokens(src)
	out := make([]unit, 0, len(toks))
	for _, t := range toks {
		if metric != text.MetricCER {
			out = append(out, unit{t.Text, t.Start, t.End})
			continue
		}
		spans := runeSpans(src[t.Start:t.End], t.Text)
		k := 0
		for _, r := range t.Text {
			u := unit{string(r), t.Start, t.End}
			if spans != nil {
				u.start, u.end = t.Start+spans[k][0], t.Start+spans[k][1]
			}
			out = append(out, u)
			k++
		}
	}
	return out
}

// runeSpans maps each rune of a canonical token to its own source rune when the source holds
// the same letters one for one (case aside), as Han, Kana and most words do; otherwise nil,
// and every rune reports the whole token's span.
func runeSpans(src, tok string) [][2]int {
	var spans [][2]int
	want := []rune(tok)
	for i, r := range src {
		if !unicode.IsLetter(r) && !unicode.IsMark(r) && !unicode.IsDigit(r) {
			continue
		}
		if len(spans) == len(want) || unicode.ToLower(r) != want[len(spans)] {
			return nil
		}
		spans = append(spans, [2]int{i, i + utf8.RuneLen(r)})
	}
	if len(spans) != len(want) {
		return nil
	}
	return spans
}

func texts(us []unit) []string {
	out := make([]string, len(us))
	for i, u := range us {
		out[i] = u.text
	}
	return out
}

// edits groups each run of non-matching steps into one Edit.
func edits(steps []step, ref, hyp []unit, reference, hypothesis string) []Edit {
	var out []Edit
	refPos, hypPos := 0, 0 // end of the last unit seen on each side
	for i := 0; i < len(steps); {
		if steps[i].match {
			refPos, hypPos = max(refPos, ref[steps[i].ref].end), max(hypPos, hyp[steps[i].hyp].end)
			i++
			continue
		}
		e := Edit{RefStart: -1, HeardStart: -1}
		for ; i < len(steps) && !steps[i].match; i++ {
			if r := steps[i].ref; r >= 0 {
				e.RefStart, e.RefEnd = spanUnion(e.RefStart, e.RefEnd, ref[r])
			}
			if h := steps[i].hyp; h >= 0 {
				e.HeardStart, e.HeardEnd = spanUnion(e.HeardStart, e.HeardEnd, hyp[h])
			}
		}
		switch {
		case e.RefStart >= 0 && e.HeardStart >= 0:
			e.Kind = Substitution
		case e.RefStart >= 0:
			e.Kind = Deletion
			e.HeardStart, e.HeardEnd = hypPos, hypPos
		default:
			e.Kind = Insertion
			e.RefStart, e.RefEnd = refPos, refPos
		}
		// Runes of one source word share its span (CER), so an edit inside a word reports the
		// whole word, which may overlap the span of a neighbouring match.
		e.Expected, e.Heard = reference[e.RefStart:e.RefEnd], hypothesis[e.HeardStart:e.HeardEnd]
		refPos, hypPos = max(refPos, e.RefEnd), max(hypPos, e.HeardEnd)
		out = append(out, e)
	}
	return out
}

func spanUnion(start, end int, u unit) (int, int) {
	if start < 0 {
		return u.start, u.end
	}
	return min(start, u.start), max(end, u.end)
}

// step is one alignment step: ref and hyp index the two sequences, -1 when the step consumes
// nothing on that side.
type step struct {
	ref, hyp int
	match    bool
}

// Alignment operations, stored one byte per cell so a 2,000 × 2,000 alignment needs 4 MB.
const (
	opMatch byte = iota
	opSub
	opDel
	opIns
)

// align is a Levenshtein alignment of ref against hyp, returned in order. On equal cost it
// prefers the diagonal, then a deletion, so the result is deterministic. Elements are interned
// to integers first, and the common prefix and suffix are matched without the quadratic table.
func align(ref, hyp []string) []step {
	ids := map[string]int32{}
	intern := func(xs []string) []int32 {
		out := make([]int32, len(xs))
		for i, x := range xs {
			id, ok := ids[x]
			if !ok {
				id = int32(len(ids))
				ids[x] = id
			}
			out[i] = id
		}
		return out
	}
	a, b := intern(ref), intern(hyp)
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	steps := make([]step, 0, max(len(a), len(b)))
	for k := range pre {
		steps = append(steps, step{ref: k, hyp: k, match: true})
	}
	for _, st := range alignCore(a[pre:len(a)-suf], b[pre:len(b)-suf]) {
		if st.ref >= 0 {
			st.ref += pre
		}
		if st.hyp >= 0 {
			st.hyp += pre
		}
		steps = append(steps, st)
	}
	for k := suf; k > 0; k-- {
		steps = append(steps, step{ref: len(a) - k, hyp: len(b) - k, match: true})
	}
	return steps
}

// alignCore is a banded Levenshtein alignment (Ukkonen): cells further than k from the
// diagonal are never computed, and k doubles until the distance found is at most k, which
// proves no cheaper path leaves the band. Cost is O((d+1)·n) for distance d, not O(n·m).
func alignCore(ref, hyp []int32) []step {
	n, m := len(ref), len(hyp)
	for k := 16; ; k *= 2 {
		if steps, ok := alignBand(ref, hyp, min(k, max(n, m))); ok || k >= max(n, m) {
			return steps
		}
	}
}

const inf = int32(1 << 30)

func alignBand(ref, hyp []int32, k int) ([]step, bool) {
	n, m := len(ref), len(hyp)
	lo := func(i int) int { return i - k + min(0, m-n) }
	hi := func(i int) int { return i + k + max(0, m-n) }
	w := hi(0) - lo(0) + 1
	ops := make([]byte, (n+1)*w)
	at := func(i, j int) int { return i*w + j - lo(i) }
	prev, cur := make([]int32, m+2), make([]int32, m+2)
	for j := range prev {
		prev[j], cur[j] = inf, inf
	}
	for j := 0; j <= min(m, hi(0)); j++ {
		prev[j] = int32(j)
		ops[at(0, j)] = opIns
	}
	for i := 1; i <= n; i++ {
		jlo, jhi := max(0, lo(i)), min(m, hi(i))
		if jlo > 0 {
			cur[jlo-1] = inf
		}
		if jlo == 0 {
			cur[0] = int32(i)
			ops[at(i, 0)] = opDel
			jlo = 1
		}
		r := ref[i-1]
		for j := jlo; j <= jhi; j++ {
			best, op := prev[j-1], opMatch
			if r != hyp[j-1] {
				best, op = best+1, opSub
			}
			if d := prev[j] + 1; d < best {
				best, op = d, opDel
			}
			if in := cur[j-1] + 1; in < best {
				best, op = in, opIns
			}
			cur[j], ops[at(i, j)] = best, op
		}
		cur[jhi+1] = inf
		prev, cur = cur, prev
	}
	if int(prev[m]) > k && k < max(n, m) {
		return nil, false
	}
	steps := make([]step, 0, max(n, m))
	for i, j := n, m; i > 0 || j > 0; {
		switch ops[at(i, j)] {
		case opMatch:
			i, j = i-1, j-1
			steps = append(steps, step{ref: i, hyp: j, match: true})
		case opSub:
			i, j = i-1, j-1
			steps = append(steps, step{ref: i, hyp: j})
		case opDel:
			i--
			steps = append(steps, step{ref: i, hyp: -1})
		default:
			j--
			steps = append(steps, step{ref: -1, hyp: j})
		}
	}
	for a, b := 0, len(steps)-1; a < b; a, b = a+1, b-1 {
		steps[a], steps[b] = steps[b], steps[a]
	}
	return steps, true
}
