//go:build live

package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// sine is d seconds of a 440 Hz sine at half scale: a known signal whose length is exact.
func sine(seconds float64) PCM {
	n := int(seconds * SampleRate)
	p := make(PCM, n*BytesPerSample)
	for i := range n {
		v := int16(16384 * math.Sin(2*math.Pi*440*float64(i)/SampleRate))
		binary.LittleEndian.PutUint16(p[i*BytesPerSample:], uint16(v))
	}
	return p
}

// liveTools finds ffmpeg and ffprobe, or skips: these tests need the tools, not the network.
func liveTools(t *testing.T) (ffmpeg, ffprobe string) {
	t.Helper()
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg and ffprobe are not both installed")
	}
	return ffmpeg, ffprobe
}

// probe returns the duration in seconds and the first audio stream's sample rate.
func probe(t *testing.T, ffprobe string, args ...string) (float64, int, []byte) {
	t.Helper()
	cmd := exec.Command(ffprobe, append([]string{"-v", "error", "-select_streams", "a:0",
		"-show_entries", "format=duration:stream=sample_rate", "-of", "default=nw=1"}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffprobe %q: %v", args, err)
	}
	var dur float64
	var rate int
	for _, line := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "duration":
			dur, _ = strconv.ParseFloat(v, 64)
		case "sample_rate":
			rate, _ = strconv.Atoi(v)
		}
	}
	return dur, rate, out
}

// streamInfo is what ffprobe reports for the first audio stream and the container.
func streamInfo(t *testing.T, ffprobe, path string) (codec, container string, rate int) {
	t.Helper()
	out, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "a:0",
		"-show_entries", "format=format_name:stream=codec_name,sample_rate", "-of", "default=nw=1", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "codec_name":
			codec = v
		case "format_name":
			container = v
		case "sample_rate":
			rate, _ = strconv.Atoi(v)
		}
	}
	return codec, container, rate
}

// Each encoded format decodes to PCM whose sample count over its sample rate is the input
// duration within 50 ms, and ffprobe sees the expected codec and container.
// accept: AUD-04#2
func TestLiveEncodedFormatsDecode(t *testing.T) {
	ffmpeg, ffprobe := liveTools(t)
	const seconds = 2.5
	p := sine(seconds)
	for _, tc := range []struct {
		f                Format
		codec, container string
	}{
		{FormatMP3, "mp3", "mp3"},
		{FormatM4A, "aac", "mov,mp4,m4a,3gp,3g2,mj2"},
		{FormatM4B, "aac", "mov,mp4,m4a,3gp,3g2,mj2"},
		{FormatOpus, "opus", "ogg"},
		{FormatOgg, "opus", "ogg"},
		{FormatFLAC, "flac", "flac"},
	} {
		path := filepath.Join(t.TempDir(), "out"+tc.f.Ext())
		e := Encoding{Format: tc.f, Rate: SampleRate, Tool: ToolFFmpeg, Bin: ffmpeg}
		if err := EncodeFile(context.Background(), e, p, path); err != nil {
			t.Fatalf("%s: %v", tc.f, err)
		}
		codec, container, rate := streamInfo(t, ffprobe, path)
		if codec != tc.codec || container != tc.container || rate <= 0 {
			t.Errorf("%s: codec %q container %q rate %d, want %q in %q", tc.f, codec, container, rate, tc.codec, tc.container)
			continue
		}
		pcm, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", path,
			"-f", "s16le", "-ac", "1", "-ar", strconv.Itoa(rate), "pipe:1").Output()
		if err != nil {
			t.Fatalf("%s: decode: %v", tc.f, err)
		}
		dur := float64(len(pcm)/BytesPerSample) / float64(rate)
		t.Logf("%s: %s in %s, %d samples at %d Hz = %.4f s", tc.f, codec, container, len(pcm)/BytesPerSample, rate, dur)
		if math.Abs(dur-seconds) > 0.050 {
			t.Errorf("%s: decoded %d samples at %d Hz = %.4f s, want %.1f ± 0.05", tc.f, len(pcm)/BytesPerSample, rate, dur, seconds)
		}
	}
}

