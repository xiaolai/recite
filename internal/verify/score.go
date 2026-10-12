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
	res, _ := score(reference, hypothesis, metric)
	return res
}

// alignment is a scored alignment with its canonical units, for the guards.
type alignment struct {
	ref, hyp []unit
	steps    []step
}

func score(reference, hypothesis string, metric text.Metric) (Result, alignment) {
	ref, hyp := units(reference, metric), units(hypothesis, metric)
	steps := align(texts(ref), texts(hyp))
	for i, st := range steps {
		// Spelled letters are heard letter by letter, so their exact spelling is what was said:
		// a folded match of "G R E Y" with "G R A Y" is a wrong letter.
		if st.match && (ref[st.ref].spelled || hyp[st.hyp].spelled) && ref[st.ref].raw != hyp[st.hyp].raw {
			steps[i].match = false
		}
	}
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
	return res, alignment{ref: ref, hyp: hyp, steps: steps}
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
// raw is the text before spelling variants were folded; spelled marks a word read out letter
// by letter ("G R E Y"), whose raw letters must match exactly.
type unit struct {
	text       string
	start, end int
	raw        string
	spelled    bool
}

func units(src string, metric text.Metric) []unit {
	return foldPronouns(rawUnits(src, metric))
}

// foldPronouns gives the Han pronouns read alike one comparison form (see hanPronouns).
func foldPronouns(us []unit) []unit {
	for i, u := range us {
		if v, ok := hanPronouns[u.text]; ok {
			us[i].text = v
		}
	}
	return us
}

func rawUnits(src string, metric text.Metric) []unit {
	toks, raw := foldVariantsRaw(CanonTokens(src))
	if metric == text.MetricCER {
		var out []unit
		for i, t := range toks {
			if spelledOut(src[t.Start:t.End]) {
				t.Text = raw[i] // compared rune by rune, so the letters are kept as spelled
			}
			out = append(out, runeUnits(src, t, func(rune) bool { return true })...)
		}
		return out
	}
	texts := make([]string, len(toks))
	for i, t := range toks {
		texts[i] = t.Text
	}
	var out []unit
	for i := 0; i < len(toks); {
		// A year read as a pair compares as its cardinal, whichever way either side read it
		// (design §8.3); the cardinal's words keep the span of the pair.
		if v, n := yearPairAt(texts, i); n > 0 {
			for _, w := range cardinal(uint64(v)) {
				out = append(out, unit{text: w, start: toks[i].Start, end: toks[i+n-1].End, raw: w})
			}
			i += n
			continue
		}
		t := toks[i]
		if strings.IndexFunc(t.Text, unspaced) >= 0 {
			// Han, Kana and Thai have no spaces between words, so inside a word-scored chunk
			// they are compared by character ("上打开App" = "上打开 App").
			out = append(out, runeUnits(src, t, unspaced)...)
		} else {
			out = append(out, unit{text: t.Text, start: t.Start, end: t.End, raw: raw[i], spelled: spelledOut(src[t.Start:t.End])})
		}
		i++
	}
	return out
}

// spelledOut reports source text that reads a word letter by letter: at least two runs of
// letters, each a single letter ("G R E Y", "G-R-E-Y").
func spelledOut(src string) bool {
	runs, run := 0, 0
	for _, r := range src {
		if unicode.IsLetter(r) || unicode.IsMark(r) {
			run++
			continue
		}
		if run > 1 {
			return false
		}
		if run == 1 {
			runs++
		}
		run = 0
	}
	if run > 1 {
		return false
	}
	if run == 1 {
		runs++
	}
	return runs >= 2
}

// unspaced reports a rune of a script written without spaces between words.
func unspaced(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Thai)
}

// runeUnits splits token t into one unit per rune for which single is true, and one unit per
// run of the other runes, each with its own source span when the source spells the token
// letter for letter.
func runeUnits(src string, t CanonToken, single func(rune) bool) []unit {
	spans := runeSpans(src[t.Start:t.End], t.Text)
	span := func(a, b int) (int, int) {
		if spans == nil {
			return t.Start, t.End
		}
		return t.Start + spans[a][0], t.Start + spans[b][1]
	}
	rs := []rune(t.Text)
	var out []unit
	for k := 0; k < len(rs); {
		e := k + 1
		if !single(rs[k]) {
			for e < len(rs) && !single(rs[e]) {
				e++
			}
		}
		a, b := span(k, e-1)
		out = append(out, unit{text: string(rs[k:e]), start: a, end: b, raw: string(rs[k:e])})
		k = e
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
	// One side empty: only deletions or only insertions, and no table at all.
	if n == 0 || m == 0 {
		steps := make([]step, 0, n+m)
		for i := range n {
			steps = append(steps, step{ref: i, hyp: -1})
		}
		for j := range m {
			steps = append(steps, step{ref: -1, hyp: j})
		}
		return steps
	}
	for k := 16; ; k *= 2 {
		if steps, ok := alignBand(ref, hyp, min(k, max(n, m))); ok || k >= max(n, m) {
			return steps
		}
	}
}

const inf = int32(1 << 30)

// bandWidth is the number of cells stored per row: the band, but never more than the m+1
// columns that exist, so a long reference against a short transcript stores O(n), not
// O(n·(n−m)). Row i stores columns max(0, lo(i)) through min(m, hi(i)).
func bandWidth(n, m, k int) int {
	return min(2*k+max(0, m-n)-min(0, m-n)+1, m+1)
}

func alignBand(ref, hyp []int32, k int) ([]step, bool) {
	n, m := len(ref), len(hyp)
	lo := func(i int) int { return i - k + min(0, m-n) }
	hi := func(i int) int { return i + k + max(0, m-n) }
	w := bandWidth(n, m, k)
	ops := make([]byte, (n+1)*w)
	at := func(i, j int) int { return i*w + j - max(0, lo(i)) }
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
