// Command fakecodex imitates the subset of the codex CLI that recite uses: `codex app-server`
// speaking newline-delimited JSON-RPC over stdio, and `codex login status`. Behaviour is chosen
// by environment variables; see internal/codexapp/fake for the list.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The eight AuthMode values of Codex 0.162.1 (design §5.1a).
var authModes = []string{"apikey", "chatgpt", "chatgptAuthTokens", "headers", "agentIdentity",
	"personalAccessToken", "bedrockApiKey", "bedrockAccessKeys"}

const futureMode = "futureMode"

func main() {
	mode := os.Getenv("FAKECODEX_MODE")
	if mode == "" {
		mode = "chatgpt"
	}
	if !validMode(mode) {
		fmt.Fprintf(os.Stderr, "fakecodex: unknown FAKECODEX_MODE %q\n", mode)
		os.Exit(64)
	}
	if err := record(); err != nil {
		fmt.Fprintln(os.Stderr, "fakecodex: record:", err)
		os.Exit(70)
	}
	if mode == "stderr-canary" {
		fmt.Fprintln(os.Stderr, "fakecodex debug: token="+canary())
	}
	args := os.Args[1:]
	switch {
	case len(args) >= 2 && args[0] == "login" && args[1] == "status":
		os.Exit(loginStatus(mode))
	case len(args) >= 1 && args[0] == "app-server":
		os.Exit(appServer(mode, os.Stdin, os.Stdout))
	default:
		fmt.Fprintf(os.Stderr, "fakecodex: unsupported arguments %q\n", args)
		os.Exit(64)
	}
}

func canary() string {
	if c := os.Getenv("FAKECODEX_CANARY"); c != "" {
		return c
	}
	return "eyJcanary.canary.canary"
}

