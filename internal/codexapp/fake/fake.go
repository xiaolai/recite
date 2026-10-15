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
//	FAKECODEX_RECORD        file that each start appends argv= and env= lines to, in any mode, then one
//	                        req=<line> line per received app-server message; an env= line holds the
//	                        value only for FAKECODEX_*, CODEX_HOME, PATH and HOME, else the name alone
//	FAKECODEX_LEAK          misbehaviour of getAuthStatus that leaks FAKECODEX_CANARY:
//	                        rpc-error (in the error message), malformed (in a truncated JSON
//	                        line), token (as authToken even with includeToken false)
//	FAKECODEX_STATE_FILE    when the file exists, its content replaces FAKECODEX_MODE
//	FAKECODEX_LOGIN_RECORD  `login`/`logout` append argv0=, args=, home= (CODEX_HOME) and
//	                        stdin= (one line read from stdin) lines here
//	FAKECODEX_LOGIN_SETS    the mode `login` writes into FAKECODEX_STATE_FILE
//	FAKECODEX_CONFIG_STORE  when set, `config/read` reports it as cli_auth_credentials_store;
//	                        unset, `config/read` is an unknown method
//	FAKECODEX_REQUIRES_OPENAI_AUTH  "false" makes getAuthStatus report requiresOpenaiAuth: false
//	FAKECODEX_ORDINARY_USAGE        "false" reports ordinaryUsageAllowed: false; "omit" leaves it out
//
// Like Codex, the app-server answers only initialize until the client has sent initialize and
// then the initialized notification (-32002 otherwise), and refuses params of the wrong type
// (-32602).
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

// cmdPackage is fakecodex's import path. Building by import path from the test's own directory
// (always inside this module) needs no source location, which -trimpath would hide.
const cmdPackage = "github.com/xiaolai/recite/internal/codexapp/fake/cmd/fakecodex"

// BuildTo builds fakecodex into dir and returns its absolute path; for use in TestMain.
func BuildTo(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("build fakecodex: %w", err)
	}
	bin := filepath.Join(abs, "codex")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, cmdPackage)
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
