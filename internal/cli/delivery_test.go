package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
)

// sentInstructions is the instructions field of the first response.create at the fake.
func sentInstructions(t *testing.T, e *sayEnv) string {
	t.Helper()
	var ev struct {
		Response struct {
			Instructions string `json:"instructions"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(sentText(t, e.srv)), &ev); err != nil {
		t.Fatal(err)
	}
	return ev.Response.Instructions
}

// The per-response instructions are INSTR v2 byte for byte, then one "Delivery:" paragraph
// (preset sentence, then the free-text style), then the chunk's emphasis line; the script
// inside the markers carries none of it.
// accept: SPK-07#1
func TestSayDeliveryInstructionsGolden(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	e := newSayEnv(t) // changes directory
	r := e.run("Read {{emph|this}} slowly.", "--marks", "--emotion", "whisper", "--style", "Like a secret.", "-o", "h.wav")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	goldenAt(t, filepath.Join(wd, "testdata", "say-delivery-request.golden"), sentText(t, e.srv)+"\n")
	want := realtime.InstructionsV2 + "\n\nDelivery: Whisper it softly and slowly. Like a secret.\n\nStress the word(s) \"this\"."
	if got := sentInstructions(t, e); got != want {
		t.Errorf("instructions\n got %q\nwant %q", got, want)
	}
	if got := sentScript(t, e); got != "<<<\nRead this slowly.\n>>>" {
		t.Errorf("script %q", got)
	}

	// Neutral with no style adds nothing: the instructions are the rules alone.
	e = newSayEnv(t)
	if r := e.run("Hello there.", "-o", "n.wav"); r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	if got := sentInstructions(t, e); got != realtime.InstructionsV2 {
		t.Errorf("neutral instructions %q", got)
	}
}

// Every preset maps to its §7.9 sentence; neutral and an empty style add nothing.
// accept: SPK-07#1
func TestDeliveryParagraph(t *testing.T) {
	want := map[string]string{
		"neutral":     "",
		"warm":        "Delivery: Deliver it warmly and kindly, with a gentle smile in the voice.",
		"calm":        "Delivery: Deliver it calmly and evenly, unhurried.",
		"excited":     "Delivery: Deliver it thrilled and energetic, with lively pace.",
		"serious":     "Delivery: Deliver it seriously and firmly, measured pace.",
		"sad":         "Delivery: Deliver it sadly, slow and heavy.",
		"whisper":     "Delivery: Whisper it softly and slowly.",
		"newsreader":  "Delivery: Deliver it like a calm, authoritative news presenter.",
		"storyteller": "Delivery: Deliver it like an engaging audiobook narrator, expressive but natural.",
	}
	if got := realtime.Emotions(); len(got) != len(want) {
		t.Fatalf("presets %v", got)
	}
	for _, name := range realtime.Emotions() {
		if got := realtime.DeliveryParagraph(name, ""); got != want[name] {
			t.Errorf("%s: %q, want %q", name, got, want[name])
		}
	}
	if got := realtime.DeliveryParagraph("neutral", " Brisk. "); got != "Delivery: Brisk." {
		t.Errorf("style only: %q", got)
	}
}

// An unknown preset is a usage error naming every preset, from a flag or from a config file,
// before any network call; a multi-line style is refused the same way.
// accept: SPK-07#2
func TestUnknownEmotionExit2(t *testing.T) {
	for _, args := range [][]string{
		{"say", "Hi", "--emotion", "angry"},
		{"say", "Hi", "--config", writeConfig(t, "emotion = \"angry\"\n")},
	} {
		r := run(args...)
		if r.code != ExitUsage || len(r.says) != 0 {
			t.Fatalf("%v: %+v", args, r)
		}
		if !strings.Contains(r.stderr, `"angry"`) || !strings.Contains(r.stderr, strings.Join(realtime.Emotions(), ", ")) {
			t.Errorf("%v: stderr lacks the preset list: %q", args, r.stderr)
		}
	}
	if r := run("say", "Hi", "--style", "a\nb"); r.code != ExitUsage || !strings.Contains(r.stderr, "single line") {
		t.Errorf("multi-line style: %+v", r)
	}
}

// Emotion and style come from flags, config, and a profile; the preset's identity and version
// and the style text are part of the cache key, and neutral leaves the key as it was.
func TestDeliverySourcesAndCacheKey(t *testing.T) {
	cfg := writeConfig(t, "emotion = \"calm\"\n[profile.narrator]\nemotion = \"storyteller\"\nstyle = \"Unhurried, warm.\"\n")
	r := run("say", "Hi", "--config", cfg)
	if r.code != ExitOK || r.says[0].Settings.Emotion != "calm" || r.says[0].Settings.Style != "" {
		t.Fatalf("config: %+v", r)
	}
	r = run("say", "Hi", "--config", cfg, "--profile", "narrator")
	if r.code != ExitOK || r.says[0].Settings.Emotion != "storyteller" || r.says[0].Settings.Style != "Unhurried, warm." {
		t.Fatalf("profile: %+v", r)
	}
	r = run("say", "Hi", "--config", cfg, "--profile", "narrator", "--emotion", "sad")
	if r.code != ExitOK || r.says[0].Settings.Emotion != "sad" {
		t.Fatalf("flag: %+v", r)
	}

	base := realtime.DefaultSettings()
	key := func(s realtime.Settings) string { return pipeline.CacheKey(planSettings(s), nil, "Hi.") }
	plain := pipeline.CacheKey(pipeline.Settings{Model: base.Model, Voice: base.Voice, Speed: base.Speed,
		Effort: string(base.Effort), InstructionsVersion: base.InstructionsVersion}, nil, "Hi.")
	if key(base) != plain {
		t.Error("neutral changed the cache key")
	}
	warm, styled := base, base
	warm.Emotion, styled.Style = "warm", "Brisk."
	if key(warm) == plain || key(styled) == plain || key(warm) == key(styled) {
		t.Error("emotion or style is not in the cache key")
	}
	if got := planSettings(warm).Emotion; got != "warm@1" {
		t.Errorf("preset cache identity %q, want name@version", got)
	}
}

// {{emph|words}} adds the stress line to that chunk's instructions only; the words inside the
// mark reach the model unchanged.
// accept: SPK-12#1
func TestEmphasisInstructionTextUnchanged(t *testing.T) {
	e := newSayEnv(t)
	if r := e.run("Open {{emph|the red door}} now.", "--marks", "-o", "e.wav"); r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	if got := sentScript(t, e); got != "<<<\nOpen the red door now.\n>>>" {
		t.Errorf("script %q", got)
	}
	want := realtime.InstructionsV2 + "\n\nStress the word(s) \"the red door\"."
	if got := sentInstructions(t, e); got != want {
		t.Errorf("instructions\n got %q\nwant %q", got, want)
	}
}
