package audio

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// PCM held in a file is written and encoded to the same bytes as the same PCM in memory,
// reading only the first n bytes of the source.
func TestFromSourceMatchesInMemory(t *testing.T) {
	p := samples(SampleRate + 3)
	held := append(append([]byte(nil), p...), 1, 2, 3, 4) // the trailing bytes must not be read
	n := int64(len(p))
	for _, f := range []Format{FormatWAV, FormatPCM} {
		var want, got bytes.Buffer
		if err := Write(&want, f, p); err != nil {
			t.Fatal(err)
		}
		if err := WriteFrom(&got, f, bytes.NewReader(held), n); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("%s: %d bytes from the source, %d in memory", f, got.Len(), want.Len())
		}
	}
	bin, _ := fakeFFmpeg(t)
	e := Encoding{Format: FormatMP3, Rate: SampleRate, Tool: ToolFFmpeg, Bin: bin}
	out := filepath.Join(t.TempDir(), "o.mp3")
	if err := EncodeFileFrom(context.Background(), e, bytes.NewReader(held), n, out); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(out); err != nil || !bytes.Equal(b, p) {
		t.Fatalf("encoder input %d bytes, %v; want the %d-byte PCM", len(b), err, n)
	}
	if err := WriteFrom(&bytes.Buffer{}, FormatWAV, bytes.NewReader(held), n-1); err == nil || !strings.Contains(err.Error(), "whole number") {
		t.Fatalf("partial sample: %v", err)
	}
}
