package realtime

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/xiaolai/recite/internal/logx"
	"github.com/xiaolai/recite/internal/openai"
)

// Dumps configures the event dumps of OBS-02.
type Dumps struct {
	// Dir is --debug-dir: when set, every render's events are written there.
	Dir string
	// StateDir receives the dump of a protocol failure when Dir is empty.
	StateDir string
}

// DumpName is the file name of a render's event dump.
func DumpName(chunkID string, attempt int) string {
	return fmt.Sprintf("%s-a%d.events.jsonl", chunkID, attempt)
}

// eventLog collects one response's events, audio payloads already redacted.
type eventLog struct {
	mu    sync.Mutex
	lines [][]byte
}

func (l *eventLog) record(dir string, raw []byte) {
	line, err := marshalPlain(struct {
		Dir   string          `json:"dir"`
		Event json.RawMessage `json:"event"`
	}{dir, RedactAudio(raw)})
	if err != nil { // RedactAudio always yields valid JSON
		return
	}
	l.mu.Lock()
	l.lines = append(l.lines, []byte(logx.Scrub(string(line))))
	l.mu.Unlock()
}

func (l *eventLog) reset() {
	l.mu.Lock()
	l.lines = nil
	l.mu.Unlock()
}

// write writes the collected events to dir/name with owner-only permissions.
func (l *eventLog) write(dir, name string) (string, error) {
	l.mu.Lock()
	body := bytes.Join(l.lines, []byte("\n"))
	l.mu.Unlock()
	if len(body) > 0 {
		body = append(body, '\n')
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("realtime: event dump: %w", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", fmt.Errorf("realtime: event dump: %w", err)
	}
	return path, nil
}

// audioFields are the base64 audio payloads by event type.
var audioFields = map[string]string{
	TypeAudioDelta:               "delta",
	"input_audio_buffer.append":  "audio",
	"response.output_audio.done": "audio",
}

// RedactAudio returns the event with its audio payload replaced by "<N bytes>" (N decoded
// bytes). A message that is not a JSON object is replaced by {"unparsed":"<N bytes>"}, so
// nothing it carries reaches a dump.
func RedactAudio(raw []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		b, _ := marshalPlain(map[string]string{"unparsed": fmt.Sprintf("<%d bytes>", len(raw))})
		return b
	}
	var typ string
	_ = json.Unmarshal(m["type"], &typ)
	field, ok := audioFields[typ]
	if !ok {
		return raw
	}
	payload, present := m[field]
	if !present {
		return raw
	}
	n := len(payload)
	var s string
	if json.Unmarshal(payload, &s) == nil {
		if dec, err := base64.StdEncoding.DecodeString(s); err == nil {
			n = len(dec)
		} else {
			n = len(s)
		}
	}
	m[field], _ = marshalPlain(fmt.Sprintf("<%d bytes>", n))
	out, err := marshalPlain(m)
	if err != nil {
		b, _ := marshalPlain(map[string]string{"unparsed": fmt.Sprintf("<%d bytes>", len(raw))})
		return b
	}
	return out
}

// dumpRender writes the response's events: always under Dumps.Dir when set, and for a
// protocol failure in any case (Dumps.Dir, else Dumps.StateDir), naming the file in the
// returned error (OBS-02). Other errors are returned unchanged.
func dumpRender(d *Dumps, log *eventLog, chunkID string, attempt int, renderErr error) error {
	if d == nil || log == nil {
		return renderErr
	}
	name := DumpName(chunkID, attempt)
	var oe *openai.Error
	protocol := errors.As(renderErr, &oe) && oe.Class == openai.ClassProtocol
	dir := d.Dir
	if dir == "" && protocol {
		dir = d.StateDir
	}
	if dir == "" {
		return renderErr
	}
	path, err := log.write(dir, name)
	if !protocol {
		return renderErr
	}
	cp := *oe
	if err != nil {
		cp.Message += fmt.Sprintf(" (the event log could not be written: %v)", err)
	} else {
		cp.Message += " (event log: " + path + ")"
	}
	return &cp
}

// marshalPlain is json.Marshal without HTML escaping, so "<N bytes>" reads as written.
func marshalPlain(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
