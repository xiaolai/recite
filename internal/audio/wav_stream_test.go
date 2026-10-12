package audio

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// countingReader records how many bytes were pulled from the underlying input.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	return n, err
}

// OpenWAV parses only the header: a 4 GiB declaration reports its size without reading or
// buffering the data, and the data reader yields exactly the bytes present, then fails short.
func TestOpenWAVStreamsLargeDeclaredSize(t *testing.T) {
	const declared = 0xfffffffe
	head := riff(chunk("fmt ", 16, fmtBody(t)), chunk("data", declared, nil))
	src := &countingReader{r: io.MultiReader(bytes.NewReader(head), bytes.NewReader(samples(3)))}
	w, err := OpenWAV(src)
	if err != nil {
		t.Fatal(err)
	}
	if w.Size != declared {
		t.Fatalf("Size = %d, want %d", w.Size, int64(declared))
	}
	if src.n != int64(len(head)) {
		t.Fatalf("OpenWAV read %d bytes, want only the %d-byte header", src.n, len(head))
	}
	var buf [4]byte
	if n, err := w.Data.Read(buf[:]); n == 0 || err != nil {
		t.Fatalf("first data read: %d, %v", n, err)
	}
	if _, err := io.Copy(io.Discard, w.Data); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short data: %v, want io.ErrUnexpectedEOF", err)
	}
}

// The data reader stops at the end of the data chunk, leaving trailing chunks unread.
func TestOpenWAVLimitsToDataChunk(t *testing.T) {
	pcm := samples(4)
	file := riff(chunk("fmt ", 16, fmtBody(t)), chunk("data", uint32(len(pcm)), pcm), chunk("LIST", 4, []byte("abcd")))
	w, err := OpenWAV(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(w.Data)
	if err != nil || !bytes.Equal(got, pcm) {
		t.Fatalf("data = %v, %v; want %v", got, err, pcm)
	}
}

// A declared data size holding a partial sample is rejected at the header.
func TestOpenWAVRejectsPartialSample(t *testing.T) {
	file := riff(chunk("fmt ", 16, fmtBody(t)), chunk("data", 3, []byte{1, 2, 3}))
	if _, err := OpenWAV(bytes.NewReader(file)); err == nil {
		t.Fatal("a 3-byte data chunk was accepted")
	}
}

// ReadWAV buffers at most MaxReadWAV bytes: a larger declaration fails with ErrWAVReadCap
// before any data is read.
func TestReadWAVCap(t *testing.T) {
	file := riff(chunk("fmt ", 16, fmtBody(t)), chunk("data", MaxReadWAV+2, nil))
	if _, err := ReadWAV(bytes.NewReader(file)); !errors.Is(err, ErrWAVReadCap) {
		t.Fatalf("ReadWAV over the cap: %v, want ErrWAVReadCap", err)
	}
	pcm := samples(4)
	small := riff(chunk("fmt ", 16, fmtBody(t)), chunk("data", uint32(len(pcm)), pcm))
	if _, err := readWAVMax(bytes.NewReader(small), int64(len(pcm))-2); !errors.Is(err, ErrWAVReadCap) {
		t.Fatalf("readWAVMax over a small cap: %v", err)
	}
	if got, err := readWAVMax(bytes.NewReader(small), int64(len(pcm))); err != nil || !bytes.Equal(got, pcm) {
		t.Fatalf("readWAVMax at the cap: %v, %v", got, err)
	}
}