// The output header shows the requested rate, and the duration is preserved within 10 ms.
// Opus is excluded from the rate check: its decoder always reports 48 kHz.
// accept: AUD-05#1
func TestLiveResampledRate(t *testing.T) {
	ffmpeg, ffprobe := liveTools(t)
	const seconds = 2.5
	p := sine(seconds)
	for _, f := range []Format{FormatWAV, FormatFLAC, FormatMP3, FormatM4A} {
		for _, rate := range []int{16000, 22050, 44100, 48000} {
			path := filepath.Join(t.TempDir(), "out"+f.Ext())
			e, err := ChooseEncoding(f, rate, false, "", exec.LookPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := EncodeFile(context.Background(), e, p, path); err != nil {
				t.Fatalf("%s@%d: %v", f, rate, err)
			}
			dur, got, out := probe(t, ffprobe, path)
			if got != rate || math.Abs(dur-seconds) > 0.010 {
				t.Errorf("%s@%d: rate %d, duration %.4f s\n%s", f, rate, got, dur, out)
			}
		}
	}
	// ffprobe reports Opus at its 48 kHz decode rate whatever was asked; the requested rate is
	// the input sample rate in the OpusHead header (RFC 7845 §5.1, bytes 12-15).
	for _, f := range []Format{FormatOpus, FormatOgg} {
		for _, rate := range []int{16000, 48000} {
			path := filepath.Join(t.TempDir(), "out"+f.Ext())
			e, err := ChooseEncoding(f, rate, false, "", exec.LookPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := EncodeFile(context.Background(), e, p, path); err != nil {
				t.Fatalf("%s@%d: %v", f, rate, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			i := bytes.Index(data, []byte("OpusHead"))
			if i < 0 || len(data) < i+16 {
				t.Fatalf("%s@%d: no OpusHead", f, rate)
			}
			got := int(binary.LittleEndian.Uint32(data[i+12 : i+16]))
			dur, _, out := probe(t, ffprobe, path)
			if got != rate || math.Abs(dur-seconds) > 0.010 {
				t.Errorf("%s@%d: OpusHead rate %d, duration %.4f s\n%s", f, rate, got, dur, out)
			}
		}
	}
	_ = ffmpeg
}

// A stream on stdout is readable by `ffprobe -`: the native streaming WAV and an ffmpeg pipe.
// accept: AUD-07#1
func TestLiveStreamReadsInFFprobe(t *testing.T) {
	ffmpeg, ffprobe := liveTools(t)
	for _, e := range []Encoding{
		{Format: FormatWAV, Rate: SampleRate},
		{Format: FormatWAV, Rate: 16000, Tool: ToolFFmpeg, Bin: ffmpeg},
		{Format: FormatMP3, Rate: SampleRate, Tool: ToolFFmpeg, Bin: ffmpeg},
		{Format: FormatM4A, Rate: SampleRate, Tool: ToolFFmpeg, Bin: ffmpeg},
	} {
		var b bytes.Buffer
		s, err := NewStreamWriter(context.Background(), e, &b)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Write(sine(1.5)); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("%+v: %v", e, err)
		}
		cmd := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_type,sample_rate", "-of", "default=nw=1", "-")
		cmd.Stdin = &b
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "codec_type=audio") || !strings.Contains(string(out), "sample_rate="+strconv.Itoa(e.Rate)) {
			t.Errorf("%s@%d: ffprobe -: %v\n%s", e.Format, e.Rate, err, out)
		}
	}
}

// A relative output path through a directory named like an ffmpeg protocol is written as a
// file: without the file: prefix, ffmpeg opens tcp:x/o.wav as a network URL and fails.
func TestLiveEncodeProtocolLikeDirectory(t *testing.T) {
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to run against the real tools")
	}
	ffmpeg, ffprobe := liveTools(t)
	t.Chdir(t.TempDir())
	if err := os.Mkdir("tcp:x", 0o700); err != nil {
		t.Fatal(err)
	}
	e := Encoding{Format: FormatWAV, Rate: 16000, Tool: ToolFFmpeg, Bin: ffmpeg}
	if err := EncodeFile(t.Context(), e, sine(0.5), filepath.Join("tcp:x", "o.wav")); err != nil {
		t.Fatal(err)
	}
	if dur, rate, _ := probe(t, ffprobe, "file:"+filepath.Join("tcp:x", "o.wav")); rate != 16000 || math.Abs(dur-0.5) > 0.05 {
		t.Fatalf("duration %v at %d Hz", dur, rate)
	}
}
