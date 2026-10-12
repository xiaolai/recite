package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/verify"
)

// multiEnv runs `say` end to end over scripted fakes: rtfake.ScriptedSynthesizer for speech
// (its takes resized to each chunk's estimate, so the duration guard passes) and a
// transcriber that hears each chunk's spoken text unless told otherwise. The scheduler is
// the real pipeline.Run, observed through CLI.RunPipeline.
type multiEnv struct {
	t     *testing.T
	dir   string
	synth *rtfake.ScriptedSynthesizer
	tr    *tagTranscriber

	mu        sync.Mutex
	cfgs      []pipeline.Config
	tasks     [][]pipeline.Task
	synthOpen int

	env map[string]string // the environment Getenv sees; nil is empty
	// wrapTr, when set, wraps the transcriber the run gets (a verifier chain, for VER-02).
	wrapTr func(stt.Transcriber) stt.Transcriber
	sigs   chan os.Signal               // when set, the run's interrupts come from here
	drain  time.Duration                // CLI.DrainTimeout; 0 is the default
	look   func(string) (string, error) // CLI.LookPath when set
	// stderrTTY is what CLI.StderrIsTTY reports (CLI-07); false, as for a pipe, by default.
	stderrTTY bool
}

func newMultiEnv(t *testing.T) *multiEnv {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return &multiEnv{t: t, dir: dir, synth: rtfake.NewScriptedSynthesizer(),
		tr: &tagTranscriber{texts: map[string]string{}, said: map[string]string{}, saidAt: map[string]string{}}}
}

func (m *multiEnv) path(name string) string { return filepath.Join(m.dir, name) }

func (m *multiEnv) run(args ...string) result {
	m.t.Helper()
	return m.runCmd("say", args...)
}

// runCmd runs command (say, voices) with args against the scripted fakes.
func (m *multiEnv) runCmd(command string, args ...string) result {
	m.t.Helper()
	m.t.Setenv("FAKECODEX_MODE", "chatgpt")
	m.t.Setenv("FAKECODEX_TOKEN_EXP", fmt.Sprint(cliNow.Add(time.Hour).Unix()))
	var out, errb bytes.Buffer
	c := New(&out, &errb)
	c.Getenv = func(k string) string { return m.env[k] }
	c.Now = func() time.Time { return cliNow }
	c.StdinIsTTY = func() bool { return true }
	c.StderrIsTTY = func() bool { return m.stderrTTY }
	c.DrainTimeout = m.drain
	if m.look != nil {
		c.LookPath = m.look
	}
	if m.sigs != nil {
		c.Signals = func() (<-chan os.Signal, func()) { return m.sigs, func() {} }
	}
	c.NewSynth = func(*auth.Credentials) (realtime.Synthesizer, error) {
		m.mu.Lock()
		m.synthOpen++
		m.mu.Unlock()
		return fitSynth{m.synth}, nil
	}
	c.NewTranscriber = func(*auth.Credentials) (stt.Transcriber, func() error, error) {
		if m.wrapTr != nil {
			return m.wrapTr(m.tr), func() error { return nil }, nil
		}
		return m.tr, func() error { return nil }, nil
	}
	c.RunPipeline = func(ctx context.Context, cfg pipeline.Config, tasks []pipeline.Task) (pipeline.Result, error) {
		m.mu.Lock()
		m.cfgs = append(m.cfgs, cfg)
		m.tasks = append(m.tasks, tasks)
		m.mu.Unlock()
		for _, tk := range tasks {
			m.tr.setText(tk.Chunk.ID, tk.Chunk.Spoken)
		}
		return pipeline.Run(ctx, cfg, tasks)
	}
	full := append([]string{"--codex-bin", fakeCodex, "--codex-home", m.t.TempDir(), "--auth-source", "codex", command}, args...)
	code := c.Run(full)
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

// lastTasks is the task list of the last run that reached the scheduler.
func (m *multiEnv) lastTasks() []pipeline.Task {
	m.t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.tasks) == 0 {
		m.t.Fatal("the scheduler never ran")
	}
	return m.tasks[len(m.tasks)-1]
}

