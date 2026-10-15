package realtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// ChunkRequest is one out-of-band response request (design §6.2).
type ChunkRequest struct {
	ChunkID string
	Attempt int
	// Text is the chunk's spoken form; it goes between the markers.
	Text string
	// Instructions are the full per-response instructions (ResponseInstructions).
	Instructions string
	// Effort is the attempt's effective effort; SessionEffort is the session's.
	// reasoning is sent only when they differ (SPK-11).
	Effort, SessionEffort Effort
}

type responseCreate struct {
	Type     string       `json:"type"`
	Response responseBody `json:"response"`
}

type responseBody struct {
	Conversation string        `json:"conversation"`
	Metadata     chunkMetadata `json:"metadata"`
	Reasoning    *reasoning    `json:"reasoning,omitempty"`
	Instructions string        `json:"instructions"`
	Input        []inputItem   `json:"input"`
}

type chunkMetadata struct {
	Chunk   string `json:"chunk"`
	Attempt string `json:"attempt"`
}

type inputItem struct {
	Type    string         `json:"type"`
	Role    string         `json:"role"`
	Content []inputContent `json:"content"`
}

type inputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ResponseCreate encodes the response.create event of r. Out of band
// (conversation "none"), so no chunk leaks into the next one's context.
func ResponseCreate(r ChunkRequest) ([]byte, error) {
	if r.ChunkID == "" || r.Attempt < 1 || r.Instructions == "" {
		return nil, fmt.Errorf("realtime: incomplete chunk request")
	}
	if !validEffort(r.Effort) || !validEffort(r.SessionEffort) {
		return nil, fmt.Errorf("realtime: unknown effort %q/%q", r.Effort, r.SessionEffort)
	}
	body := responseBody{
		Conversation: "none",
		Metadata:     chunkMetadata{Chunk: r.ChunkID, Attempt: strconv.Itoa(r.Attempt)},
		Instructions: r.Instructions,
		Input: []inputItem{{Type: "message", Role: "user", Content: []inputContent{
			{Type: "input_text", Text: UserMessage(r.Text)},
		}}},
	}
	if r.Effort != r.SessionEffort {
		body.Reasoning = &reasoning{Effort: r.Effort}
	}
	return marshal(responseCreate{Type: "response.create", Response: body})
}

// marshal is json.Marshal without HTML escaping: instructions carry the <<< >>> markers, and the
// wire body should show them as written.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("realtime: encode: %w", err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
