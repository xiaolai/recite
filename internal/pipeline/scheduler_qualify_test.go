//go:build qualify

package pipeline_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/stt"
	"github.com/xiaolai/recite/internal/text"
	"github.com/xiaolai/recite/internal/verify"
)

// BenchmarkLiveThroughput renders about five minutes of English with gpt-realtime-2.1 in 25 s
// chunks, 3 render and 6 verify workers, cold cache (no cache exists yet), and reports the
// real-time factor (design §14.4 target: ≥ 12×; R#18). It spends real usage: `make qualify`.
func BenchmarkLiveThroughput(b *testing.B) {
	if os.Getenv("RECITE_LIVE") == "" {
		b.Skip("set RECITE_LIVE=1 to run against the real service")
	}
	ctx := context.Background()
	st := auth.Probe(ctx, auth.ProbeConfig{Source: auth.SourceAuto, Getenv: os.Getenv})
	if st.Creds == nil {
		b.Fatalf("auth not ready: %s", st.State)
	}
	b.Cleanup(func() { _ = st.Close() })
	hc, err := openai.NewClient()
	if err != nil {
		b.Fatal(err)
	}
	set := realtime.DefaultSettings()
	set.Model = "gpt-realtime-2.1"
	spec, err := set.SessionSpec()
	if err != nil {
		b.Fatal(err)
	}
	minter := &realtime.Minter{Client: hc, BaseURL: openai.APIBase, Creds: st.Creds}
	gate := openai.NewGate(nil, nil)
	synth := &realtime.Synth{Minter: minter, Client: hc, BaseURL: openai.APIBase, OnRateLimits: pipeline.PauseOnExhausted(gate)}
	rt := &stt.Realtime{Minter: minter, Client: hc, BaseURL: openai.APIBase}
	b.Cleanup(func() { _ = rt.Close() })
	tr := &stt.Chain{Primary: &stt.HTTP{Client: hc, BaseURL: openai.APIBase, Creds: st.Creds}, Fallback: rt}

	form, err := spoken.Normalize(benchText(4500), "en", spoken.Options{})
	if err != nil {
		b.Fatal(err)
	}
	seg, err := text.Segment(form, text.SegmentOptions{Budget: 25, Breaks: text.ParagraphBreaks(form.Display)})
	if err != nil {
		b.Fatal(err)
	}
	var tasks []pipeline.Task
	for i, c := range seg.Chunks {
		tasks = append(tasks, pipeline.Task{
			Chunk: verify.Chunk{ID: chunkID(i), Spoken: c.Text, Language: text.Language{Code: "en", Certain: true},
				Estimate: time.Duration(c.Seconds * float64(time.Second))},
			Spec: spec,
		})
	}
	for b.Loop() {
		var audioSeconds float64
		cfg := pipeline.Config{
			Jobs: 3, VerifyJobs: 6, Verify: verify.DefaultOptions(), Synth: synth, Transcriber: tr, Gate: gate,
			Deliver: func(_ int, r verify.ChunkResult) error {
				audioSeconds += r.PCM.Duration().Seconds()
				return nil
			},
		}
		start := time.Now()
		res, err := pipeline.Run(ctx, cfg, tasks)
		wall := time.Since(start)
		if code, _ := pipeline.ExitCode(res, err, verify.OutputPolicy{AllowUnverified: true}); code != 0 {
			b.Fatalf("exit %d: %v", code, err)
		}
		b.ReportMetric(audioSeconds/wall.Seconds(), "x-realtime")
		b.ReportMetric(audioSeconds, "audio-s")
	}
}
