package audio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// Loudness is a --loudness preset (AUD-06, design §9).
type Loudness string

// Loudness presets.
const (
	LoudnessNone      Loudness = "none"
	LoudnessPodcast   Loudness = "podcast"
	LoudnessAudiobook Loudness = "audiobook"
)

// Loudnesses lists the presets --loudness accepts.
var Loudnesses = []Loudness{LoudnessPodcast, LoudnessAudiobook, LoudnessNone}

// LoudnessTarget is a preset's integrated loudness (LUFS) and true-peak ceiling (dBTP).
type LoudnessTarget struct {
	I, TP float64
}

// loudnessRange is loudnorm's LRA target, ffmpeg's default: design §9 sets none, and speech
// rarely exceeds it, so the second pass can stay linear.
const loudnessRange = 11.0

// Target is the preset's target; ok is false for none.
func (l Loudness) Target() (t LoudnessTarget, ok bool) {
	switch l {
	case LoudnessPodcast:
		return LoudnessTarget{I: -16, TP: -1.5}, true
	case LoudnessAudiobook:
		return LoudnessTarget{I: -20, TP: -3}, true
	}
	return LoudnessTarget{}, false
}

// ParseLoudness parses a --loudness value.
func ParseLoudness(s string) (Loudness, error) {
	l := Loudness(strings.ToLower(s))
	for _, x := range Loudnesses {
		if l == x {
			return l, nil
		}
	}
	return "", encoderErrorf("--loudness %q: want one of podcast, audiobook, none", s)
}

// DefaultLoudness is the preset used when --loudness is not given: none for wav and pcm,
// podcast for every encoded format (design §9).
func DefaultLoudness(f Format) Loudness {
	if f == FormatWAV || f == FormatPCM {
		return LoudnessNone
	}
	return LoudnessPodcast
}

// WithLoudness sets e's loudness preset. Normalisation needs ffmpeg, so a native or afconvert
// encoding switches to ffmpeg, and without ffmpeg it is an encoder error (exit 2, before any
// network call).
func WithLoudness(e Encoding, l Loudness, look func(string) (string, error)) (Encoding, error) {
	e.Loudness = l
	if _, ok := l.Target(); !ok || e.Tool == ToolFFmpeg {
		return e, nil
	}
	bin, err := look(ToolFFmpeg)
	if err != nil {
		return e, encoderErrorf("--loudness %s needs ffmpeg, which is not on PATH; install ffmpeg (e.g. `brew install ffmpeg` or your package manager), or pass --loudness none", l)
	}
	e.Tool, e.Bin = ToolFFmpeg, bin
	return e, nil
}

// Measurement is loudnorm's first-pass analysis of the input.
type Measurement struct {
	I, TP, LRA, Thresh, Offset float64
}

// loudnormMinI is the lowest measured_I loudnorm accepts; quieter input (silence measures
// -inf) carries nothing to normalise.
const loudnormMinI = -99

// Silent reports whether the input is too quiet to normalise.
func (m Measurement) Silent() bool { return m.I < loudnormMinI }

// MeasureArgs is the ffmpeg argument vector of the first pass: it reads internal PCM on stdin
// and prints loudnorm's JSON analysis to stderr. The log level must be info for the JSON.
func MeasureArgs(l Loudness) []string {
	return measureArgs(l, "-f", "s16le", "-ar", strconv.Itoa(SampleRate), "-ac", strconv.Itoa(Channels), "-i", "pipe:0")
}

// MeasureFileArgs is [MeasureArgs] for the encoded file at path: the check that the encoder
// kept the true peak under the ceiling.
func MeasureFileArgs(l Loudness, path string) []string {
	return measureArgs(l, "-i", ffmpegPath(path))
}

