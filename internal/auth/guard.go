package auth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xiaolai/recite/internal/logx"
	"github.com/xiaolai/recite/internal/openai"
)

// MsgAPIKey is the AUTH-03 refusal for an API-key sign-in.
const MsgAPIKey = "Codex is signed in with an API key; recite works only with a ChatGPT sign-in"

// modeChatGPT is the only accepted sign-in (AUTH-03).
const modeChatGPT = "chatgpt"

func authModeError(msg string) error { return &openai.Error{Class: openai.ClassAuthMode, Message: msg} }

func codexError(msg string) error { return &openai.Error{Class: openai.ClassCodexError, Message: msg} }

// guard accepts only a ChatGPT sign-in; method is authMethod or auth_mode, "" when absent.
// The full state classification and guides are AUTH-07 (WP-1.4).
func guard(method string) error {
	switch method {
	case modeChatGPT:
		return nil
	case "":
		return authModeError("Codex is not signed in; run `codex login`")
	case "apikey":
		return authModeError(MsgAPIKey)
	}
	if len(method) > 64 {
		method = method[:64]
	}
	return authModeError(fmt.Sprintf("Codex is signed in with %q, which recite does not support; recite needs a ChatGPT sign-in", logx.Scrub(method)))
}

// newBearer validates a ChatGPT access token and wraps it. The signature is not
// checked: exp is read for user experience only (design §5.1).
func newBearer(bearer, source string) (Token, error) {
	logx.RegisterSecret(bearer)
	exp, err := jwtExpiry(bearer)
	if err != nil {
		return Token{}, codexError("Codex reported a ChatGPT sign-in without a usable token")
	}
	return NewToken(bearer, exp, "", source), nil
}

func jwtExpiry(tok string) (time.Time, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || !strings.HasPrefix(tok, "eyJ") {
		return time.Time{}, fmt.Errorf("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("JWT payload: not base64url")
	}
	var claims struct {
		Exp *int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == nil {
		return time.Time{}, fmt.Errorf("JWT payload: no exp")
	}
	return time.Unix(*claims.Exp, 0), nil
}
