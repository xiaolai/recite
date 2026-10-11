//go:build live

package stt_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/stt"
)

const liveSentence = "The quick brown fox jumps over the lazy dog."

// accept: VER-01#4
func TestLiveHTTPKnownSentence(t *testing.T) {
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to run against the real service")
	}
	ctx := context.Background()
	st := auth.Probe(ctx, auth.ProbeConfig{Source: auth.SourceAuto, Getenv: os.Getenv})
	if st.Creds == nil {
		t.Fatalf("auth not ready: %s", st.State)
	}
	t.Cleanup(func() { _ = st.Close() })
	c, err := openai.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	synth := &realtime.Synth{Minter: &realtime.Minter{Client: c, BaseURL: openai.APIBase, Creds: st.Creds}, Client: c, BaseURL: openai.APIBase}
	spec, err := realtime.DefaultSettings().SessionSpec()
	if err != nil {
		t.Fatal(err)
	}
	ss, err := synth.Open(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	take, err := ss.Render(ctx, realtime.RenderRequest{ChunkID: "c0001", Text: liveSentence})
	if err != nil || take.Status != realtime.StatusCompleted {
		t.Fatalf("render: %v, status %q", err, take.Status)
	}
	h := &stt.HTTP{Client: c, BaseURL: openai.APIBase, Creds: st.Creds}
	got, err := h.Transcribe(ctx, take.PCM, stt.Hint{Languages: []string{"en"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got.Text) != liveSentence {
		t.Errorf("text = %q, want %q", got.Text, liveSentence)
	}
	if len(got.Languages) == 0 || got.Languages[0].Code != "en" {
		t.Errorf("languages = %+v, want en first", got.Languages)
	}
	t.Logf("seconds %d, %d logprobs", got.Seconds, len(got.Tokens))
}