func (m *multiEnv) lastCfg() pipeline.Config {
	m.t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.cfgs) == 0 {
		m.t.Fatal("the scheduler never ran")
	}
	return m.cfgs[len(m.cfgs)-1]
}

// untouched asserts that nothing past the local checks ran.
func (m *multiEnv) untouched() {
	m.t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.synthOpen != 0 || len(m.cfgs) != 0 || len(m.synth.Calls()) != 0 {
		m.t.Fatalf("network stage reached: %d synths, %d runs, %d renders", m.synthOpen, len(m.cfgs), len(m.synth.Calls()))
	}
}

// fitSynth resizes every take of the scripted synthesizer to its chunk's estimate, keeping
// the chunk/attempt tag, so the duration guard passes whatever the chunk length.
type fitSynth struct{ s *rtfake.ScriptedSynthesizer }

func (f fitSynth) Open(ctx context.Context, spec realtime.SessionSpec) (realtime.SpeechSession, error) {
	ss, err := f.s.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return fitSession{ss}, nil
}

type fitSession struct{ realtime.SpeechSession }

func (f fitSession) Render(ctx context.Context, req realtime.RenderRequest) (realtime.Take, error) {
	t, err := f.SpeechSession.Render(ctx, req)
	if err != nil {
		return t, err
	}
	id, n, ok := rtfake.PCMTag(t.PCM)
	if !ok {
		return t, errors.New("untagged fake take")
	}
	t.PCM = rtfake.TaggedPCM(id, n, req.Estimate)
	return t, nil
}

// tagTranscriber hears each take as its chunk's spoken text, or as said[chunk] when set, or
// as saidAt["chunk#attempt"] for that one take.
type tagTranscriber struct {
	mu     sync.Mutex
	texts  map[string]string
	said   map[string]string
	saidAt map[string]string
	calls  int
}

// mishearAttempt makes one take of chunk id be heard as s.
func (tt *tagTranscriber) mishearAttempt(id string, attempt int, s string) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.saidAt[fmt.Sprintf("%s#%d", id, attempt)] = s
}

func (tt *tagTranscriber) setText(id, s string) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.texts[id] = s
}

func (tt *tagTranscriber) mishear(id, s string) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.said[id] = s
}

func (tt *tagTranscriber) Calls() int {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	return tt.calls
}

func (tt *tagTranscriber) Transcribe(ctx context.Context, pcm []byte, _ stt.Hint) (stt.Transcript, error) {
	if err := ctx.Err(); err != nil {
		return stt.Transcript{}, err
	}
	id, n, ok := rtfake.PCMTag(pcm)
	if !ok {
		return stt.Transcript{}, errors.New("untagged take")
	}
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.calls++
	text := tt.texts[id]
	if s, ok := tt.said[id]; ok {
		text = s
	}
	if s, ok := tt.saidAt[fmt.Sprintf("%s#%d", id, n)]; ok {
		text = s
	}
	return stt.Transcript{Text: text, Languages: []stt.DetectedLanguage{}}, nil
}

const threeParagraphs = "The first paragraph is short.\n\nThe second paragraph follows it.\n\nThe third paragraph ends the text."

// expectedOutput is the assembly of first-attempt takes of tasks with the given gap (in
// samples) after every chunk but the last.
func expectedOutput(tasks []pipeline.Task, attempt int, gap int) []byte {
	var out []byte
	for i, tk := range tasks {
		out = append(out, audio.Fade(rtfake.TaggedPCM(tk.Chunk.ID, attempt, tk.Chunk.Estimate))...)
		if i+1 < len(tasks) {
			out = append(out, make([]byte, gap*audio.BytesPerSample)...)
		}
	}
	return out
}

