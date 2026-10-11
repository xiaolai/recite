package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/usage"
	"github.com/xiaolai/recite/internal/verify"
)

// events checks that every stdout line of r validates against the event schema and that the
// last one is done with r's exit code (CLI-06), and returns the decoded events.
func events(t *testing.T, r result) []map[string]any {
	t.Helper()
	sch := eventsSchema(t)
	if !strings.HasSuffix(r.stdout, "\n") {
		t.Fatalf("stdout does not end in a line: %q", r.stdout)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n") {
		validate(t, sch, line)
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	last := out[len(out)-1]
	if last["event"] != "done" || last["exit"] != float64(r.code) {
		t.Fatalf("last event %v, want done with exit %d", last, r.code)
	}
	return out
}

// states lists the chunk events as "id state", in order.
func states(evs []map[string]any) []string {
	var s []string
	for _, ev := range evs {
		if ev["event"] == "chunk" {
			s = append(s, ev["id"].(string)+" "+ev["state"].(string))
		}
	}
	return s
}

// Every line of `say --json` is a schema-valid event and the last is done with the exit code,
// on success, after a rejection, from the cache, on a usage error and on a login failure.
// accept: CLI-06#1
func TestSayJSONEventsValidateAndEndWithDone(t *testing.T) {
	m := newMultiEnv(t)
	m.env = map[string]string{"RECITE_CACHE_DIR": m.path("cache")}
	r := m.run(threeParagraphs, "-o", "a.wav", "--json")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	evs := events(t, r)
	if evs[0]["event"] != "plan" || evs[0]["chunks"] != 3.0 || evs[0]["cached"] != 0.0 || evs[0]["dry_run"] != false {
		t.Fatalf("first event %v, want the plan", evs[0])
	}
	want := []string{"c0001 rendered", "c0001 verified", "c0002 rendered", "c0002 verified", "c0003 rendered", "c0003 verified"}
	if got := states(evs); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("chunk events %v, want %v", got, want)
	}
	if done := evs[len(evs)-1]; done["output"] != "a.wav" || done["duration_s"].(float64) <= 0 {
		t.Fatalf("done %v", done)
	}

	// Second run: every chunk from the cache.
	r = m.run(threeParagraphs, "-o", "b.wav", "--json")
	evs = events(t, r)
	if evs[0]["cached"] != 3.0 || evs[1]["cached"] != true || len(states(evs)) != 3 {
		t.Fatalf("cached run events %v", evs)
	}

	// A misheard first take: rendered, rejected with its reason, rendered, verified.
	m = newMultiEnv(t)
	m.tr.mishearAttempt("c0001", 1, "something else entirely")
	r = m.run("One short sentence to say.", "-o", "x.wav", "--json")
	evs = events(t, r)
	want = []string{"c0001 rendered", "c0001 rejected", "c0001 rendered", "c0001 verified"}
	if got := states(evs); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("chunk events %v, want %v (stderr %q)", got, want, r.stderr)
	}

	// Unverified after every attempt: exit 5, still ending in done.
	m = newMultiEnv(t)
	m.tr.mishear("c0001", "something else entirely")
	r = m.run("One short sentence to say.", "-o", "y.wav", "--max-attempts", "1", "--json")
	if r.code != exitcode.VerifyFailed {
		t.Fatalf("code %d", r.code)
	}
	events(t, r)

	// Usage errors, before and after flag parsing, and a login failure.
	for _, args := range [][]string{
		{"Hi", "--json", "--jobs", "9"},
		{"Hi", "--json", "--no-such-flag"},
		{"--json", "Hi", "-o", "-"},
		{"Hi", "--json", "-o", "z.wav", "--codex-bin", m.path("no-codex")},
	} {
		r := m.run(args...)
		if r.code == exitcode.OK {
			t.Fatalf("%v: exit 0", args)
		}
		events(t, r)
	}
}

// The progress line is ANSI-free off a TTY, with --no-color and with NO_COLOR; on a colour TTY
// it erases the line, which shows the check above is not vacuous. -q turns it off.
// accept: CLI-07#1
func TestProgressNoANSIOffTTYOrNoColor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tty      bool
		env      map[string]string
		args     []string
		ansi     bool
		progress bool
	}{
		{name: "pipe", progress: false},
		{name: "tty", tty: true, ansi: true, progress: true},
		{name: "tty NO_COLOR", tty: true, env: map[string]string{"NO_COLOR": "1"}, progress: true},
		{name: "tty --no-color", tty: true, args: []string{"--no-color"}, progress: true},
		{name: "tty -q", tty: true, args: []string{"-q"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMultiEnv(t)
			m.stderrTTY, m.env = tc.tty, tc.env
			r := m.run(append([]string{threeParagraphs, "-o", "a.wav"}, tc.args...)...)
			if r.code != exitcode.OK {
				t.Fatalf("code %d stderr %q", r.code, r.stderr)
			}
			if got := strings.Contains(r.stderr, "\x1b"); got != tc.ansi {
				t.Fatalf("ANSI in stderr %v, want %v: %q", got, tc.ansi, r.stderr)
			}
			if got := strings.Contains(r.stderr, "\rrecite: chunks 3/3, verified 3, retried 0, failed 0"); got != tc.progress {
				t.Fatalf("progress line %v, want %v: %q", got, tc.progress, r.stderr)
			}
		})
	}
}

