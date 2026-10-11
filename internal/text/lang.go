package text

import (
	"sort"
	"strings"
	"unicode"
)

// Script is a Unicode script name as used by package unicode ("Latin", "Han", ...).
// The empty Script means no letter was found.
type Script string

// Scripts with a role in rates, scorers or language resolution.
const (
	ScriptUnknown    Script = ""
	ScriptLatin      Script = "Latin"
	ScriptHan        Script = "Han"
	ScriptHiragana   Script = "Hiragana"
	ScriptKatakana   Script = "Katakana"
	ScriptHangul     Script = "Hangul"
	ScriptThai       Script = "Thai"
	ScriptCyrillic   Script = "Cyrillic"
	ScriptGreek      Script = "Greek"
	ScriptArabic     Script = "Arabic"
	ScriptHebrew     Script = "Hebrew"
	ScriptDevanagari Script = "Devanagari"
)

// scriptOrder fixes the counting order, and with it the tie-break between
// scripts of equal count, so detection never depends on map iteration.
var scriptOrder = func() []string {
	first := []string{"Latin", "Han", "Hiragana", "Katakana", "Hangul", "Thai", "Cyrillic",
		"Greek", "Arabic", "Hebrew", "Devanagari"}
	seen := map[string]bool{"Common": true, "Inherited": true}
	for _, s := range first {
		seen[s] = true
	}
	var rest []string
	for name := range unicode.Scripts {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(first, rest...)
}()

// ScriptCounts is the number of letter runes per script; Common and Inherited
// runes (digits, punctuation, marks) are not counted.
type ScriptCounts map[Script]int

// ScriptInfo is the dominant script of a text with its share of counted runes.
type ScriptInfo struct {
	Script     Script
	Confidence float64 // dominant count / all counted runes; 0 when nothing was counted
	Counts     ScriptCounts
}

// DetectScript counts letter runes per Unicode script (TXT-04). Ties go to
// the script earlier in scriptOrder (Latin, Han, Hiragana, ...).
func DetectScript(s string) ScriptInfo {
	counts := ScriptCounts{}
	total := 0
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		for _, name := range scriptOrder {
			if unicode.Is(unicode.Scripts[name], r) {
				counts[Script(name)]++
				total++
				break
			}
		}
	}
	info := ScriptInfo{Counts: counts}
	best := 0
	for _, name := range scriptOrder {
		if c := counts[Script(name)]; c > best {
			best, info.Script = c, Script(name)
		}
	}
	if total > 0 {
		info.Confidence = float64(best) / float64(total)
	}
	return info
}

// LanguageSource records how a language was decided.
type LanguageSource string

// Language sources, in resolution order (design §7.3).
const (
	SourceMark        LanguageSource = "mark"
	SourceFlag        LanguageSource = "flag"
	SourceFrontMatter LanguageSource = "frontmatter"
	SourceScript      LanguageSource = "script"
	SourceDefaultLat  LanguageSource = "default-latin"
	SourceDefaultHan  LanguageSource = "default-han"
	SourceUnknown     LanguageSource = "unknown"
)

// Language is a resolved chunk language. Only a Certain language sends the
// STT languages[] hint and arms the language guard (R#16).
type Language struct {
	Code    string // BCP 47-style code such as "en"; empty when unknown
	Source  LanguageSource
	Certain bool
}

// Default languages for uncertain script-based resolution.
const (
	DefaultLatinLang = "en"
	DefaultHanLang   = "zh"
)

// ResolveOptions carries the explicit language sources and the defaults.
// Empty strings mean "not given"; empty defaults fall back to en / zh.
type ResolveOptions struct {
	Mark             string // {{lang}} mark covering the chunk
	Flag             string // --lang
	FrontMatter      string // front-matter language
	DefaultLatinLang string
	DefaultHanLang   string
}

