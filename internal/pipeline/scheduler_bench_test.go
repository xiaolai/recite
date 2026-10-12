package pipeline_test

import (
	"context"
	"io"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/text"
	"github.com/xiaolai/recite/internal/verify"
)

const heapMetric = "/memory/classes/heap/objects:bytes"

// heapPeak samples the live heap every millisecond until stopped (RUN-02: runtime/metrics,
// sampled).
type heapPeak struct {
	stop chan struct{}
	done chan uint64
}

func sampleHeap() *heapPeak {
	h := &heapPeak{stop: make(chan struct{}), done: make(chan uint64, 1)}
	go func() {
		s := []metrics.Sample{{Name: heapMetric}}
		var peak uint64
		t := time.NewTicker(time.Millisecond)
		defer t.Stop()
		for {
			metrics.Read(s)
			peak = max(peak, s[0].Value.Uint64())
			select {
			case <-h.stop:
				h.done <- peak
				return
			case <-t.C:
			}
		}
	}()
	return h
}

func (h *heapPeak) Stop() uint64 { close(h.stop); return <-h.done }

// benchText is about size bytes of plain English prose in paragraphs.
func benchText(size int) string {
	const para = "The river ran quietly past the old mill, and the miller watched it from the bridge. " +
		"Every morning he counted the boats that drifted down toward the sea. " +
		"Some carried grain, some carried timber, and a few carried nothing at all. " +
		"He never learned where the empty ones were going, though he often wondered about them.\n\n"
	var b strings.Builder
	b.Grow(size + len(para))
	for b.Len() < size {
		b.WriteString(para)
	}
	return b.String()
}

// estSynth renders every chunk as a take exactly as long as its estimate.
type estSynth struct{}

func (estSynth) Open(context.Context, realtime.SessionSpec) (realtime.SpeechSession, error) {
	return estSession{}, nil
}

type estSession struct{}

func (estSession) Render(_ context.Context, r realtime.RenderRequest) (realtime.Take, error) {
	return realtime.Take{PCM: rtfake.TaggedPCM(r.ChunkID, r.Attempt, r.Estimate), Said: r.Text, Status: realtime.StatusCompleted}, nil
}
func (estSession) Age() time.Duration { return 0 }
func (estSession) Close() error       { return nil }

// accept: RUN-02#5
// BenchmarkPeakHeap10MB plans, renders, verifies and assembles a 10 MB input against the fakes
// and reports the peak Go heap (RUN-02, design §14.4 target: < 200 MB). Each fake take is as long as its
// chunk's estimate, so the PCM held per chunk is realistic.
func BenchmarkPeakHeap10MB(b *testing.B) {
	input := benchText(10 << 20)
	// The soft limit cmd/recite sets in production.
	defer debug.SetMemoryLimit(debug.SetMemoryLimit(pipeline.MemoryLimit))
	for b.Loop() {
		runtime.GC()
		h := sampleHeap()
		peakRun(b, input)
		peak := h.Stop()
		b.ReportMetric(float64(peak)/(1<<20), "peak-heap-MB")
	}
}

func peakRun(b *testing.B, input string) {
	b.Helper()
	form, err := spoken.Normalize(input, "en", spoken.Options{})
	if err != nil {
		b.Fatal(err)
	}
	seg, err := text.Segment(form, text.SegmentOptions{Breaks: text.ParagraphBreaks(form.Display)})
	if err != nil {
		b.Fatal(err)
	}
	set := pipeline.Settings{Model: "gpt-realtime-2.1", Voice: "marin", Speed: 1, Effort: "low"}
	plan, err := pipeline.Build(pipeline.Input{Display: form.Display, Seg: seg}, set)
	if err != nil {
		b.Fatal(err)
	}
	gaps, err := audio.GapSamples(audio.DefaultGaps(), plan.Joins())
	if err != nil {
		b.Fatal(err)
	}
	tasks := make([]pipeline.Task, len(plan.Chunks))
	for i, c := range plan.Chunks {
		d := time.Duration(c.EstMillis) * time.Millisecond
		tasks[i] = pipeline.Task{
			Chunk: verify.Chunk{ID: c.ID, Spoken: c.Spoken, Estimate: d},
			Spec:  realtime.SessionSpec{Model: set.Model, Voice: set.Voice, Speed: set.Speed, Instructions: "read", Effort: "low"},
		}
	}
	tr := newEcho(tasks)
	asm := audio.NewAssembler(io.Discard, gaps)
	cfg := pipeline.Config{
		Jobs: 3, Verify: verify.OptionsFor(verify.ModeNormal), Synth: estSynth{}, Transcriber: tr,
		Retry:   openai.NewRetrier(1, noSleep),
		Deliver: func(i int, r verify.ChunkResult) error { return asm.Add(i, r.PCM) },
	}
	if _, err := pipeline.Run(context.Background(), cfg, tasks); err != nil {
		b.Fatal(err)
	}
	if err := asm.Close(); err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(len(tasks)), "chunks")
}
