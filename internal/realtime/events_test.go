package realtime_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/openai"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/realtime/fake"
)

// accept: NET-04#1
func TestEveryFixtureDecodes(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fake.FixtureDir(), "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	consumed := map[string]int{}
	for _, f := range files {
		sc, err := fake.ParseScenarioFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, st := range sc.Steps {
			if st.Dir != "server" {
				continue
			}
			ev, err := realtime.DecodeEvent(st.Event)
			if err != nil {
				t.Fatalf("%s step %d (%s): %v", filepath.Base(f), i, st.Type, err)
			}
			if _, ok := ev.(realtime.Unknown); ok {
				t.Errorf("%s step %d: fixture type %s decodes as unknown", filepath.Base(f), i, st.Type)
			}
			if ev.EventType() != st.Type {
				t.Errorf("type %q, want %q", ev.EventType(), st.Type)
			}
			consumed[st.Type]++
		}
	}
	for _, typ := range []string{realtime.TypeSessionCreated, realtime.TypeResponseCreated, realtime.TypeAudioDelta,
		realtime.TypeTranscriptDelta, realtime.TypeResponseDone, realtime.TypeRateLimitsUpdated, realtime.TypeError,
		realtime.TypeInputCommitted, realtime.TypeTranscriptionDelta, realtime.TypeTranscriptionCompleted} {
		if consumed[typ] == 0 {
			t.Errorf("no fixture exercises %s", typ)
		}
	}
}

