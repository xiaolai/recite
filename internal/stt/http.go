package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"syscall"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
)

const (
	// HTTPModel is the HTTP transcription model (VER-01, E15).
	HTTPModel = "gpt-transcribe"
	// Padding is the silence added on each side of a take before transcription.
	Padding = 300 // milliseconds

	transcriptionsPath = "/v1/audio/transcriptions"
	maxReply           = 16 << 20
)

// TokenSource yields the ChatGPT bearer; *auth.Credentials implements it.
type TokenSource interface {
	Token(ctx context.Context) (auth.Token, error)
}

// HTTP transcribes a take with one multipart POST /v1/audio/transcriptions (VER-01).
type HTTP struct {
	Client  *http.Client // the production client from openai.NewClient
	BaseURL string       // openai.APIBase; tests use a reserved test origin
	Creds   TokenSource
	Logger  *slog.Logger // nil discards

	// Boundary fixes the multipart boundary; empty means a random one. For golden tests.
	Boundary string
}

var _ Transcriber = (*HTTP)(nil)

// Transcribe implements Transcriber. A dropped connection (E26: about one call in fifty) is
// retried once; every other failure is returned classified, a 401 missing_scope as
// route-denied on route http-stt.
func (h *HTTP) Transcribe(ctx context.Context, pcm []byte, hint Hint) (Transcript, error) {
	if h.Client == nil || h.BaseURL == "" || h.Creds == nil {
		return Transcript{}, errors.New("stt: HTTP needs a Client, a BaseURL and Creds")
	}
	padded := Pad(pcm)
	body, contentType, err := h.Body(padded, hint)
	if err != nil {
		return Transcript{}, err
	}
	tok, err := h.Creds.Token(ctx)
	if err != nil {
		return Transcript{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, openai.STTTimeout(audio.PCM(padded).Duration()))
	defer cancel()
	t, err := h.post(ctx, tok, body, contentType)
	if err != nil && dropped(err) && ctx.Err() == nil {
		h.logger().Info("stt: connection dropped; retrying the transcription once", "err", err)
		t, err = h.post(ctx, tok, body, contentType)
	}
	return t, err
}

func (h *HTTP) logger() *slog.Logger {
	if h.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return h.Logger
}

// Pad surrounds pcm with Padding milliseconds of silence on each side.
func Pad(pcm []byte) []byte {
	n := audio.BytesPerSecond * Padding / 1000
	out := make([]byte, n+len(pcm)+n)
	copy(out[n:], pcm)
	return out
}

// Body builds the multipart request body for an already padded take: model, file (WAV),
// languages[] per hint language, prompt (the comma-joined keywords) when there are any, and
// include[]=logprobs. It returns the body and its Content-Type.
func (h *HTTP) Body(padded []byte, hint Hint) ([]byte, string, error) {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	if h.Boundary != "" {
		if err := mw.SetBoundary(h.Boundary); err != nil {
			return nil, "", fmt.Errorf("stt: boundary: %w", err)
		}
	}
	field := func(name, value string) error { return mw.WriteField(name, value) }
	if err := field("model", HTTPModel); err != nil {
		return nil, "", err
	}
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="file"; filename="take.wav"`)
	hdr.Set("Content-Type", "audio/wav")
	part, err := mw.CreatePart(hdr)
	if err != nil {
		return nil, "", fmt.Errorf("stt: multipart: %w", err)
	}
	if err := audio.WriteWAV(part, padded); err != nil {
		return nil, "", fmt.Errorf("stt: encode take: %w", err)
	}
	for _, l := range hint.Languages {
		if err := field("languages[]", l); err != nil {
			return nil, "", err
		}
	}
	if len(hint.Keywords) > 0 {
		if err := field("prompt", strings.Join(hint.Keywords, ", ")); err != nil {
			return nil, "", err
		}
	}
	if err := field("include[]", "logprobs"); err != nil {
		return nil, "", err
	}
	if err := mw.Close(); err != nil {
		return nil, "", fmt.Errorf("stt: multipart: %w", err)
	}
	return b.Bytes(), mw.FormDataContentType(), nil
}

func (h *HTTP) post(ctx context.Context, tok auth.Token, body []byte, contentType string) (Transcript, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(h.BaseURL, "/")+transcriptionsPath, bytes.NewReader(body))
	if err != nil {
		return Transcript{}, fmt.Errorf("stt: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.Bearer())
	req.Header.Set("Content-Type", contentType)
	resp, err := h.Client.Do(req)
	if err != nil {
		return Transcript{}, transportError("stt: transcription request", err)
	}
	if err := openai.CheckResponse(resp, openai.RouteHTTPSTT); err != nil {
		return Transcript{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReply))
	if err != nil {
		return Transcript{}, transportError("stt: read transcription", err)
	}
	return DecodeTranscription(raw)
}

// DecodeTranscription parses a transcription reply: text (required), languages (a possibly
// empty list of {"code": …}), logprobs and usage.seconds (rounded up to whole seconds).
func DecodeTranscription(raw []byte) (Transcript, error) {
	var reply struct {
		Text      *string            `json:"text"`
		Languages []DetectedLanguage `json:"languages"`
		Logprobs  []TokenLogprob     `json:"logprobs"`
		Usage     struct {
			Seconds float64 `json:"seconds"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return Transcript{}, &openai.Error{Class: openai.ClassProtocol, Message: "transcription reply is not valid JSON", Err: err}
	}
	if reply.Text == nil {
		return Transcript{}, &openai.Error{Class: openai.ClassProtocol, Message: "transcription reply lacks text"}
	}
	t := Transcript{
		Text:      *reply.Text,
		Languages: reply.Languages,
		Tokens:    reply.Logprobs,
		Seconds:   int(math.Ceil(reply.Usage.Seconds)),
	}
	if t.Languages == nil {
		t.Languages = []DetectedLanguage{}
	}
	return t, nil
}

// dropped reports a connection that broke before a reply was complete (E26). Timeouts,
// refusals and HTTP errors are not drops.
func dropped(err error) bool {
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET)
}

// transportError classifies a connection failure, keeping the cause for dropped().
func transportError(msg string, err error) error {
	var classified *openai.Error
	if errors.Is(err, context.Canceled) || errors.As(err, &classified) {
		return fmt.Errorf("%s: %w", msg, err) // already classified (the origin policy, a cancel)
	}
	class := openai.Classify(err)
	if class == openai.ClassInternal {
		class = openai.ClassNetwork
	}
	return &openai.Error{Class: class, Message: msg, Err: err}
}
