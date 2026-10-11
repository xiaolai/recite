package cli

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/realtime"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
	"github.com/xiaolai/recite/internal/stt"
)

func noTools(string) (string, error) { return "", exec.ErrNotFound }

// fakeEncoder installs an ffmpeg that records its argument vector, one argument per line, and
// copies stdin to its last argument (or stdout for pipe:1). A loudness analysis pass (output
// format null) is logged to args.log.measure instead, and prints loudnorm's JSON with an
// input of -22 LUFS. It lives in a directory whose name a shell would split and expand.
func fakeEncoder(t *testing.T) (look func(string) (string, error), args func() []string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), `bin dir 'q" $(touch pwned)`)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	log, bin := filepath.Join(dir, "args.log"), filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nfor a in \"$@\"; do if [ \"$a\" = null ]; then\n" +
		"for b in \"$@\"; do printf '%s\\n' \"$b\"; done > \"$FAKE_FFMPEG_LOG.measure\"\ncat > /dev/null\n" +
		"printf '[Parsed_loudnorm_0]\\n{\\n\"input_i\" : \"-22.00\",\\n\"input_tp\" : \"-6.50\",\\n\"input_lra\" : \"3.20\",\\n" +
		"\"input_thresh\" : \"-32.10\",\\n\"target_offset\" : \"0.40\"\\n}\\n' >&2\nexit 0\nfi; done\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\"; done > \"$FAKE_FFMPEG_LOG\"\n" +
		"for last in \"$@\"; do :; done\nlast=${last#file:}\nif [ \"$last\" = pipe:1 ]; then exec cat; fi\nexec cat > \"$last\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_FFMPEG_LOG", log)
	look = func(name string) (string, error) {
		if name == audio.ToolFFmpeg {
			return bin, nil
		}
		return "", exec.ErrNotFound
	}
	args = func() []string {
		t.Helper()
		b, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("the encoder never ran: %v", err)
		}
		return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	}
	return look, args
}

// With no encoder, an encoded output or a resampled WAV is exit 2 naming ffmpeg, before the
// login probe: the codex executable does not exist, so a probe would fail differently.
// accept: AUD-04#1
func TestSayMissingEncoderExit2BeforeAnyRequest(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"Hi", "-o", "x.mp3"},
		{"Hi", "--format", "flac"},
		{"Hi", "-o", "x.wav", "--sample-rate", "16000"},
		{"Hi", "-o", "-", "--format", "opus"},
	} {
		var out, errb bytes.Buffer
		c := New(&out, &errb)
		c.Getenv = func(string) string { return "" }
		c.Now = func() time.Time { return cliNow }
		c.StdinIsTTY = func() bool { return true }
		c.LookPath = noTools
		synths := 0
		c.NewSynth = func(*auth.Credentials) (realtime.Synthesizer, error) { synths++; return nil, fmt.Errorf("unreachable") }
		c.NewTranscriber = func(*auth.Credentials) (stt.Transcriber, func() error, error) {
			return nil, nil, fmt.Errorf("unreachable")
		}
		full := append([]string{"--codex-bin", filepath.Join(t.TempDir(), "no-codex"), "--auth-source", "codex", "say"}, args...)
		code := c.Run(full)
		if code != exitcode.Usage || !strings.Contains(errb.String(), "needs ffmpeg") || synths != 0 || out.Len() != 0 {
			t.Errorf("%q: code %d, %d synths, stderr %q", args, code, synths, errb.String())
		}
		if entries, _ := os.ReadDir("."); len(entries) != 0 {
			t.Errorf("%q: files written: %v", args, entries)
		}
	}
}

