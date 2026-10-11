package calibrate

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind is how a candidate was constructed: the original passage or one corruption of it.
type Kind string

// Construction kinds (VER-07).
const (
	Good         Kind = "good"
	WordSwap     Kind = "word-swap"     // two adjacent words swapped
	ClauseDrop   Kind = "clause-drop"   // one clause removed
	Preamble     Kind = "preamble"      // an assistant-style preamble added
	NumberChange Kind = "number-change" // a number changed
	NameChange   Kind = "name-change"   // a name changed
	ContentWord  Kind = "content-word"  // a content word replaced by one of another meaning
)

// Corruptions are the corruption kinds, in the order candidates cycle through them.
var Corruptions = []Kind{WordSwap, ClauseDrop, Preamble, NumberChange, NameChange, ContentWord}

// ErrNotApplicable means the passage has nothing the corruption can edit.
var ErrNotApplicable = errors.New("calibrate: corruption not applicable to the passage")

var (
	preambles = []string{"Here is the passage.", "Sure, here it is.", "Certainly. Here is the text you asked for."}
	numbers   = []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten",
		"eleven", "twelve", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety", "hundred", "thousand"}
	names    = []string{"Lincoln", "Georgia", "Virginia", "Boston", "Jefferson", "Ohio", "Madison", "Carolina"}
	antonyms = map[string]string{
		"all": "no", "equal": "unequal", "free": "enslaved", "living": "dead", "dead": "living",
		"long": "short", "great": "small", "never": "always", "always": "never", "add": "remove",
		"remember": "forget", "forget": "remember", "new": "old", "unalienable": "revocable",
		"just": "unjust", "highest": "lowest", "least": "most", "more": "less", "less": "more",
		"right": "wrong", "true": "false", "perish": "flourish", "unfinished": "finished",
	}
	replacements = []string{"winter", "silver", "broken", "distant", "hollow", "foreign"}
	function     = set(`a an the and or but nor so yet for of in on at to by from with as into upon
		that this these those it its is are was were be been being have has had do does did shall
		should will would may might can could not no who whom which what when where we us our
		they them their he his him she her you your i me my any every each such than then there here`)
)

func set(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

// Corrupt applies corruption k to passage, choosing among the possible edits with rng.
// It returns ErrNotApplicable when the passage offers no edit of that kind.
func Corrupt(passage string, k Kind, rng *rand.Rand) (string, error) {
	w := strings.Fields(passage)
	var out []string
	var err error
	switch k {
	case WordSwap:
		out, err = wordSwap(w, rng)
	case ClauseDrop:
		out, err = clauseDrop(w, rng)
	case Preamble:
		out = append(strings.Fields(preambles[rng.IntN(len(preambles))]), w...)
	case NumberChange:
		out, err = numberChange(w, rng)
	case NameChange:
		out, err = nameChange(w, rng)
	case ContentWord:
		out, err = contentWord(w, rng)
	default:
		return "", fmt.Errorf("calibrate: unknown corruption %q", k)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", k, err)
	}
	return strings.Join(out, " "), nil
}

// split separates a word into leading punctuation, its core and trailing punctuation.
func split(w string) (lead, core, trail string) {
	i := strings.IndexFunc(w, isWordRune)
	if i < 0 {
		return w, "", ""
	}
	j := strings.LastIndexFunc(w, isWordRune)
	_, n := utf8.DecodeRuneInString(w[j:])
	return w[:i], w[i : j+n], w[j+n:]
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' }

func alpha(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return !unicode.IsLetter(r) }) < 0
}

func withCore(w, core string) string {
	lead, _, trail := split(w)
	return lead + core + trail
}

