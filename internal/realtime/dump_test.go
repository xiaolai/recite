package realtime_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/realtime/fake"
)

// accept: OBS-02#1
func TestProtocolFaultDumpsEventsAndNamesThePath(t *testing.T) {
	state := t.TempDir()
	srv := fake.NewServer(t, loadScenario(t, "speak"), fake.WithoutAudio())
	s := newSynth(t, srv, newStatic())
	s.Dumps = &realtime.Dumps{StateDir: state} // no --debug-dir
	ss := open(t, s)
	_, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "Hello from recite.", Attempt: 2})
	if openai.Classify(err) != openai.ClassProtocol {
		t.Fatalf("err %v, want protocol", err)
	}
	path := filepath.Join(state, "c0001-a2.events.jsonl")
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error %q does not name the dump %s", err, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 2 || !strings.Contains(string(b), `{"dir":"client","event":{"type":"response.create"`) || !strings.Contains(string(b), `"response.done"`) {
		t.Fatalf("dump lacks the request and the response events:\n%s", b)
	}
	for _, l := range lines {
		if !json.Valid([]byte(l)) {
			t.Fatalf("invalid JSON line %q", l)
		}
	}
}

func TestDebugDirDumpsEveryRenderWithAudioRedacted(t *testing.T) {
	dir := t.TempDir()
	srv := fake.NewServer(t, loadScenario(t, "speak"))
	s := newSynth(t, srv, newStatic())
	s.Dumps = &realtime.Dumps{Dir: dir, StateDir: t.TempDir()}
	ss := open(t, s)
	if _, err := ss.Render(context.Background(), realtime.RenderRequest{ChunkID: "c0001", Text: "Hello from recite."}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "c0001-a1.events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), realtime.TypeAudioDelta) || !strings.Contains(string(b), ` bytes>"`) {
		t.Fatalf("audio deltas missing or not redacted:\n%s", b)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var rec struct {
			Event struct {
				Type  string `json:"type"`
				Delta string `json:"delta"`
			} `json:"event"`
		}
		if err := json.Unmarshal([]byte(l), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Event.Type == realtime.TypeAudioDelta && !strings.HasPrefix(rec.Event.Delta, "<") {
			t.Fatalf("audio payload kept: %.40q", rec.Event.Delta)
		}
	}
}

func TestRedactAudio(t *testing.T) {
	var m map[string]string
	if err := json.Unmarshal(realtime.RedactAudio([]byte(`{"type":"input_audio_buffer.append","audio":"AAAA"}`)), &m); err != nil {
		t.Fatal(err)
	}
	if m["audio"] != "<3 bytes>" || m["type"] != "input_audio_buffer.append" {
		t.Fatalf("got %v", m)
	}
	if got := string(realtime.RedactAudio([]byte(`not json AAAA`))); strings.Contains(got, "AAAA") {
		t.Fatalf("unparsed message kept: %s", got)
	}
	keep := `{"type":"response.done"}`
	if got := string(realtime.RedactAudio([]byte(keep))); got != keep {
		t.Fatalf("non-audio event changed: %s", got)
	}
}
