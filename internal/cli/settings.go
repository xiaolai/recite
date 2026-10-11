package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/config"
	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/verify"
)

// reservedKeys are config keys of design §13 that this version does not implement yet; a
// file setting one fails rather than being silently ignored.
var reservedKeys = map[string]string{
	"output_dir": "no feature implements an output directory yet",
	"voices":     "dialogue speaker map, TXT-06, M5",
}

// guardKey is the [guard] key of a code's per-code action: the code without "G-", in lower
// case (design §13: url, emoji, footnote).
func guardKey(code guard.Code) string {
	return "guard." + strings.ToLower(strings.TrimPrefix(string(code), "G-"))
}

// configurableCodes are the guard codes whose action may be changed.
func configurableCodes() []guard.Code {
	var out []guard.Code
	for _, code := range guard.Codes() {
		if len(guard.Allowed(code)) > 1 {
			out = append(out, code)
		}
	}
	return out
}

// settingSchema is every setting a config file, the environment or a flag may set (design
// §13), with its default, flag and variable. Defaults equal the flag defaults (asserted by a
// test), and each check is the parser the flag value goes through.
func settingSchema(getenv func(string) string, modelUnchecked bool) *config.Schema {
	gaps := audio.DefaultGaps()
	keys := []config.Key{
		{Name: config.ProfileKey, Flag: "profile", Env: "RECITE_PROFILE", NoFile: true},
		{Name: titleKey, NoFile: true, FrontMatter: true},
		{Name: "voice", Default: realtime.DefaultVoice, Flag: "voice", Env: "RECITE_VOICE", FrontMatter: true, Check: check(realtime.ParseVoice)},
		{Name: "model", Default: realtime.DefaultModel, Flag: "model", Env: "RECITE_MODEL",
			Check: func(s string) error { _, err := realtime.ParseModel(s, modelUnchecked); return err }},
		{Name: "effort", Default: string(realtime.DefaultEffort), Flag: "effort", Env: "RECITE_EFFORT", FrontMatter: true, Check: check(realtime.ParseEffort)},
		{Name: "speed", Default: "1.0", Flag: "speed", Env: "RECITE_SPEED", FrontMatter: true, Check: check(realtime.ParseSpeed)},
		{Name: "emotion", Default: realtime.DefaultEmotion, Flag: "emotion", Check: check(realtime.ParseEmotion)},
		{Name: "style", Flag: "style", FrontMatter: true, Check: check(realtime.ParseStyle)},
		{Name: langKey, NoFile: true, FrontMatter: true, Check: checkLang},
		// --lexicon FILE is read into this key by resolveSettings: the flag names a file, the
		// key holds the table.
		{Name: lexiconKey, FrontMatter: true, Table: encodeLexicon, Check: check(spoken.ParseLexiconText)},
		{Name: "jobs", Default: strconv.Itoa(pipeline.DefaultJobs), Flag: "jobs", Env: "RECITE_JOBS", Check: intIn(1, pipeline.MaxJobs)},
		{Name: "verify.mode", Default: string(verify.ModeNormal), Flag: "verify", Check: check(verify.ParseMode)},
		{Name: "verify.max_attempts", Default: strconv.Itoa(verify.DefaultMaxAttempts), Flag: "max-attempts", Check: intIn(1, 1<<20)},
		{Name: "gaps.sentence", Default: gaps.Sentence.String(), Flag: "gap-sentence", Check: check(audio.ParseGapRange)},
		{Name: "gaps.list", Default: gaps.List.String(), Flag: "gap-list", Check: check(audio.ParseGapRange)},
		{Name: "gaps.paragraph", Default: gaps.Paragraph.String(), Flag: "gap-paragraph", Check: check(audio.ParseGapRange)},
		{Name: "gaps.section", Default: gaps.Section.String(), Flag: "gap-section", Check: check(audio.ParseGapRange)},
		{Name: "gaps.seed", Default: "0", Flag: "gap-seed", Check: func(s string) error { _, err := strconv.ParseInt(s, 10, 64); return err }},
		// No default: the preset follows the output format and -o - (AUD-06, AUD-07).
		{Name: "loudness", Flag: "loudness", Check: check(audio.ParseLoudness)},
		{Name: "guard.policy", Default: string(guard.PolicyFix), Flag: "guard", Check: check(guard.ParsePolicy)},
	}
	for _, code := range configurableCodes() {
		keys = append(keys, config.Key{Name: guardKey(code), Default: string(guard.Allowed(code)[0]), Check: func(s string) error {
			_, err := guard.ParseAction(code, s)
			return err
		}})
	}
	keys = append(keys,
		config.Key{Name: "normalize.mode", Default: string(spoken.ModeAuto), Flag: "normalize", Check: check(spoken.ParseMode)},
		config.Key{Name: "normalize.date_style", Default: string(spoken.DateUS), Flag: "date-style", Check: check(spoken.ParseDateStyle)},
		config.Key{Name: "normalize.disable", Flag: "normalize-disable", List: true, Check: check(spoken.ParseDisabled)},
		config.Key{Name: "codex_bin", Flag: "codex-bin", Env: "RECITE_CODEX_BIN", NoFile: true},
		config.Key{Name: "cache_dir", Default: defaultCacheDir(getenv), Env: "RECITE_CACHE_DIR", NoFile: true, Check: absPath},
	)
	s, err := config.NewSchema(keys, reservedKeys)
	if err != nil {
		panic(err) // the key table above is static
	}
	return s
}