func TestDecodedFields(t *testing.T) {
	sc := loadScenario(t, "speak")
	var pcm int
	var done realtime.ResponseDone
	for _, st := range sc.Steps {
		if st.Dir != "server" {
			continue
		}
		ev, err := realtime.DecodeEvent(st.Event)
		if err != nil {
			t.Fatal(err)
		}
		switch e := ev.(type) {
		case realtime.AudioDelta:
			pcm += len(e.PCM)
		case realtime.ResponseDone:
			done = e
		}
	}
	if pcm == 0 || done.Status != realtime.StatusCompleted || done.Usage.InputTokens != 102 || done.Usage.OutputTokens != 61 ||
		done.Metadata["chunk"] != "c0001" {
		t.Fatalf("pcm %d done %+v", pcm, done)
	}
	ev, err := realtime.DecodeEvent([]byte(`{"type":"rate_limits.updated","rate_limits":[{"name":"tokens","limit":10,"remaining":0,"reset_seconds":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	wantClass(t, ev.(realtime.RateLimitsUpdated).Exhausted(), openai.ClassRateLimited)
	ev, err = realtime.DecodeEvent([]byte(`{"type":"error","error":{"type":"server_error","code":null,"message":"x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	wantClass(t, ev.(realtime.ErrorEvent).Err, openai.ClassServer)
}

// accept: NET-04#3
func TestResponseDoneWithoutStatusIsProtocol(t *testing.T) {
	_, err := realtime.DecodeEvent([]byte(`{"type":"response.done","response":{"id":"resp_1","output":[]}}`))
	wantClass(t, err, openai.ClassProtocol)
	if got := err.Error(); !strings.Contains(got, "response.done") || !strings.Contains(got, "response.status") {
		t.Fatalf("error does not name the event and field: %v", err)
	}
}

// A status outside the four documented values is as unusable as a missing one: the take
// cannot be judged, so it must fail loud (retried on a fresh session, with an event dump)
// instead of reaching the status guard as a silent reject.
func TestResponseDoneWithUnknownStatusIsProtocol(t *testing.T) {
	for _, status := range []string{``, `in_progress`, `Completed`} {
		_, err := realtime.DecodeEvent([]byte(`{"type":"response.done","response":{"id":"resp_1","status":"` + status + `"}}`))
		wantClass(t, err, openai.ClassProtocol)
		if got := err.Error(); !strings.Contains(got, "response.status") {
			t.Fatalf("status %q: error does not name the field: %v", status, err)
		}
	}
	for _, status := range []string{"completed", "failed", "incomplete", "cancelled"} {
		ev, err := realtime.DecodeEvent([]byte(`{"type":"response.done","response":{"id":"resp_1","status":"` + status + `"}}`))
		if err != nil || ev.(realtime.ResponseDone).Status != realtime.ResponseStatus(status) {
			t.Fatalf("status %q: %v, %v", status, ev, err)
		}
	}
}

func TestMissingRequiredFieldsAreProtocol(t *testing.T) {
	for _, raw := range []string{
		`not json`,
		`{"event_id":"x"}`,
		`{"type":""}`,
		`{"type":"session.created"}`,
		`{"type":"response.created","response":{}}`,
		`{"type":"response.output_audio.delta","response_id":"r"}`,
		`{"type":"response.output_audio.delta","response_id":"r","delta":"!!not base64"}`,
		`{"type":"response.output_audio_transcript.delta","delta":"x"}`,
		`{"type":"response.done"}`,
		`{"type":"rate_limits.updated"}`,
		`{"type":"rate_limits.updated","rate_limits":[{"name":"tokens"}]}`,
		`{"type":"error"}`,
		`{"type":"input_audio_buffer.committed"}`,
		`{"type":"conversation.item.input_audio_transcription.delta","item_id":"i"}`,
		`{"type":"conversation.item.input_audio_transcription.completed","item_id":"i"}`,
		`{"type":"conversation.item.input_audio_transcription.failed","item_id":"i"}`,
	} {
		if _, err := realtime.DecodeEvent([]byte(raw)); openai.Classify(err) != openai.ClassProtocol {
			t.Errorf("%s: err %v, want protocol", raw, err)
		}
	}
	ev, err := realtime.DecodeEvent([]byte(`{"type":"brand.new_event","x":1}`))
	if u, ok := ev.(realtime.Unknown); err != nil || !ok || u.Type != "brand.new_event" {
		t.Fatalf("unknown type: %v %v", ev, err)
	}
}

// accept: NET-04#4
func FuzzDecodeEvent(f *testing.F) {
	// The checked-in corpus is testdata/fuzz/FuzzDecodeEvent; every fixture event seeds it too.
	files, _ := filepath.Glob(filepath.Join(fake.FixtureDir(), "*.jsonl"))
	for _, file := range files {
		sc, err := fake.ParseScenarioFile(file)
		if err != nil {
			f.Fatal(err)
		}
		for _, st := range sc.Steps {
			if len(st.Event) < 4096 {
				f.Add([]byte(st.Event))
			}
		}
	}
	if _, err := os.Stat(filepath.Join("testdata", "fuzz", "FuzzDecodeEvent")); err != nil {
		f.Fatalf("seed corpus missing: %v", err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		ev, err := realtime.DecodeEvent(data)
		if (ev == nil) == (err == nil) {
			t.Fatalf("event %v and error %v", ev, err)
		}
		if err != nil && openai.Classify(err) != openai.ClassProtocol {
			t.Fatalf("decode error of class %s", openai.Classify(err))
		}
	})
}

// The usage details split text from audio tokens for the manifest and the price estimate.
func TestResponseDoneUsageDetails(t *testing.T) {
	ev, err := realtime.DecodeEvent([]byte(`{"type":"response.done","response":{"id":"r","status":"completed",` +
		`"usage":{"input_tokens":120,"output_tokens":435,"input_token_details":{"text_tokens":118,"audio_tokens":2},` +
		`"output_token_details":{"text_tokens":80,"audio_tokens":355}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := ev.(realtime.ResponseDone).Usage
	want := realtime.TokenUsage{InputTokens: 120, OutputTokens: 435, InputTextTokens: 118, OutputTextTokens: 80, OutputAudioTokens: 355, Detailed: true}
	if got != want {
		t.Fatalf("usage %+v, want %+v", got, want)
	}
}
