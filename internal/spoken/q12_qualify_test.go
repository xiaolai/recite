//go:build qualify

package spoken_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/livebudget"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/verify"
)

// q12Sentences are the Q12 sentences, each with the word an {{emph}} mark stresses. The
// stressed word changes the meaning, so a listener can tell which reading was asked for.
var q12Sentences = [20]struct{ text, stress string }{
	{"I never said she stole the money.", "she"},
	{"I never said she stole the money.", "money"},
	{"We are leaving on Friday, not Saturday.", "Friday"},
	{"Put the red box on the table.", "red"},
	{"He wanted tea, but she wanted coffee.", "she"},
	{"This is the only key that opens the door.", "only"},
	{"Please read the second paragraph again.", "second"},
	{"The meeting starts at ten, not eleven.", "ten"},
	{"You can take the bus or walk.", "walk"},
	{"I asked for a small coffee.", "small"},
	{"They finished the project yesterday.", "yesterday"},
	{"Turn left at the next corner.", "left"},
	{"My brother lives in Boston now.", "brother"},
	{"The cat sat on the mat.", "cat"},
	{"She bought three tickets for the show.", "three"},
	{"We should leave before it rains.", "before"},
	{"He plays the piano, not the guitar.", "piano"},
	{"That was my last chance.", "last"},
	{"I will call you tonight.", "tonight"},
	{"The answer is in the blue folder.", "blue"},
}

// q12Seed shuffles the clip order; it is fixed so the answer key can be regenerated.
const q12Seed = 12

// TestQ12EmphasisListening renders the Q12 blind listening set (SPK-12, design Q12): each of
// 20 sentences once plain and once with an emphasis instruction on one word, both verified by
// the normal verifier (the no-loss-of-fidelity half of the bullet). It writes, per sentence,
// clips NN-A.wav and NN-B.wav in a seeded random order, a listening sheet that does not reveal
// the order, and a separate answer key. It does not gate: the owner listens, marks for each
// sentence which clip stresses the named word (or prefers), then scores against the key; at
// least 14 of 20 (70%) keeps SPK-12 as specified, otherwise the mark is documented as
// best-effort. Record the result in dev-docs/spikes/q12-emphasis.md.
//
// It spends real usage (40 takes, one invocation):
//
//	RECITE_LIVE=1 go test -tags qualify -run TestQ12EmphasisListening -count=1 -v ./internal/spoken
//
// Output: $TMPDIR/recite-live/q12-<evidence ID>/ with clips/, sheet.md and answer-key.md.
//
// Q12 was not run: design D22 descoped `{{emph}}` to best-effort, and SPK-12#2 is now proved
// by the README wording (internal/cli). This harness stays for a future listening pass.
func TestQ12EmphasisListening(t *testing.T) {
	set := realtime.DefaultSettings()
	spec, err := set.SessionSpec()
	if err != nil {
		t.Fatal(err)
	}
	opts := verify.DefaultOptions()
	var tasks []pipeline.Task
	for i, s := range q12Sentences {
		if !strings.Contains(s.text, s.stress) {
			t.Fatalf("sentence %d %q lacks its stressed word %q", i+1, s.text, s.stress)
		}
		plain := corpusTask(t, fmt.Sprintf("s%02d-plain", i+1), s.text, spec)
		emph := corpusTask(t, fmt.Sprintf("s%02d-emph", i+1), s.text, spec)
		emph.Style = realtime.EmphasisInstruction(s.stress)
		tasks = append(tasks, plain, emph)
	}
	if shards := shard(tasks, opts.MaxAttempts, liveBudget); len(shards) != 1 {
		t.Fatalf("the Q12 set needs %d invocations; it is meant to fit one", len(shards))
	}
	// Seeded: the same seed always gives the same order, and so the same key.
	rng := rand.New(rand.NewPCG(q12Seed, q12Seed))
	emphFirst := make([]bool, len(q12Sentences))
	for i := range emphFirst {
		emphFirst[i] = rng.IntN(2) == 0
	}
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to render the Q12 listening set against the real service")
	}
	var est time.Duration
	for _, tk := range tasks {
		est += tk.Chunk.Estimate
	}
	if err := livebudget.Preflight(est, opts.MaxAttempts, liveBudget); err != nil {
		t.Fatal(err)
	}
	// The chunk IDs mark which take is emphasised; the seed fixes the clip order.
	evidence := evidenceID(tasks, opts, spec, revision()) + fmt.Sprintf("-seed%d", q12Seed)
	dir := filepath.Join(os.TempDir(), "recite-live", "q12-"+evidence)
	clips := filepath.Join(dir, "clips")
	if err := os.MkdirAll(clips, 0o700); err != nil {
		t.Fatal(err)
	}

	synth, tr, gate := liveServices(t)
	budget := livebudget.New(liveBudget)
	kept := newDeliveredAudio()
	res, err := budget.Pipeline(pipeline.Run)(context.Background(), pipeline.Config{
		Jobs: 3, Verify: opts, Synth: synth, Transcriber: tr, Gate: gate,
		Deliver: kept.Deliver,
	}, tasks)
	if err != nil {
		t.Fatalf("run: %v (audio received %v)", err, budget.Used().Round(time.Second))
	}
	if len(res.Chunks) != len(tasks) {
		t.Fatalf("%d results, want %d", len(res.Chunks), len(tasks))
	}
	t.Logf("audio received %v of the %v budget", budget.Used().Round(time.Second), liveBudget)

	var sheet, key strings.Builder
	sheet.WriteString("# Q12 listening sheet\n\nFor each sentence, play A and B, then write which one stresses the named word (A, B, or same).\n\n")
	sheet.WriteString("| # | Sentence | Word | Stressed clip |\n|---|---|---|---|\n")
	key.WriteString("# Q12 answer key (open only after the listening session)\n\n")
	fmt.Fprintf(&key, "Seed %d, evidence %s.\n\n", q12Seed, evidence)
	key.WriteString("| # | Emphasised clip | Plain verdict | Emphasised verdict |\n|---|---|---|---|\n")
	for i, s := range q12Sentences {
		plain, emph := res.Chunks[2*i], res.Chunks[2*i+1]
		first, second, emphLabel := plain, emph, "B"
		if emphFirst[i] {
			first, second, emphLabel = emph, plain, "A"
		}
		writeClip(t, filepath.Join(clips, fmt.Sprintf("%02d-A.wav", i+1)), kept.Of(t, first))
		writeClip(t, filepath.Join(clips, fmt.Sprintf("%02d-B.wav", i+1)), kept.Of(t, second))
		fmt.Fprintf(&sheet, "| %d | %s | %s | |\n", i+1, s.text, s.stress)
		fmt.Fprintf(&key, "| %d | %s | %s | %s |\n", i+1, emphLabel, plain.State, emph.State)
	}
	if err := os.WriteFile(filepath.Join(dir, "sheet.md"), []byte(sheet.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "answer-key.md"), []byte(key.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("Q12 listening set in %s (sheet.md, clips/; answer-key.md is for scoring afterwards)", dir)
}

// writeClip writes a take as WAV; a chunk with no eligible take gets no clip, and the key
// shows its verdict.
func writeClip(t *testing.T, path string, p audio.PCM) {
	t.Helper()
	if len(p) == 0 {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := audio.WriteWAV(f, p); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
