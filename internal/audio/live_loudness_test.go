//go:build live

package audio

import (
	"context"
	"encoding/binary"
	"math"
	"math/rand/v2"
	"os/exec"
	"path/filepath"
	"testing"
)

// speechLike is 12 s of syllable-rate modulated tones and noise with short pauses: a signal
// whose loudness varies like speech, far from either target, with peaks well above its average.
func speechLike(gain float64) PCM {
	n := 12 * SampleRate
	rng := rand.New(rand.NewPCG(1, 2))
	p := make(PCM, n*BytesPerSample)
	for i := range n {
		t := float64(i) / SampleRate
		env := math.Max(0, math.Sin(2*math.Pi*4*t)) // ~4 syllables per second
		if int(t)%3 == 2 && t-math.Floor(t) > 0.6 {
			env = 0 // a pause every three seconds
		}
		v := 0.6*math.Sin(2*math.Pi*180*t) + 0.25*math.Sin(2*math.Pi*720*t) + 0.15*(rng.Float64()*2-1)
		s := gain * env * v * 32767
		binary.LittleEndian.PutUint16(p[i*BytesPerSample:], uint16(int16(math.Max(-32768, math.Min(32767, s)))))
	}
	return p
}

// measured is the integrated loudness and true peak of the file at path, by loudnorm's analysis.
func measured(t *testing.T, ffmpeg, path string) Measurement {
	t.Helper()
	out, err := exec.Command(ffmpeg, "-hide_banner", "-nostdin", "-i", path,
		"-af", "loudnorm=print_format=json", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("measure %s: %v: %s", path, err, out)
	}
	m, err := ParseMeasurement(out)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Every encoded format, from a quiet and a loud source, meets the preset: integrated loudness
// within ±1 LU of the target and the true peak at or below the ceiling.
// accept: AUD-06#1
func TestLiveLoudnessTargets(t *testing.T) {
	ffmpeg, _ := liveTools(t)
	look := func(string) (string, error) { return ffmpeg, nil }
	dir := t.TempDir()
	for _, l := range []Loudness{LoudnessPodcast, LoudnessAudiobook} {
		target, _ := l.Target()
		for _, gain := range []float64{0.05, 0.9} {
			src := speechLike(gain)
			for _, f := range []Format{FormatWAV, FormatMP3, FormatM4A, FormatOpus, FormatFLAC} {
				for _, rate := range []int{SampleRate, 48000} {
					e, err := ChooseEncoding(f, rate, false, "", look)
					if err != nil {
						t.Fatal(err)
					}
					if e, err = WithLoudness(e, l, look); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(dir, "out"+f.Ext())
					if err := EncodeFile(context.Background(), e, src, path); err != nil {
						t.Fatalf("%s %s %d: %v", l, f, rate, err)
					}
					m := measured(t, ffmpeg, path)
					t.Logf("%s gain %.2f %s %d Hz: I %.2f LUFS, TP %.2f dBTP", l, gain, f, rate, m.I, m.TP)
					if math.Abs(m.I-target.I) > 1 || m.TP > target.TP {
						t.Errorf("%s gain %.2f %s %d Hz: I %.2f (want %.1f ±1), TP %.2f (want ≤ %.1f)",
							l, gain, f, rate, m.I, target.I, m.TP, target.TP)
					}
				}
			}
		}
	}
}
