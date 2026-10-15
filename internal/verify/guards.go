package verify

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/text"
	"github.com/xiaolai/sayso/go/numwords"
)

// Reason names the guard or threshold that rejected a take (VER-05, VER-06, design §8).
type Reason string

// Rejection reasons. The first four are pre-STT guards: a take they reject is never
// transcribed and never eligible.
const (
	ReasonSaid          Reason = "said"
	ReasonEmpty         Reason = "empty"
	ReasonDuration      Reason = "duration"
	ReasonStatus        Reason = "status"
	ReasonLanguage      Reason = "language"
	ReasonProtected     Reason = "protected-token"
	ReasonEdgeInsertion Reason = "edge-insertion"
	ReasonWordOrder     Reason = "word-order"
	ReasonContentWord   Reason = "content-word"
	ReasonScore         Reason = "score"
)

// GuardVersion identifies the behaviour of the guard set (pre-STT and post-STT guards and
// their limits). Bump it on any change that can turn a guard's verdict: cached verification
// records it, and a cached take judged by another version is re-verified (RUN-04). It also
// covers the scorer's comparison units (spelling variants, folded Han pronouns), which change
// scores without changing Canon.
//
// 2: spelling variants and compounds, 她/他/它 folded, the word-order guard, and causal
// connectives taken off the function-word list.
// 3: spelled letters keep their exact spelling, substitutions crossed over matched words are a
// word-order error, the empty and duration guards judge trimmed audio under --no-trim, and
// lexicon ranges keep their offsets when case folding changes byte lengths.
// 4: spelled letters keep their identity under CER and inside the alignment, lexicon keys are
// matched by the lexicon's own word-boundary rule (Han keys inside Han text), and a comparison
// too large to align is rejected as unverifiable.
const GuardVersion = 4

// Pre-STT guard limits (design §8.1).
const (
	MinTakeDuration  = 200 * time.Millisecond
	MinDurationRatio = 0.5
	MaxDurationRatio = 2.2
)

// Rejection is a failed guard with a human-readable detail for the report.
type Rejection struct {
	Reason Reason
	Detail string
}

// PreSTT runs the guards that need no network, in this order: status, empty, said, duration.
// It returns the take's audio, trimmed unless noTrim, and the first failing guard, or nil.
// Empty and duration always judge the trimmed audio, so a take of silence counts as empty;
// noTrim only keeps the silence in the audio returned for output.
func PreSTT(c Chunk, take realtime.Take, noTrim bool) (audio.PCM, *Rejection) {
	return preSTT(c, take, noTrim, false)
}

// preSTT is PreSTT with the said guard optionally skipped (Options.BypassSaid).
func preSTT(c Chunk, take realtime.Take, noTrim, bypassSaid bool) (audio.PCM, *Rejection) {
	trimmed := audio.Trim(audio.PCM(take.PCM))
	pcm := trimmed
	if noTrim {
		pcm = audio.PCM(take.PCM)
	}
	if take.Status != realtime.StatusCompleted {
		return pcm, &Rejection{ReasonStatus, fmt.Sprintf("response ended %q", take.Status)}
	}
	d := trimmed.Duration()
	if d < MinTakeDuration {
		return pcm, &Rejection{ReasonEmpty, fmt.Sprintf("%v of audio", d)}
	}
	if !bypassSaid && !Equivalent(take.Said, c.Spoken) {
		return pcm, &Rejection{ReasonSaid, fmt.Sprintf("the model said %q", take.Said)}
	}
	if r := d.Seconds() / c.Estimate.Seconds(); r < MinDurationRatio || r > MaxDurationRatio {
		return pcm, &Rejection{ReasonDuration, fmt.Sprintf("%v is %.2f× the %v estimate", d, r, c.Estimate)}
	}
	return pcm, nil
}

// LanguageGuard rejects a transcript whose first detected language differs from the chunk's
// language, only when that language is certain (TXT-04). A transcript reporting no language
// (the realtime fallback reports none) is not judged.
func LanguageGuard(lang text.Language, t stt.Transcript) *Rejection {
	if !lang.Certain || lang.Code == "" || len(t.Languages) == 0 || t.Languages[0].Code == "" {
		return nil
	}
	if got := t.Languages[0].Code; primaryTag(got) != primaryTag(lang.Code) {
		return &Rejection{ReasonLanguage, fmt.Sprintf("heard %q, expected %q", got, lang.Code)}
	}
	return nil
}

func primaryTag(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexAny(code, "-_"); i >= 0 {
		code = code[:i]
	}
	return code
}

