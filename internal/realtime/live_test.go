//go:build live

package realtime_test

import (
	"context"
	"slices"
	"testing"

	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/stt"
	rtext "github.com/xiaolai/recite/internal/text"
	"github.com/xiaolai/recite/internal/verify"
)

const liveHello = "Hello from recite."

func liveRender(t *testing.T, s *realtime.Synth, set realtime.Settings, text string) realtime.Take {
	t.Helper()
	ss := liveOpen(t, s, set)
	take, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: text})
	if err != nil {
		t.Fatal(err)
	}
	if take.Status != realtime.StatusCompleted || len(take.PCM) == 0 {
		t.Fatalf("status %q, %d PCM bytes", take.Status, len(take.PCM))
	}
	return take
}

// liveVerified renders text and verifies it the way `say` does: pipeline.Run with the
// production synthesizer and the verifier's STT chain (HTTP, realtime fallback), the default
// verify options and attempt limit, and a chunk built as the CLI builds a mark-free one.
func liveVerified(t *testing.T, s *realtime.Synth, set realtime.Settings, text string) verify.ChunkResult {
	t.Helper()
	spec, err := set.SessionSpec()
	if err != nil {
		t.Fatal(err)
	}
	hc, err := openai.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	rt := &stt.Realtime{Minter: s.Minter, Client: hc, BaseURL: openai.APIBase}
	t.Cleanup(func() { _ = rt.Close() })
	tr := &stt.Chain{Primary: &stt.HTTP{Client: hc, BaseURL: openai.APIBase, Creds: s.Minter.Creds}, Fallback: rt}
	task := pipeline.Task{
		Chunk: verify.Chunk{
			ID:       "c0001",
			Spoken:   text,
			Language: rtext.Resolve(text, rtext.ResolveOptions{}),
			Estimate: realtime.EstimateDuration(text, spec.Speed),
		},
		Spec: spec,
	}
	res, err := pipeline.Run(context.Background(), pipeline.Config{
		Jobs: 1, Verify: verify.OptionsFor(verify.ModeNormal), Synth: s, Transcriber: tr,
		Deliver: func(int, verify.ChunkResult) error { return nil },
	}, []pipeline.Task{task})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Chunks) != 1 {
		t.Fatalf("%d chunk results, want 1", len(res.Chunks))
	}
	r := res.Chunks[0]
	for _, a := range r.Attempts {
		var heard []string
		for _, tx := range a.Transcriptions {
			heard = append(heard, tx.Text)
		}
		t.Logf("attempt %d: status %s, said mismatch %v, rejection %+v, score %.3f, heard %q",
			a.N, a.Status, a.SaidMismatch, a.Rejection, a.Score, heard)
	}
	if r.State != verify.StateVerified {
		t.Fatalf("chunk state %s after %d attempts, want verified", r.State, len(r.Attempts))
	}
	return r
}

// The take verifies, and STT heard the kept take exactly: one of its recorded transcripts
// canonicalises to the text.
//
// accept: SPK-01#4
func TestLiveRenderHello(t *testing.T) {
	r := liveVerified(t, liveSynth(t), realtime.DefaultSettings(), liveHello)
	kept := r.Attempts[r.Final-1]
	want := verify.Canon(liveHello)
	exact := false
	for _, tx := range kept.Transcriptions {
		exact = exact || slices.Equal(verify.Canon(tx.Text), want)
	}
	if !exact {
		t.Fatalf("no transcript of the kept take (attempt %d) reads %q exactly", r.Final, liveHello)
	}
}

// accept: SPK-02#1
func TestLiveRenderEachModel(t *testing.T) {
	s := liveSynth(t)
	for _, m := range realtime.Models {
		t.Run(m, func(t *testing.T) {
			set := realtime.DefaultSettings()
			set.Model = m
			take := liveRender(t, s, set, liveHello)
			if fold(take.Said) != fold(liveHello) {
				t.Errorf("Said = %q", take.Said)
			}
		})
	}
}

// accept: SPK-10#3
func TestLiveRenderEachEffort(t *testing.T) {
	s := liveSynth(t)
	for _, e := range realtime.Efforts {
		t.Run(string(e), func(t *testing.T) {
			set := realtime.DefaultSettings()
			set.Effort = e
			liveVerified(t, s, set, liveHello)
		})
	}
}

// accept: SPK-04#1
func TestLiveSpeedShortensAudio(t *testing.T) {
	s := liveSynth(t)
	text := "The quick brown fox jumps over the lazy dog, then runs back across the field."
	pcm := map[float64]int{}
	for _, sp := range []float64{1.0, 1.5} {
		set := realtime.DefaultSettings()
		set.Speed = sp
		pcm[sp] = len(liveRender(t, s, set, text).PCM)
	}
	if pcm[1.5] >= pcm[1.0] {
		t.Fatalf("speed 1.5: %d bytes, speed 1.0: %d bytes; want shorter", pcm[1.5], pcm[1.0])
	}
	t.Logf("1.0: %d bytes, 1.5: %d bytes", pcm[1.0], pcm[1.5])
}
