// Package fake builds and describes fakecodex, a stand-in `codex` executable for tests (FND-05).
//
// Environment of the built binary:
//
//	FAKECODEX_MODE          one of Modes (default chatgpt); unknown values exit 64
//	FAKECODEX_TOKEN_EXP     unix exp of the chatgpt JWT (default now+1h)
//	FAKECODEX_REFRESHED_EXP unix exp after refresh-ok refreshes (default now+2h)
//	FAKECODEX_VERSION       version in the initialize userAgent (default 0.162.1)
//	FAKECODEX_DELAY_MS      per-message delay in slow mode (default 15000, past the 10 s timeout)
//	FAKECODEX_CANARY        stderr-canary token (default eyJcanary.canary.canary)
//	FAKECODEX_RECORD        file that receives argv= and env= lines, in any mode, then one
//	                        req=<line> line per received app-server message
//	FAKECODEX_LEAK          misbehaviour of getAuthStatus that leaks FAKECODEX_CANARY:
//	                        rpc-error (in the error message), malformed (in a truncated JSON
//	                        line), token (as authToken even with includeToken false)
package fake

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// AuthModes are the eight Codex 0.162.1 AuthMode values (design §5.1a); each is also a mode.
var AuthModes = []string{"apikey", "chatgpt", "chatgptAuthTokens", "headers", "agentIdentity",
	"personalAccessToken", "bedrockApiKey", "bedrockAccessKeys"}

// Modes lists every FAKECODEX_MODE value.
var Modes = append(append([]string(nil), AuthModes...),
	"logged-out", "slow", "crash", "refresh-ok", "refresh-fails",
	"future-mode",    // authMethod "futureMode", a value a later Codex might add
	"rpc-error",      // getAuthStatus → {"code":-32000,"message":"credential store unavailable"}
	"unknown-method", // getAuthStatus → -32601 method not found (an old Codex)
	"stderr-canary",  // chatgpt, and the canary token written to stderr
	"null-token",     // chatgpt, but authToken null even with includeToken true
	"bad-jwt",        // chatgpt, but authToken is not a JWT
)

// BuildTo builds fakecodex into dir and returns its path; for use in TestMain.
func BuildTo(dir string) (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("fakecodex: runtime.Caller failed")
	}
	bin := filepath.Join(dir, "codex")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/fakecodex")
	cmd.Dir = filepath.Dir(file)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build fakecodex: %w", err)
	}
	return bin, nil
}

// Build builds fakecodex into a per-test temp dir and returns its path.
func Build(t testing.TB) string {
	t.Helper()
	bin, err := BuildTo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return bin
}
