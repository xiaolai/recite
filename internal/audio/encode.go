package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/xiaolai/recite/internal/exitcode"
)

// Format is an output container (design §9, AUD-04).
type Format string

// Output formats. wav and pcm at the internal rate are written natively; every other format,
// and wav or pcm at another rate, goes through an external encoder.
const (
	FormatWAV  Format = "wav"
	FormatPCM  Format = "pcm"
	FormatMP3  Format = "mp3"
	FormatM4A  Format = "m4a"
	FormatM4B  Format = "m4b"
	FormatOpus Format = "opus"
	FormatOgg  Format = "ogg"
	FormatFLAC Format = "flac"
)

// Formats lists every output format, in the order of design §12.2.
var Formats = []Format{FormatWAV, FormatPCM, FormatMP3, FormatM4A, FormatM4B, FormatOpus, FormatOgg, FormatFLAC}

// SampleRates are the rates --sample-rate accepts (AUD-05); SampleRate is the default.
var SampleRates = []int{16000, 22050, SampleRate, 44100, 48000}

// opusRates are the input rates the Opus encoder accepts.
var opusRates = []int{8000, 12000, 16000, 24000, 48000}

// EncoderError is a missing encoder or a format/rate the encoders cannot produce: a usage
// error, exit 2, found before any network call (design §9).
type EncoderError struct{ msg string }

func (e *EncoderError) Error() string { return e.msg }

// ExitCode is the CLI exit code of an encoder error.
func (e *EncoderError) ExitCode() int { return exitcode.Usage }

func encoderErrorf(format string, a ...any) error { return &EncoderError{fmt.Sprintf(format, a...)} }

func formatList() string {
	s := make([]string, len(Formats))
	for i, f := range Formats {
		s[i] = string(f)
	}
	return strings.Join(s, " ")
}

// rawAAC is the extension and --format value of raw ADTS AAC, which recite does not write: an
// ADTS stream carries encoder priming that shifts its timeline away from the assembly's (D20).
const rawAAC = "aac"

func rawAACError() error {
	return encoderErrorf("raw ADTS AAC output is unsupported; use m4a for AAC audio")
}

// ParseFormat parses a --format value.
func ParseFormat(s string) (Format, error) {
	f := Format(strings.ToLower(s))
	if f == rawAAC {
		return "", rawAACError()
	}
	if !slices.Contains(Formats, f) {
		return "", encoderErrorf("unsupported format %q: want one of %s", s, formatList())
	}
	return f, nil
}

// FormatFor returns the format of path's extension.
func FormatFor(path string) (Format, error) {
	ext := strings.ToLower(filepath.Ext(path))
	f := Format(strings.TrimPrefix(ext, "."))
	if f == rawAAC {
		return "", rawAACError()
	}
	if ext == "" || !slices.Contains(Formats, f) {
		return "", encoderErrorf("unsupported output format %q: want one of %s, or pass --format", ext, formatList())
	}
	return f, nil
}

// Ext is the file extension of f, with the dot.
func (f Format) Ext() string { return "." + string(f) }

// ParseSampleRate parses a --sample-rate value.
func ParseSampleRate(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || !slices.Contains(SampleRates, n) {
		return 0, encoderErrorf("--sample-rate %q: want one of 16000, 22050, 24000, 44100, 48000", s)
	}
	return n, nil
}

// Encoder tools.
const (
	ToolFFmpeg    = "ffmpeg"
	ToolAfconvert = "afconvert"
)

// Encoding is how one output is produced: the format, the sample rate, and the external
// tool (Bin, an absolute path found on PATH) or none for a native write.
type Encoding struct {
	Format Format
	Rate   int
	Tool   string // "" = native
	Bin    string
	// Loudness is the normalisation preset (AUD-06); "" and none leave the level alone. Any
	// other preset needs Tool to be ffmpeg (see [WithLoudness]).
	Loudness Loudness
}

// Native reports whether recite writes the output itself.
func (e Encoding) Native() bool { return e.Tool == "" }