// The M1 single-chunk limit is gone: a multi-paragraph text becomes one chunk per paragraph,
// every chunk is rendered and verified in-tool, and the WAV holds the takes in order with the
// paragraph gap between them.
// accept: CLI-02#1
func TestSayMultiChunkVerifiesAndAssembles(t *testing.T) {
	m := newMultiEnv(t)
	r := m.run(threeParagraphs, "-o", "out.wav", "--verify", "normal", "--gap-paragraph", "500ms")
	if r.code != exitcode.OK {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	tasks := m.lastTasks()
	if len(tasks) != 3 || tasks[1].Chunk.ID != "c0002" || tasks[1].Chunk.Spoken != "The second paragraph follows it." {
		t.Fatalf("tasks %+v", tasks)
	}
	if m.tr.Calls() != 3 {
		t.Fatalf("%d transcriptions, want one per chunk", m.tr.Calls())
	}
	if got, want := readWAV(t, m.path("out.wav")), expectedOutput(tasks, 1, 12000); !bytes.Equal(got, want) {
		t.Fatalf("output %d bytes, want %d", len(got), len(want))
	}
}

// --chunk-seconds sets the segmentation budget.
// accept: CLI-02#1
func TestSayChunkSecondsReachesSegmenter(t *testing.T) {
	long := strings.Repeat("This sentence is here to make the paragraph long. ", 12)
	m := newMultiEnv(t)
	if r := m.run(long, "-o", "a.wav"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	wide := len(m.lastTasks())
	if r := m.run(long, "-o", "b.wav", "--chunk-seconds", "6"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if narrow := len(m.lastTasks()); narrow <= wide || narrow < 4 {
		t.Fatalf("--chunk-seconds 6 gave %d chunks, 25 gave %d", narrow, wide)
	}
	for _, bad := range []string{"0", "-1", "x"} {
		if r := m.run("Hi", "--chunk-seconds", bad); r.code != exitcode.Usage {
			t.Errorf("--chunk-seconds %s: %+v", bad, r)
		}
	}
}

// The scheduler and verification flags reach pipeline.Config.
// accept: CLI-02#1
func TestSaySchedulerAndVerifyFlagsReachConfig(t *testing.T) {
	m := newMultiEnv(t)
	r := m.run(threeParagraphs, "-o", "a.wav", "--jobs", "2", "--verify-jobs", "5", "--session-chunks", "7",
		"--verify", "strict", "--max-attempts", "4", "--request-extra-attempts", "2", "--no-trim")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	cfg := m.lastCfg()
	want := verify.Options{Mode: verify.ModeStrict, MaxAttempts: 4, RequestExtraAttempts: 2, NoTrim: true}
	if cfg.Jobs != 2 || cfg.VerifyJobs != 5 || cfg.SessionChunks != 7 || cfg.Verify != want {
		t.Fatalf("config jobs %d verify-jobs %d session-chunks %d verify %+v", cfg.Jobs, cfg.VerifyJobs, cfg.SessionChunks, cfg.Verify)
	}
	if cfg.Transcriber == nil || cfg.Stop == nil {
		t.Fatal("no transcriber or stop channel")
	}
	// Defaults: 3 jobs, verify-jobs 0 (= 2 × jobs in the scheduler), 50 renders per session.
	if r := m.run(threeParagraphs, "-o", "b.wav"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if cfg := m.lastCfg(); cfg.Jobs != 3 || cfg.VerifyJobs != 0 || cfg.SessionChunks != 50 || cfg.Verify != verify.DefaultOptions() {
		t.Fatalf("default config %+v", cfg.Verify)
	}
}

// The default verify mode is off (D23): with no setting, say runs without STT and the policy
// marks the run verify-off; --verify normal, or verify.mode in the config file, opts in.
// accept: VER-09#2
func TestSayDefaultVerifyModeIsOff(t *testing.T) {
	m := newMultiEnv(t)
	if r := m.run(threeParagraphs, "-o", "a.wav"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if cfg := m.lastCfg(); cfg.Verify.Mode != verify.ModeOff || cfg.Verify != verify.DefaultOptions() {
		t.Fatalf("default verify %+v, want mode off", cfg.Verify)
	}
	if n := m.tr.Calls(); n != 0 {
		t.Fatalf("%d transcriptions with verification off", n)
	}
	for _, args := range [][]string{
		{"--verify", "normal"},
		{"--config", writeConfig(t, "[verify]\nmode = \"normal\"\n")},
	} {
		if r := m.run(append([]string{threeParagraphs, "-o", "b.wav", "--force"}, args...)...); r.code != 0 {
			t.Fatalf("%v: %+v", args, r)
		}
		if mode := m.lastCfg().Verify.Mode; mode != verify.ModeNormal {
			t.Fatalf("%v: mode %s, want normal", args, mode)
		}
	}
}

// --max-attempts bounds the renders of a chunk that never verifies; --verify strict raises the
// acceptance threshold; --verify off never transcribes.
// accept: CLI-02#1
func TestSayVerifyFlagsReachAdjudication(t *testing.T) {
	m := newMultiEnv(t)
	m.tr.mishear("c0001", "something else entirely")
	r := m.run("One short sentence to say.", "-o", "a.wav", "--verify", "normal", "--max-attempts", "2")
	if r.code != exitcode.VerifyFailed || m.synth.Attempts("c0001") != 2 || !strings.Contains(r.stderr, "chunk c0001 unverified after 2 attempt(s)") {
		t.Fatalf("code %d, %d renders, stderr %q", r.code, m.synth.Attempts("c0001"), r.stderr)
	}

	// 30 words, one function word misheard: score 29/30 ≈ 0.967, accepted under normal
	// (0.95), not under strict (0.98).
	words := strings.TrimSpace(strings.Repeat("alpha beta the gamma delta ", 6))
	sentence := strings.ToUpper(words[:1]) + words[1:] + "."
	heard := strings.Replace(sentence, " the ", " a ", 1)
	m = newMultiEnv(t)
	m.tr.mishear("c0001", heard)
	if r := m.run(sentence, "-o", "n.wav", "--verify", "normal"); r.code != 0 || m.synth.Attempts("c0001") != 1 {
		t.Fatalf("normal: code %d, %d renders, stderr %q", r.code, m.synth.Attempts("c0001"), r.stderr)
	}
	m = newMultiEnv(t)
	m.tr.mishear("c0001", heard)
	if r := m.run(sentence, "-o", "s.wav", "--verify", "strict", "--max-attempts", "1"); r.code != exitcode.VerifyFailed {
		t.Fatalf("strict: code %d stderr %q", r.code, r.stderr)
	}

	m = newMultiEnv(t)
	r = m.run(threeParagraphs, "-o", "off.wav", "--verify", "off", "--allow-unverified")
	if r.code != 0 || m.tr.Calls() != 0 || m.lastCfg().Transcriber != nil {
		t.Fatalf("off: code %d, %d transcriptions, stderr %q", r.code, m.tr.Calls(), r.stderr)
	}
	readWAV(t, m.path("off.wav"))
}

// A chunk flagged G-REQUEST gets --request-extra-attempts more renders (TXT-14).
// accept: CLI-02#1
func TestSayRequestLikeChunkGetsExtraAttempts(t *testing.T) {
	m := newMultiEnv(t)
	m.tr.mishear("c0001", "no")
	r := m.run("Can you tell me a joke?", "-o", "a.wav", "--verify", "normal", "--max-attempts", "2", "--request-extra-attempts", "2")
	if r.code != exitcode.VerifyFailed || m.synth.Attempts("c0001") != 4 {
		t.Fatalf("code %d, %d renders, stderr %q", r.code, m.synth.Attempts("c0001"), r.stderr)
	}
	if !m.lastTasks()[0].Chunk.RequestLike {
		t.Fatal("chunk not request-like")
	}
}

// --allow-unverified and --strict-output decide the exit code and whether the file is written
// (VER-09).
// accept: CLI-02#1
func TestSayOutputPolicyFlags(t *testing.T) {
	for _, tc := range []struct {
		flags []string
		code  int
		file  bool
	}{
		{nil, exitcode.VerifyFailed, true},
		{[]string{"--allow-unverified"}, exitcode.OK, true},
		{[]string{"--strict-output"}, exitcode.VerifyFailed, false},
	} {
		m := newMultiEnv(t)
		m.tr.mishear("c0002", "nothing like it")
		r := m.run(append([]string{threeParagraphs, "-o", "out.wav", "--verify", "normal", "--max-attempts", "1"}, tc.flags...)...)
		_, err := os.Stat(m.path("out.wav"))
		if r.code != tc.code || (err == nil) != tc.file {
			t.Errorf("%v: code %d, file %v, stderr %q", tc.flags, r.code, err == nil, r.stderr)
		}
		if !strings.Contains(r.stderr, "chunk c0002 unverified") {
			t.Errorf("%v: the unverified chunk is not reported: %q", tc.flags, r.stderr)
		}
	}
}

// Fixed gap flags set the silence exactly; a range draws per join from --gap-seed.
// accept: CLI-02#1
func TestSayGapFlagsReachAssembly(t *testing.T) {
	m := newMultiEnv(t)
	text := "One sentence here. Another sentence there. A third one ends it."
	r := m.run(text, "-o", "fixed.wav", "--chunk-seconds", "2", "--gap-sentence", "300ms")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	tasks := m.lastTasks()
	if len(tasks) != 3 {
		t.Fatalf("%d chunks, want 3", len(tasks))
	}
	if got, want := readWAV(t, m.path("fixed.wav")), expectedOutput(tasks, 1, 7200); !bytes.Equal(got, want) {
		t.Fatalf("fixed gaps: %d bytes, want %d", len(got), len(want))
	}

	lengths := map[string]int{}
	for _, seed := range []string{"1", "2", "1"} {
		m := newMultiEnv(t)
		out := "seed" + seed + ".wav"
		if r := m.run(text, "-o", out, "--chunk-seconds", "2", "--gap-sentence", "100ms..2s", "--gap-seed", seed, "--force"); r.code != 0 {
			t.Fatalf("%+v", r)
		}
		n := len(readWAV(t, m.path(out)))
		if prev, ok := lengths[seed]; ok && prev != n {
			t.Fatalf("seed %s gave %d then %d bytes", seed, prev, n)
		}
		lengths[seed] = n
	}
	if lengths["1"] == lengths["2"] {
		t.Fatalf("--gap-seed 1 and 2 drew the same gaps (%d bytes)", lengths["1"])
	}
}

// An invalid gap value is a usage error before any network call.
// accept: AUD-03#5
// accept: CLI-02#1
func TestSayInvalidGapExits2(t *testing.T) {
	m := newMultiEnv(t)
	for _, args := range [][]string{
		{"--gap-sentence", "11s"},
		{"--gap-list", "1s..500ms"},
		{"--gap-paragraph", "-5ms"},
		{"--gap-section", "soon"},
	} {
		r := m.run(append([]string{threeParagraphs, "-o", "out.wav"}, args...)...)
		if r.code != exitcode.Usage || !strings.Contains(r.stderr, args[0]) {
			t.Errorf("%v: code %d stderr %q", args, r.code, r.stderr)
		}
	}
	m.untouched()
	if _, err := os.Stat(m.path("out.wav")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("output written: %v", err)
	}
}

// --jobs above 6 (and the other scheduler limits) exit 2 before any network call.
// accept: RUN-02#4
// accept: CLI-02#1
func TestSayJobsLimitsExit2(t *testing.T) {
	m := newMultiEnv(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--jobs", "7"}, "--jobs 7: want 1 to 6"},
		{[]string{"--jobs", "0"}, "--jobs 0: want 1 to 6"},
		{[]string{"--verify-jobs", "-1"}, "--verify-jobs -1"},
		{[]string{"--session-chunks", "0"}, "--session-chunks 0"},
		{[]string{"--max-attempts", "0"}, "--max-attempts 0"},
		{[]string{"--request-extra-attempts", "-1"}, "--request-extra-attempts -1"},
		{[]string{"--verify", "loose"}, "want strict, normal or off"},
	} {
		r := m.run(append([]string{"Hello there.", "-o", "out.wav"}, tc.args...)...)
		if r.code != exitcode.Usage || !strings.Contains(r.stderr, tc.want) {
			t.Errorf("%v: code %d stderr %q", tc.args, r.code, r.stderr)
		}
	}
	m.untouched()
	if r := m.run(threeParagraphs, "-o", "six.wav", "--jobs", "6"); r.code != 0 || m.lastCfg().Jobs != 6 {
		t.Fatalf("--jobs 6: %+v", r)
	}
}

// --guard and --normalize shape every chunk's spoken text.
// accept: CLI-02#1
func TestSayGuardAndNormalizeReachChunks(t *testing.T) {
	text := "Revenue hit $3.5M 🎉 this year.\n\nGrowth was 50% overall."
	m := newMultiEnv(t)
	if r := m.run(text, "-o", "a.wav"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	tasks := m.lastTasks()
	if len(tasks) != 2 || strings.Contains(tasks[0].Chunk.Spoken, "🎉") || !strings.Contains(tasks[0].Chunk.Spoken, "three point five million dollars") ||
		!strings.Contains(tasks[1].Chunk.Spoken, "fifty percent") {
		t.Fatalf("spoken %q / %q", tasks[0].Chunk.Spoken, tasks[1].Chunk.Spoken)
	}
	if r := m.run(text, "-o", "b.wav", "--normalize", "off", "--config", writeConfig(t, "[guard]\nemoji = \"keep\"\n")); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	tasks = m.lastTasks()
	if !strings.Contains(tasks[0].Chunk.Spoken, "$3.5M 🎉") || !strings.Contains(tasks[1].Chunk.Spoken, "50%") {
		t.Fatalf("spoken %q / %q", tasks[0].Chunk.Spoken, tasks[1].Chunk.Spoken)
	}
	m = newMultiEnv(t)
	if r := m.run("Take 1/2 now.\n\nAnd more.", "-o", "c.wav", "--guard", "strict"); r.code != exitcode.Usage {
		t.Fatalf("strict: %+v", r)
	}
	m.untouched()
}

// Every gap flag lands in the request's gap settings. Plain text (M2) has no list items or
// sections, so --gap-list and --gap-section reach assembly only once Markdown input exists;
// GapSamples applies them per class (audio tests).
// accept: CLI-02#1
func TestSayGapFlagsParse(t *testing.T) {
	r := run("say", "Hi", "--gap-sentence", "250ms", "--gap-list", "400ms..450ms", "--gap-paragraph", "0", "--gap-section", "2s", "--gap-seed", "9")
	if r.code != ExitOK || len(r.says) != 1 {
		t.Fatalf("%+v", r)
	}
	ms := time.Millisecond
	want := audio.Gaps{
		Sentence: audio.GapRange{Min: 250 * ms, Max: 250 * ms}, List: audio.GapRange{Min: 400 * ms, Max: 450 * ms},
		Paragraph: audio.GapRange{}, Section: audio.GapRange{Min: 2 * time.Second, Max: 2 * time.Second}, Seed: 9,
	}
	if got := r.says[0].Gaps; got != want {
		t.Fatalf("gaps %+v, want %+v", got, want)
	}
}