// record writes argv and the environment, one per line, to $FAKECODEX_RECORD when set.
func record() error {
	path := os.Getenv("FAKECODEX_RECORD")
	if path == "" {
		return nil
	}
	var b strings.Builder
	for _, a := range os.Args {
		b.WriteString("argv=" + a + "\n")
	}
	env := os.Environ()
	slices.Sort(env)
	for _, e := range env {
		b.WriteString("env=" + e + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// recordRequest appends one received line as req=<line> to $FAKECODEX_RECORD when set.
func recordRequest(line []byte) error {
	path := os.Getenv("FAKECODEX_RECORD")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintf(f, "req=%s\n", line)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

var extraModes = []string{"logged-out", "slow", "crash", "refresh-ok", "refresh-fails", "future-mode",
	"rpc-error", "unknown-method", "stderr-canary"}

func validMode(m string) bool { return slices.Contains(authModes, m) || slices.Contains(extraModes, m) }

// authMethod is the getAuthStatus authMethod for mode; ok=false means null (logged out).
func authMethod(mode string) (string, bool) {
	switch mode {
	case "logged-out":
		return "", false
	case "future-mode":
		return futureMode, true
	case "slow", "refresh-ok", "refresh-fails", "stderr-canary", "rpc-error", "unknown-method", "crash":
		return "chatgpt", true
	}
	return mode, true // one of authModes, checked by validMode
}

func loginStatus(mode string) int {
	if mode == "crash" {
		return 2
	}
	m, ok := authMethod(mode)
	switch {
	case !ok:
		fmt.Println("Not logged in")
		return 1
	case m == "chatgpt":
		fmt.Println("Logged in using ChatGPT")
	case m == "apikey":
		fmt.Println("Logged in using an API key")
	default:
		fmt.Println("Logged in using " + m)
	}
	return 0
}

type request struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func appServer(mode string, in io.Reader, out io.Writer) int {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	enc := json.NewEncoder(out)
	reply := func(id json.RawMessage, result any, rerr *rpcError) {
		msg := map[string]any{"id": id}
		if rerr != nil {
			msg["error"] = rerr
		} else {
			msg["result"] = result
		}
		_ = enc.Encode(msg)
	}
	refreshed := false
	leak := os.Getenv("FAKECODEX_LEAK")
	for sc.Scan() {
		if err := recordRequest(sc.Bytes()); err != nil {
			fmt.Fprintln(os.Stderr, "fakecodex: record:", err)
			return 70
		}
		var req request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			fmt.Fprintln(os.Stderr, "fakecodex: malformed request")
			continue
		}
		if mode == "crash" && req.Method == "initialize" {
			fmt.Fprintln(os.Stderr, "fakecodex: crashing as requested")
			return 101
		}
		if mode == "slow" {
			time.Sleep(delay())
		}
		if len(req.ID) == 0 { // notification, e.g. initialized
			continue
		}
		switch req.Method {
		case "initialize":
			reply(req.ID, map[string]any{"userAgent": "fakecodex/0.162.1"}, nil)
		case "getAuthStatus":
			var p struct {
				IncludeToken bool `json:"includeToken"`
				RefreshToken bool `json:"refreshToken"`
			}
			_ = json.Unmarshal(req.Params, &p)
			switch leak {
			case "rpc-error":
				reply(req.ID, nil, &rpcError{Code: -32000, Message: "store failed for " + canary()})
				continue
			case "malformed":
				_, _ = fmt.Fprintf(out, "{\"id\":%s,\"result\":{\"authToken\":\"%s\"\n", req.ID, canary())
				continue
			}
			switch mode {
			case "unknown-method":
				reply(req.ID, nil, &rpcError{Code: -32601, Message: "Method not found: getAuthStatus"})
				continue
			case "rpc-error":
				reply(req.ID, nil, &rpcError{Code: -32000, Message: "credential store unavailable"})
				continue
			case "refresh-fails":
				if p.RefreshToken {
					reply(req.ID, nil, &rpcError{Code: -32000, Message: "token refresh failed"})
					continue
				}
			case "refresh-ok":
				if p.RefreshToken {
					refreshed = true
				}
			}
			m, ok := authMethod(mode)
			res := map[string]any{"authMethod": nil, "authToken": nil, "requiresOpenaiAuth": true}
			if ok {
				res["authMethod"] = m
				if p.IncludeToken {
					res["authToken"] = token(m, refreshed)
				} else if leak == "token" {
					res["authToken"] = canary() // a Codex that ignores includeToken:false
				}
			}
			reply(req.ID, res, nil)
		case "account/read":
			// Shapes measured on Codex 0.162.1: a ChatGPT login carries email, plan and a
			// workspaceRouting block with the account ID; an API-key login reports type "apiKey";
			// logged out is account: null. Email and IDs are canaries for leak tests.
			m, ok := authMethod(mode)
			result := map[string]any{"account": nil, "requiresOpenaiAuth": true}
			switch {
			case ok && m == "chatgpt":
				result["account"] = map[string]any{"type": "chatgpt", "email": "canary@example.invalid", "planType": "pro"}
				result["workspaceRouting"] = map[string]any{"chatgptAccountId": "acct-canary-0000",
					"backendOrigin": "https://chatgpt.com", "accountRoutingOverride": "NO_CONSTRAINT"}
			case ok && m == "apikey":
				result["account"] = map[string]any{"type": "apiKey"}
			case ok:
				result["account"] = map[string]any{"type": m}
			}
			reply(req.ID, result, nil)
		case "account/rateLimits/read":
			// Shape measured on Codex 0.162.1 (Pro): one weekly primary window, no secondary.
			limit := map[string]any{
				"limitId": "codex", "limitName": nil, "normalModelSlug": nil,
				"primary":         map[string]any{"usedPercent": 41, "windowDurationMins": 10080, "resetsAt": 1800000000},
				"secondary":       nil,
				"credits":         map[string]any{"hasCredits": true, "unlimited": false, "balance": "1000.0000000000"},
				"individualLimit": nil, "spendControlReached": false, "planType": "pro", "rateLimitReachedType": nil,
			}
			reply(req.ID, map[string]any{"ordinaryUsageAllowed": true, "rateLimits": limit,
				"rateLimitsByLimitId": map[string]any{"codex": limit}}, nil)
		default:
			reply(req.ID, nil, &rpcError{Code: -32601, Message: "Method not found: " + req.Method})
		}
	}
	return 0
}

func delay() time.Duration {
	if ms, err := strconv.Atoi(os.Getenv("FAKECODEX_DELAY_MS")); err == nil {
		return time.Duration(ms) * time.Millisecond
	}
	return 15 * time.Second // past recite's 10 s app-server timeout
}

// token returns a fake JWT for chatgpt, a fake key for apikey, and an opaque value otherwise.
func token(method string, refreshed bool) string {
	switch method {
	case "chatgpt":
		exp := envInt("FAKECODEX_TOKEN_EXP", time.Now().Add(time.Hour).Unix())
		if refreshed {
			exp = envInt("FAKECODEX_REFRESHED_EXP", time.Now().Add(2*time.Hour).Unix())
		}
		return JWT(exp, refreshed)
	case "apikey":
		return "sk-fakecodex-apikey"
	default:
		return "fake-" + method + "-credential"
	}
}

func envInt(name string, def int64) int64 {
	if v, err := strconv.ParseInt(os.Getenv(name), 10, 64); err == nil {
		return v
	}
	return def
}

// JWT builds an unsigned-looking token: eyJ… header, payload with exp, fake signature.
func JWT(exp int64, refreshed bool) string {
	b := base64.RawURLEncoding
	h := b.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	p := b.EncodeToString(fmt.Appendf(nil, `{"exp":%d,"sub":"fake-subject","refreshed":%t}`, exp, refreshed))
	return h + "." + p + "." + b.EncodeToString([]byte("fake-signature"))
}
