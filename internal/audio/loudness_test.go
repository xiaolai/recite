package audio

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/exitcode"
)

func TestParseLoudness(t *testing.T) {
	for _, s := range []string{"podcast", "Audiobook", "none"} {
		if _, err := ParseLoudness(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	_, err := ParseLoudness("loud")
	var ee *EncoderError
	if !errors.As(err, &ee) || ee.ExitCode() != exitcode.Usage {
		t.Fatalf("loud: %v, want a usage error", err)
	}
}

func TestLoudnessTargets(t *testing.T) {
	for l, want := range map[Loudness]LoudnessTarget{LoudnessPodcast: {-16, -1.5}, LoudnessAudiobook: {-20, -3}} {
		if got, ok := l.Target(); !ok || got != want {
			t.Errorf("%s: %+v %v, want %+v", l, got, ok, want)
		}
	}
	if _, ok := LoudnessNone.Target(); ok {
		t.Error("none has a target")
	}
}

func TestDefaultLoudness(t *testing.T) {
	for _, f := range Formats {
		want := LoudnessPodcast
		if f == FormatWAV || f == FormatPCM {
			want = LoudnessNone
		}
		if got := DefaultLoudness(f); got != want {
			t.Errorf("%s: %s, want %s", f, got, want)
		}
	}
}

func TestWithLoudness(t *testing.T) {
	native := Encoding{Format: FormatWAV, Rate: SampleRate}
	e, err := WithLoudness(native, LoudnessNone, noTool)
	if err != nil || !e.Native() {
		t.Fatalf("none: %+v %v", e, err)
	}
	// A normalised WAV goes through ffmpeg; without it, exit 2 naming ffmpeg.
	e, err = WithLoudness(native, LoudnessPodcast, func(string) (string, error) { return "/x/ffmpeg", nil })
	if err != nil || e.Tool != ToolFFmpeg || e.Bin != "/x/ffmpeg" || e.Loudness != LoudnessPodcast {
		t.Fatalf("podcast: %+v %v", e, err)
	}
	_, err = WithLoudness(Encoding{Format: FormatM4A, Rate: SampleRate, Tool: ToolAfconvert, Bin: "/x/afconvert"}, LoudnessAudiobook, noTool)
	var ee *EncoderError
	if !errors.As(err, &ee) || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("afconvert: %v, want an encoder error naming ffmpeg", err)
	}
}

func noTool(string) (string, error) { return "", exec.ErrNotFound }

// The JSON block loudnorm prints at the end of a first pass, as ffmpeg 8 writes it.
const loudnormLog = `size=N/A time=00:00:03.00 bitrate=N/A speed=41.8x
[Parsed_loudnorm_0 @ 0x7bcc818c00]
{
	"input_i" : "-21.75",
	"input_tp" : "-18.06",
	"input_lra" : "0.00",
	"input_thresh" : "-31.75",
	"output_i" : "-15.95",
	"output_tp" : "-12.27",
	"output_lra" : "0.00",
	"output_thresh" : "-25.95",
	"normalization_type" : "dynamic",
	"target_offset" : "-0.05"
}
[out#0/null @ 0x7bcc818480] video:0KiB audio:1125KiB
`

func TestParseMeasurement(t *testing.T) {
	m, err := ParseMeasurement([]byte(loudnormLog))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Measurement{I: -21.75, TP: -18.06, LRA: 0, Thresh: -31.75, Offset: -0.05}); m != want || m.Silent() {
		t.Fatalf("%+v, want %+v", m, want)
	}
	silent := strings.NewReplacer(`"-21.75"`, `"-inf"`, `"-18.06"`, `"-inf"`, `"-31.75"`, `"-70.00"`, `"-0.05"`, `"inf"`).Replace(loudnormLog)
	if m, err := ParseMeasurement([]byte(silent)); err != nil || !m.Silent() {
		t.Fatalf("silence: %+v %v, want silent", m, err)
	}
	for _, bad := range []string{"no json here", `{"input_i":"x"}`, `{"input_i":"-1"}`} {
		if _, err := ParseMeasurement([]byte(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// The first pass prints at info level (loudnorm's JSON); the second applies the measurement
// linearly and always sets the output rate, since loudnorm runs at 192 kHz.
func TestLoudnessArgs(t *testing.T) {
	a := MeasureArgs(LoudnessAudiobook)
	if !slices.Contains(a, "loudnorm=I=-20.00:TP=-3.00:LRA=11.00:print_format=json") ||
		!slices.Contains(a, "info") || a[len(a)-2] != "null" || a[len(a)-1] != "-" {
		t.Fatalf("measure args %q", a)
	}
	e := Encoding{Format: FormatMP3, Rate: SampleRate, Tool: ToolFFmpeg, Loudness: LoudnessPodcast}
	got := NormalizeArgs(e, "out.mp3", Measurement{I: -21.75, TP: -18.06, LRA: 0, Thresh: -31.75, Offset: -0.05})
	want := "loudnorm=I=-16.00:TP=-1.50:LRA=11.00:measured_I=-21.75:measured_TP=-18.06:measured_LRA=0.00:" +
		"measured_thresh=-31.75:offset=-0.05:linear=true:print_format=none"
	i := slices.Index(got, "-af")
	if i < 0 || got[i+1] != want || got[i+2] != "-ar" || got[i+3] != "24000" || got[len(got)-1] != "file:out.mp3" {
		t.Fatalf("normalize args %q", got)
	}
	if slices.Contains(FFmpegArgs(e, "out.mp3"), "-af") {
		t.Fatal("FFmpegArgs applies a filter")
	}
}

func TestStreamRefusesLoudness(t *testing.T) {
	e := Encoding{Format: FormatMP3, Rate: SampleRate, Tool: ToolFFmpeg, Bin: "/nonexistent/ffmpeg", Loudness: LoudnessPodcast}
	var ee *EncoderError
	if _, err := NewStreamWriter(t.Context(), e, nil); !errors.As(err, &ee) {
		t.Fatalf("%v, want an encoder error", err)
	}
}