// Resolve decides the language of text per TXT-04 / design §7.3.
func Resolve(text string, opts ResolveOptions) Language {
	for _, c := range []struct {
		code string
		src  LanguageSource
	}{{opts.Mark, SourceMark}, {opts.Flag, SourceFlag}, {opts.FrontMatter, SourceFrontMatter}} {
		if code := strings.TrimSpace(c.code); code != "" {
			return Language{Code: code, Source: c.src, Certain: true}
		}
	}
	info := DetectScript(text)
	dom := languageScript(text, info)
	switch {
	case info.Counts[ScriptHiragana]+info.Counts[ScriptKatakana] > 0:
		return Language{Code: "ja", Source: SourceScript, Certain: true}
	case info.Counts[ScriptHangul] > 0:
		return Language{Code: "ko", Source: SourceScript, Certain: true}
	case dom == ScriptHan:
		// Not certain: kanji-only Japanese exists.
		return Language{Code: orDefault(opts.DefaultHanLang, DefaultHanLang), Source: SourceDefaultHan}
	case dom == ScriptLatin:
		// Not certain: script cannot tell English from French.
		return Language{Code: orDefault(opts.DefaultLatinLang, DefaultLatinLang), Source: SourceDefaultLat}
	}
	return Language{Source: SourceUnknown}
}

// languageScript decides between Han and Latin for text that mixes them by
// spoken weight, not rune count: a Han rune is about one syllable, while a
// Latin rune is a fraction of a word. "iPhone 17 Pro的价格是" has nine Latin
// runes but two Latin words against four Han runes, and is Chinese. Han wins a
// tie. Text without both scripts keeps its dominant script.
func languageScript(text string, info ScriptInfo) Script {
	han, latin := info.Counts[ScriptHan], info.Counts[ScriptLatin]
	if han == 0 || latin == 0 || info.Script != ScriptHan && info.Script != ScriptLatin {
		return info.Script
	}
	if han >= latinWords(text) {
		return ScriptHan
	}
	return ScriptLatin
}

// latinWords counts maximal runs of Latin letters.
func latinWords(text string) int {
	n, in := 0, false
	for _, r := range text {
		l := unicode.IsLetter(r) && unicode.Is(unicode.Latin, r)
		if l && !in {
			n++
		}
		in = l
	}
	return n
}

func orDefault(v, d string) string {
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return d
}

// STTHint returns the STT languages[] hint: the code when certain, else nil.
func STTHint(l Language) []string {
	if !l.Certain || l.Code == "" {
		return nil
	}
	return []string{l.Code}
}

// Metric is the verification error rate used for a script.
type Metric string

// Error-rate metrics (design §8.4).
const (
	MetricWER Metric = "wer"
	MetricCER Metric = "cer"
)

// Scorer returns CER for scripts written without spaces between words, WER otherwise.
func Scorer(s Script) Metric {
	switch s {
	case ScriptHan, ScriptHiragana, ScriptKatakana, ScriptHangul, ScriptThai:
		return MetricCER
	}
	return MetricWER
}

// speechRates is characters per second at speed 1.0 (design §7.2). Latin and
// Han are measured; the rest are provisional estimates from typical syllable
// rates and characters per syllable, to be refined by measurement.
var speechRates = map[Script]float64{
	ScriptLatin:      14,  // measured
	ScriptHan:        4.3, // measured
	ScriptHiragana:   7.5, // ~7-8 morae/s, one kana per mora
	ScriptKatakana:   7.5,
	ScriptHangul:     6, // ~one syllable block per syllable
	ScriptThai:       10,
	ScriptCyrillic:   13, // alphabetic, similar to Latin with longer words
	ScriptGreek:      13,
	ScriptArabic:     11, // abjad: fewer written vowels per second of speech
	ScriptHebrew:     11,
	ScriptDevanagari: 11,
}

// DefaultSpeechRate applies to scripts without a table entry.
const DefaultSpeechRate = 12.0

// SpeechRate returns the estimated characters per second for a script.
func SpeechRate(s Script) float64 {
	if r, ok := speechRates[s]; ok {
		return r
	}
	return DefaultSpeechRate
}
