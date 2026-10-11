package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/exitcode"
)

func lookNone(string) (string, error) { return "", exec.ErrNotFound }

func lookOnly(tools ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		if slices.Contains(tools, name) {
			return "/fake/bin/" + name, nil
		}
		return "", exec.ErrNotFound
	}
}

func TestParseFormatAndRate(t *testing.T) {
	for _, f := range Formats {
		if got, err := ParseFormat(strings.ToUpper(string(f))); err != nil || got != f {
			t.Errorf("ParseFormat(%s) = %q %v", f, got, err)
		}
		if got, err := FormatFor("dir/x" + f.Ext()); err != nil || got != f {
			t.Errorf("FormatFor(x%s) = %q %v", f.Ext(), got, err)
		}
	}
	for _, bad := range []string{"wma", ""} {
		if _, err := ParseFormat(bad); err == nil {
			t.Errorf("format %q accepted", bad)
		}
	}
	if _, err := FormatFor("noext"); err == nil {
		t.Error("a path without extension has a format")
	}
	// Raw ADTS AAC is refused with a usage error that points to m4a (D20).
	for _, parse := range []func() (Format, error){
		func() (Format, error) { return ParseFormat("AAC") },
		func() (Format, error) { return FormatFor("dir/x.aac") },
	} {
		var ee *EncoderError
		if _, err := parse(); !errors.As(err, &ee) || ee.ExitCode() != exitcode.Usage ||
			!strings.Contains(err.Error(), "raw ADTS AAC output is unsupported") || !strings.Contains(err.Error(), "m4a") {
			t.Errorf("aac: %v", err)
		}
	}
	for _, r := range []string{"16000", "22050", "24000", "44100", "48000"} {
		if _, err := ParseSampleRate(r); err != nil {
			t.Errorf("%s: %v", r, err)
		}
	}
	for _, r := range []string{"8000", "96000", "x", ""} {
		var ee *EncoderError
		if _, err := ParseSampleRate(r); !errors.As(err, &ee) || ee.ExitCode() != exitcode.Usage {
			t.Errorf("%q: %v", r, err)
		}
	}
}

// A missing encoder is a usage error naming ffmpeg; nothing else is needed for native output.
// accept: AUD-04#1
func TestChooseEncoding(t *testing.T) {
	for _, f := range []Format{FormatWAV, FormatPCM} {
		e, err := ChooseEncoding(f, SampleRate, true, "linux", lookNone)
		if err != nil || !e.Native() {
			t.Errorf("%s: %+v %v", f, e, err)
		}
	}
	for _, tc := range []struct {
		f      Format
		rate   int
		stream bool
		goos   string
	}{
		{FormatMP3, SampleRate, false, "linux"},
		{FormatWAV, 16000, false, "linux"},  // resampling needs ffmpeg (AUD-05)
		{FormatM4A, 44100, false, "darwin"}, // afconvert does not resample here
		{FormatM4A, SampleRate, true, "darwin"},
		{FormatOpus, SampleRate, false, "darwin"},
	} {
		_, err := ChooseEncoding(tc.f, tc.rate, tc.stream, tc.goos, lookOnly(ToolAfconvert))
		var ee *EncoderError
		if !errors.As(err, &ee) || ee.ExitCode() != exitcode.Usage || !strings.Contains(err.Error(), "ffmpeg") {
			t.Errorf("%+v: %v", tc, err)
		}
	}
	if e, err := ChooseEncoding(FormatM4A, SampleRate, false, "darwin", lookOnly(ToolAfconvert)); err != nil || e.Tool != ToolAfconvert {
		t.Errorf("afconvert fallback: %+v %v", e, err)
	}
	if e, err := ChooseEncoding(FormatM4A, SampleRate, false, "linux", lookOnly(ToolAfconvert)); err == nil {
		t.Errorf("afconvert off macOS: %+v", e)
	}
	if e, err := ChooseEncoding(FormatM4A, SampleRate, false, "darwin", lookOnly(ToolAfconvert, ToolFFmpeg)); err != nil || e.Tool != ToolFFmpeg || e.Bin != "/fake/bin/ffmpeg" {
		t.Errorf("ffmpeg is preferred: %+v %v", e, err)
	}
	if _, err := ChooseEncoding(FormatOpus, 44100, false, "linux", lookOnly(ToolFFmpeg)); err == nil {
		t.Error("opus at 44100 Hz accepted")
	}
}

