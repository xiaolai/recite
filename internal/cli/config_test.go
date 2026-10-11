package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/config"
	"github.com/xiaolai/recite/internal/realtime"
)

// writeConfig writes body to a fresh config file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// userConfig installs body as the default user config file ($XDG_CONFIG_HOME/recite).
func userConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	p := filepath.Join(dir, "recite", "config.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Every schema default is the default of the flag that carries the key, so a value that
// came from no layer means the same with or without the config machinery.
func TestSettingDefaultsMatchFlags(t *testing.T) {
	fs := newFlagSet("x")
	var g Globals
	g.register(fs)
	var all settingFlags
	all.register(fs)
	for _, k := range settingSchema(func(string) string { return "" }, false).Keys() {
		if k.Flag == "" {
			continue
		}
		f := fs.Lookup(k.Flag)
		if f == nil {
			t.Errorf("%s: flag --%s not registered by say", k.Name, k.Flag)
			continue
		}
		if f.DefValue != k.Default {
			t.Errorf("%s: schema default %q, flag --%s default %q", k.Name, k.Default, k.Flag, f.DefValue)
		}
	}
}

// accept: CFG-04#1
func TestPrecedenceEndToEnd(t *testing.T) {
	userConfig(t, "voice = \"alloy\"\nspeed = 0.9\njobs = 2\neffort = \"low\"\n\n[profile.p]\nvoice = \"ash\"\nspeed = 1.1\njobs = 4\n")
	cases := []struct {
		name  string
		env   map[string]string
		args  []string
		voice string
		speed float64
		jobs  int
		eff   realtime.Effort
	}{
		{"user", nil, nil, "alloy", 0.9, 2, "low"},
		{"profile", nil, []string{"--profile", "p"}, "ash", 1.1, 4, "low"},
		{"profile from env", map[string]string{"RECITE_PROFILE": "p"}, nil, "ash", 1.1, 4, "low"},
		{"env over profile", map[string]string{"RECITE_PROFILE": "p", "RECITE_VOICE": "coral"}, nil, "coral", 1.1, 4, "low"},
		{"flag over env", map[string]string{"RECITE_VOICE": "coral", "RECITE_JOBS": "5"}, []string{"--voice", "sage", "--profile", "p"}, "sage", 1.1, 5, "low"},
		{"flag over file", nil, []string{"--jobs", "6"}, "alloy", 0.9, 6, "low"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			r := run(append([]string{"say", "Hi"}, tc.args...)...)
			if r.code != ExitOK || len(r.says) != 1 {
				t.Fatalf("%+v", r)
			}
			got := r.says[0]
			if got.Settings.Voice != tc.voice || got.Settings.Speed != tc.speed || got.Jobs != tc.jobs || got.Settings.Effort != tc.eff {
				t.Fatalf("voice %s speed %v jobs %d effort %s", got.Settings.Voice, got.Settings.Speed, got.Jobs, got.Settings.Effort)
			}
		})
	}
	if r := run("--profile", "p", "say", "Hi"); r.code != ExitOK || r.says[0].Settings.Voice != "ash" {
		t.Fatalf("--profile before the command: %+v", r)
	}
}

// Every config key reaches the request `say` builds.
func TestConfigFileReachesSay(t *testing.T) {
	cfg := writeConfig(t, `voice = "marin"
model = "gpt-realtime-2"
effort = "medium"
speed = 1.25
jobs = 5

[verify]
mode = "strict"
max_attempts = 4

[gaps]
sentence = "250ms"
list = "300ms..400ms"
paragraph = "700ms"
section = "1s..2s"
seed = 7

[guard]
policy = "warn"
url = "drop"

[normalize]
mode = "off"
date_style = "iso"
disable = ["en.year"]
`)
	r := run("say", "Hi", "--config", cfg)
	if r.code != ExitOK || len(r.says) != 1 {
		t.Fatalf("%+v", r)
	}
	got := r.says[0]
	if got.Settings.Voice != "marin" || got.Settings.Model != "gpt-realtime-2" || got.Settings.Effort != "medium" || got.Settings.Speed != 1.25 ||
		got.Jobs != 5 || got.Verify.Mode != "strict" || got.Verify.MaxAttempts != 4 || got.Gaps.Seed != 7 ||
		got.Gaps.Sentence.String() != "250ms" || got.Gaps.List.String() != "300ms..400ms" || got.Gaps.Paragraph.String() != "700ms" ||
		got.Gaps.Section.String() != "1s..2s" || got.Guard.Policy != "warn" || got.Guard.Actions["G-URL"] != "drop" ||
		got.Spoken.Mode != "off" || got.Spoken.DateStyle != "iso" || len(got.Spoken.Disabled) != 1 || got.Spoken.Disabled[0] != "en.year" {
		t.Fatalf("%+v", got)
	}
	// A flag replaces the file's list rather than adding to it.
	r = run("say", "Hi", "--config", cfg, "--normalize-disable", "en.ordinal")
	if d := r.says[0].Spoken.Disabled; len(d) != 1 || d[0] != "en.ordinal" {
		t.Fatalf("disabled %v", d)
	}
}