func measureArgs(l Loudness, input ...string) []string {
	t, _ := l.Target()
	args := append([]string{"-hide_banner", "-loglevel", "info", "-nostdin"}, input...)
	return append(args, "-af", fmt.Sprintf("loudnorm=I=%s:TP=%s:LRA=%s:print_format=json", num(t.I), num(t.TP), num(loudnessRange)),
		"-f", "null", "-")
}

// normalizeFilter is the second-pass filter toward t: the first pass's measurement lets
// loudnorm apply one linear gain when the true-peak ceiling allows it.
func normalizeFilter(t LoudnessTarget, m Measurement) string {
	return fmt.Sprintf("loudnorm=I=%s:TP=%s:LRA=%s:measured_I=%s:measured_TP=%s:measured_LRA=%s:measured_thresh=%s:offset=%s:linear=true:print_format=none",
		num(t.I), num(t.TP), num(loudnessRange), num(m.I), num(m.TP), num(m.LRA), num(m.Thresh), num(m.Offset))
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

// ParseMeasurement reads the JSON block loudnorm prints at the end of ffmpeg's stderr.
func ParseMeasurement(stderr []byte) (Measurement, error) {
	start, end := bytes.LastIndexByte(stderr, '{'), bytes.LastIndexByte(stderr, '}')
	if start < 0 || end < start {
		return Measurement{}, fmt.Errorf("audio: loudnorm printed no measurement")
	}
	var raw struct {
		I      string `json:"input_i"`
		TP     string `json:"input_tp"`
		LRA    string `json:"input_lra"`
		Thresh string `json:"input_thresh"`
		Offset string `json:"target_offset"`
	}
	if err := json.Unmarshal(stderr[start:end+1], &raw); err != nil {
		return Measurement{}, fmt.Errorf("audio: loudnorm measurement: %w", err)
	}
	var m Measurement
	for _, f := range []struct {
		name, s string
		dst     *float64
	}{{"input_i", raw.I, &m.I}, {"input_tp", raw.TP, &m.TP}, {"input_lra", raw.LRA, &m.LRA},
		{"input_thresh", raw.Thresh, &m.Thresh}, {"target_offset", raw.Offset, &m.Offset}} {
		v, err := strconv.ParseFloat(strings.TrimSpace(f.s), 64)
		if err != nil {
			return Measurement{}, fmt.Errorf("audio: loudnorm measurement %s %q: %w", f.name, f.s, err)
		}
		*f.dst = v
	}
	if math.IsNaN(m.I) || math.IsNaN(m.TP) {
		return Measurement{}, fmt.Errorf("audio: loudnorm measurement is not a number")
	}
	return m, nil
}

// maxMeasureStderr bounds the first pass's stderr; the JSON is at its end, so the tail is kept.
const maxMeasureStderr = 64 << 10

// tailBuffer keeps the last maxMeasureStderr bytes written to it.
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if over := len(t.b) - maxMeasureStderr; over > 0 {
		t.b = append(t.b[:0:0], t.b[over:]...)
	}
	return len(p), nil
}

// measure runs the first pass over p.
func measure(ctx context.Context, e Encoding, p PCM) (Measurement, error) {
	return runMeasure(ctx, e.Bin, MeasureArgs(e.Loudness), bytes.NewReader(p))
}

// measureFile analyses the encoded file at path.
func measureFile(ctx context.Context, e Encoding, path string) (Measurement, error) {
	return runMeasure(ctx, e.Bin, MeasureFileArgs(e.Loudness, path), nil)
}

func runMeasure(ctx context.Context, bin string, args []string, stdin io.Reader) (Measurement, error) {
	var stderr tailBuffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin, cmd.Stderr = stdin, &stderr
	if err := cmd.Run(); err != nil {
		tail := stderr.b[max(0, len(stderr.b)-maxToolStderr):]
		return Measurement{}, fmt.Errorf("audio: ffmpeg loudness analysis failed: %w: %s", err, strings.TrimSpace(string(tail)))
	}
	return ParseMeasurement(stderr.b)
}