// ChooseEncoding picks the encoder for format f at rate, before any network call (AUD-04):
// native for wav and pcm at the internal rate, else ffmpeg, else afconvert for m4a on
// macOS when the rate is the internal one and the output is a file (afconvert reads and
// writes files only, and resampling goes through ffmpeg, AUD-05). look finds a tool on PATH.
func ChooseEncoding(f Format, rate int, stream bool, goos string, look func(string) (string, error)) (Encoding, error) {
	e := Encoding{Format: f, Rate: rate}
	if !slices.Contains(SampleRates, rate) {
		return e, encoderErrorf("sample rate %d: want one of 16000, 22050, 24000, 44100, 48000", rate)
	}
	if (f == FormatOpus || f == FormatOgg) && !slices.Contains(opusRates, rate) {
		return e, encoderErrorf("%s output cannot be %d Hz: Opus takes 16000, 24000 or 48000 here", f, rate)
	}
	if (f == FormatWAV || f == FormatPCM) && rate == SampleRate {
		return e, nil
	}
	if bin, err := look(ToolFFmpeg); err == nil {
		e.Tool, e.Bin = ToolFFmpeg, bin
		return e, nil
	}
	if goos == "darwin" && f == FormatM4A && rate == SampleRate && !stream {
		if bin, err := look(ToolAfconvert); err == nil {
			e.Tool, e.Bin = ToolAfconvert, bin
			return e, nil
		}
	}
	why := "to encode " + string(f)
	if f == FormatWAV || f == FormatPCM {
		why = fmt.Sprintf("to resample to %d Hz", rate)
	}
	return e, encoderErrorf("%s needs ffmpeg, which is not on PATH; install ffmpeg (e.g. `brew install ffmpeg` or your package manager)", why)
}

// FFmpegArgs is the ffmpeg argument vector that reads internal PCM on stdin and writes the
// output to out: a file path, or "pipe:1" for stdout. It is passed to exec, never to a shell.
func FFmpegArgs(e Encoding, out string) []string { return ffmpegArgs(e, out, "") }

// NormalizeArgs is [FFmpegArgs] with the second loudness pass applied, from the first pass's
// measurement m (AUD-06).
func NormalizeArgs(e Encoding, out string, m Measurement) []string {
	return ffmpegArgs(e, out, normalizeFilter(e.Loudness, m))
}

func ffmpegArgs(e Encoding, out, filter string) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "s16le", "-ar", strconv.Itoa(SampleRate), "-ac", strconv.Itoa(Channels), "-i", "pipe:0"}
	if filter != "" {
		// loudnorm resamples to 192 kHz internally, so the output rate is always set.
		args = append(args, "-af", filter, "-ar", strconv.Itoa(e.Rate))
	} else if e.Rate != SampleRate {
		args = append(args, "-ar", strconv.Itoa(e.Rate))
	}
	var codec []string
	var muxer string
	switch e.Format {
	case FormatWAV:
		codec, muxer = []string{"-c:a", "pcm_s16le"}, "wav"
	case FormatPCM:
		codec, muxer = []string{"-c:a", "pcm_s16le"}, "s16le"
	case FormatMP3:
		codec, muxer = []string{"-c:a", "libmp3lame", "-b:a", "192k"}, "mp3"
	case FormatM4A, FormatM4B:
		codec, muxer = []string{"-c:a", "aac", "-b:a", "160k"}, "ipod"
		if out == "pipe:1" {
			// MP4 needs a seekable output unless it is fragmented.
			codec = append(codec, "-movflags", "+frag_keyframe+empty_moov")
		}
	case FormatOpus:
		codec, muxer = []string{"-c:a", "libopus", "-b:a", "64k"}, "opus"
	case FormatOgg:
		codec, muxer = []string{"-c:a", "libopus", "-b:a", "64k"}, "ogg"
	case FormatFLAC:
		codec, muxer = []string{"-c:a", "flac"}, "flac"
	}
	args = append(args, codec...)
	return append(args, "-f", muxer, "-y", out)
}

// AfconvertArgs is the afconvert argument vector converting the WAV at in to the m4a out.
func AfconvertArgs(in, out string) []string {
	return []string{"-f", "m4af", "-d", "aac", "-b", "160000", in, out}
}

// maxToolStderr bounds the encoder's stderr kept for the error message.
const maxToolStderr = 4 << 10

// boundedBuffer keeps the first maxToolStderr bytes written to it.
type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := maxToolStderr - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func toolError(tool string, err error, stderr *boundedBuffer) error {
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return fmt.Errorf("audio: %s failed: %w: %s", tool, err, msg)
	}
	return fmt.Errorf("audio: %s failed: %w", tool, err)
}

