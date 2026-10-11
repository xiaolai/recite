//go:build live || qualify

package realtime_test

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
)

// liveSynth wires the production synthesizer to the real Codex sign-in.
func liveSynth(t *testing.T) *realtime.Synth {
	t.Helper()
	if os.Getenv("RECITE_LIVE") == "" {
		t.Skip("set RECITE_LIVE=1 to run against the real service")
	}
	st := auth.Probe(context.Background(), auth.ProbeConfig{Source: auth.SourceAuto, Getenv: os.Getenv})
	if st.Creds == nil {
		t.Fatalf("auth not ready: %s", st.State)
	}
	t.Cleanup(func() { _ = st.Close() })
	c, err := openai.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	return &realtime.Synth{Minter: &realtime.Minter{Client: c, BaseURL: openai.APIBase, Creds: st.Creds}, Client: c, BaseURL: openai.APIBase}
}

func liveOpen(t *testing.T, s *realtime.Synth, set realtime.Settings) realtime.SpeechSession {
	t.Helper()
	spec, err := set.SessionSpec()
	if err != nil {
		t.Fatal(err)
	}
	ss, err := s.Open(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	return ss
}

var nonWord = regexp.MustCompile(`[\W_]+`)

// fold is probe_hijack.canon: lower case, punctuation and spaces removed.
func fold(s string) string { return nonWord.ReplaceAllString(strings.ToLower(s), "") }