func check[T any](parse func(string) (T, error)) func(string) error {
	return func(s string) error { _, err := parse(s); return err }
}

func intIn(lo, hi int) func(string) error {
	return func(s string) error {
		n, err := strconv.Atoi(s)
		if err != nil || n < lo || n > hi {
			return fmt.Errorf("%q: want a whole number from %d to %d", s, lo, hi)
		}
		return nil
	}
}

func absPath(s string) error {
	if !filepath.IsAbs(s) {
		return fmt.Errorf("%q: want an absolute directory path", s)
	}
	return nil
}

// defaultCacheDir is recite's cache directory (design §11): $XDG_CACHE_HOME/recite, else
// ~/Library/Caches/recite on macOS and ~/.cache/recite elsewhere; "" without a home.
func defaultCacheDir(getenv func(string) string) string {
	if x := getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "recite")
	}
	home := getenv("HOME")
	if home == "" {
		return ""
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Caches", "recite")
	}
	return filepath.Join(home, ".cache", "recite")
}

// configPath is the user config file: --config when given (it must exist), else the default
// location.
func (c *CLI) configPath() (path string, required bool) {
	if c.globals.Config != "" {
		return c.globals.Config, true
	}
	return config.UserConfigPath(c.Getenv), false
}

// resolveSettings merges every layer for the command whose flags fs holds (CFG-04), then
// writes each value that did not come from a flag or a default into its flag, so the
// command's existing flag parsing is the single place a value is interpreted. Every error is
// a usage error.
func (c *CLI) resolveSettings(fs *flag.FlagSet, fm *frontMatterSource) (config.Settings, error) {
	unchecked := false
	if f := fs.Lookup("model-unchecked"); f != nil {
		unchecked = f.Value.String() == "true"
	}
	schema := settingSchema(c.Getenv, unchecked)
	byFlag := map[string]config.Key{}
	for _, k := range schema.Keys() {
		if k.Flag != "" {
			byFlag[k.Flag] = k
		}
	}
	flags := config.Values{}
	fs.Visit(func(f *flag.Flag) {
		if k, ok := byFlag[f.Name]; ok {
			flags[k.Name] = config.Value{Raw: f.Value.String(), Layer: config.LayerFlag, Origin: "--" + f.Name}
		}
	})
	// Global flags given before the command name were parsed by another FlagSet.
	for name, val := range map[string]string{config.ProfileKey: c.globals.Profile, "codex_bin": c.globals.CodexBin} {
		k, _ := schema.Lookup(name)
		if _, ok := flags[name]; !ok && val != "" {
			flags[name] = config.Value{Raw: val, Layer: config.LayerFlag, Origin: "--" + k.Flag}
		}
	}
	if f := fs.Lookup(lexiconFlag); f != nil && flagGiven(fs, lexiconFlag) {
		raw, err := readLexiconFile(f.Value.String())
		if err != nil {
			return config.Settings{}, err
		}
		flags[lexiconKey] = config.Value{Raw: raw, Layer: config.LayerFlag, Origin: "--" + lexiconFlag}
	}
	path, required := c.configPath()
	user, err := config.ReadFile(schema, path, config.LayerUser, true, required)
	if err != nil {
		return config.Settings{}, err
	}
	fmValues, err := fm.values(schema)
	if err != nil {
		return config.Settings{}, err
	}
	st, err := config.Resolve(config.Input{Schema: schema, User: user, Project: c.project, FrontMatter: fmValues, Getenv: c.Getenv, Flags: flags})
	if err != nil {
		return config.Settings{}, err
	}
	for _, k := range schema.Keys() {
		v := st.Get(k.Name)
		if k.Flag == "" || v.Layer == config.LayerFlag || v.Layer == config.LayerDefault || fs.Lookup(k.Flag) == nil {
			continue
		}
		if err := fs.Set(k.Flag, v.Raw); err != nil {
			return config.Settings{}, fmt.Errorf("%s: %s: %w", v.Source(), k.Name, err)
		}
	}
	return st, nil
}

