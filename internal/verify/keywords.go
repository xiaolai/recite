package verify

import (
	_ "embed"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xiaolai/recite/internal/canon"
)

// MaxKeywords bounds the STT prompt (VER-10).
const MaxKeywords = 20

// commonEnglish is the 10,000 most frequent English words, one per line, most frequent first.
// It was counted from 35 public-domain Project Gutenberg books (Austen, Dickens, Twain, Doyle,
// Melville, Tolstoy in translation, Darwin, Adam Smith, Joyce and others; header and licence
// text stripped), keeping words that appear in at least three books and are written in lower
// case at least half the time, so proper names stay out. Its vocabulary is literary and
// somewhat dated: a modern word ("email") may count as rare and become a keyword, which only
// costs a slot in the prompt.
//
//go:embed data/en-10k.txt
var commonEnglishList string

var commonEnglish = func() map[string]bool {
	words := strings.Fields(commonEnglishList)
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}()

// CommonEnglishSize is the number of words in the bundled frequency list.
func CommonEnglishSize() int { return len(commonEnglish) }

// KeywordPrompt is the STT prompt for a chunk: its keywords, comma-joined (VER-10).
func KeywordPrompt(chunk string, lexicon []string) string {
	return strings.Join(Keywords(chunk, lexicon), ", ")
}

// Keywords picks at most MaxKeywords words from chunk that a recogniser is likely to get wrong,
// in this priority: lexicon keys present in the chunk, tokens containing digits, capitalised
// words not at the start of a sentence, and Latin-script words absent from the frequency list.
// Duplicates (case-insensitive) are dropped. The keywords together are always fewer than 30% of
// the chunk's tokens, so the prompt can never become the text (X-10).
func Keywords(chunk string, lexicon []string) []string {
	words := keywordTokens(chunk)
	budget := len(words) // tokens still allowed, as 10 × count < 3 × len(words)
	var out []string
	used := 0
	seen := map[string]bool{}
	add := func(k string) {
		key := strings.ToLower(k)
		n := len(keywordTokens(k))
		if len(out) == MaxKeywords || seen[key] || n == 0 || 10*(used+n) >= 3*budget {
			return
		}
		seen[key] = true
		out = append(out, k)
		used += n
	}
	lower := strings.ToLower(chunk)
	for _, k := range lexicon {
		if k = strings.TrimSpace(k); k != "" && containsWord(lower, strings.ToLower(k)) {
			add(k)
		}
	}
	for _, w := range words {
		if strings.IndexFunc(w.text, unicode.IsDigit) >= 0 {
			add(w.text)
		}
	}
	for _, w := range words {
		if !w.initial && isCapitalised(w.text) {
			add(w.text)
		}
	}
	for _, w := range words {
		if isRareEnglish(w.text) {
			add(w.text)
		}
	}
	return out
}

type keywordToken struct {
	text       string
	initial    bool // first word of the chunk or of a sentence
	start, end int  // byte offsets in the source
}

// keywordTokens splits s into words: runs of letters, digits and marks, with '.', '-' and
// apostrophes kept between two of them ("3.5", "forty-two", "O'Neil"). A letter of a script
// written without spaces (Han, Kana, Hangul, Thai) is one token on its own.
func keywordTokens(s string) []keywordToken {
	var out []keywordToken
	initial := true
	start := -1
	emit := func(end int) {
		if start >= 0 {
			out = append(out, keywordToken{text: s[start:end], initial: initial, start: start, end: end})
			initial = false
			start = -1
		}
	}
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case isSyllabicLetter(r):
			emit(i)
			out = append(out, keywordToken{text: s[i : i+size], initial: initial, start: i, end: i + size})
			initial = false
		case isWordChar(r):
			if start < 0 {
				start = i
			}
		case start >= 0 && i+size < len(s) && joins(s[start:i], r, s[i+size:]):
		default:
			// The period of a known abbreviation ("Mrs.") does not end a sentence.
			abbrev := r == '.' && start >= 0 && isAbbreviation(s[start:i])
			emit(i)
			if !abbrev && (r == '.' || r == '!' || r == '?' || r == '。' || r == '！' || r == '？' || r == '\n') {
				initial = true
			}
		}
		i += size
	}
	emit(len(s))
	return out
}

