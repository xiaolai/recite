package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xiaolai/recite/internal/auth"
	"github.com/xiaolai/recite/internal/openai"
)

// Session-body constants (design §6.1).
const (
	// SecretTTL is the lifetime asked for a minted client secret.
	SecretTTL = 600 * time.Second
	// SampleRate is the PCM rate of realtime audio, in and out.
	SampleRate = 24000

	mintPath     = "/v1/realtime/client_secrets"
	maxMintReply = 1 << 20
)

// Efforts lists the reasoning efforts the session accepts.
var Efforts = []Effort{"minimal", "low", "medium", "high", "xhigh"}

// The wire shapes of the mint body. Every field is typed so a golden test pins
// the exact JSON; nothing is built from maps.
type mintBody struct {
	Session      any          `json:"session"`
	ExpiresAfter expiresAfter `json:"expires_after"`
}

type expiresAfter struct {
	Anchor  string `json:"anchor"`
	Seconds int    `json:"seconds"`
}

type speechSession struct {
	Type             string      `json:"type"`
	Model            string      `json:"model"`
	Instructions     string      `json:"instructions"`
	Tools            []struct{}  `json:"tools"`
	ToolChoice       string      `json:"tool_choice"`
	OutputModalities []string    `json:"output_modalities"`
	Reasoning        reasoning   `json:"reasoning"`
	Audio            speechAudio `json:"audio"`
}

type reasoning struct {
	Effort Effort `json:"effort"`
}

type speechAudio struct {
	Output audioOutput `json:"output"`
}

type audioOutput struct {
	Format audioFormat `json:"format"`
	Voice  string      `json:"voice"`
	Speed  float64     `json:"speed"`
}

type audioFormat struct {
	Type string `json:"type"`
	Rate int    `json:"rate"`
}

type transcriptionSession struct {
	Type  string             `json:"type"`
	Audio transcriptionAudio `json:"audio"`
}

type transcriptionAudio struct {
	Input transcriptionInput `json:"input"`
}

type transcriptionInput struct {
	Format        audioFormat        `json:"format"`
	Transcription transcriptionModel `json:"transcription"`
	// TurnDetection is always null: recite commits the audio buffer itself.
	TurnDetection *struct{} `json:"turn_detection"`
}

type transcriptionModel struct {
	Model string `json:"model"`
}

func pcm() audioFormat { return audioFormat{Type: "audio/pcm", Rate: SampleRate} }

func expiry() expiresAfter {
	return expiresAfter{Anchor: "created_at", Seconds: int(SecretTTL / time.Second)}
}

// SpeechSessionBody is the mint body of a speech session (design §6.1): no
// tools, audio output only, PCM 24 kHz. User-facing validation of the values
// is SPK-02..05; a bad value here is a programming error.
func SpeechSessionBody(spec SessionSpec) ([]byte, error) {
	if spec.Model == "" || spec.Voice == "" || spec.Speed <= 0 {
		return nil, fmt.Errorf("realtime: incomplete speech session spec")
	}
	if !validEffort(spec.Effort) {
		return nil, fmt.Errorf("realtime: unknown effort %q", spec.Effort)
	}
	return marshal(mintBody{
		Session: speechSession{
			Type:             "realtime",
			Model:            spec.Model,
			Instructions:     spec.Instructions,
			Tools:            []struct{}{},
			ToolChoice:       "none",
			OutputModalities: []string{"audio"},
			Reasoning:        reasoning{Effort: spec.Effort},
			Audio:            speechAudio{Output: audioOutput{Format: pcm(), Voice: spec.Voice, Speed: spec.Speed}},
		},
		ExpiresAfter: expiry(),
	})
}

// TranscriptionSessionBody is the mint body of a realtime transcription
// session, the VER-02 fallback verifier.
func TranscriptionSessionBody(model string) ([]byte, error) {
	if model == "" {
		return nil, fmt.Errorf("realtime: empty transcription model")
	}
	return marshal(mintBody{
		Session: transcriptionSession{
			Type: "transcription",
			Audio: transcriptionAudio{Input: transcriptionInput{
				Format:        pcm(),
				Transcription: transcriptionModel{Model: model},
			}},
		},
		ExpiresAfter: expiry(),
	})
}

