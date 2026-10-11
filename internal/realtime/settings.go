package realtime

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Speech settings live here rather than in internal/cli because the valid
// values are facts about the realtime wire (the models, voices and efforts the
// session accepts); the CLI, config and profiles (M3) all validate through
// the same functions, and only map a failure to exit 2.

// Models lists the speech models recite accepts without --model-unchecked (SPK-02).
var Models = []string{"gpt-realtime-2.1", "gpt-realtime-2", "gpt-realtime-2.1-mini"}

// Voices lists the voices the speech session accepts (SPK-03, E1).
var Voices = []string{"alloy", "ash", "ballad", "coral", "echo", "sage", "shimmer", "verse", "marin", "cedar"}

// Defaults of the speech settings (design §12.2).
const (
	DefaultModel  = "gpt-realtime-2.1"
	DefaultVoice  = "cedar"
	DefaultSpeed  = 1.0
	DefaultEffort = Effort("minimal")
	MinSpeed      = 0.25
	MaxSpeed      = 1.5
)

// Settings are the validated speech settings of one output.
type Settings struct {
	Model               string
	Voice               string
	Speed               float64
	Effort              Effort
	InstructionsVersion int
	Emotion             string // a delivery preset of Emotions (design §7.9)
	Style               string // free-text delivery style, one line
}

// Delivery is the "Delivery:" paragraph of s, or "" (DeliveryParagraph).
func (s Settings) Delivery() string { return DeliveryParagraph(s.Emotion, s.Style) }

// DefaultSettings returns the settings with every default applied.
func DefaultSettings() Settings {
	return Settings{Model: DefaultModel, Voice: DefaultVoice, Speed: DefaultSpeed, Effort: DefaultEffort, InstructionsVersion: InstructionsVersion, Emotion: DefaultEmotion}
}

// SessionSpec is the session configuration for s, with its versioned rules as
// the session instructions.
func (s Settings) SessionSpec() (SessionSpec, error) {
	instr, err := Instructions(s.InstructionsVersion)
	if err != nil {
		return SessionSpec{}, err
	}
	return SessionSpec{Model: s.Model, Voice: s.Voice, Speed: s.Speed, Instructions: instr, Effort: s.Effort}, nil
}

// ParseModel validates a model name; unchecked admits any non-empty name.
func ParseModel(s string, unchecked bool) (string, error) {
	if s == "" {
		return "", fmt.Errorf("empty model")
	}
	if unchecked || slices.Contains(Models, s) {
		return s, nil
	}
	return "", fmt.Errorf("unknown model %q; valid models: %s (or pass --model-unchecked)", s, strings.Join(Models, ", "))
}

// ParseVoice validates a voice name.
func ParseVoice(s string) (string, error) {
	if slices.Contains(Voices, s) {
		return s, nil
	}
	return "", fmt.Errorf("unknown voice %q; valid voices: %s", s, strings.Join(Voices, ", "))
}

var speedSyntax = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,2})?$`)

// ParseSpeed validates a speed: 0.25 to 1.5 in steps of 0.01. It parses the
// decimal text rather than a float so 1.505 is rejected, not rounded.
func ParseSpeed(s string) (float64, error) {
	bad := fmt.Errorf("invalid speed %q; valid speeds: %.2f to %.2f in steps of 0.01", s, MinSpeed, MaxSpeed)
	if !speedSyntax.MatchString(s) {
		return 0, bad
	}
	whole, frac, _ := strings.Cut(s, ".")
	for len(frac) < 2 {
		frac += "0"
	}
	w, err := strconv.Atoi(whole)
	if err != nil || w > 100 {
		return 0, bad
	}
	f, _ := strconv.Atoi(frac) // two digits, guaranteed by speedSyntax
	hundredths := w*100 + f
	if hundredths < 25 || hundredths > 150 {
		return 0, bad
	}
	return float64(hundredths) / 100, nil
}

// ParseEffort validates a reasoning effort. The undocumented "none" is not exposed.
func ParseEffort(s string) (Effort, error) {
	if validEffort(Effort(s)) {
		return Effort(s), nil
	}
	names := make([]string, len(Efforts))
	for i, e := range Efforts {
		names[i] = string(e)
	}
	return "", fmt.Errorf("unknown effort %q; valid efforts: %s", s, strings.Join(names, ", "))
}

// ParseEffortLadder validates --retry-effort: a comma-separated list of efforts, one per
// attempt (SPK-11). "" means no ladder and returns nil.
func ParseEffortLadder(s string) ([]Effort, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]Effort, len(parts))
	for i, p := range parts {
		e, err := ParseEffort(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("--retry-effort %q, entry %d: %w", s, i+1, err)
		}
		out[i] = e
	}
	return out, nil
}

// EffectiveEffort is the effort of a 1-based attempt (SPK-11): ladder[attempt-1] when a ladder
// is set, its last entry repeating, otherwise base.
func EffectiveEffort(base Effort, ladder []Effort, attempt int) Effort {
	if len(ladder) == 0 {
		return base
	}
	return ladder[min(max(attempt, 1), len(ladder))-1]
}
