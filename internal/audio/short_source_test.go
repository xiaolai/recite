package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A source holding fewer than n bytes fails every encoder pass rather than encoding the
// shorter audio and reporting success.
func TestEncodeFileFromShortSource(t *testing.T) {
	short := bytes.NewReader(samples(1)) // 2 bytes
	bin, _ := fakeFFmpeg(t)
	for name, e := range map[string]Encoding{
		"ffmpeg":    {Format: FormatMP3, Rate: SampleRate, Tool: ToolFFmpeg, Bin: bin},
		"afconvert": {Format: FormatM4A, Rate: SampleRate, Tool: ToolAfconvert, Bin: bin},
	} {
		out := filepath.Join(t.TempDir(), "o")
		if err := EncodeFileFrom(context.Background(), e, short, 4, out); err == nil {
			t.Errorf("%s: a 2-byte source encoded as 4 bytes without an error", name)
		}
	}
	// The loudness measuring pass reads through the same reader.
	if _, err := io.ReadAll(pcmSource{short, 4}.reader()); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short pass read: %v, want io.ErrUnexpectedEOF", err)
	}
	if b, err := io.ReadAll(pcmSource{short, 2}.reader()); err != nil || len(b) != 2 {
		t.Fatalf("exact pass read: %d bytes, %v", len(b), err)
	}
}

// Durations past the range of samples*time.Second stay positive and exact.
func TestDurationsBeyondOverflow(t *testing.T) {
	const hours = 200
	n := int64(hours) * 3600 * BytesPerSecond
	if got := DurationOf(n); got != hours*time.Hour {
		t.Fatalf("DurationOf(%d hours of PCM) = %v", hours, got)
	}
	if got := SampleDuration(int64(hours)*3600*SampleRate+SampleRate/2, SampleRate); got != hours*time.Hour+time.Second/2 {
		t.Fatalf("SampleDuration = %v", got)
	}
	if got := samplesOf(hours * time.Hour); got != hours*3600*SampleRate {
		t.Fatalf("samplesOf(%d h) = %d", hours, got)
	}
}

// riff builds a RIFF/WAVE file from raw chunks.
func riff(chunks ...[]byte) []byte {
	b := []byte("RIFF\x00\x00\x00\x00WAVE")
	for _, c := range chunks {
		b = append(b, c...)
	}
	return b
}

func chunk(id string, size uint32, body []byte) []byte {
	b := append([]byte(id), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(b[4:], size)
	return append(b, body...)
}

// fmtBody is the 16-byte fmt chunk body of the internal format.
func fmtBody(t testing.TB) []byte {
	var good bytes.Buffer
	if err := WriteWAV(&good, samples(2)); err != nil {
		t.Fatal(err)
	}
	return good.Bytes()[20:36]
}

// Malformed headers are rejected with an error, never a panic, and a declared data size is
// checked against the bytes actually present.
func TestReadWAVMalformedHeaders(t *testing.T) {
	body := fmtBody(t)
	for name, tc := range map[string]struct {
		file []byte
		want string
	}{
		"fmt size wraps padding": {riff(chunk("fmt ", 0xffffffff, body)), "fmt chunk of"},
		"fmt size huge":          {riff(chunk("fmt ", 0x7fffffff, body)), "fmt chunk of"},
		"data size 4 GiB":        {riff(chunk("fmt ", 16, body), chunk("data", 0xfffffffe, nil)), "declared"},
		"data short":             {riff(chunk("fmt ", 16, body), chunk("data", 8, []byte{1, 2})), "declared"},
	} {
		_, err := ReadWAV(bytes.NewReader(tc.file))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error containing %q", name, err, tc.want)
		}
	}
}

func FuzzReadWAV(f *testing.F) {
	body := fmtBody(f)
	var good bytes.Buffer
	if err := WriteWAV(&good, samples(4)); err != nil {
		f.Fatal(err)
	}
	f.Add(good.Bytes())
	f.Add(riff(chunk("fmt ", 0xffffffff, body)))
	f.Add(riff(chunk("fmt ", 16, body), chunk("data", 0xfffffffe, nil)))
	f.Fuzz(func(t *testing.T, b []byte) {
		p, err := ReadWAV(bytes.NewReader(b))
		if err == nil && len(p) > len(b) {
			t.Fatalf("%d bytes of PCM from a %d-byte file", len(p), len(b))
		}
	})
}