// Raw ADTS AAC is refused as exit 2 pointing to m4a, before the login probe and with every
// encoder available, so the refusal is the format's and not a missing tool's (D20).
// accept: AUD-04#3
func TestSayRawAACExit2BeforeAnyRequest(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"Hi", "-o", "x.aac"},
		{"Hi", "-o", "x.AAC"},
		{"Hi", "--format", "aac"},
		{"Hi", "-o", "-", "--format", "aac"},
	} {
		var out, errb bytes.Buffer
		c := New(&out, &errb)
		c.Getenv = func(string) string { return "" }
		c.Now = func() time.Time { return cliNow }
		c.StdinIsTTY = func() bool { return true }
		c.LookPath = func(name string) (string, error) { return "/fake/bin/" + name, nil }
		synths := 0
		c.NewSynth = func(*auth.Credentials) (realtime.Synthesizer, error) { synths++; return nil, fmt.Errorf("unreachable") }
		c.NewTranscriber = func(*auth.Credentials) (stt.Transcriber, func() error, error) {
			return nil, nil, fmt.Errorf("unreachable")
		}
		full := append([]string{"--codex-bin", filepath.Join(t.TempDir(), "no-codex"), "--auth-source", "codex", "say"}, args...)
		code := c.Run(full)
		if code != exitcode.Usage || !strings.Contains(errb.String(), "raw ADTS AAC output is unsupported") ||
			!strings.Contains(errb.String(), "m4a") || synths != 0 || out.Len() != 0 {
			t.Errorf("%q: code %d, %d synths, stderr %q", args, code, synths, errb.String())
		}
		if entries, _ := os.ReadDir("."); len(entries) != 0 {
			t.Errorf("%q: files written: %v", args, entries)
		}
	}
}

// The encoder is executed with an argument vector, never through a shell: an output path
// with spaces, quotes and $(...) reaches it literally, as the temp file next to the target,
// and the assembled PCM arrives on its stdin.
// accept: AUD-04#1
func TestSayEncoderArgsWithoutShell(t *testing.T) {
	m := newMultiEnv(t)
	look, args := fakeEncoder(t)
	m.look = look
	name := `my "talk" $(touch pwned) & 'x'.mp3`
	r := m.run(threeParagraphs, "-o", name, "--gap-paragraph", "300ms", "--sample-rate", "44100", "--loudness", "none")
	if r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	got := args()
	tmp, ok := strings.CutPrefix(got[len(got)-1], "file:")
	if !ok || filepath.Dir(tmp) != filepath.Dir(name) || !strings.HasPrefix(filepath.Base(tmp), "."+name+".") {
		t.Fatalf("encoder output %q, want a temp file beside %q", tmp, name)
	}
	want := audio.FFmpegArgs(audio.Encoding{Format: audio.FormatMP3, Rate: 44100, Tool: audio.ToolFFmpeg}, tmp)
	if !slices.Equal(got, want) {
		t.Fatalf("args\n got %q\nwant %q", got, want)
	}
	b, err := os.ReadFile(m.path(name))
	if err != nil || !bytes.Equal(b, expectedOutput(m.lastTasks(), 1, 7200)) {
		t.Fatalf("the output is not the encoder's output of the assembly: %d bytes, %v", len(b), err)
	}
	if _, err := os.Stat(m.path("pwned")); err == nil {
		t.Fatal("a shell ran the command substitution")
	}
	var man struct {
		Output struct {
			Format     string `json:"format"`
			SampleRate int    `json:"sample_rate"`
		} `json:"output"`
	}
	mb, err := os.ReadFile(m.path(name + ".json"))
	if err != nil || json.Unmarshal(mb, &man) != nil || man.Output.Format != "mp3" || man.Output.SampleRate != 44100 {
		t.Fatalf("manifest output %+v: %v", man.Output, err)
	}
}

// streamHeader checks the streaming WAV header: size fields at the maximum, internal format.
func streamHeader(t *testing.T, b []byte) []byte {
	t.Helper()
	le := binary.LittleEndian
	if len(b) < 44 || string(b[0:4]) != "RIFF" || le.Uint32(b[4:]) != 0xFFFFFFFF || string(b[8:16]) != "WAVEfmt " ||
		le.Uint32(b[24:]) != audio.SampleRate || string(b[36:40]) != "data" || le.Uint32(b[40:]) != 0xFFFFFFFF {
		t.Fatalf("not a streaming WAV header: % x", b[:min(len(b), 44)])
	}
	return b[44:]
}