// marshal is json.Marshal without HTML escaping: instructions carry the <<< >>>
// markers, and the wire body should show them as written.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("realtime: encode mint body: %w", err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

func validEffort(e Effort) bool {
	for _, v := range Efforts {
		if e == v {
			return true
		}
	}
	return false
}

// Credentials yields the bearer and refreshes it after a refusal; *auth.Credentials
// implements it (AUTH-04).
type Credentials interface {
	Token(ctx context.Context) (auth.Token, error)
	RefreshAfter(ctx context.Context, stale auth.Token) (auth.Token, error)
}

// Minter mints ephemeral client secrets (NET-02).
type Minter struct {
	Client  *http.Client // the production client from openai.NewClient
	BaseURL string       // openai.APIBase; tests use a reserved test origin
	Creds   Credentials
}

// MintSpeech mints a secret for a speech session.
func (m *Minter) MintSpeech(ctx context.Context, spec SessionSpec) (auth.ClientSecret, error) {
	body, err := SpeechSessionBody(spec)
	if err != nil {
		return auth.ClientSecret{}, err
	}
	return m.mint(ctx, body, openai.RouteSpeechMint)
}

// MintTranscription mints a secret for a realtime transcription session.
func (m *Minter) MintTranscription(ctx context.Context, model string) (auth.ClientSecret, error) {
	body, err := TranscriptionSessionBody(model)
	if err != nil {
		return auth.ClientSecret{}, err
	}
	return m.mint(ctx, body, openai.RouteRealtimeSTT)
}

// mint posts body; on auth-expired it refreshes once through Creds and retries
// once (AUTH-04).
func (m *Minter) mint(ctx context.Context, body []byte, route openai.Route) (auth.ClientSecret, error) {
	tok, err := m.Creds.Token(ctx)
	if err != nil {
		return auth.ClientSecret{}, err
	}
	sec, err := m.post(ctx, tok, body, route)
	if openai.Classify(err) != openai.ClassAuthExpired {
		return sec, err
	}
	fresh, rerr := m.Creds.RefreshAfter(ctx, tok)
	if rerr != nil {
		return auth.ClientSecret{}, rerr
	}
	sec, err = m.post(ctx, fresh, body, route)
	if openai.Classify(err) == openai.ClassAuthExpired {
		return auth.ClientSecret{}, &openai.Error{Class: openai.ClassAuthExpired, Message: "the refreshed ChatGPT sign-in was refused too; run `codex login`", Err: err}
	}
	return sec, err
}

// post makes one mint request. Neither the request nor the response body is
// ever logged: both carry or yield secrets.
func (m *Minter) post(ctx context.Context, tok auth.Token, body []byte, route openai.Route) (auth.ClientSecret, error) {
	ctx, cancel := context.WithTimeout(ctx, openai.MintTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(m.BaseURL, "/")+mintPath, bytes.NewReader(body))
	if err != nil {
		return auth.ClientSecret{}, fmt.Errorf("realtime: mint request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.Bearer())
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.Client.Do(req)
	if err != nil {
		return auth.ClientSecret{}, fmt.Errorf("realtime: mint: %w", err)
	}
	if err := openai.CheckResponse(resp, route); err != nil {
		return auth.ClientSecret{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMintReply))
	defer clear(raw) // the reply holds the secret
	if err != nil {
		return auth.ClientSecret{}, fmt.Errorf("realtime: mint: read reply: %w", err)
	}
	var reply struct {
		Value     *string `json:"value"`
		ExpiresAt *int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return auth.ClientSecret{}, &openai.Error{Class: openai.ClassProtocol, Message: "mint reply is not valid JSON", Err: errors.New("malformed mint reply")}
	}
	if reply.Value == nil || *reply.Value == "" || reply.ExpiresAt == nil {
		return auth.ClientSecret{}, &openai.Error{Class: openai.ClassProtocol, Message: "mint reply lacks value or expires_at"}
	}
	return auth.NewClientSecret(*reply.Value, time.Unix(*reply.ExpiresAt, 0)), nil
}
