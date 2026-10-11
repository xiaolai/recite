package auth

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xiaolai/recite/internal/logx"
)

const (
	canaryBearer = "eyJcanary.canary.canary"
	canarySecret = "ek_canary"
)

// accept: AUTH-05#4
func TestSecretsNeverFormat(t *testing.T) {
	tok := NewToken(canaryBearer, time.Unix(0, 0), "plus", "codex")
	cs := NewClientSecret(canarySecret, time.Unix(0, 0))
	for _, v := range []any{tok, &tok, cs, &cs, []any{tok, cs}, struct{ T Token }{tok}} {
		out := fmt.Sprintf("%v %+v %#v %s %q %x %d", v, v, v, v, v, v, v)
		js, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %T: %v", v, err)
		}
		for _, s := range []string{out, string(js)} {
			if strings.Contains(s, "canary") {
				t.Errorf("%T leaked: %s", v, s)
			}
			// json.Marshal escapes < and >, so look for the word itself.
			if !strings.Contains(s, "redacted") {
				t.Errorf("%T: no redaction marker in %s", v, s)
			}
		}
	}
	if tok.Bearer() != canaryBearer || cs.Bearer() != canarySecret {
		t.Fatal("Bearer() must return the secret")
	}
}

// accept: AUTH-05#3
func TestConstructorsRegisterOpaqueSecrets(t *testing.T) {
	_ = NewToken("opaque-canary-token-77", time.Time{}, "", "")
	_ = NewClientSecret("opaque-canary-secret-88", time.Time{})
	got := logx.Scrub("a opaque-canary-token-77 b opaque-canary-secret-88")
	if got != "a <redacted> b <redacted>" {
		t.Fatalf("Scrub = %q", got)
	}
}
