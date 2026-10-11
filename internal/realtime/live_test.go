//go:build live

package realtime_test

import (
	"context"
	"testing"

	"github.com/xiaolai/recite/internal/realtime"
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

// The model's own transcript stands in for STT until the verifier lands (VER-01).
//
// accept: SPK-01#4
func TestLiveRenderHello(t *testing.T) {
	take := liveRender(t, liveSynth(t), realtime.DefaultSettings(), liveHello)
	if fold(take.Said) != fold(liveHello) {
		t.Fatalf("Said = %q, want %q", take.Said, liveHello)
	}
	t.Logf("first audio %v, wall %v, %d bytes, usage %+v", take.FirstAudio, take.Wall, len(take.PCM), take.Usage)
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
			take := liveRender(t, s, set, liveHello)
			if fold(take.Said) != fold(liveHello) {
				t.Errorf("Said = %q", take.Said)
			}
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