func isWordChar(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) }

func isJoiner(r rune) bool { return r == '.' || isHyphen(r) || isApostrophe(r) }

// joins reports whether r, between the word so far and the rest of the text, stays inside the
// word: '.', a hyphen or an apostrophe before another word character, and ':' or ',' between
// digits ("9:30", "1,000").
func joins(word string, r rune, rest string) bool {
	next, _ := utf8.DecodeRuneInString(rest)
	if !isWordChar(next) || isSyllabicLetter(next) {
		return false
	}
	if isJoiner(r) {
		return r != '.' || !isAbbreviation(word)
	}
	prev, _ := utf8.DecodeLastRuneInString(word)
	return (r == ':' || r == ',') && unicode.IsDigit(prev) && unicode.IsDigit(next)
}

// isAbbreviation reports a word the canonicaliser expands ("Mrs", "Dr", "St", "etc").
func isAbbreviation(w string) bool { return canon.IsAbbreviation(w) }

// isPronounI reports "I" and its contractions, capitalised everywhere and never names.
func isPronounI(w string) bool {
	return w == "I" || strings.HasPrefix(w, "I'") || strings.HasPrefix(w, "I’")
}

func isCapitalised(w string) bool {
	r, _ := utf8.DecodeRuneInString(w)
	return unicode.IsUpper(r) && !isPronounI(w) && !isAbbreviation(w)
}

// isRareEnglish reports a Latin-script word whose lower-case form, and every plain inflection
// stem of it, is absent from the frequency list.
func isRareEnglish(w string) bool {
	if isPronounI(w) || isAbbreviation(w) {
		return false
	}
	for _, r := range w {
		if !unicode.Is(unicode.Latin, r) && !isJoiner(r) {
			return false
		}
	}
	lw := strings.ToLower(w)
	for _, cand := range stems(lw) {
		if commonEnglish[cand] {
			return false
		}
	}
	return true
}

// stems returns w and the forms left by removing a possessive or a regular inflection.
func stems(w string) []string {
	w = strings.TrimSuffix(strings.TrimSuffix(w, "'s"), "’s")
	out := []string{w}
	if i := strings.IndexFunc(w, isApostrophe); i > 0 {
		out = append(out, w[:i]) // don't → don, we'll → we
	}
	for _, p := range strings.FieldsFunc(w, func(r rune) bool { return isHyphen(r) }) {
		if p != w {
			out = append(out, p) // each part of a compound
		}
	}
	for _, suf := range []string{"s", "es", "ed", "d", "ing", "ly", "er", "est"} {
		if b, ok := strings.CutSuffix(w, suf); ok && len(b) >= 2 {
			out = append(out, b, b+"e")
			if strings.HasSuffix(b, "i") {
				out = append(out, b[:len(b)-1]+"y") // studies → study
			}
			if n := len(b); n >= 2 && b[n-1] == b[n-2] {
				out = append(out, b[:n-1]) // stopped → stop
			}
		}
	}
	return out
}

// containsWord reports whether k occurs in s with no word character on either side.
func containsWord(s, k string) bool {
	for off := 0; ; {
		i := strings.Index(s[off:], k)
		if i < 0 {
			return false
		}
		i += off
		before, _ := utf8.DecodeLastRuneInString(s[:i])
		after, _ := utf8.DecodeRuneInString(s[i+len(k):])
		if (i == 0 || !isWordChar(before)) && (i+len(k) == len(s) || !isWordChar(after)) {
			return true
		}
		off = i + 1
	}
}
