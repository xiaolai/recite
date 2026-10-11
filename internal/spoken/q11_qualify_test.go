//go:build qualify

package spoken_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/livebudget"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/text"
	"github.com/xiaolai/recite/internal/verify"
)

// q11Takes is the number of independent takes of each hint entry (design Q11).
const q11Takes = 3

// q11Corpus is the 20 hint entries of Q11: names and loanwords the model is likely to
// misread, and Chinese polyphones with a pinyin hint. Each sentence holds its term once.
var q11Corpus = []struct{ sentence, term, hint string }{
	{"Dr. Okafor will see you now.", "Okafor", "oh-KAH-for"},
	{"Siobhan sent the report this morning.", "Siobhan", "shi-VAWN"},
	{"Our guest today is Saoirse.", "Saoirse", "SEER-sha"},
	{"The meeting is with Mr. Nguyen.", "Nguyen", "WIN"},
	{"Xochitl wrote the first draft.", "Xochitl", "SO-cheel"},
	{"Please thank Joaquin for the coffee.", "Joaquin", "wah-KEEN"},
	{"Niamh joined the team in March.", "Niamh", "NEEV"},
	{"The paper is by Professor Szymborska.", "Szymborska", "shim-BOR-ska"},
	{"We drove through Worcester on the way.", "Worcester", "WUSS-ter"},
	{"The ferry leaves from Gloucester at noon.", "Gloucester", "GLOSS-ter"},
	{"Add a little gyro meat to the plate.", "gyro", "YEER-oh"},
	{"The quinoa needs another ten minutes.", "quinoa", "KEEN-wah"},
	{"Kubernetes runs every service here.", "Kubernetes", "koo-ber-NET-eez"},
	{"The nginx config was rewritten.", "nginx", "engine X"},
	{"Her name is Eilidh, not Ella.", "Eilidh", "AY-lee"},
	{"我们明天去重庆开会。", "重庆", "chóng qìng"},
	{"这家银行的行长很年轻。", "行长", "háng zhǎng"},
	{"他的音乐很好听。", "音乐", "yīn yuè"},
	{"孩子们都长大了。", "长大", "zhǎng dà"},
	{"请把书还给他。", "还给", "huán gěi"},
}

// q11Take is one take as the owner scores it.
type q11Take struct {
	File, Term, Hint, Sentence string
	Entry, Take                int
	Verdict                    verify.State
	Heard                      string
}

