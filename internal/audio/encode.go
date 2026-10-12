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
	// Chapters is ffmetadata text that ffmpeg puts into an m4a or m4b as chapter markers
	// (OUT-04); "" writes none.
	Chapters string
}

// ChapterFormat reports whether f carries chapter markers.
func ChapterFormat(f Format) bool { return f == FormatM4A || f == FormatM4B }

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
func FFmpegArgs(e Encoding, out string) []string { return ffmpegArgs(e, "", out, "") }

// FFmpegChapterArgs is FFmpegArgs with the ffmetadata file at meta ("" for none) as a second
// input whose chapters are mapped into the output.
func FFmpegChapterArgs(e Encoding, meta, out string) []string { return ffmpegArgs(e, meta, out, "") }

// NormalizeArgs is [FFmpegArgs] with the second loudness pass applied, from the first pass's
// measurement m (AUD-06).
func NormalizeArgs(e Encoding, out string, m Measurement) []string {
	t, _ := e.Loudness.Target()
	return ffmpegArgs(e, "", out, normalizeFilter(t, m))
}

// ffmpegArgs builds every encoding command: meta adds a chapter input, filter the loudness pass.
func ffmpegArgs(e Encoding, meta, out, filter string) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "s16le", "-ar", strconv.Itoa(SampleRate), "-ac", strconv.Itoa(Channels), "-i", "pipe:0"}
	if meta != "" {
		// Input options precede their -i; every output option follows the last input.
		args = append(args, "-f", "ffmetadata", "-i", ffmpegPath(meta), "-map", "0:a", "-map_metadata", "1", "-map_chapters", "1")
	}
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
	return append(args, "-f", muxer, "-y", ffmpegPath(out))
}

// ffmpegPath is the ffmpeg URL of a file path. ffmpeg reads a "name:" prefix as a protocol,
// so a relative path through a directory such as "tcp:x" or "http:x" would open a network
// URL; the file: protocol keeps every path a file. pipe:1 (stdout) is passed as it is.
func ffmpegPath(p string) string {
	if p == "pipe:1" {
		return p
	}
	return "file:" + p
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
	return EncodeFileFrom(ctx, e, bytes.NewReader(p), int64(len(p)), path)
}

// EncodeFileFrom is [EncodeFile] of the n bytes of PCM at the start of src, so audio held in a
// file reaches the encoder without being loaded into memory. Each encoder pass reads src from
// the start.
func EncodeFileFrom(ctx context.Context, e Encoding, src io.ReaderAt, n int64, path string) error {
	if err := checkSize(n); err != nil {
		return err
	}
	p := pcmSource{src, n}
	if e.Chapters != "" && (e.Tool != ToolFFmpeg || !ChapterFormat(e.Format)) {
		return fmt.Errorf("audio: chapters need ffmpeg and m4a or m4b, not %s through %q", e.Format, e.Tool)
	}
	switch e.Tool {
	case ToolFFmpeg:
		meta, cleanup, err := chapterFile(e.Chapters)
		if err != nil {
			return err
		}
		defer cleanup()
		target, ok := e.Loudness.Target()
		if !ok {
			return encodeFFmpeg(ctx, e, p, meta, path, "")
		}
		m, err := measure(ctx, e, p)
		if err != nil {
			return err
		}
		if m.Silent() {
			return encodeFFmpeg(ctx, e, p, meta, path, "")
		}
		return encodeUnderCeiling(ctx, e, p, meta, path, target, m)
	case ToolAfconvert:
		if _, ok := e.Loudness.Target(); ok {
			return errors.New("audio: afconvert cannot normalise loudness")
		}
		return afconvert(ctx, e, p, path)
	}
	return fmt.Errorf("audio: unknown encoder %q", e.Tool)
}

// pcmSource is n bytes of PCM at the start of a ReaderAt.
type pcmSource struct {
	src io.ReaderAt
	n   int64
}

// reader reads the PCM from its start; every pass gets its own. A source shorter than n
// fails the read with io.ErrUnexpectedEOF instead of ending early, so no pass can encode
// truncated audio and report success.
func (p pcmSource) reader() io.Reader {
	return &exactReader{r: io.NewSectionReader(p.src, 0, p.n), left: p.n}
}

// exactReader turns an EOF before left bytes have been read into io.ErrUnexpectedEOF.
type exactReader struct {
	r    io.Reader
	left int64
}

func (e *exactReader) Read(b []byte) (int, error) {
	n, err := e.r.Read(b)
	e.left -= int64(n)
	if err == io.EOF && e.left > 0 {
		return n, fmt.Errorf("audio: PCM source ended %d bytes short: %w", e.left, io.ErrUnexpectedEOF)
	}
	return n, err
}

func encodeFFmpeg(ctx context.Context, e Encoding, p pcmSource, meta, path, filter string) error {
	var stderr boundedBuffer
	cmd := exec.CommandContext(ctx, e.Bin, ffmpegArgs(e, meta, path, filter)...)
	cmd.Stdin, cmd.Stderr = p.reader(), &stderr
	if err := cmd.Run(); err != nil {
		return toolError(ToolFFmpeg, err, &stderr)
	}
	return nil
}

// maxPeakPasses bounds the normalised encodes; peakStep is the margin added to each overshoot.
const (
	maxPeakPasses = 3
	peakStep      = 0.1
)

// encodeUnderCeiling normalises p toward target and checks the encoded file: a lossy codec
// (AAC, MP3, Opus) and resampling can lift the true peak above the ceiling loudnorm met on
// the PCM. Each pass that overshoots lowers loudnorm's ceiling by the measured overshoot plus
// peakStep and encodes again, so the file gives up only the headroom its codec needed. A file
// still over the ceiling after maxPeakPasses is an error, never published.
func encodeUnderCeiling(ctx context.Context, e Encoding, p pcmSource, meta, path string, target LoudnessTarget, m Measurement) error {
	aim := target
	for pass := 1; ; pass++ {
		if err := encodeFFmpeg(ctx, e, p, meta, path, normalizeFilter(aim, m)); err != nil {
			return err
		}
		got, err := measureFile(ctx, e, path)
		if err != nil {
			return err
		}
		if got.TP <= target.TP {
			return nil
		}
		if pass == maxPeakPasses {
			return fmt.Errorf("audio: the %s true peak is %.2f dBTP after %d passes, above the %s ceiling of %.1f dBTP",
				e.Format, got.TP, pass, e.Loudness, target.TP)
		}
		aim.TP -= got.TP - target.TP + peakStep
	}
}

// chapterFile writes ffmetadata text to a private temp directory: its path, or "" for no
// chapters, and the cleanup to run after the encoder.
func chapterFile(text string) (string, func(), error) {
	if text == "" {
		return "", func() {}, nil
	}
	dir, err := os.MkdirTemp("", "recite-chapters-")
	if err != nil {
		return "", nil, fmt.Errorf("audio: chapters temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) } // best effort: a private temp directory
	path := filepath.Join(dir, "chapters.txt")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("audio: chapters file: %w", err)
	}
	return path, cleanup, nil
}

func afconvert(ctx context.Context, e Encoding, p pcmSource, path string) error {
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
	werr := WriteWAVFrom(f, p.reader(), p.n)
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
