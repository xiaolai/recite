package text

import (
	"reflect"
	"testing"
)

// accept: TXT-04#1
// accept: TXT-04#3
func TestResolveTable(t *testing.T) {
	cases := []struct {
		name, text string
		script     Script
		code       string
		src        LanguageSource
		certain    bool
		hint       []string
		metric     Metric
	}{
		{"english", "The quick brown fox jumps.", ScriptLatin, "en", SourceDefaultLat, false, nil, MetricWER},
		{"french", "Bonjour, comment allez-vous ?", ScriptLatin, "en", SourceDefaultLat, false, nil, MetricWER},
		{"spanish", "¿Dónde está la biblioteca?", ScriptLatin, "en", SourceDefaultLat, false, nil, MetricWER},
		{"chinese", "我们明天去北京。", ScriptHan, "zh", SourceDefaultHan, false, nil, MetricCER},
		// Four Han and four Hiragana runes: the tie goes to Han; Kana still makes it ja.
		{"japanese", "東京都に行きます", ScriptHan, "ja", SourceScript, true, []string{"ja"}, MetricCER},
		{"japanese kanji only", "東京都", ScriptHan, "zh", SourceDefaultHan, false, nil, MetricCER},
		{"katakana", "コーヒーショップ", ScriptKatakana, "ja", SourceScript, true, []string{"ja"}, MetricCER},
		{"korean", "안녕하세요, 반갑습니다.", ScriptHangul, "ko", SourceScript, true, []string{"ko"}, MetricCER},
		{"russian", "Привет, как дела?", ScriptCyrillic, "", SourceUnknown, false, nil, MetricWER},
		{"mixed latin+han", "The word 東京 means eastern capital", ScriptLatin, "en", SourceDefaultLat, false, nil, MetricWER},
		// Script is by rune count (design §7.3), language by spoken weight: Latin
		// words against Han runes, so a Latin brand name does not make Chinese English.
		{"latin brand in chinese", "iPhone 17 Pro的价格是¥8,999", ScriptLatin, "zh", SourceDefaultHan, false, nil, MetricWER},
		{"latin product in chinese", "我用MacBook Pro写代码", ScriptLatin, "zh", SourceDefaultHan, false, nil, MetricWER},
		{"han term in english", "Use 微信 to pay the bill", ScriptLatin, "en", SourceDefaultLat, false, nil, MetricWER},
		{"one word each", "iPhone好", ScriptLatin, "zh", SourceDefaultHan, false, nil, MetricWER},
		{"mixed latin+kana", "Say こんにちは to everyone here", ScriptLatin, "ja", SourceScript, true, []string{"ja"}, MetricWER},
		{"digits only", "123 456", ScriptUnknown, "", SourceUnknown, false, nil, MetricWER},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info := DetectScript(c.text)
			if info.Script != c.script {
				t.Fatalf("script = %q, want %q", info.Script, c.script)
			}
			l := Resolve(c.text, ResolveOptions{})
			want := Language{Code: c.code, Source: c.src, Certain: c.certain}
			if l != want {
				t.Fatalf("language = %+v, want %+v", l, want)
			}
			if h := STTHint(l); !reflect.DeepEqual(h, c.hint) {
				t.Fatalf("hint = %v, want %v", h, c.hint)
			}
			if m := Scorer(info.Script); m != c.metric {
				t.Fatalf("scorer = %q, want %q", m, c.metric)
			}
		})
	}
}

// accept: TXT-04#2
func TestFrenchWithoutLangIsUncertain(t *testing.T) {
	l := Resolve("Bonjour, comment allez-vous ?", ResolveOptions{})
	if l.Code != "en" || l.Certain || STTHint(l) != nil {
		t.Fatalf("got %+v hint %v, want en, not certain, no hint", l, STTHint(l))
	}
	l = Resolve("Bonjour, comment allez-vous ?", ResolveOptions{Flag: "fr"})
	if l != (Language{Code: "fr", Source: SourceFlag, Certain: true}) || !reflect.DeepEqual(STTHint(l), []string{"fr"}) {
		t.Fatalf("with --lang fr: got %+v", l)
	}
}

func TestResolvePrecedenceAndDefaults(t *testing.T) {
	l := Resolve("東京都", ResolveOptions{Mark: "ja", Flag: "zh", FrontMatter: "ko"})
	if l != (Language{Code: "ja", Source: SourceMark, Certain: true}) {
		t.Fatalf("mark: %+v", l)
	}
	l = Resolve("x", ResolveOptions{FrontMatter: "de"})
	if l != (Language{Code: "de", Source: SourceFrontMatter, Certain: true}) {
		t.Fatalf("frontmatter: %+v", l)
	}
	l = Resolve("hello", ResolveOptions{DefaultLatinLang: "fr"})
	if l != (Language{Code: "fr", Source: SourceDefaultLat}) {
		t.Fatalf("latin default: %+v", l)
	}
	l = Resolve("東京都", ResolveOptions{DefaultHanLang: "ja"})
	if l != (Language{Code: "ja", Source: SourceDefaultHan}) {
		t.Fatalf("han default: %+v", l)
	}
}

func TestScriptConfidenceAndRates(t *testing.T) {
	info := DetectScript("abc 東")
	if info.Script != ScriptLatin || info.Confidence != 0.75 {
		t.Fatalf("got %+v", info)
	}
	if SpeechRate(ScriptLatin) != 14 || SpeechRate(ScriptHan) != 4.3 || SpeechRate("Ogham") != DefaultSpeechRate {
		t.Fatal("speech rate table")
	}
	if Scorer(ScriptThai) != MetricCER || Scorer(ScriptCyrillic) != MetricWER {
		t.Fatal("scorer")
	}
}
