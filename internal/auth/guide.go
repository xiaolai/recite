package auth

import (
	"strings"
)

// AuthURL is the one link every guide ends with: the Codex authentication page
// (design §5.1a, A#10). The live suite checks that it resolves and mentions
// `codex login`.
const AuthURL = "https://learn.chatgpt.com/docs/auth"

// Step is one line of a guide: an explanation, a command to run, or both.
type Step struct {
	Text    string
	Command string
}

// Guide tells the user what is wrong and how to fix it (design §5.1a).
type Guide struct {
	State   State
	Message string
	Steps   []Step
}

// Event is the --json form of a guide: the auth_error event of the events
// schema (docs/schema/events.schema.json v1).
type Event struct {
	Event       string   `json:"event"`
	State       State    `json:"state"`
	Message     string   `json:"message"`
	FixCommands []string `json:"fix_commands"`
	Link        string   `json:"link"`
}

// FixCommands lists the guide's commands in order.
func (g Guide) FixCommands() []string {
	out := []string{}
	for _, s := range g.Steps {
		if s.Command != "" {
			out = append(out, s.Command)
		}
	}
	return out
}

// Event returns the auth_error event.
func (g Guide) Event() Event {
	return Event{Event: "auth_error", State: g.State, Message: g.Message, FixCommands: g.FixCommands(), Link: AuthURL}
}

// Text renders the plain guide for stderr.
func (g Guide) Text() string {
	var b strings.Builder
	b.WriteString(g.Message)
	b.WriteString("\n")
	for _, s := range g.Steps {
		if s.Text != "" {
			b.WriteString("  " + s.Text + "\n")
		}
		if s.Command != "" {
			b.WriteString("      " + s.Command + "\n")
		}
	}
	b.WriteString("More help: " + AuthURL + "\n")
	return b.String()
}

// Guide returns the guide for a non-ready state; ok is false for ready.
func (s *Status) Guide() (Guide, bool) {
	return GuideFor(s.State, s.Context, s.Mode, s.CodexVersion)
}

// GuideFor builds the guide of state. Every `<codex>` is the resolved
// executable's absolute path, shell-quoted, with CODEX_HOME=<home> in front
// when a home is set: a bare `codex` would be looked up again by the user's
// shell and could run another installation (A#6). Only no-codex, which has
// no executable, uses plain commands.
func GuideFor(state State, c Context, mode, version string) (Guide, bool) {
	codex := CodexCommand(c)
	g := Guide{State: state}
	switch state {
	case StateReady:
		return Guide{}, false
	case StateNoCodex:
		g.Message = "Codex CLI is not installed. recite uses your ChatGPT sign-in through Codex."
		if c.MissingBin != "" {
			g.Message = "The Codex executable you named was not found: " + c.MissingBin +
				". Check --codex-bin or RECITE_CODEX_BIN, or install Codex:"
		}
		g.Steps = []Step{
			{Text: "Install it with npm (macOS, Linux):", Command: "npm install -g @openai/codex"},
			{Text: "or with Homebrew (macOS):", Command: "brew install --cask codex"},
			{Text: "or with the install script (macOS, Linux):", Command: "curl -fsSL https://chatgpt.com/codex/install.sh | sh"},
			{Text: "Then sign in:", Command: "codex login"},
		}
	case StateCodexTooOld:
		if version == "" {
			version = "(unknown version)"
		}
		g.Message = "Codex " + version + " is too old for recite (needs ≥ " + MinCodexVersion + ")."
		g.Steps = []Step{
			{Text: "Update it:", Command: codex + " update"},
			{Text: "or upgrade it the way it was installed."},
		}
	case StateNotSignedIn:
		g.Message = "Codex is not signed in."
		g.Steps = []Step{
			{Text: "Sign in (opens a browser):", Command: codex + " login"},
			{Text: "On a machine without a browser, such as over SSH:", Command: codex + " login --device-auth"},
			{Text: "Device-code login must first be enabled in your ChatGPT security settings (or by your workspace admin); if it isn't available, sign in on a machine with a browser."},
			{Text: "Or run `recite auth login`, which runs the same command for you."},
		}
	case StateAPIKey:
		g.Message = "Codex is signed in with an API key. recite works only with a ChatGPT sign-in, and never uses API keys."
		g.Steps = []Step{
			{Text: "Sign out, then sign in with ChatGPT:", Command: codex + " logout"},
			{Command: codex + " login"},
			{Text: "Warning: this changes how Codex itself signs in."},
		}
	case StateUnsupported:
		g.Message = "Codex is signed in with " + mode + ", which recite does not support. recite needs a personal ChatGPT sign-in."
		g.Steps = []Step{
			{Text: "See the current sign-in:", Command: codex + " login status"},
			{Text: "Switching to a ChatGPT sign-in is your decision, so recite prints no command for it."},
		}
	case StateExpired:
		g.Message = "Your ChatGPT sign-in for Codex has expired or is about to."
		g.Steps = []Step{
			{Text: "Run any Codex command to refresh it, for example:", Command: codex + " login status"},
			{Text: "or sign in again:", Command: codex + " login"},
		}
	case StateCodexError:
		g.Message = "Codex could not report a usable sign-in."
		g.Steps = []Step{
			{Text: "See Codex's own message:", Command: codex + " login status"},
			{Text: "For details:", Command: "recite doctor"},
		}
	default:
		g.Message = "recite could not determine the Codex sign-in."
	}
	return g, true
}

// CodexCommand renders `<codex>` for the context: the shell-quoted absolute
// executable, prefixed with CODEX_HOME=<home> when a home is set. With no
// resolved executable it is plain `codex`.
func CodexCommand(c Context) string {
	bin := "codex"
	if c.CodexBin != "" {
		bin = ShellQuote(c.CodexBin)
	}
	if c.CodexHome != "" {
		return "CODEX_HOME=" + ShellQuote(c.CodexHome) + " " + bin
	}
	return bin
}

// ShellQuote quotes s for a POSIX shell, leaving it bare when that is safe.
func ShellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