// Off a TTY the progress is a plain line at most every progressInterval, with the design §14.3
// fields; on a TTY without colour, a shorter line pads over the longer one.
// accept: CLI-07#1
func TestProgressPlainPeriodicLines(t *testing.T) {
	start := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	now := start
	var b bytes.Buffer
	p := &progress{w: &b, now: func() time.Time { return now }, start: start, last: start, total: 3, estTotal: 30}
	chunk := verify.ChunkResult{State: verify.StateVerified, PCM: make([]byte, 10*48000)} // 10 s
	now = start.Add(5 * time.Second)
	p.chunk(chunk, 10)
	if b.Len() != 0 {
		t.Fatalf("printed before the interval: %q", b.String())
	}
	now = start.Add(10 * time.Second)
	p.chunk(verify.ChunkResult{State: verify.StateUnverified, PCM: make([]byte, 10*48000), Attempts: make([]verify.Attempt, 2)}, 10)
	want := "recite: chunks 2/3, verified 1, retried 1, failed 1, audio 20s, 2.0x real time, ETA 5s\n"
	if b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
	p.finish()
	if strings.Contains(b.String(), "\x1b") || b.String() != want {
		t.Fatalf("finish wrote %q", b.String())
	}

	b.Reset()
	q := &progress{w: &b, tty: true, now: func() time.Time { return start }, start: start, total: 1}
	q.draw("long line")
	q.draw("short")
	q.finish()
	if got := b.String(); got != "\rlong line\rshort    \n" {
		t.Fatalf("TTY without ANSI: %q", got)
	}
}

// --dry-run plans and estimates with no codex binary and nothing on the network: no synth,
// no transcriber, no scheduler. --json emits the plan event, then done.
// accept: RUN-06#1
func TestDryRunNeedsNoCodexNorNetwork(t *testing.T) {
	m := newMultiEnv(t)
	m.env = map[string]string{"RECITE_CACHE_DIR": m.path("cache"), "PATH": "", "HTTPS_PROXY": "http://127.0.0.1:1"}
	noCodex := []string{"--codex-bin", m.path("no-codex"), "--auth-source", "codex"}
	r := m.run(append([]string{threeParagraphs, "--dry-run", "--jobs", "2"}, noCodex...)...)
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	for _, want := range []string{"chunks:     3\n", "cached:     0\n", "audio:      about ", "at --jobs 2\n", "price:      $", usage.Note} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	m.untouched()
	if _, err := os.Stat(m.path("cache")); !os.IsNotExist(err) {
		t.Fatalf("the dry run created the cache: %v", err)
	}
	if ents, _ := os.ReadDir(m.dir); len(ents) != 0 {
		t.Fatalf("the dry run wrote %v", ents)
	}

	r = m.run(append([]string{threeParagraphs, "--dry-run", "--json"}, noCodex...)...)
	evs := events(t, r)
	if len(evs) != 2 || evs[0]["event"] != "plan" || evs[0]["dry_run"] != true || evs[0]["chunks"] != 3.0 {
		t.Fatalf("events %v", evs)
	}
	m.untouched()

	// The estimate follows the plan: audio left / (jobs × per-worker rate), priced from the
	// list price with one transcription.
	est, jobs := evs[0]["est_seconds"].(float64), evs[0]["jobs"].(float64)
	if wall := evs[0]["est_wall_seconds"].(float64); math.Abs(wall-round3(est/(realtimePerWorker*jobs))) > 1e-9 {
		t.Fatalf("wall %v for %v s at %v jobs", wall, est, jobs)
	}
	usd, _ := usage.ListPrices.EstimateSpeech("gpt-realtime-2.1", stt.HTTPModel, est)
	if got := evs[0]["estimate_usd"].(float64); math.Abs(got-usd) > 1e-6 {
		t.Fatalf("estimate %v, want %v", got, usd)
	}
}

// After a run fills the cache, the dry run counts the hits and prices only what is left.
// accept: RUN-06#1
func TestDryRunCountsCacheHits(t *testing.T) {
	m := newMultiEnv(t)
	m.env = map[string]string{"RECITE_CACHE_DIR": m.path("cache")}
	if r := m.run(threeParagraphs, "-o", "a.wav"); r.code != exitcode.OK {
		t.Fatalf("first: %+v", r)
	}
	renders := len(m.synth.Calls())
	before := cacheFiles(t, m.path("cache"))
	r := m.run(threeParagraphs, "--dry-run", "--json")
	evs := events(t, r)
	if evs[0]["cached"] != 3.0 || evs[0]["est_wall_seconds"] != 0.0 || evs[0]["estimate_usd"] != 0.0 {
		t.Fatalf("plan %v", evs[0])
	}
	if len(m.synth.Calls()) != renders {
		t.Fatal("the dry run rendered")
	}
	// Counting hits records no access, so a dry run never changes what pruning keeps.
	if after := cacheFiles(t, m.path("cache")); !maps.Equal(before, after) {
		t.Fatalf("the dry run changed the cache:\nbefore %v\nafter  %v", before, after)
	}
}

// cacheFiles maps every file under dir to its size and modification time.
func cacheFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		files[path] = fmt.Sprint(fi.Size(), fi.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
