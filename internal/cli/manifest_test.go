package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/manifest"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/pipeline"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/usage"
)

func manifestSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open(filepath.Join(repoRoot(t), "docs", "schema", "manifest.schema.json"))
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
	if err := c.AddResource("manifest.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("manifest.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

// repoRoot is two levels above this package; tests chdir into temp dirs, so it is resolved
// from the source file's package directory captured at init.
func repoRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(pkgDir, "..", "..")
}

var pkgDir = func() string {
	d, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return d
}()

// readManifest decodes a manifest strictly.
func readManifest(t *testing.T, path string) manifest.Manifest {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	m, err := manifest.Read(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

// validManifest validates the manifest at path against the published schema and decodes it.
func validManifest(t *testing.T, sch *jsonschema.Schema, path string) manifest.Manifest {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := sch.Validate(doc); err != nil {
		t.Fatalf("%s does not validate: %v\n%s", path, err, b)
	}
	return readManifest(t, path)
}

const unrelated = "Completely unrelated words were heard instead of the paragraph."

// Every kind of fake run writes a manifest that validates: verified, unverified (exit 5 with
// a file), no output under --strict-output, --verify off, a custom --manifest path, and an
// interrupted run's partial state.
// accept: OUT-02#1
func TestManifestValidatesAgainstSchema(t *testing.T) {
	sch := manifestSchema(t)
	m := newMultiEnv(t)
	if r := m.run(threeParagraphs, "-o", "ok.wav"); r.code != exitcode.OK {
		t.Fatalf("ok: %+v", r)
	}
	if got := validManifest(t, sch, m.path("ok.wav.json")); got.Status != manifest.StatusComplete || got.Summary.Verified != 3 ||
		got.Output == nil || got.Output.Path != "ok.wav" || got.Verifier.Route != "http" {
		t.Fatalf("ok manifest: %+v", got)
	}

	m.tr.mishear("c0002", unrelated)
	if r := m.run(threeParagraphs, "-o", "unv.wav", "--max-attempts", "2"); r.code != exitcode.VerifyFailed {
		t.Fatalf("unverified: %+v", r)
	}
	got := validManifest(t, sch, m.path("unv.wav.json"))
	if got.Status != manifest.StatusComplete || got.Summary.Unverified != 1 || got.Chunks[1].State != "unverified" || got.Chunks[1].Verified {
		t.Fatalf("unverified manifest: %+v", got.Summary)
	}
	if r := m.run(threeParagraphs, "-o", "strict.wav", "--max-attempts", "1", "--strict-output"); r.code != exitcode.VerifyFailed {
		t.Fatalf("strict: %+v", r)
	}
	if got := validManifest(t, sch, m.path("strict.wav.json")); got.Status != manifest.StatusNoOutput || got.Output != nil {
		t.Fatalf("strict manifest: status %q output %+v", got.Status, got.Output)
	}
	if _, err := os.Stat(m.path("strict.wav")); !os.IsNotExist(err) {
		t.Fatalf("strict output written: %v", err)
	}

	if r := m.run(threeParagraphs, "-o", "off.wav", "--verify", "off", "--allow-unverified", "--manifest", "custom.json"); r.code != exitcode.OK {
		t.Fatalf("off: %+v", r)
	}
	if got := validManifest(t, sch, m.path("custom.json")); got.Verifier.Route != "none" || got.Verifier.STTModel != nil || got.Summary.Unverified != 3 {
		t.Fatalf("off manifest: %+v %+v", got.Verifier, got.Summary)
	}
	if _, err := os.Stat(m.path("off.wav.json")); !os.IsNotExist(err) {
		t.Fatalf("--manifest PATH also wrote the default: %v", err)
	}
	m.tr.mu.Lock()
	delete(m.tr.said, "c0002")
	m.tr.mu.Unlock()
	if r := m.run(threeParagraphs, "-o", "none.wav", "--no-manifest"); r.code != exitcode.OK {
		t.Fatalf("--no-manifest: %+v", r)
	}
	if _, err := os.Stat(m.path("none.wav.json")); !os.IsNotExist(err) {
		t.Fatalf("--no-manifest wrote one: %v", err)
	}
	if r := m.run("Hi", "--manifest", "x.json", "--no-manifest"); r.code != exitcode.Usage {
		t.Fatalf("both flags: %+v", r)
	}
	// An existing manifest of a finished run is not replaced without --force.
	if r := m.run(threeParagraphs, "-o", "fresh.wav", "--manifest", "ok.wav.json"); r.code != exitcode.Usage || !strings.Contains(r.stderr, "already exists") {
		t.Fatalf("existing manifest: %+v", r)
	}
}

// interruptedRun interrupts a five-chunk run once its first two chunks are cached, as
// TestSaySIGINTKeepsVerifiedTakesInCache does.
func interruptedRun(t *testing.T, m *multiEnv, root string, args ...string) result {
	t.Helper()
	m.sigs = make(chan os.Signal, 2)
	m.drain = 50 * time.Millisecond
	m.synth.Hook = func(ctx context.Context, c rtfake.RenderCall) error {
		if c.Request.ChunkID != "c0003" {
			return nil
		}
		for len(entries(t, root)) < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
		m.sigs <- os.Interrupt
		<-ctx.Done()
		return ctx.Err()
	}
	r := m.run(append([]string{fiveParagraphs}, args...)...)
	m.synth.Hook, m.sigs = nil, nil
	return r
}

const fiveParagraphs = threeParagraphs + "\n\nA fourth paragraph.\n\nA fifth paragraph."

// checkTimeline asserts that each chunk's start_s/end_s is where the assembly put its audio in
// the output, that gap_after_s is the silence before the next chunk, and that the last chunk
// ends the file.
func checkTimeline(t *testing.T, m manifest.Manifest, wav audio.PCM, tasks []pipeline.Task) {
	t.Helper()
	sample := func(s *float64) int64 {
		if s == nil {
			t.Fatal("a finished chunk has no timeline")
		}
		return int64(math.Round(*s * audio.SampleRate))
	}
	var cursor int64
	for i, c := range m.Chunks {
		start, end := sample(c.StartS), sample(c.EndS)
		if start != cursor || end < start {
			t.Fatalf("%s: [%d, %d) samples, want start %d", c.ID, start, end, cursor)
		}
		want := audio.Fade(rtfake.TaggedPCM(c.ID, c.FinalAttempt, tasks[i].Chunk.Estimate))
		if !bytes.Equal(wav[start*audio.BytesPerSample:end*audio.BytesPerSample], want) {
			t.Fatalf("%s: the output at [%d, %d) is not its take #%d", c.ID, start, end, c.FinalAttempt)
		}
		gap := sample(c.GapAfterS)
		for _, b := range wav[end*audio.BytesPerSample : (end+gap)*audio.BytesPerSample] {
			if b != 0 {
				t.Fatalf("%s: the gap after it is not silence", c.ID)
			}
		}
		if i == len(m.Chunks)-1 && gap != 0 {
			t.Fatalf("gap after the last chunk: %d", gap)
		}
		cursor = end + gap
	}
	if want := int64(len(wav) / audio.BytesPerSample); cursor != want {
		t.Fatalf("timeline ends at %d samples, the output has %d", cursor, want)
	}
	if m.Output == nil || math.Abs(m.Output.DurationS-float64(cursor)/audio.SampleRate) > 1e-9 {
		t.Fatalf("output duration %+v, timeline %d samples", m.Output, cursor)
	}
}

// accept: OUT-02#3
func TestManifestTimelineEqualsAssembly(t *testing.T) {
	m := newMultiEnv(t)
	m.tr.mishearAttempt("c0002", 1, unrelated) // c0002 keeps its second take
	r := m.run(fiveParagraphs, "-o", "out.wav", "--gap-paragraph", "300ms..900ms", "--gap-seed", "7")
	if r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	got := readManifest(t, m.path("out.wav.json"))
	if len(got.Chunks) != 5 || got.Chunks[1].FinalAttempt != 2 {
		t.Fatalf("chunks %+v", got.Chunks)
	}
	checkTimeline(t, got, readWAV(t, m.path("out.wav")), m.lastTasks())
}

// Interrupt, then re-run the same command: the partial manifest is replaced, the chunks
// verified before the interrupt are reused from the cache, and the manifest timeline matches
// the output assembled from the takes actually used.
// accept: RUN-05#1
func TestResumedRunManifestTimeline(t *testing.T) {
	sch := manifestSchema(t)
	m := newMultiEnv(t)
	root := m.path("cache")
	m.env = map[string]string{"RECITE_CACHE_DIR": root}
	if r := interruptedRun(t, m, root, "-o", "out.wav", "--jobs", "1"); r.code != exitcode.Interrupted {
		t.Fatalf("first run: %+v", r)
	}
	partial := validManifest(t, sch, m.path("out.wav.json"))
	if partial.Status != manifest.StatusInterrupted || partial.Output != nil || partial.Summary.Pending == 0 {
		t.Fatalf("partial manifest: %q %+v", partial.Status, partial.Summary)
	}
	renders := len(m.synth.Calls())
	r := m.run(fiveParagraphs, "-o", "out.wav", "--jobs", "1")
	if r.code != exitcode.OK {
		t.Fatalf("re-run: %+v", r)
	}
	got := validManifest(t, sch, m.path("out.wav.json"))
	if got.Status != manifest.StatusComplete {
		t.Fatalf("status %q", got.Status)
	}
	for i, c := range got.Chunks {
		if want := i < 2; c.Cached != want {
			t.Errorf("%s cached %v, want %v", c.ID, c.Cached, want)
		}
	}
	if n := len(m.synth.Calls()) - renders; n != 3 || got.Summary.Renders != 3 || got.Summary.Cached != 2 {
		t.Fatalf("re-run rendered %d, manifest renders %d cached %d", n, got.Summary.Renders, got.Summary.Cached)
	}
	checkTimeline(t, got, readWAV(t, m.path("out.wav")), m.lastTasks())
}

// deniedTranscriber is an HTTP verifier route this sign-in may not use.
type deniedTranscriber struct{}

func (deniedTranscriber) Transcribe(context.Context, []byte, stt.Hint) (stt.Transcript, error) {
	return stt.Transcript{}, &openai.Error{Class: openai.ClassRouteDenied, Route: openai.RouteHTTPSTT, Message: "route denied"}
}

// The VER-08 attempt record and the VER-02 fallback appear in the manifest.
// accept: VER-02#2
// accept: OUT-02#1
func TestManifestRecordsAttemptsAndFallback(t *testing.T) {
	sch := manifestSchema(t)
	m := newMultiEnv(t)
	m.wrapTr = func(tr stt.Transcriber) stt.Transcriber {
		return &stt.Chain{Primary: deniedTranscriber{}, Fallback: tr}
	}
	m.tr.mishearAttempt("c0002", 1, unrelated)
	if r := m.run(threeParagraphs, "-o", "out.wav"); r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	got := validManifest(t, sch, m.path("out.wav.json"))
	v := got.Verifier
	if v.Route != "realtime" || v.STTModel == nil || *v.STTModel != stt.RealtimeModel || v.Fallback == nil ||
		v.Fallback.From != "http" || v.Fallback.To != "realtime" || !strings.Contains(v.Fallback.Cause, "route denied") {
		t.Fatalf("verifier %+v fallback %+v", v, v.Fallback)
	}
	c := got.Chunks[1]
	if c.Attempts != 2 || c.FinalAttempt != 2 || c.STTCalls != 2 || c.State != "verified" || len(c.AttemptLog) != 2 {
		t.Fatalf("c0002: %+v", c)
	}
	first, second := c.AttemptLog[0], c.AttemptLog[1]
	if first.N != 1 || !first.Eligible || first.Rejection == nil || first.Rejection.Reason == "" || len(first.STTScores) != 1 ||
		second.N != 2 || second.Rejection != nil || second.Score != 1 {
		t.Fatalf("attempt log %+v / %+v", first, second)
	}
	if got.Summary.Renders != 4 || got.Summary.STTCalls != 4 {
		t.Fatalf("summary %+v", got.Summary)
	}

	// Without a fallback the record says so.
	m.wrapTr = nil
	if r := m.run(threeParagraphs, "-o", "plain.wav"); r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	if got := validManifest(t, sch, m.path("plain.wav.json")); got.Verifier.Fallback != nil || got.Verifier.Route != "http" {
		t.Fatalf("verifier %+v", got.Verifier)
	}
}

// A run with two scripted rejections writes exactly two <chunk>-a<attempt> pairs, private,
// with the reason, scores, transcript and alignment.
// accept: OUT-06#1
func TestKeepRejectedWritesTwoPairs(t *testing.T) {
	m := newMultiEnv(t)
	m.tr.mishearAttempt("c0001", 1, unrelated)
	m.tr.mishearAttempt("c0003", 1, "The third paragraph ends the story.")
	keep := m.path("rejected")
	if r := m.run(threeParagraphs, "-o", "out.wav", "--keep-rejected", keep); r.code != exitcode.OK {
		t.Fatalf("%+v", r)
	}
	ents, err := os.ReadDir(keep)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
		fi, err := e.Info()
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v %v", e.Name(), fi.Mode(), err)
		}
	}
	sort.Strings(names)
	if want := []string{"c0001-a1.json", "c0001-a1.wav", "c0003-a1.json", "c0003-a1.wav"}; fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("kept %v, want %v", names, want)
	}
	if fi, err := os.Stat(keep); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("directory %v %v", fi.Mode(), err)
	}
	var rep manifest.RejectedReport
	b, err := os.ReadFile(filepath.Join(keep, "c0003-a1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Schema != manifest.RejectedSchema || rep.Chunk != "c0003" || rep.Attempt != 1 || rep.Reason == "" ||
		len(rep.Transcriptions) == 0 || rep.Transcriptions[0].Text != "The third paragraph ends the story." ||
		len(rep.Transcriptions[0].Alignment) == 0 || rep.Expected != "The third paragraph ends the text." {
		t.Fatalf("report %s", b)
	}
	if id, n, ok := rtfake.PCMTag(readWAV(t, filepath.Join(keep, "c0003-a1.wav"))); !ok || id != "c0003" || n != 1 {
		t.Fatalf("kept audio is %s#%d (%v)", id, n, ok)
	}
	// An unsafe --keep-rejected directory stops the run before any request.
	if err := os.Symlink(m.path("elsewhere"), m.path("link")); err != nil {
		t.Fatal(err)
	}
	renders := len(m.synth.Calls())
	if r := m.run(threeParagraphs, "-o", "x.wav", "--keep-rejected", m.path("link")); r.code != exitcode.Usage || len(m.synth.Calls()) != renders {
		t.Fatalf("symlinked --keep-rejected: %+v", r)
	}
}

// firstMishear hears the first take as unrelated words and every later one as rendered.
type firstMishear struct {
	echo  echoTranscriber
	calls int
}

func (f *firstMishear) Transcribe(ctx context.Context, pcm []byte, h stt.Hint) (stt.Transcript, error) {
	f.calls++
	if f.calls == 1 {
		return stt.Transcript{Text: unrelated, Languages: []stt.DetectedLanguage{}, Seconds: 2}, nil
	}
	t, err := f.echo.Transcribe(ctx, pcm, h)
	t.Seconds = 2
	return t, err
}

// An end-to-end fake run at -vv with --debug-dir, --keep-rejected, a manifest and the usage
// ledger: none of the artifacts, nor stdout or stderr, holds the exact bearer token, the
// client secret, the canary the Codex child writes to its stderr, an email or an account ID.
// accept: AUTH-05#1
// accept: OUT-02#2
// accept: OUT-06#1
// accept: OBS-03#1
func TestRunArtifactsCarryNoSecrets(t *testing.T) {
	e := newSayEnv(t)
	e.mode = "stderr-canary"
	state, debug, keep := e.path("state"), e.path("debug"), e.path("rejected")
	e.env = map[string]string{"XDG_STATE_HOME": state}
	e.tr = &firstMishear{echo: echoTranscriber{e.said}}
	r := e.run("Hello from recite.", "-o", "h.wav", "-vv", "--debug-dir", debug, "--keep-rejected", keep, "--jobs", "1", "--session-chunks", "1")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %s", r.code, r.stderr)
	}
	bearer := strings.TrimPrefix(e.srv.MintRequests()[0].Header.Get("Authorization"), "Bearer ")
	secret := strings.TrimPrefix(e.srv.WebSocketHeaders()[0].Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(bearer, "eyJ") || !strings.HasPrefix(secret, "ek_") {
		t.Fatalf("the run used no token or no client secret: %q %q", bearer, secret)
	}
	needles := []string{bearer, secret, "eyJcanary.canary.canary", "canary@example.invalid", "acct-canary-0000"}

	// Every artifact kind must exist, or the search below proves nothing.
	ledger := filepath.Join(state, "recite", usage.FileName)
	artifacts := map[string]string{
		"manifest": e.path("h.wav.json"), "ledger": ledger,
		"rejected report": filepath.Join(keep, "c0001-a1.json"), "rejected audio": filepath.Join(keep, "c0001-a1.wav"),
	}
	for kind, p := range artifacts {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("no %s: %v", kind, err)
		}
	}
	if ents, err := os.ReadDir(debug); err != nil || len(ents) == 0 {
		t.Fatalf("no debug dump: %v", err)
	}
	var lines int
	for _, l := range strings.Split(strings.TrimSpace(mustRead(t, ledger)), "\n") {
		var en usage.Entry
		if err := json.Unmarshal([]byte(l), &en); err != nil {
			t.Fatalf("ledger line %q: %v", l, err)
		}
		lines++
	}
	if lines != 4 { // two renders, two STT calls
		t.Fatalf("%d ledger lines, want 4:\n%s\nstderr:\n%s", lines, mustRead(t, ledger), r.stderr)
	}

	search := map[string]string{"stdout": r.stdout, "stderr": r.stderr}
	for _, root := range []string{e.dir} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			search[p] = mustRead(t, p)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range search {
		for _, n := range needles {
			if strings.Contains(body, n) {
				t.Errorf("%s contains %q", name, n)
			}
		}
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
