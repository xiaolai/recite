package audio

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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
func (p PCM) Duration() time.Duration { return DurationOf(int64(len(p))) }

// DurationOf is the playing time of n bytes of PCM.
func DurationOf(n int64) time.Duration {
	return SampleDuration(n/BytesPerSample, SampleRate)
}

// SampleDuration is the playing time of samples at rate, truncated to the nanosecond. It
// divides before it multiplies: samples*time.Second overflows int64 past about 106 hours at
// 24 kHz (53 hours of bytes), while the quotient and remainder stay in range.
func SampleDuration(samples int64, rate int) time.Duration {
	r := int64(rate)
	return time.Duration(samples/r)*time.Second + time.Duration(samples%r*int64(time.Second)/r)
}

// checkSize rejects a length holding a partial sample.
func checkSize(n int64) error {
	if n < 0 || n%BytesPerSample != 0 {
		return fmt.Errorf("audio: PCM length %d is not a whole number of %d-byte samples", n, BytesPerSample)
	}
	return nil
}

// Validate rejects a buffer holding a partial sample.
func (p PCM) Validate() error { return checkSize(int64(len(p))) }

const (
	wavHeaderSize = 44
	formatPCM     = 1
	// maxFmtChunk bounds the fmt chunk read: PCM needs 16 bytes, WAVE_FORMAT_EXTENSIBLE 40.
	maxFmtChunk = 1024
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
	return WriteWAVFrom(w, bytes.NewReader(p), int64(len(p)))
}

// WriteWAVFrom is [WriteWAV] of the n bytes of PCM read from r, so audio held in a file is
// written without loading it into memory.
func WriteWAVFrom(w io.Writer, r io.Reader, n int64) error {
	if err := checkSize(n); err != nil {
		return err
	}
	if err := CheckWAVSize(uint64(n)); err != nil {
		return err
	}
	bw := bufio.NewWriter(w)
	if err := writeWAVHeader(bw, uint32(wavHeaderSize-8+n), uint32(n)); err != nil {
		return err
	}
	if _, err := io.CopyN(bw, r, n); err != nil {
		return fmt.Errorf("audio: write WAV data: %w", err)
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("audio: write WAV: %w", err)
	}
	return nil
}

// WritePCM writes p as raw s16le samples (.pcm).
func WritePCM(w io.Writer, p PCM) error {
	return writePCMFrom(w, bytes.NewReader(p), int64(len(p)))
}

func writePCMFrom(w io.Writer, r io.Reader, n int64) error {
	if err := checkSize(n); err != nil {
		return err
	}
	if _, err := io.CopyN(w, r, n); err != nil {
		return fmt.Errorf("audio: write PCM: %w", err)
	}
	return nil
}

// MaxReadWAV is the most PCM [ReadWAV] buffers in memory: 256 MiB, about 93 minutes of the
// internal format. Larger audio is read with [OpenWAV], which streams the data chunk.
const MaxReadWAV = 256 << 20

// ErrWAVReadCap is returned by [ReadWAV] for a data chunk larger than [MaxReadWAV].
var ErrWAVReadCap = errors.New("audio: WAV data is larger than ReadWAV buffers; stream it with OpenWAV")

// WAVStream is an opened WAV file: its data chunk size, and a reader limited to that chunk.
// Reading Data past the bytes present fails with io.ErrUnexpectedEOF rather than ending early.
type WAVStream struct {
	Size int64
	Data io.Reader
}

// ReadWAV reads a whole RIFF/WAVE file in the internal format into memory, refusing a data
// chunk larger than [MaxReadWAV] before reading it. Other formats (non-PCM, other rates,
// channels or depths) are rejected rather than converted.
func ReadWAV(r io.Reader) (PCM, error) { return readWAVMax(r, MaxReadWAV) }

func readWAVMax(r io.Reader, limit int64) (PCM, error) {
	w, err := OpenWAV(r)
	if err != nil {
		return nil, err
	}
	if w.Size > limit {
		return nil, fmt.Errorf("%w: %d bytes declared, limit %d", ErrWAVReadCap, w.Size, limit)
	}
	// Read through a buffer that grows with the input, never to the declared size up
	// front: a short file declaring the cap must fail on its missing bytes, not allocate.
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, w.Data); err != nil {
		return nil, fmt.Errorf("audio: read WAV data: %w", err)
	}
	return PCM(buf.Bytes()), nil
}

// OpenWAV parses a RIFF/WAVE header in the internal format up to the start of the data
// chunk, reading nothing of the data itself, so audio of any size is streamed. Other formats
// are rejected rather than converted.
func OpenWAV(r io.Reader) (*WAVStream, error) {
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
			if size < 16 || size > maxFmtChunk {
				return nil, fmt.Errorf("audio: fmt chunk of %d bytes", size)
			}
			b := make([]byte, uint64(size)+uint64(size%2))
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
			if err := checkSize(int64(size)); err != nil {
				return nil, err
			}
			return &WAVStream{Size: int64(size), Data: &exactReader{r: io.LimitReader(r, int64(size)), left: int64(size)}}, nil
		default:
			if _, err := io.CopyN(io.Discard, r, int64(size)+int64(size%2)); err != nil {
				return nil, fmt.Errorf("audio: skip %q chunk: %w", id, err)
			}
		}
	}
}

// Write encodes p in a native format (wav or pcm at the internal rate). Encoded formats go
// through an [Encoding].
func Write(w io.Writer, f Format, p PCM) error {
	return WriteFrom(w, f, bytes.NewReader(p), int64(len(p)))
}

// WriteFrom is [Write] of the n bytes of PCM read from r.
func WriteFrom(w io.Writer, f Format, r io.Reader, n int64) error {
	switch f {
	case FormatWAV:
		return WriteWAVFrom(w, r, n)
	case FormatPCM:
		return writePCMFrom(w, r, n)
	}
	return fmt.Errorf("audio: %q is not a native format", f)
}

// streamSize fills the size fields of a streaming WAV header: the length is unknown when
// the header is written, so the fields hold the maximum (design §9, AUD-07).
const streamSize = 0xFFFFFFFF

// writeWAVHeader writes the 44-byte header of a PCM WAV with the given data size.
func writeWAVHeader(w io.Writer, riffSize, dataSize uint32) error {
	var h [wavHeaderSize]byte
	le := binary.LittleEndian
	copy(h[0:], "RIFF")
	le.PutUint32(h[4:], riffSize)
	copy(h[8:], "WAVEfmt ")
	le.PutUint32(h[16:], 16)
	le.PutUint16(h[20:], formatPCM)
	le.PutUint16(h[22:], Channels)
	le.PutUint32(h[24:], SampleRate)
	le.PutUint32(h[28:], BytesPerSecond)
	le.PutUint16(h[32:], BytesPerSample)
	le.PutUint16(h[34:], BitsPerSample)
	copy(h[36:], "data")
	le.PutUint32(h[40:], dataSize)
	if _, err := w.Write(h[:]); err != nil {
		return fmt.Errorf("audio: write WAV header: %w", err)
	}
	return nil
}