// mismatch is one content-word difference at an aligned position: Pos is the reference unit
// index (for an insertion, the index of the reference unit after it).
type mismatch struct {
	Pos      int
	Ref, Hyp string
}

// judgement is what the post-STT guards found in one transcript.
type judgement struct {
	result    Result
	rejection *Rejection
	content   []mismatch
}

// judge scores one transcript against the chunk and runs the language, protected-token and
// edge-insertion guards, and collects the content-word mismatches (design §8.4).
func judge(c Chunk, t stt.Transcript) judgement {
	// The transcript is compared with lexicon equivalents rewritten, then in spoken form; eq
	// and hyp map offsets back through both steps.
	eq := rewriteEquivalents(t.Text, sayPairsFor(c.Spoken, c.Equivalents))
	metric := ScriptMetric(c.Spoken)
	ref, hyp := toFormSpans(c.Spoken, c.Language.Code, c.LangSpans), toForm(eq.text, c.Language.Code)
	res, al := score(ref.text, hyp.text, metric)
	if res.Unaligned {
		return judgement{result: res, rejection: &Rejection{ReasonScore, fmt.Sprintf(
			"unverifiable: %d reference units against %d heard exceed the alignment limit", res.RefLen, len(al.hyp))}}
	}
	// Report edits against the reference and transcript as they are, for the protected ranges,
	// the recorded transcript and the logprobs.
	for i, e := range res.Edits {
		e.RefStart, e.RefEnd = ref.back(e.RefStart, e.RefEnd)
		e.HeardStart, e.HeardEnd = eq.back(hyp.back(e.HeardStart, e.HeardEnd))
		e.Expected, e.Heard = c.Spoken[e.RefStart:e.RefEnd], t.Text[e.HeardStart:e.HeardEnd]
		res.Edits[i] = e
	}
	j := judgement{result: res}
	if r := LanguageGuard(c.Language, t); r != nil {
		j.rejection = r
		return j
	}
	prot := protectedRanges(c)
	for _, e := range res.Edits {
		if e.Kind == Insertion {
			continue
		}
		for _, p := range prot {
			if e.RefStart < p[1] && p[0] < e.RefEnd {
				j.rejection = &Rejection{ReasonProtected, fmt.Sprintf("%s: expected %q / heard %q", e.Kind, e.Expected, e.Heard)}
				return j
			}
		}
	}
	lead, trail := edgeInsertions(al.steps)
	limit := 2
	if metric == text.MetricCER {
		limit = 4
	}
	if lead >= limit || trail >= limit {
		j.rejection = &Rejection{ReasonEdgeInsertion, fmt.Sprintf("%d inserted before, %d after the text", lead, trail)}
		return j
	}
	if exp, heard, ok := transposition(al, metric); ok {
		j.rejection = &Rejection{ReasonWordOrder, fmt.Sprintf("expected %q / heard %q", exp, heard)}
		return j
	}
	j.content = contentMismatches(al, primaryTag(c.Language.Code))
	return j
}

