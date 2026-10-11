package fake

import (
	"context"
	"errors"
	"testing"
	"time"

	rtfake "github.com/xiaolai/recite/internal/realtime/fake"
	"github.com/xiaolai/recite/internal/stt"
)

// accept: FND-06#1
func TestScriptedTranscriberPerChunkAndAttempt(t *testing.T) {
	tr := NewScriptedTranscriber().
		On("c3", Fail(rtfake.ErrClass("transient")), Text("three", "en")).
		On("c5", Text("Sure! Here is the text: hello world", "en"))
	tr.Default = Text("default")
	ctx := context.Background()
	hint := stt.Hint{Languages: []string{"en"}}
	pcm3 := rtfake.TaggedPCM("c3", 2, 2*time.Second)

	var ce *rtfake.ClassError
	if _, err := tr.Transcribe(ctx, pcm3, hint); !errors.As(err, &ce) {
		t.Fatalf("attempt 1: %v", err)
	}
	got, err := tr.Transcribe(ctx, pcm3, hint)
	if err != nil || got.Text != "three" || got.Seconds != 2 || got.Languages[0].Code != "en" {
		t.Fatalf("attempt 2: %+v %v", got, err)
	}
	got, _ = tr.Transcribe(ctx, rtfake.TaggedPCM("c5", 1, time.Second), hint)
	if got.Text != "Sure! Here is the text: hello world" {
		t.Fatalf("c5: %q", got.Text)
	}
	if got, _ := tr.Transcribe(ctx, make([]byte, 100), stt.Hint{}); got.Text != "default" {
		t.Fatalf("untagged: %q", got.Text)
	}
	calls := tr.Calls()
	if len(calls) != 4 || calls[1].ChunkID != "c3" || calls[1].Attempt != 2 || calls[1].TakeAttempt != 2 || calls[3].ChunkID != "" {
		t.Fatalf("calls = %+v", calls)
	}
}