func TestFFmpegArgs(t *testing.T) {
	head := "-hide_banner -loglevel error -nostdin -f s16le -ar 24000 -ac 1 -i pipe:0 "
	for _, tc := range []struct {
		f    Format
		rate int
		out  string
		want string
	}{
		{FormatMP3, 24000, "o", "-c:a libmp3lame -b:a 192k -f mp3 -y o"},
		{FormatM4A, 24000, "o", "-c:a aac -b:a 160k -f ipod -y o"},
		{FormatM4B, 24000, "pipe:1", "-c:a aac -b:a 160k -movflags +frag_keyframe+empty_moov -f ipod -y pipe:1"},
		{FormatOpus, 48000, "o", "-ar 48000 -c:a libopus -b:a 64k -f opus -y o"},
		{FormatOgg, 24000, "o", "-c:a libopus -b:a 64k -f ogg -y o"},
		{FormatFLAC, 44100, "o", "-ar 44100 -c:a flac -f flac -y o"},
		{FormatWAV, 16000, "pipe:1", "-ar 16000 -c:a pcm_s16le -f wav -y pipe:1"},
		{FormatPCM, 22050, "o", "-ar 22050 -c:a pcm_s16le -f s16le -y o"},
	} {
		got := strings.Join(FFmpegArgs(Encoding{Format: tc.f, Rate: tc.rate, Tool: ToolFFmpeg}, tc.out), " ")
		if got != head+tc.want {
			t.Errorf("%s@%d:\n got %s\nwant %s", tc.f, tc.rate, got, head+tc.want)
		}
	}
	if got := AfconvertArgs("i", "o"); strings.Join(got, " ") != "-f m4af -d aac -b 160000 i o" {
		t.Errorf("afconvert: %q", got)
	}
}

// fakeFFmpeg writes an encoder that records its argument vector, one argument per line, in
// log and copies stdin to its last argument (or stdout for pipe:1). It lives under a
// directory whose name a shell would split and expand.
func fakeFFmpeg(t *testing.T) (bin, log string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), `tools dir 'q" $(touch pwned)`)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	log = filepath.Join(dir, "args.log")
	bin = filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > \"$FAKE_FFMPEG_LOG\"\n" +
		"for last in \"$@\"; do :; done\nif [ \"$last\" = pipe:1 ]; then exec cat; fi\nexec cat > \"$last\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_FFMPEG_LOG", log)
	return bin, log
}

func loggedArgs(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// The encoder gets an argument vector, not a shell line: a path with spaces, quotes and $(...)
// reaches it as one literal argument, and the PCM arrives on stdin.
func TestEncodeFileWithoutShell(t *testing.T) {
	bin, log := fakeFFmpeg(t)
	dir := filepath.Join(t.TempDir(), `out dir 'a" $(touch pwned)`)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	out := filepath.Join(dir, `my "song" $(touch pwned) & ;.mp3`)
	p := samples(1000)
	e := Encoding{Format: FormatMP3, Rate: SampleRate, Tool: ToolFFmpeg, Bin: bin}
	if err := EncodeFile(context.Background(), e, p, out); err != nil {
		t.Fatal(err)
	}
	if got := loggedArgs(t, log); !slices.Equal(got, FFmpegArgs(e, out)) {
		t.Fatalf("args %q", got)
	}
	if b, err := os.ReadFile(out); err != nil || !bytes.Equal(b, p) {
		t.Fatalf("the PCM did not reach the encoder's stdin: %d bytes, %v", len(b), err)
	}
	for _, d := range []string{dir, filepath.Dir(bin)} {
		if _, err := os.Stat(filepath.Join(d, "pwned")); err == nil {
			t.Fatal("a shell ran the command substitution")
		}
	}
}

func TestEncodeFileReportsFailure(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'Unknown encoder libmp3lame' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := EncodeFile(context.Background(), Encoding{Format: FormatMP3, Rate: SampleRate, Tool: ToolFFmpeg, Bin: bin}, samples(10), filepath.Join(t.TempDir(), "o.mp3"))
	if err == nil || !strings.Contains(err.Error(), "Unknown encoder libmp3lame") {
		t.Fatalf("%v", err)
	}
}

// Native WAV streams with a header whose size fields hold the maximum.
// accept: AUD-07#1
func TestStreamWriterNativeWAV(t *testing.T) {
	var b bytes.Buffer
	s, err := NewStreamWriter(context.Background(), Encoding{Format: FormatWAV, Rate: SampleRate}, &b)
	if err != nil {
		t.Fatal(err)
	}
	p := samples(300)
	if _, err := s.Write(p); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil || s.Close() != nil {
		t.Fatal(err)
	}
	h := b.Bytes()
	le := binary.LittleEndian
	if string(h[0:4]) != "RIFF" || le.Uint32(h[4:]) != 0xFFFFFFFF || string(h[36:40]) != "data" || le.Uint32(h[40:]) != 0xFFFFFFFF ||
		le.Uint32(h[24:]) != SampleRate || !bytes.Equal(h[44:], p) {
		t.Fatalf("stream header % x", h[:44])
	}
	if _, err := s.Write(p); err == nil {
		t.Fatal("write after close accepted")
	}
}

// Other formats stream through an ffmpeg pipe to the destination.
// accept: AUD-07#1
func TestStreamWriterFFmpegPipe(t *testing.T) {
	bin, log := fakeFFmpeg(t)
	var b bytes.Buffer
	e := Encoding{Format: FormatMP3, Rate: 16000, Tool: ToolFFmpeg, Bin: bin}
	s, err := NewStreamWriter(context.Background(), e, &b)
	if err != nil {
		t.Fatal(err)
	}
	p := samples(500)
	if _, err := s.Write(p); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b.Bytes(), p) || !slices.Equal(loggedArgs(t, log), FFmpegArgs(e, "pipe:1")) {
		t.Fatalf("%d bytes, args %q", b.Len(), loggedArgs(t, log))
	}
}