// edgeInsertions counts inserted units before the first matched unit and after the last. With
// no match at all, every insertion counts as leading.
func edgeInsertions(steps []step) (lead, trail int) {
	first, last := -1, -1
	for i, s := range steps {
		if s.match {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	for i, s := range steps {
		if s.ref >= 0 || s.match {
			continue
		}
		switch {
		case first < 0 || i < first:
			lead++
		case i > last:
			trail++
		}
	}
	return lead, trail
}

func contentMismatches(al alignment, lang string) []mismatch {
	var out []mismatch
	pos := 0
	for _, s := range al.steps {
		var m mismatch
		m.Pos = pos
		if s.ref >= 0 {
			m.Ref = al.ref[s.ref].text
			pos = s.ref + 1
		}
		if s.hyp >= 0 {
			m.Hyp = al.hyp[s.hyp].text
		}
		if s.match {
			continue
		}
		if !isFunctionWord(lang, m.Ref) || !isFunctionWord(lang, m.Hyp) {
			out = append(out, m)
		}
	}
	return out
}

// repeated reports whether a and b share a mismatch.
func repeated(a, b []mismatch) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// protectedRanges lists the byte ranges of c.Spoken that no substitution or deletion may
// touch: tokens holding a digit or a number word, capitalised words not at the start of a
// sentence, lexicon keys, and the caller's mark spans ({{spell}}, {{digits}}, {{say}}).
// Number words written as words by the author are protected too: they read the same as
// those the spoken form produced, and a wrong number is as wrong either way.
func protectedRanges(c Chunk) [][2]int {
	var out [][2]int
	for _, tok := range keywordTokens(c.Spoken) {
		if strings.IndexFunc(tok.text, unicode.IsDigit) >= 0 || (!tok.initial && isCapitalised(tok.text)) || hasNumberWord(tok.text) {
			out = append(out, [2]int{tok.start, tok.end})
		}
	}
	// Case is folded rune by rune on the original text: lowercasing the whole string can
	// change byte lengths ("İ" → "i") and shift every offset after it.
	for _, k := range c.Lexicon {
		k = strings.TrimSpace(k)
		n := utf8.RuneCountInString(k)
		for i := 0; k != "" && i < len(c.Spoken); {
			if e := runeEnd(c.Spoken, i, n); e >= 0 && strings.EqualFold(c.Spoken[i:e], k) && spoken.WordBounded(c.Spoken, i, e) {
				out = append(out, [2]int{i, e})
				i = e
				continue
			}
			_, w := utf8.DecodeRuneInString(c.Spoken[i:])
			i += w
		}
	}
	for _, r := range c.Protected {
		out = append(out, [2]int{r.Start, r.End})
	}
	return out
}

func hasNumberWord(tok string) bool {
	for _, w := range Canon(tok) {
		if isNumberWord(w) || isScale(w) || ordinalWords[w] {
			return true
		}
	}
	return false
}

var ordinalWords = func() map[string]bool {
	m := map[string]bool{}
	add := func(w string) { m[numwords.Ordinal([]string{w})[0]] = true }
	for v := range 20 {
		add(numwords.Cardinal(uint64(v))[0])
	}
	for v := 20; v < 100; v += 10 {
		add(numwords.Cardinal(uint64(v))[0])
	}
	for _, s := range []string{"hundred", "thousand", "million", "billion", "trillion"} {
		add(s)
	}
	return m
}()

// functionWords are the per-language words whose mismatch is left to the score (VER-06).
// Semantic operators (negations, can/must, quantifiers) are deliberately absent, and so are the
// connectives that carry a clause's logic (because, so, but, although, though): dropping
// "because" turns a reason into a bare statement, which the score alone lets through.
var functionWords = map[string]map[string]bool{
	"en": set(`a an the of in on at to for from by with about as into onto over under up down out
		off through between among after before during until upon and or yet if than that
		then while is am are was were be been being do does did have has
		had will would shall should may might could i me my mine we us our ours you your yours he
		him his she her hers it its they them their theirs this these those there here what which
		who whom whose`),
	"zh": set(`的 了 是 在 和 与 及 我 你 他 她 它 们 这 那 吗 呢 吧 啊 呀 着 过 地 得 把 被 就 也 还 又 很 于 而 之 以 其`),
}

func set(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

// isFunctionWord reports a function word of lang; "" (the empty side of an insertion or a
// deletion) counts as one, so only the side that holds a word decides.
func isFunctionWord(lang, w string) bool {
	return w == "" || functionWords[lang][w]
}

// LowConfidenceLogprob is the mean token logprob below which a mismatched span is recorded as
// low-confidence STT (design §8.5).
const LowConfidenceLogprob = -1.0

// SpanConfidence is the mean logprob of the STT tokens covering one mismatched heard span.
type SpanConfidence struct {
	Edit        Edit
	MeanLogprob float64
	Low         bool
}

// confidences maps the transcript's tokens onto its text and averages the logprobs over each
// edit's heard span. Tokens that do not concatenate to the text give no confidences.
func confidences(t stt.Transcript, edits []Edit) []SpanConfidence {
	var b strings.Builder
	starts := make([]int, len(t.Tokens))
	for i, tok := range t.Tokens {
		starts[i] = b.Len()
		b.WriteString(tok.Token)
	}
	if len(t.Tokens) == 0 || b.String() != t.Text {
		return nil
	}
	var out []SpanConfidence
	for _, e := range edits {
		if e.HeardEnd <= e.HeardStart {
			continue
		}
		var sum float64
		n := 0
		for i, tok := range t.Tokens {
			if s, end := starts[i], starts[i]+len(tok.Token); s < e.HeardEnd && e.HeardStart < end {
				sum += tok.Logprob
				n++
			}
		}
		if n > 0 {
			mean := sum / float64(n)
			out = append(out, SpanConfidence{Edit: e, MeanLogprob: mean, Low: mean < LowConfidenceLogprob})
		}
	}
	return out
}