const (
	lexiconKey  = "lexicon"
	lexiconFlag = "lexicon"
)

// encodeLexicon validates a [lexicon] table and gives its canonical text.
func encodeLexicon(t map[string]any) (string, error) {
	l, err := spoken.ParseLexicon(t)
	if err != nil {
		return "", err
	}
	return l.String(), nil
}

// readLexiconFile reads --lexicon FILE: a TOML file whose top level is the pronunciation
// table, in the [lexicon] syntax of the config file (design §7.8).
func readLexiconFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("--lexicon: %w", err)
	}
	var t map[string]any
	if _, err := toml.Decode(string(data), &t); err != nil {
		return "", fmt.Errorf("--lexicon %s: %w", path, err)
	}
	raw, err := encodeLexicon(t)
	if err != nil {
		return "", fmt.Errorf("--lexicon %s: %w", path, err)
	}
	return raw, nil
}

func flagGiven(fs *flag.FlagSet, name string) bool {
	given := false
	fs.Visit(func(f *flag.Flag) { given = given || f.Name == name })
	return given
}

// lexiconOf is the effective lexicon of resolved settings.
func lexiconOf(st config.Settings) (spoken.Lexicon, error) {
	v := st.Get(lexiconKey)
	l, err := spoken.ParseLexiconText(v.Raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", v.Source(), err)
	}
	return l, nil
}

// guardActions are the per-code actions set in [guard] (TXT-09).
func guardActions(st config.Settings) (map[guard.Code]guard.Action, error) {
	var out map[guard.Code]guard.Action
	for _, code := range configurableCodes() {
		v := st.Get(guardKey(code))
		if v.Layer == config.LayerDefault {
			continue
		}
		act, err := guard.ParseAction(code, v.Raw)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = map[guard.Code]guard.Action{}
		}
		out[code] = act
	}
	return out, nil
}

// listFlag is a repeatable flag whose value is the comma-joined list, so it carries the same
// text as a TOML array of the matching config key.
type listFlag struct{ items *[]string }

func (l listFlag) String() string {
	if l.items == nil {
		return ""
	}
	return strings.Join(*l.items, ",")
}

func (l listFlag) Set(s string) error {
	if s == "" {
		return errors.New("empty value")
	}
	*l.items = append(*l.items, s)
	return nil
}