// matchCase gives repl the capitalisation of orig's first letter.
func matchCase(orig, repl string) string {
	if orig == "" || repl == "" {
		return repl
	}
	r := []rune(repl)
	if unicode.IsUpper([]rune(orig)[0]) {
		r[0] = unicode.ToUpper(r[0])
	} else {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}

// pick returns an element of pool other than not (case-insensitively).
func pick(pool []string, not string, rng *rand.Rand) string {
	var c []string
	for _, p := range pool {
		if !strings.EqualFold(p, not) {
			c = append(c, p)
		}
	}
	return c[rng.IntN(len(c))]
}

func wordSwap(w []string, rng *rand.Rand) ([]string, error) {
	var at []int
	for i := 1; i+1 < len(w); i++ {
		_, a, ta := split(w[i])
		_, b, _ := split(w[i+1])
		// No punctuation between the two, so the swap reads as a slip, not a re-punctuation.
		if alpha(a) && alpha(b) && ta == "" && !strings.EqualFold(a, b) {
			at = append(at, i)
		}
	}
	if len(at) == 0 {
		return nil, ErrNotApplicable
	}
	i := at[rng.IntN(len(at))]
	out := append([]string(nil), w...)
	_, a, _ := split(w[i])
	_, b, _ := split(w[i+1])
	out[i], out[i+1] = withCore(w[i], b), withCore(w[i+1], a)
	return out, nil
}

// clauseDrop removes one clause other than the first; clauses end at a comma or semicolon
// (Latin or Chinese).
func clauseDrop(w []string, rng *rand.Rand) ([]string, error) {
	var bounds []int // end index (exclusive) of each clause
	for i, x := range w {
		if strings.HasSuffix(x, ",") || strings.HasSuffix(x, ";") || strings.HasSuffix(x, "，") ||
			strings.HasSuffix(x, "；") || i == len(w)-1 {
			bounds = append(bounds, i+1)
		}
	}
	if len(bounds) < 2 {
		return nil, ErrNotApplicable
	}
	k := 1 + rng.IntN(len(bounds)-1)
	start, end := bounds[k-1], bounds[k]
	out := append(append([]string(nil), w[:start]...), w[end:]...)
	if end == len(w) {
		// The last clause went: its final punctuation replaces the previous clause's comma.
		_, _, trail := split(w[end-1])
		last := out[len(out)-1]
		out[len(out)-1] = strings.TrimRight(last, ",;，；") + trail
	}
	return out, nil
}

func numberChange(w []string, rng *rand.Rand) ([]string, error) {
	return replaceWhere(w, rng, func(w []string, i int) bool {
		_, c, _ := split(w[i])
		return isNumberWord(c) || isDigits(c)
	}, func(c string) string {
		if isDigits(c) {
			n, _ := strconv.Atoi(c)
			return strconv.Itoa(n + 1 + rng.IntN(9))
		}
		return matchCase(c, pick(numbers, c, rng))
	})
}

func isNumberWord(c string) bool {
	for _, n := range numbers {
		if strings.EqualFold(c, n) {
			return true
		}
	}
	return false
}

func isDigits(c string) bool {
	return c != "" && strings.IndexFunc(c, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

// properNames are names of places, bodies and days; a name change prefers them over other
// capitalised words, which older prose capitalises freely.
var properNames = set(`states america congress senate britain hampshire massachusetts connecticut
	york jersey pennsylvania december monday`)

func nameChange(w []string, rng *rand.Rand) ([]string, error) {
	repl := func(c string) string { return pick(names, c, rng) }
	out, err := replaceWhere(w, rng, func(w []string, i int) bool {
		_, c, _ := split(w[i])
		return isName(w, i) && properNames[strings.ToLower(c)]
	}, repl)
	if !errors.Is(err, ErrNotApplicable) {
		return out, err
	}
	return replaceWhere(w, rng, isName, repl)
}

// isName is a capitalised word that does not start a sentence.
func isName(w []string, i int) bool {
	_, c, _ := split(w[i])
	if i == 0 || !alpha(c) || c == "I" || !unicode.IsUpper([]rune(c)[0]) || isNumberWord(c) {
		return false
	}
	return !strings.ContainsAny(w[i-1][len(w[i-1])-1:], ".!?:")
}

func contentWord(w []string, rng *rand.Rand) ([]string, error) {
	// An antonym changes the meaning for certain; a replacement word is the fallback.
	out, err := replaceWhere(w, rng, func(w []string, i int) bool {
		_, c, _ := split(w[i])
		_, ok := antonyms[strings.ToLower(c)]
		return ok
	}, func(c string) string { return matchCase(c, antonyms[strings.ToLower(c)]) })
	if !errors.Is(err, ErrNotApplicable) {
		return out, err
	}
	return replaceWhere(w, rng, func(w []string, i int) bool {
		_, c, _ := split(w[i])
		return alpha(c) && len(c) >= 4 && !function[strings.ToLower(c)] && unicode.IsLower([]rune(c)[0])
	}, func(c string) string { return pick(replacements, c, rng) })
}

// replaceWhere replaces the core of one word chosen among those where ok holds.
func replaceWhere(w []string, rng *rand.Rand, ok func([]string, int) bool, repl func(core string) string) ([]string, error) {
	var at []int
	for i := range w {
		if ok(w, i) {
			at = append(at, i)
		}
	}
	if len(at) == 0 {
		return nil, ErrNotApplicable
	}
	i := at[rng.IntN(len(at))]
	out := append([]string(nil), w...)
	_, c, _ := split(w[i])
	out[i] = withCore(w[i], repl(c))
	return out, nil
}
