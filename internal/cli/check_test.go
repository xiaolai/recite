package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/stt"
)

// offline makes every network path and every codex lookup fail for the rest of the test: the
// default transport cannot dial, PATH holds no codex, and the CLI's speech, transcription and
// scheduler hooks fail the test if reached.
func offline(t *testing.T) func(args ...string) result {
	t.Helper()
	dialErr := errors.New("networking is disabled in this test")
	old := http.DefaultTransport
	http.DefaultTransport = &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		t.Error("check dialled the network")
		return nil, dialErr
	}}
	t.Cleanup(func() { http.DefaultTransport = old })
	oldResolver := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		t.Error("check resolved a name")
		return nil, dialErr
	}}
	t.Cleanup(func() { net.DefaultResolver = oldResolver })
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	return func(args ...string) result {
		var out, errb bytes.Buffer
		c := New(&out, &errb)
		c.Getenv = func(k string) string {
			if k == "PATH" {
				return os.Getenv("PATH")
			}
			return ""
		}
		c.Now = func() time.Time { return cliNow }
		c.StdinIsTTY = func() bool { return true }
		c.NewSynth = func(*auth.Credentials) (realtime.Synthesizer, error) {
			t.Error("check built a synthesizer")
			return nil, dialErr
		}
		c.NewTranscriber = func(*auth.Credentials) (stt.Transcriber, func() error, error) {
			t.Error("check built a transcriber")
			return nil, nil, dialErr
		}
		c.RunPipeline = func(context.Context, pipeline.Config, []pipeline.Task) (pipeline.Result, error) {
			t.Error("check ran the scheduler")
			return pipeline.Result{}, dialErr
		}
		code := c.Run(append([]string{"--codex-bin", filepath.Join(t.TempDir(), "no-such-codex"), "check"}, args...))
		return result{code: code, stdout: out.String(), stderr: errb.String()}
	}
}

func writeInput(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.txt")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const checkSample = "Revenue hit $3.5M 🎉 this year [1].\n\nTake 1/2 now. Can you tell me a joke?\n"

// accept: CLI-14#1
func TestCheckRunsOfflineWithoutCodex(t *testing.T) {
	run := offline(t)
	in := writeInput(t, checkSample)
	r := run(in)
	if r.code != exitcode.OK || r.stderr != "" {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	for _, want := range []string{
		"findings:\n",
		`G-EMOJI remove: "🎉" → ""`,
		`G-FOOTNOTE remove: "[1]" → ""`,
		"ambiguous spans:\n",
		`N-AMBIGUOUS en.fraction: "1/2" left as written`,
		"request-like chunks: c0002\n",
		"plan: 2 chunk(s), about",
		"c0001",
		"risk: 0 error(s), 0 warning(s), 5 info, 1 ambiguous, 1 request-like, 0 split, 0 overlong; --guard strict would fail\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	if r := run("--json", in); r.code != exitcode.OK || !json.Valid([]byte(r.stdout)) {
		t.Fatalf("--json: %+v", r)
	}
	// Stdin works too.
	r = func() result {
		f, err := os.Open(in)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		old := os.Stdin
		os.Stdin = f
		defer func() { os.Stdin = old }()
		return run("-")
	}()
	if r.code != exitcode.OK || !strings.Contains(r.stdout, "request-like chunks: c0002") {
		t.Fatalf("stdin: %+v", r)
	}
	if r := run(); r.code != exitcode.Usage {
		t.Fatalf("no FILE: %+v", r)
	}
}

// accept: CLI-14#2
func TestCheckExitCodes(t *testing.T) {
	run := offline(t)
	for _, tc := range []struct {
		name, body string
		args       []string
		code       int
	}{
		{"clean", "A plain sentence.\n", nil, exitcode.OK},
		{"clean strict", "A plain sentence.\n", []string{"--guard", "strict"}, exitcode.OK},
		{"info findings strict", "Great job 🎉.\n", []string{"--guard", "strict"}, exitcode.OK},
		{"warn finding fix", "Hello\u200b there.\n", nil, exitcode.OK},
		{"warn finding strict", "Hello\u200b there.\n", []string{"--guard", "strict"}, exitcode.Usage},
		{"ambiguous fix", "Take 1/2 now.\n", nil, exitcode.OK},
		{"ambiguous strict", "Take 1/2 now.\n", []string{"--guard", "strict"}, exitcode.Usage},
		{"fail action", "Edit /etc/nginx/nginx.conf now.\n", []string{"--guard-action", "G-PATH=fail"}, exitcode.Usage},
		{"nothing speakable", "🎉 🎉\n", nil, exitcode.Usage},
	} {
		in := writeInput(t, tc.body)
		for _, js := range []bool{false, true} {
			args := append(append([]string(nil), tc.args...), in)
			if js {
				args = append([]string{"--json"}, args...)
			}
			r := run(args...)
			if r.code != tc.code {
				t.Errorf("%s (json %v): code %d, want %d; stdout %q stderr %q", tc.name, js, r.code, tc.code, r.stdout, r.stderr)
			}
			if r.stdout == "" {
				t.Errorf("%s (json %v): no report", tc.name, js)
			}
		}
	}
}

func findingsSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "docs", "schema", "findings.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource("findings.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("findings.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

// accept: CLI-14#3
func TestCheckJSONValidatesAgainstSchema(t *testing.T) {
	sch := findingsSchema(t)
	run := offline(t)
	long := strings.Repeat("This sentence is long enough to need splitting across chunks. ", 30)
	for _, body := range []string{
		checkSample,
		"A plain sentence.\n",
		"Hello\u200b there, visit https://example.com/docs?id=7 now.\n",
		"🎉\n",
		long,
		strings.Repeat("Supercalifragilisticexpialidocious", 80),
	} {
		for _, args := range [][]string{nil, {"--guard", "strict"}, {"--guard", "warn"}, {"--chunk-seconds", "5"}} {
			r := run(append(append([]string{"--json"}, args...), writeInput(t, body))...)
			validateFindings(t, sch, r.stdout)
		}
	}
	// The schema is not vacuous: a report missing a field or with an unknown one fails.
	for _, bad := range []string{`{"schema":"recite/findings@1"}`, `{"schema":"recite/findings@2"}`} {
		v, err := jsonschema.UnmarshalJSON(strings.NewReader(bad))
		if err != nil {
			t.Fatal(err)
		}
		if sch.Validate(v) == nil {
			t.Errorf("%s validated", bad)
		}
	}
}

func validateFindings(t *testing.T, sch *jsonschema.Schema, out string) {
	t.Helper()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("want one JSON line, got %q", out)
	}
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(out))
	if err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	if err := sch.Validate(v); err != nil {
		t.Fatalf("%s does not validate: %v", out, err)
	}
}