// accept: CFG-01#1
func TestConfigErrorsExit2(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"typo", "voice = \"marin\"\nvocie = \"cedar\"\n", `config.toml:2: unknown key "vocie"`},
		{"duration", "[gaps]\nsentence = \"250\"\n", `config.toml:2: gaps.sentence: gap "250": "250" is not a duration`},
		{"value", "\n\nspeed = 3\n", `config.toml:3: speed: invalid speed "3"`},
		{"jobs", "jobs = 9\n", `config.toml:1: jobs: "9": want a whole number from 1 to 6`},
		{"guard code", "[guard]\ntypo = \"report\"\n", `config.toml:2: unknown key "guard.typo"`},
		{"reserved", "loudness = \"podcast\"\n", `config.toml:1: key "loudness" is not supported by this version`},
		{"env-only key", "codex_bin = \"/x\"\n", `config.toml:1: unknown key "codex_bin"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := userConfig(t, tc.body)
			for _, cmd := range [][]string{{"say", "Hi"}, {"config", "show"}} {
				r := run(cmd...)
				if r.code != ExitUsage || len(r.says) != 0 || !strings.Contains(r.stderr, p+strings.TrimPrefix(tc.want, "config.toml")) {
					t.Errorf("%v: code %d stderr %q, want %q", cmd, r.code, r.stderr, tc.want)
				}
			}
		})
	}
	if r := run("--config", filepath.Join(t.TempDir(), "none.toml"), "say", "Hi"); r.code != ExitUsage || !strings.Contains(r.stderr, "none.toml") {
		t.Errorf("missing --config: %+v", r)
	}
}

// accept: CFG-02#1
func TestUnknownProfileExit2(t *testing.T) {
	userConfig(t, "[profile.narrator]\nvoice = \"marin\"\n[profile.anchor]\nvoice = \"ash\"\n")
	r := run("say", "Hi", "--profile", "narator")
	if r.code != ExitUsage || !strings.Contains(r.stderr, `unknown profile "narator" (from --profile); available profiles: anchor, narrator`) {
		t.Fatalf("%+v", r)
	}
	t.Setenv("RECITE_PROFILE", "podcast")
	r = run("config", "show")
	if r.code != ExitUsage || !strings.Contains(r.stderr, `unknown profile "podcast" (from RECITE_PROFILE); available profiles: anchor, narrator`) {
		t.Fatalf("%+v", r)
	}
}

// accept: CFG-03#1
func TestEnvironmentVariables(t *testing.T) {
	cacheDir := t.TempDir()
	userConfig(t, "[profile.p]\nverify.mode = \"off\"\n")
	env := map[string]string{
		"RECITE_VOICE":     "verse",
		"RECITE_MODEL":     "gpt-realtime-2.1-mini",
		"RECITE_EFFORT":    "high",
		"RECITE_SPEED":     "0.75",
		"RECITE_JOBS":      "1",
		"RECITE_PROFILE":   "p",
		"RECITE_CODEX_BIN": "/opt/codex/bin/codex",
		"RECITE_CACHE_DIR": cacheDir,
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	r := run("say", "Hi")
	if r.code != ExitOK || len(r.says) != 1 {
		t.Fatalf("%+v", r)
	}
	got := r.says[0]
	if got.Settings.Voice != "verse" || got.Settings.Model != "gpt-realtime-2.1-mini" || got.Settings.Effort != "high" ||
		got.Settings.Speed != 0.75 || got.Jobs != 1 || got.Verify.Mode != "off" || got.Globals.CodexBin != "/opt/codex/bin/codex" {
		t.Fatalf("%+v", got)
	}
	r = run("--json", "config", "show")
	var shown struct{ Settings []settingRow }
	if err := json.Unmarshal([]byte(r.stdout), &shown); err != nil {
		t.Fatalf("%v: %q", err, r.stdout)
	}
	seen := map[string]bool{}
	for _, row := range shown.Settings {
		if row.Layer == "env" {
			seen[row.Origin] = true
			if row.Value != env[row.Origin] {
				t.Errorf("%s shows %q", row.Origin, row.Value)
			}
		}
	}
	for k := range env {
		if !seen[k] {
			t.Errorf("%s not reported by config show", k)
		}
	}
	// Each malformed variable is a usage error naming it, even under a flag that overrides it.
	valid := map[string]string{"RECITE_VOICE": "ash", "RECITE_MODEL": "gpt-realtime-2", "RECITE_EFFORT": "low", "RECITE_SPEED": "1.0", "RECITE_JOBS": "2"}
	for k, bad := range map[string]string{
		"RECITE_VOICE": "nova2", "RECITE_MODEL": "gpt-4o", "RECITE_EFFORT": "max", "RECITE_SPEED": "2",
		"RECITE_JOBS": "0", "RECITE_PROFILE": "nope", "RECITE_CACHE_DIR": "relative/dir",
	} {
		t.Run(k, func(t *testing.T) {
			t.Setenv(k, bad)
			runs := [][]string{{"say", "Hi"}, {"config", "show"}}
			if good, ok := valid[k]; ok {
				runs = append(runs, []string{"say", "Hi", "--" + strings.ToLower(strings.TrimPrefix(k, "RECITE_")), good})
			}
			for _, args := range runs {
				r := run(args...)
				if r.code != ExitUsage || len(r.says) != 0 || !strings.Contains(r.stderr, k) {
					t.Errorf("%v: code %d stderr %q", args, r.code, r.stderr)
				}
			}
		})
	}
}

// OPENAI_API_KEY is not a setting: it changes nothing recite resolves (design §14.2).
func TestOpenAIAPIKeyNotRecognised(t *testing.T) {
	without := run("say", "Hi")
	showWithout := run("config", "show")
	t.Setenv("OPENAI_API_KEY", "sk-canary")
	with := run("say", "Hi")
	showWith := run("config", "show")
	if !reflect.DeepEqual(without.says, with.says) || with.code != ExitOK {
		t.Fatalf("OPENAI_API_KEY changed the request:\n%+v\n%+v", without.says, with.says)
	}
	if showWith.stdout != showWithout.stdout || strings.Contains(showWith.stdout, "sk-canary") {
		t.Fatalf("OPENAI_API_KEY changed config show:\n%s", showWith.stdout)
	}
	for _, k := range settingSchema(os.Getenv, false).Keys() {
		if strings.Contains(k.Env, "OPENAI") {
			t.Fatalf("key %s reads %s", k.Name, k.Env)
		}
	}
	// It is not a config key either.
	if r := run("say", "Hi", "--config", writeConfig(t, "openai_api_key = \"sk-canary\"\n")); r.code != ExitUsage {
		t.Fatalf("%+v", r)
	}
}

// accept: CLI-10#1
func TestConfigShowGolden(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", "/xdg-cache") // fixed, so the golden's columns do not depend on the temp path
	t.Setenv("XDG_CONFIG_HOME", dir)
	p := filepath.Join(dir, "recite", "config.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "voice = \"alloy\"\njobs = 2\n\n[guard]\nurl = \"full\"\n\n[profile.narrator]\nvoice = \"marin\"\nspeed = 0.9\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RECITE_PROFILE", "narrator")
	t.Setenv("RECITE_EFFORT", "low")
	r := run("config", "show", "--gap-seed", "9")
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	golden(t, "config-show", strings.ReplaceAll(r.stdout, dir, "$DIR"))
	var layers []string
	for _, l := range []config.Layer{config.LayerDefault, config.LayerUser, config.LayerProfile, config.LayerEnv, config.LayerFlag} {
		if !strings.Contains(r.stdout, "  "+l.String()) {
			layers = append(layers, l.String())
		}
	}
	if len(layers) > 0 {
		t.Fatalf("layers missing from config show: %v", layers)
	}
}

func TestConfigPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	r := run("config", "path")
	want := "config  $DIR/recite/config.toml (missing)\ncache   $DIR/cache/recite\n"
	if got := strings.ReplaceAll(r.stdout, dir, "$DIR"); r.code != ExitOK || got != want {
		t.Fatalf("code %d got %q want %q", r.code, got, want)
	}
	// A broken file is still located, so it can be fixed.
	userConfig(t, "vocie = 1\n")
	if r := run("config", "path"); r.code != ExitOK || !strings.Contains(r.stdout, "(found)") {
		t.Fatalf("%+v", r)
	}
	if r := run("config", "path", "--config", "/nowhere/c.toml"); !strings.HasPrefix(r.stdout, "config  /nowhere/c.toml (missing)") {
		t.Fatalf("%+v", r)
	}
	for _, args := range [][]string{{"config"}, {"config", "list"}, {"config", "show", "extra"}} {
		if r := run(args...); r.code != ExitUsage {
			t.Errorf("%v: %+v", args, r)
		}
	}
}