// EncodeFile writes p to the file at path through e's encoder, replacing its content; a native
// encoding is written with [Write] instead. The PCM reaches ffmpeg through stdin; afconvert
// reads a WAV from a private temp directory. A loudness preset runs ffmpeg twice: a measuring
// pass, then the encode with the measured values (AUD-06). Silence has no loudness to
// normalise and is encoded as it is.
func EncodeFile(ctx context.Context, e Encoding, p PCM, path string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	switch e.Tool {
	case ToolFFmpeg:
		args := FFmpegArgs(e, path)
		if _, ok := e.Loudness.Target(); ok {
			m, err := measure(ctx, e, p)
			if err != nil {
				return err
			}
			if !m.Silent() {
				args = NormalizeArgs(e, path, m)
			}
		}
		var stderr boundedBuffer
		cmd := exec.CommandContext(ctx, e.Bin, args...)
		cmd.Stdin, cmd.Stderr = bytes.NewReader(p), &stderr
		if err := cmd.Run(); err != nil {
			return toolError(ToolFFmpeg, err, &stderr)
		}
		return nil
	case ToolAfconvert:
		if _, ok := e.Loudness.Target(); ok {
			return errors.New("audio: afconvert cannot normalise loudness")
		}
		return afconvert(ctx, e, p, path)
	}
	return fmt.Errorf("audio: unknown encoder %q", e.Tool)
}

func afconvert(ctx context.Context, e Encoding, p PCM, path string) error {
	dir, err := os.MkdirTemp("", "recite-afconvert-")
	if err != nil {
		return fmt.Errorf("audio: afconvert temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }() // best effort: a private temp directory
	in := filepath.Join(dir, "in.wav")
	f, err := os.OpenFile(in, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("audio: afconvert input: %w", err)
	}
	werr := WriteWAV(f, p)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	var stderr boundedBuffer
	cmd := exec.CommandContext(ctx, e.Bin, AfconvertArgs(in, path)...)
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	if err := cmd.Run(); err != nil {
		return toolError(ToolAfconvert, err, &stderr)
	}
	return nil
}

// StreamWriter takes internal PCM and writes the encoded stream to its destination as it
// arrives (AUD-07). Close ends the stream: for an encoder, it closes the encoder's input
// and waits for it, so a stream cut short still ends as a valid prefix.
type StreamWriter struct {
	w      io.Writer
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *boundedBuffer
	closed bool
}

// NewStreamWriter starts a stream in encoding e to dst. Native wav gets a streaming header
// whose size fields hold the maximum; ffmpeg writes to dst through its stdout.
func NewStreamWriter(ctx context.Context, e Encoding, dst io.Writer) (*StreamWriter, error) {
	if _, ok := e.Loudness.Target(); ok {
		// Two-pass loudness needs the whole output before the first byte (AUD-07).
		return nil, encoderErrorf("a stream cannot be loudness-normalised: two passes need the whole output")
	}
	switch e.Tool {
	case "":
		if e.Format == FormatWAV {
			if err := writeWAVHeader(dst, streamSize, streamSize); err != nil {
				return nil, err
			}
		}
		return &StreamWriter{w: dst}, nil
	case ToolFFmpeg:
		s := &StreamWriter{stderr: &boundedBuffer{}}
		s.cmd = exec.CommandContext(ctx, e.Bin, FFmpegArgs(e, "pipe:1")...)
		s.cmd.Stdout, s.cmd.Stderr = dst, s.stderr
		in, err := s.cmd.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("audio: ffmpeg stdin: %w", err)
		}
		if err := s.cmd.Start(); err != nil {
			return nil, toolError(ToolFFmpeg, err, s.stderr)
		}
		s.stdin, s.w = in, in
		return s, nil
	}
	return nil, encoderErrorf("%s cannot stream to stdout; install ffmpeg", e.Tool)
}

// Write passes PCM on.
func (s *StreamWriter) Write(p []byte) (int, error) {
	if s.closed {
		return 0, errors.New("audio: write to a closed stream")
	}
	n, err := s.w.Write(p)
	if err != nil {
		return n, fmt.Errorf("audio: stream write: %w", err)
	}
	return n, nil
}

// Close ends the stream; it is safe to call more than once.
func (s *StreamWriter) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.cmd == nil {
		return nil
	}
	cerr := s.stdin.Close()
	if err := s.cmd.Wait(); err != nil {
		return toolError(ToolFFmpeg, err, s.stderr)
	}
	if cerr != nil {
		return fmt.Errorf("audio: close ffmpeg stdin: %w", cerr)
	}
	return nil
}
