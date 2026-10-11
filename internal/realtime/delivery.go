package realtime

import (
	"fmt"
	"strconv"
	"strings"
)

// EmotionVersion versions the preset sentences below (design §7.9). Changing any sentence
// needs a new version, because the version is part of every cache key.
const EmotionVersion = 1

// DefaultEmotion is the preset that adds no delivery sentence.
const DefaultEmotion = "neutral"

// emotionSentences are the tested delivery sentences of the presets (design §7.9, E24), in
// display order. neutral adds nothing.
var emotionSentences = []struct{ name, sentence string }{
	{"neutral", ""},
	{"warm", "Deliver it warmly and kindly, with a gentle smile in the voice."},
	{"calm", "Deliver it calmly and evenly, unhurried."},
	{"excited", "Deliver it thrilled and energetic, with lively pace."},
	{"serious", "Deliver it seriously and firmly, measured pace."},
	{"sad", "Deliver it sadly, slow and heavy."},
	{"whisper", "Whisper it softly and slowly."},
	{"newsreader", "Deliver it like a calm, authoritative news presenter."},
	{"storyteller", "Deliver it like an engaging audiobook narrator, expressive but natural."},
}

// Emotions lists the preset names in display order.
func Emotions() []string {
	out := make([]string, len(emotionSentences))
	for i, e := range emotionSentences {
		out[i] = e.name
	}
	return out
}

// ParseEmotion validates a preset name.
func ParseEmotion(s string) (string, error) {
	for _, e := range emotionSentences {
		if e.name == s {
			return s, nil
		}
	}
	return "", fmt.Errorf("unknown emotion %q; valid presets: %s", s, strings.Join(Emotions(), ", "))
}

// ParseStyle validates a free-text delivery style: one line, so it cannot open a paragraph of
// its own after the rules.
func ParseStyle(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n") {
		return "", fmt.Errorf("style %q: want a single line", s)
	}
	return strings.TrimSpace(s), nil
}

// EmotionCacheID is the preset's identity in the cache key: name@version. neutral is ""
// because it adds nothing to the instructions, so neutral takes keep the keys they had before
// presets existed.
func EmotionCacheID(emotion string) string {
	if emotion == "" || emotion == DefaultEmotion {
		return ""
	}
	return emotion + "@" + strconv.Itoa(EmotionVersion)
}

// EmphasisInstruction is the instruction line of an {{emph|words}} mark (SPK-12): the words
// stay in the script unchanged, and this line asks for the stress.
func EmphasisInstruction(words string) string {
	return `Stress the word(s) "` + strings.TrimSpace(words) + `".`
}

// DeliveryParagraph is the "Delivery:" paragraph for a preset and a free-text style, or ""
// when neither adds anything. It goes after the rules (ResponseInstructions), never inside
// the markers.
func DeliveryParagraph(emotion, style string) string {
	var parts []string
	for _, e := range emotionSentences {
		if e.name == emotion && e.sentence != "" {
			parts = append(parts, e.sentence)
		}
	}
	if s := strings.TrimSpace(style); s != "" {
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return ""
	}
	return "Delivery: " + strings.Join(parts, " ")
}
