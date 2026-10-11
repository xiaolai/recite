package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

func samples(n int) PCM {
	rng := rand.New(rand.NewPCG(1, 2))
	p := make(PCM, 2*n)
	for i := range n {
		binary.LittleEndian.PutUint16(p[2*i:], uint16(rng.IntN(1<<16)))
	}
	return p
}

// accept: AUD-01#1
func TestWAVRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, SampleRate, SampleRate*3 + 7} {
		p := samples(n)
		var b bytes.Buffer
		if err := WriteWAV(&b, p); err != nil {
			t.Fatal(err)
		}
		if b.Len() != 44+len(p) {
			t.Fatalf("size %d", b.Len())
		}
		got, err := ReadWAV(&b)
		if err != nil || !bytes.Equal(got, p) {
			t.Fatalf("n=%d: round trip differs (%v)", n, err)
		}
	}
	if d := samples(SampleRate * 2).Duration(); d != 2*time.Second {
		t.Fatalf("duration %v", d)
	}
}

func TestReadWAVSkipsUnknownChunks(t *testing.T) {
	var b bytes.Buffer
	p := samples(10)
	if err := WriteWAV(&b, p); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	// Insert an odd-sized LIST chunk (with its pad byte) before data.
	extra := append([]byte("LIST\x03\x00\x00\x00abc\x00"), raw[36:]...)
	file := append(append([]byte{}, raw[:36]...), extra...)
	got, err := ReadWAV(bytes.NewReader(file))
	if err != nil || !bytes.Equal(got, p) {
		t.Fatalf("got %v", err)
	}
}

func TestReadWAVRejectsOtherFormats(t *testing.T) {
	var b bytes.Buffer
	if err := WriteWAV(&b, samples(4)); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	binary.LittleEndian.PutUint16(raw[20:], 3) // IEEE float
	if _, err := ReadWAV(bytes.NewReader(raw)); err == nil || !strings.Contains(err.Error(), "unsupported WAV format 3") {
		t.Fatalf("float accepted: %v", err)
	}
	if _, err := ReadWAV(strings.NewReader("RIFX0000WAVE")); err == nil {
		t.Fatal("RIFX accepted")
	}
}

// accept: AUD-01#3
func TestWAVOver4GiBRefused(t *testing.T) {
	if err := CheckWAVSize(MaxWAVData); err != nil {
		t.Fatalf("limit refused: %v", err)
	}
	for _, n := range []uint64{MaxWAVData + 1, 4 << 30, 1 << 40} {
		err := CheckWAVSize(n)
		if !errors.Is(err, ErrWAVTooLarge) || !strings.Contains(err.Error(), "4 GiB") {
			t.Fatalf("%d: %v", n, err)
		}
	}
	// The header's 32-bit RIFF size is exactly at its maximum at the limit.
	if 36+uint64(MaxWAVData) != 1<<32-1 {
		t.Fatal("limit does not match the RIFF size field")
	}
}

func TestPCMOutput(t *testing.T) {
	p := samples(5)
	var b bytes.Buffer
	if err := Write(&b, FormatPCM, p); err != nil || !bytes.Equal(b.Bytes(), p) {
		t.Fatalf("pcm: %v", err)
	}
	if err := WriteWAV(&b, PCM{1}); err == nil {
		t.Fatal("partial sample accepted")
	}
	for path, want := range map[string]Format{"a.wav": FormatWAV, "B.WAV": FormatWAV, "x/y.pcm": FormatPCM} {
		if f, err := FormatFor(path); err != nil || f != want {
			t.Errorf("%s: %q %v", path, f, err)
		}
	}
	if _, err := FormatFor("a.mp3"); err == nil {
		t.Error("mp3 accepted")
	}
}