// TestQ11HintTakes renders the Q11 experiment (SPK-08, design Q11) for the owner to score:
// each of the 20 hint entries × q11Takes takes, rendered once with the hint line in that
// chunk's instructions, and verified once by the normal verifier against the unchanged text.
// It does not gate: the decision needs a listener. It writes, under
// $TMPDIR/recite-live/q11-<evidence ID>/, one WAV per take (eNN-tK.wav) and labels.csv, a
// template with one row per take whose `intended` column the owner fills with yes or no.
// TestQ11Score then reads the filled template.
//
// It spends real usage:
//
//	RECITE_LIVE=1 go test -tags qualify -run TestQ11HintTakes -count=1 -v ./internal/spoken
//
// accept: SPK-08#3
func TestQ11HintTakes(t *testing.T) {
	if len(q11Corpus) != 20 {
		t.Fatalf("corpus has %d entries, want 20", len(q11Corpus))
	}
	set := realtime.DefaultSettings()
	spec, err := set.SessionSpec()
	if err != nil {
		t.Fatal(err)
	}
	opts := verify.DefaultOptions()
	opts.MaxAttempts, opts.RequestExtraAttempts = 1, 0

	type key struct{ entry, take int }
	var tasks []pipeline.Task
	keys := map[string]key{}
	instr := sha256.New()
	for i, c := range q11Corpus {
		tk := q11Task(t, c.sentence, c.term, c.hint, spec)
		fmt.Fprintf(instr, "%s\n", tk.Style)
		for take := 1; take <= q11Takes; take++ {
			tk := tk
			tk.Chunk.ID = fmt.Sprintf("e%02d-t%d", i+1, take)
			tasks = append(tasks, tk)
			keys[tk.Chunk.ID] = key{i + 1, take}
		}
	}
	var est time.Duration
	for _, tk := range tasks {
		est += tk.Chunk.Estimate
	}
	if err := livebudget.Preflight(est, 1, liveBudget); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to render the Q11 takes against the real service")
	}
	// The instructions are not in evidenceID's identity; the hint lines are folded into the
	// revision so a changed corpus gets a new directory.
	evidence := evidenceID(tasks, opts, spec, revision()+"+q11-"+hex.EncodeToString(instr.Sum(nil))[:12])
	dir := filepath.Join(os.TempDir(), "recite-live", "q11-"+evidence)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	st := auth.Probe(ctx, auth.ProbeConfig{Source: auth.SourceAuto, Getenv: os.Getenv})
	if st.Creds == nil {
		t.Fatalf("auth not ready: %s", st.State)
	}
	t.Cleanup(func() { _ = st.Close() })
	hc, err := openai.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	minter := &realtime.Minter{Client: hc, BaseURL: openai.APIBase, Creds: st.Creds}
	gate := openai.NewGate(nil, nil)
	keep := &keepSynth{Synthesizer: &realtime.Synth{Minter: minter, Client: hc, BaseURL: openai.APIBase, OnRateLimits: pipeline.PauseOnExhausted(gate)}, pcm: map[string][]byte{}}
	rt := &stt.Realtime{Minter: minter, Client: hc, BaseURL: openai.APIBase}
	t.Cleanup(func() { _ = rt.Close() })
	tr := &stt.Chain{Primary: &stt.HTTP{Client: hc, BaseURL: openai.APIBase, Creds: st.Creds}, Fallback: rt}

	budget := livebudget.New(liveBudget)
	res, err := budget.Pipeline(pipeline.Run)(ctx, pipeline.Config{
		Jobs: 3, Verify: opts, Synth: keep, Transcriber: tr, Gate: gate,
		Deliver: func(int, verify.ChunkResult) error { return nil },
	}, tasks)
	if err != nil {
		t.Fatalf("run: %v (audio received %v)", err, budget.Used().Round(time.Second))
	}
	var takes []q11Take
	for _, r := range res.Chunks {
		k := keys[r.ChunkID]
		c := q11Corpus[k.entry-1]
		pcm := keep.get(r.ChunkID)
		if len(pcm) == 0 {
			t.Fatalf("%s: no audio was rendered", r.ChunkID)
		}
		file := r.ChunkID + ".wav"
		var b bytes.Buffer
		if err := audio.WriteWAV(&b, audio.PCM(pcm)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		tk := q11Take{File: file, Term: c.term, Hint: c.hint, Sentence: c.sentence, Entry: k.entry, Take: k.take, Verdict: r.State}
		for _, a := range r.Attempts {
			for _, x := range a.Transcriptions {
				tk.Heard = x.Text
			}
		}
		takes = append(takes, tk)
	}
	if len(takes) != len(tasks) {
		t.Fatalf("%d takes, want %d", len(takes), len(tasks))
	}
	writeQ11Labels(t, filepath.Join(dir, "labels.csv"), takes)
	t.Logf("Q11: %d takes and labels.csv in %s (audio received %v). Listen to each take, fill the intended column with yes or no, then run TestQ11Score with RECITE_Q11_LABELS=%s",
		len(takes), dir, budget.Used().Round(time.Second), filepath.Join(dir, "labels.csv"))
}

// q11Header is the labels.csv header; the owner fills `intended`.
var q11Header = []string{"file", "entry", "take", "term", "hint", "sentence", "stt_verdict", "heard", "intended"}