// -o - streams WAV to stdout: stdout holds the header and the assembly and nothing else (logs
// go to stderr), and no file is written, not even a manifest.
// accept: AUD-07#1
func TestSayStreamsToStdout(t *testing.T) {
	m := newMultiEnv(t)
	m.look = noTools // native WAV needs no encoder
	r := m.run(threeParagraphs, "-o", "-", "--gap-paragraph", "300ms", "-v")
	if r.code != exitcode.OK {
		t.Fatalf("%+v", r.stderr)
	}
	if pcm := streamHeader(t, []byte(r.stdout)); !bytes.Equal(pcm, expectedOutput(m.lastTasks(), 1, 7200)) {
		t.Fatalf("stdout holds %d bytes of audio, not the assembly", len(pcm))
	}
	if !strings.Contains(r.stderr, "streamed output") {
		t.Fatalf("the log is not on stderr: %q", r.stderr)
	}
	if entries, _ := os.ReadDir(m.dir); len(entries) != 0 {
		t.Fatalf("files written while streaming: %v", entries)
	}
	// --manifest still writes one, recording the stream.
	r = m.run(threeParagraphs, "-o", "-", "--manifest", "run.json")
	var man struct {
		Status string `json:"status"`
		Output struct {
			Path string `json:"path"`
		} `json:"output"`
	}
	b, err := os.ReadFile(m.path("run.json"))
	if r.code != exitcode.OK || err != nil || json.Unmarshal(b, &man) != nil || man.Output.Path != "-" || man.Status != "complete" {
		t.Fatalf("code %d, manifest %s, %v", r.code, b, err)
	}
}

// Other formats, and WAV at another rate, stream through an ffmpeg pipe to stdout.
// accept: AUD-07#1
func TestSayStreamsThroughEncoder(t *testing.T) {
	m := newMultiEnv(t)
	look, args := fakeEncoder(t)
	m.look = look
	r := m.run(threeParagraphs, "-o", "-", "--format", "mp3", "--sample-rate", "16000", "--gap-paragraph", "300ms")
	if r.code != exitcode.OK {
		t.Fatalf("%+v", r.stderr)
	}
	want := audio.FFmpegArgs(audio.Encoding{Format: audio.FormatMP3, Rate: 16000, Tool: audio.ToolFFmpeg}, "pipe:1")
	if got := args(); !slices.Equal(got, want) {
		t.Fatalf("args %q", got)
	}
	if !bytes.Equal([]byte(r.stdout), expectedOutput(m.lastTasks(), 1, 7200)) {
		t.Fatalf("stdout is not the encoder's output: %d bytes", len(r.stdout))
	}
}

// Streaming cannot take bytes back: --json, --strict-output and a two-pass --loudness with
// -o - are exit 2 before any network call, with every encoder available.
// accept: AUD-07#2
func TestSayStreamConflictsExit2(t *testing.T) {
	m := newMultiEnv(t)
	m.look = func(name string) (string, error) { return "/fake/bin/" + name, nil }
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--json"}, "-o - and --json exclude each other"},
		{[]string{"--strict-output"}, "-o - and --strict-output exclude each other"},
		{[]string{"--loudness", "podcast"}, "-o - and --loudness podcast exclude each other"},
		{[]string{"--loudness", "audiobook", "--format", "mp3"}, "-o - and --loudness audiobook exclude each other"},
	} {
		r := m.run(append([]string{"Hi", "-o", "-"}, tc.args...)...)
		// Under --json stdout is the event stream, so it holds only the done event (CLI-06).
		wantOut := ""
		if tc.args[0] == "--json" {
			wantOut = `{"event":"done","exit":2}` + "\n"
		}
		if r.code != exitcode.Usage || !strings.Contains(r.stderr, tc.want) || r.stdout != wantOut {
			t.Errorf("%q: code %d stdout %d bytes stderr %q", tc.args, r.code, len(r.stdout), r.stderr)
		}
	}
	m.untouched()
}

// The last chunk failing after earlier chunks streamed ends the stream after the delivered
// prefix, and the exit code is 5.
// accept: AUD-07#3
func TestSayStreamLateFailureExit5(t *testing.T) {
	m := newMultiEnv(t)
	m.synth.On("c0003", rtfake.Outcome{Echo: true, Duration: time.Second, Status: realtime.StatusIncomplete})
	r := m.run(threeParagraphs, "-o", "-", "--gap-paragraph", "300ms", "--max-attempts", "1")
	if r.code != exitcode.VerifyFailed {
		t.Fatalf("code %d, stderr %q", r.code, r.stderr)
	}
	tasks := m.lastTasks()
	want := append(expectedOutput(tasks[:2], 1, 7200), make([]byte, 7200*audio.BytesPerSample)...)
	if pcm := streamHeader(t, []byte(r.stdout)); !bytes.Equal(pcm, want) {
		t.Fatalf("stdout holds %d bytes of audio, want the %d-byte prefix of chunks 1 and 2", len(pcm), len(want))
	}
}
