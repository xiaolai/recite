package fake

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/realtime"
)

var _ realtime.Synthesizer = (*ScriptedSynthesizer)(nil)

// accept: FND-06#1
func TestScriptedSynthesizerChunkFailsTwiceThenPasses(t *testing.T) {
	syn := NewScriptedSynthesizer().
		On("c3", Fail(ErrClass("transient")), Fail(ErrClass("transient")), Pass(2*time.Second)).
		On("c5", Say("Sure! Here is the text: hello world", time.Second))
	ctx := context.Background()
	sess, err := syn.Open(ctx, realtime.SessionSpec{Model: "gpt-realtime-2.1", Voice: "marin"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		_, err := sess.Render(ctx, realtime.RenderRequest{ChunkID: "c3", Text: "three"})
		var ce *ClassError
		if !errors.As(err, &ce) || ce.Class != "transient" {
			t.Fatalf("attempt %d: err = %v", i, err)
		}
	}
	take, err := sess.Render(ctx, realtime.RenderRequest{ChunkID: "c3", Text: "three"})
	if err != nil || take.Said != "three" || take.Status != realtime.StatusCompleted || len(take.PCM) != 2*BytesPerSecond {
		t.Fatalf("attempt 3: %v said=%q status=%q pcm=%d", err, take.Said, take.Status, len(take.PCM))
	}
	if id, a, ok := PCMTag(take.PCM); !ok || id != "c3" || a != 3 {
		t.Fatalf("tag = %q %d %v", id, a, ok)
	}
	take, err = sess.Render(ctx, realtime.RenderRequest{ChunkID: "c5", Text: "hello world"})
	if err != nil || take.Said != "Sure! Here is the text: hello world" {
		t.Fatalf("c5: %v %q", err, take.Said)
	}
	if syn.Attempts("c3") != 3 || len(syn.Calls()) != 4 || syn.Calls()[3].Attempt != 1 {
		t.Fatalf("calls = %+v", syn.Calls())
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Render(ctx, realtime.RenderRequest{ChunkID: "c1"}); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("render after close: %v", err)
	}
}

// accept: FND-06#1
func TestScriptedSynthesizerStatusOpenFailureAndRace(t *testing.T) {
	syn := NewScriptedSynthesizer().
		FailOpen(ErrClass("auth-expired")).
		On("c1", Outcome{Echo: true, Duration: time.Second, Status: realtime.StatusIncomplete})
	ctx := context.Background()
	if _, err := syn.Open(ctx, realtime.SessionSpec{}); err == nil {
		t.Fatal("open did not fail")
	}
	sess, err := syn.Open(ctx, realtime.SessionSpec{})
	if err != nil {
		t.Fatal(err)
	}
	take, err := sess.Render(ctx, realtime.RenderRequest{ChunkID: "c1", Text: "x"})
	if err != nil || take.Status != realtime.StatusIncomplete {
		t.Fatalf("status = %q, %v", take.Status, err)
	}
	// Concurrent renders: per-chunk attempts stay exact under -race.
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if _, err := sess.Render(ctx, realtime.RenderRequest{ChunkID: fmt.Sprintf("k%d", i)}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	for i := 0; i < 10; i++ {
		if n := syn.Attempts(fmt.Sprintf("k%d", i)); n != 8 {
			t.Fatalf("k%d attempts = %d, want 8", i, n)
		}
	}
	if len(syn.Opens()) != 2 || sess.Age() != 81*time.Second {
		t.Fatalf("opens=%d age=%v", len(syn.Opens()), sess.Age())
	}
}

// accept: FND-06#1
func TestPCMTagSurvivesSilencePadding(t *testing.T) {
	// Verification pads takes with 300 ms of silence before transcription (design §8.2), so the
	// tag must still be found when it is not at the start of the buffer.
	pcm := TaggedPCM("c7", 2, 10*time.Millisecond)
	padded := append(make([]byte, 14400), pcm...)
	padded = append(padded, make([]byte, 14400)...)
	if id, a, ok := PCMTag(padded); !ok || id != "c7" || a != 2 {
		t.Fatalf("PCMTag(padded) = %q, %d, %v; want c7, 2, true", id, a, ok)
	}
}