func writeQ11Labels(t *testing.T, path string, takes []q11Take) {
	t.Helper()
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write(q11Header)
	for _, tk := range takes {
		_ = w.Write([]string{tk.File, strconv.Itoa(tk.Entry), strconv.Itoa(tk.Take), tk.Term, tk.Hint, tk.Sentence, string(tk.Verdict), tk.Heard, ""})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestQ11Score reads a filled labels.csv (RECITE_Q11_LABELS) and reports the Q11 outcome:
// the share of takes the listener heard as intended (the bar is 80%) and the share whose
// STT transcript still verified. It spends nothing.
//
//	RECITE_Q11_LABELS=PATH go test -tags qualify -run TestQ11Score -count=1 -v ./internal/spoken
func TestQ11Score(t *testing.T) {
	path := os.Getenv("RECITE_Q11_LABELS")
	if path == "" {
		t.Skip("set RECITE_Q11_LABELS to a labels.csv filled after listening")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1+len(q11Corpus)*q11Takes || strings.Join(rows[0], ",") != strings.Join(q11Header, ",") {
		t.Fatalf("%s: want the header and %d rows", path, len(q11Corpus)*q11Takes)
	}
	intended, verified := 0, 0
	for i, r := range rows[1:] {
		switch strings.ToLower(strings.TrimSpace(r[8])) {
		case "yes":
			intended++
		case "no":
		default:
			t.Fatalf("row %d (%s): intended must be yes or no, not %q", i+2, r[0], r[8])
		}
		if r[6] == string(verify.StateVerified) {
			verified++
		}
	}
	n := float64(len(rows) - 1)
	pi, pv := float64(intended)/n, float64(verified)/n
	verdict := "hint meets the bar: document it as supported"
	if pi < 0.8 {
		verdict = "below the 80% bar: document hint as best-effort and recommend say"
	}
	t.Logf("Q11: heard as intended %d/%d (%.1f%%), STT verified %d/%d (%.1f%%): %s", intended, int(n), 100*pi, verified, int(n), 100*pv, verdict)
}

// q11Task is one sentence through the text stages `say` runs with a one-entry hint lexicon:
// the text is unchanged and the chunk carries the hint line, as SPK-08 adds it.
func q11Task(t *testing.T, sentence, term, hint string, spec realtime.SessionSpec) pipeline.Task {
	t.Helper()
	lex, err := spoken.ParseLexicon(map[string]any{term: map[string]any{"hint": hint}})
	if err != nil {
		t.Fatal(err)
	}
	g, err := guard.Run(sentence, guard.Options{})
	if err != nil {
		t.Fatalf("%q: guard: %v", sentence, err)
	}
	lang := text.Resolve(g.Text, text.ResolveOptions{})
	form, err := spoken.Normalize(g.Text, lang.Code, spoken.Options{Lexicon: lex})
	if err != nil {
		t.Fatalf("%q: %v", sentence, err)
	}
	if !strings.Contains(form.Spoken, term) {
		t.Fatalf("%q: the spoken form %q lost the term %q", sentence, form.Spoken, term)
	}
	n := 0
	for _, sg := range form.Segments {
		if sg.Rule == spoken.RuleLexiconHint {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%q: the hint %q matched %d times, want once", sentence, term, n)
	}
	return pipeline.Task{
		Chunk: verify.Chunk{Spoken: form.Spoken, Language: lang, Estimate: realtime.EstimateDuration(form.Spoken, spec.Speed), Lexicon: []string{term}},
		Spec:  spec,
		Style: lex[0].Instruction(),
	}
}

// keepSynth keeps each render's raw audio by chunk ID, so a take that failed verification
// can still be scored by ear.
type keepSynth struct {
	realtime.Synthesizer
	mu  sync.Mutex
	pcm map[string][]byte
}

func (s *keepSynth) Open(ctx context.Context, spec realtime.SessionSpec) (realtime.SpeechSession, error) {
	ss, err := s.Synthesizer.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return keepSession{ss, s}, nil
}

func (s *keepSynth) get(id string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pcm[id]
}

type keepSession struct {
	realtime.SpeechSession
	s *keepSynth
}

func (ks keepSession) Render(ctx context.Context, req realtime.RenderRequest) (realtime.Take, error) {
	take, err := ks.SpeechSession.Render(ctx, req)
	if err == nil {
		ks.s.mu.Lock()
		ks.s.pcm[req.ChunkID] = take.PCM
		ks.s.mu.Unlock()
	}
	return take, err
}
