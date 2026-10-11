package audio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// The internal format (design §9): PCM s16le, 24 kHz, mono.
const (
	SampleRate     = 24000
	Channels       = 1
	BitsPerSample  = 16
	BytesPerSample = BitsPerSample / 8 * Channels
	BytesPerSecond = SampleRate * BytesPerSample
)

// PCM is audio in the internal format. Its length is a whole number of samples.
type PCM []byte

// Duration is the playing time of p.
func (p PCM) Duration() time.Duration {
	return time.Duration(int64(len(p)) * int64(time.Second) / BytesPerSecond)
}

// Validate rejects a buffer holding a partial sample.
func (p PCM) Validate() error {
	if len(p)%BytesPerSample != 0 {
		return fmt.Errorf("audio: PCM length %d is not a whole number of %d-byte samples", len(p), BytesPerSample)
	}
	return nil
}

const (
	wavHeaderSize = 44
	formatPCM     = 1
	// MaxWAVData is the largest data chunk a WAV header can describe: the RIFF
	// size field (36 + data) is 32 bits.
	MaxWAVData = 1<<32 - 1 - (wavHeaderSize - 8)
)

// ErrWAVTooLarge is returned for audio a WAV header cannot describe.
var ErrWAVTooLarge = errors.New("audio: the audio is larger than a WAV file can hold (4 GiB); write .pcm or split the output")

// CheckWAVSize fails when dataLen bytes of PCM cannot be written as one WAV file.
// It is a pure check, so the limit is tested without allocating 4 GiB.
func CheckWAVSize(dataLen uint64) error {
	if dataLen > MaxWAVData {
		return fmt.Errorf("%w: %d bytes of PCM, limit %d", ErrWAVTooLarge, dataLen, uint64(MaxWAVData))
	}
	return nil
}

// WriteWAV writes p as a RIFF/WAVE PCM file. The size is checked before any byte
// is written, so an oversized buffer never yields a corrupt header.
func WriteWAV(w io.Writer, p PCM) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := CheckWAVSize(uint64(len(p))); err != nil {
		return err
	}
	var h [wavHeaderSize]byte
	le := binary.LittleEndian
	copy(h[0:], "RIFF")
	le.PutUint32(h[4:], uint32(wavHeaderSize-8+len(p)))
	copy(h[8:], "WAVEfmt ")
	le.PutUint32(h[16:], 16)
	le.PutUint16(h[20:], formatPCM)
	le.PutUint16(h[22:], Channels)
	le.PutUint32(h[24:], SampleRate)
	le.PutUint32(h[28:], BytesPerSecond)
	le.PutUint16(h[32:], BytesPerSample)
	le.PutUint16(h[34:], BitsPerSample)
	copy(h[36:], "data")
	le.PutUint32(h[40:], uint32(len(p)))
	bw := bufio.NewWriter(w)
	if _, err := bw.Write(h[:]); err != nil {
		return fmt.Errorf("audio: write WAV header: %w", err)
	}
	if _, err := bw.Write(p); err != nil {
		return fmt.Errorf("audio: write WAV data: %w", err)
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("audio: write WAV: %w", err)
	}
	return nil
}

// WritePCM writes p as raw s16le samples (.pcm).
func WritePCM(w io.Writer, p PCM) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if _, err := w.Write(p); err != nil {
		return fmt.Errorf("audio: write PCM: %w", err)
	}
	return nil
}

// ReadWAV reads a RIFF/WAVE file in the internal format. Other formats (non-PCM,
// other rates, channels or depths) are rejected rather than converted.
func ReadWAV(r io.Reader) (PCM, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("audio: read RIFF header: %w", err)
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, errors.New("audio: not a RIFF/WAVE file")
	}
	le := binary.LittleEndian
	gotFmt := false
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return nil, fmt.Errorf("audio: WAV has no data chunk: %w", err)
		}
		id, size := string(ch[0:4]), le.Uint32(ch[4:])
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, fmt.Errorf("audio: fmt chunk of %d bytes", size)
			}
			b := make([]byte, size+size%2)
			if _, err := io.ReadFull(r, b); err != nil {
				return nil, fmt.Errorf("audio: read fmt chunk: %w", err)
			}
			format, ch, rate, bits := le.Uint16(b[0:]), le.Uint16(b[2:]), le.Uint32(b[4:]), le.Uint16(b[14:])
			if format != formatPCM || ch != Channels || rate != SampleRate || bits != BitsPerSample {
				return nil, fmt.Errorf("audio: unsupported WAV format %d, %d channels, %d Hz, %d bits; want PCM (1), mono, 24000 Hz, 16 bits", format, ch, rate, bits)
			}
			gotFmt = true
		case "data":
			if !gotFmt {
				return nil, errors.New("audio: WAV data chunk before fmt chunk")
			}
			p := make(PCM, size)
			if _, err := io.ReadFull(r, p); err != nil {
				return nil, fmt.Errorf("audio: read WAV data: %w", err)
			}
			return p, p.Validate()
		default:
			if _, err := io.CopyN(io.Discard, r, int64(size)+int64(size%2)); err != nil {
				return nil, fmt.Errorf("audio: skip %q chunk: %w", id, err)
			}
		}
	}
}

// Format is an output container recite writes natively.
type Format string

// Native formats (M1). Encoded formats arrive with the encoder drivers (M4).
const (
	FormatWAV Format = "wav"
	FormatPCM Format = "pcm"
)

// FormatFor returns the native format of path's extension.
func FormatFor(path string) (Format, error) {
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".wav":
		return FormatWAV, nil
	case ".pcm":
		return FormatPCM, nil
	default:
		return "", fmt.Errorf("unsupported output format %q: this version writes .wav and .pcm", ext)
	}
}

// Write encodes p in format f.
func Write(w io.Writer, f Format, p PCM) error {
	switch f {
	case FormatWAV:
		return WriteWAV(w, p)
	case FormatPCM:
		return WritePCM(w, p)
	}
	return fmt.Errorf("audio: unknown format %q", f)
}
