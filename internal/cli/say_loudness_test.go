package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/exitcode"
)

func manifestLoudness(t *testing.T, path string) string {
	t.Helper()
	var man struct {
		Output struct {
			Loudness string `json:"loudness"`
		} `json:"output"`
	}
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &man) != nil {
		t.Fatalf("manifest %s: %v", path, err)
	}
	return man.Output.Loudness
}

// An encoded format defaults to podcast: ffmpeg first measures the assembly, then encodes it
// with loudnorm fed the measurement, both as argument vectors. The manifest records the preset.
func TestSayLoudnessTwoPasses(t *testing.T) {
	m := newMultiEnv(t)
	look, args := fakeEncoder(t)
	m.look = look
	for _, tc := range []struct {
		extra []string
		want  audio.Loudness
	}{{nil, audio.LoudnessPodcast}, {[]string{"--loudness", "audiobook"}, audio.LoudnessAudiobook}} {
		name := string(tc.want) + ".mp3"
		r := m.run(append([]string{threeParagraphs, "-o", name, "--gap-paragraph", "300ms"}, tc.extra...)...)
		if r.code != exitcode.OK {
			t.Fatalf("%s: %+v", tc.want, r)
		}
		if _, err := os.Stat(m.path(name)); err != nil {
			t.Fatal(err)
		}
		measure, err := os.ReadFile(os.Getenv("FAKE_FFMPEG_LOG") + ".measure")
		if err != nil {
			t.Fatalf("%s: no analysis pass: %v", tc.want, err)
		}
		if got := strings.Split(strings.TrimSuffix(string(measure), "\n"), "\n"); !slices.Equal(got, audio.MeasureArgs(tc.want)) {
			t.Fatalf("%s: analysis args %q", tc.want, got)
		}
		got := args()
		e := audio.Encoding{Format: audio.FormatMP3, Rate: audio.SampleRate, Tool: audio.ToolFFmpeg, Loudness: tc.want}
		want := audio.NormalizeArgs(e, got[len(got)-1], audio.Measurement{I: -22, TP: -6.5, LRA: 3.2, Thresh: -32.1, Offset: 0.4})
		if !slices.Equal(got, want) {
			t.Fatalf("%s: encode args\n got %q\nwant %q", tc.want, got, want)
		}
		if l := manifestLoudness(t, m.path(name+".json")); l != string(tc.want) {
			t.Fatalf("manifest loudness %q, want %s", l, tc.want)
		}
	}
}

// WAV defaults to none and is written natively; --loudness on WAV goes through ffmpeg, and
// without ffmpeg it is exit 2 naming it, before any network call.
func TestSayLoudnessWAV(t *testing.T) {
	m := newMultiEnv(t)
	m.look = noTools
	r := m.run(threeParagraphs, "-o", "plain.wav", "--gap-paragraph", "300ms")
	if r.code != exitcode.OK || manifestLoudness(t, m.path("plain.wav.json")) != "none" {
		t.Fatalf("%+v", r)
	}
	m2 := newMultiEnv(t)
	m2.look = noTools
	r = m2.run("Hi", "-o", "loud.wav", "--loudness", "podcast")
	if r.code != exitcode.Usage || !strings.Contains(r.stderr, "--loudness podcast needs ffmpeg") {
		t.Fatalf("%+v", r)
	}
	m2.untouched()
	if _, err := os.Stat(m2.path("loud.wav")); err == nil {
		t.Fatal("output written")
	}
	r = m2.run("Hi", "-o", "x.wav", "--loudness", "loud")
	if r.code != exitcode.Usage || !strings.Contains(r.stderr, `--loudness "loud"`) {
		t.Fatalf("%+v", r)
	}
	m2.untouched()
}

// --loudness none with -o - streams; a stream defaults to none whatever the format.
func TestSayStreamLoudnessNone(t *testing.T) {
	m := newMultiEnv(t)
	look, args := fakeEncoder(t)
	m.look = look
	for _, extra := range [][]string{{"--loudness", "none"}, nil} {
		r := m.run(append([]string{threeParagraphs, "-o", "-", "--format", "mp3"}, extra...)...)
		if r.code != exitcode.OK || slices.Contains(args(), "-af") {
			t.Fatalf("%q: %+v", extra, r.stderr)
		}
	}
	if _, err := os.Stat(os.Getenv("FAKE_FFMPEG_LOG") + ".measure"); err == nil {
		t.Fatal("a stream ran a loudness analysis")
	}
}

// Without ffmpeg, the m4a fallback to afconvert cannot normalise: the podcast default gives
// way to none with a warning, while an explicit preset still needs ffmpeg.
func TestLoudnessAfconvertFallback(t *testing.T) {
	var logs bytes.Buffer
	c := New(io.Discard, io.Discard)
	c.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	enc := audio.Encoding{Format: audio.FormatM4A, Rate: audio.SampleRate, Tool: audio.ToolAfconvert, Bin: "/x/afconvert"}
	if l := c.loudness(SayRequest{}, "x.m4a", enc); l != audio.LoudnessNone || !strings.Contains(logs.String(), "loudness not normalised") {
		t.Fatalf("default: %s, log %q", l, logs.String())
	}
	if l := c.loudness(SayRequest{Loudness: audio.LoudnessPodcast}, "x.m4a", enc); l != audio.LoudnessPodcast {
		t.Fatalf("explicit: %s", l)
	}
	if l := c.loudness(SayRequest{}, stdoutTarget, enc); l != audio.LoudnessNone {
		t.Fatalf("stream: %s", l)
	}
}
