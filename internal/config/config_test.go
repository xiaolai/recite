package config

import (
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

func durationCheck(s string) error {
	_, err := time.ParseDuration(s)
	return err
}

func intCheck(s string) error {
	_, err := strconv.Atoi(s)
	return err
}

func testSchema(t *testing.T) *Schema {
	t.Helper()
	s, err := NewSchema([]Key{
		{Name: ProfileKey, Flag: "profile", Env: "RECITE_PROFILE", NoFile: true},
		{Name: "voice", Default: "cedar", Flag: "voice", Env: "RECITE_VOICE"},
		{Name: "model", Default: "m1", Flag: "model", Env: "RECITE_MODEL"},
		{Name: "jobs", Default: "3", Flag: "jobs", Env: "RECITE_JOBS", Check: intCheck},
		{Name: "speed", Default: "1.0", Flag: "speed"},
		{Name: "verify.mode", Default: "normal", Flag: "verify"},
		{Name: "gaps.sentence", Default: "200ms", Flag: "gap-sentence", Check: durationCheck},
		{Name: "normalize.disable", List: true},
	}, map[string]string{"style": "planned for M4"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func noEnv(string) string { return "" }

// accept: CFG-04#1
func TestPrecedenceProperty(t *testing.T) {
	s := testSchema(t)
	keys := []string{"voice", "model", "jobs", "speed", "verify.mode", "gaps.sentence"}
	rng := rand.New(rand.NewPCG(1, 2))
	for iter := range 2000 {
		layers := map[Layer]Values{}
		want := map[string]Value{}
		for _, k := range keys {
			kk, _ := s.Lookup(k)
			want[k] = Value{Raw: kk.Default, Layer: LayerDefault}
			for _, l := range Layers()[1:] {
				if rng.IntN(2) == 0 {
					continue
				}
				if layers[l] == nil {
					layers[l] = Values{}
				}
				v := Value{Raw: fmt.Sprintf("%s-%d", l, iter), Layer: l, Origin: l.String()}
				layers[l][k] = v
				want[k] = v // layers are visited lowest first, so the last set wins
			}
		}
		got, err := Merge(s, layers)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range keys {
			if g := got.Get(k); g != want[k] {
				t.Fatalf("iteration %d key %s: got %+v, want %+v (layers %v)", iter, k, g, want[k], layers)
			}
		}
	}
}

// The precedence order is design §13 exactly.
func TestLayerOrder(t *testing.T) {
	var names []string
	for _, l := range Layers() {
		names = append(names, l.String())
	}
	want := "default user profile project front matter env flag"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("layers %q, want %q", got, want)
	}
}

func TestMergeRejectsMislabelledValue(t *testing.T) {
	_, err := Merge(testSchema(t), map[Layer]Values{LayerEnv: {"voice": {Raw: "x", Layer: LayerFlag}}})
	if err == nil {
		t.Fatal("no error")
	}
}

// accept: CFG-01#1
func TestUnknownKeyNamesKeyAndLine(t *testing.T) {
	s := testSchema(t)
	cases := []struct {
		name, data, want string
	}{
		{"top", "voice = \"marin\"\n\nvocie = \"cedar\"\n", `cfg.toml:3: unknown key "vocie"`},
		{"section", "[verify]\nmode = \"strict\"\nmod = \"x\"\n", `cfg.toml:3: unknown key "verify.mod"`},
		{"unknown section", "voice = \"marin\"\n[verfy]\nmode = \"x\"\n", `cfg.toml:2: unknown key "verfy"`},
		{"profile", "[profile.a]\nvoice = \"marin\"\nvocie = \"x\"\n", `cfg.toml:3: unknown key "vocie"`},
		{"reserved", "style = \"warm\"\n", `cfg.toml:1: key "style" is not supported by this version (planned for M4)`},
		{"env only", "profile = \"a\"\n", `cfg.toml:1: unknown key "profile"`},
		{"bad value", "jobs = \"many\"\n", `cfg.toml:1: jobs: strconv.Atoi`},
		{"syntax", "voice = \n", `cfg.toml:1: `},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseFile(s, "cfg.toml", []byte(tc.data), LayerUser, tc.name != "env only")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want it to contain %q", err, tc.want)
			}
			var fe *FileError
			if !errors.As(err, &fe) || fe.Line == 0 {
				t.Fatalf("err %v carries no line", err)
			}
		})
	}
}

func TestSeveralErrorsInLineOrder(t *testing.T) {
	_, err := ParseFile(testSchema(t), "c", []byte("zzz = 1\naaa = 2\n"), LayerUser, true)
	if err == nil || err.Error() != "c:1: unknown key \"zzz\"\nc:2: unknown key \"aaa\"" {
		t.Fatalf("err %v", err)
	}
}

// accept: CFG-01#1
func TestDurationsParseAsGoDurations(t *testing.T) {
	s := testSchema(t)
	f, err := ParseFile(s, "c", []byte("[gaps]\nsentence = \"250ms\"\n"), LayerUser, true)
	if err != nil {
		t.Fatal(err)
	}
	if v := f.Top["gaps.sentence"]; v.Raw != "250ms" || v.Origin != "c:2" {
		t.Fatalf("got %+v", v)
	}
	for _, bad := range []string{`"250"`, `"fast"`, `250`} {
		if _, err := ParseFile(s, "c", []byte("[gaps]\nsentence = "+bad+"\n"), LayerUser, true); err == nil || !strings.HasPrefix(err.Error(), "c:2: gaps.sentence:") {
			t.Errorf("%s: err %v", bad, err)
		}
	}
}

func TestValueText(t *testing.T) {
	s := testSchema(t)
	f, err := ParseFile(s, "c", []byte("speed = 1.25\njobs = 2\n[normalize]\ndisable = [\"en.year\", \"en.ordinal\"]\n"), LayerUser, true)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"speed": "1.25", "jobs": "2", "normalize.disable": "en.year,en.ordinal"} {
		if got := f.Top[k].Raw; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	for _, bad := range []string{"voice = [\"a\"]\n", "[normalize]\ndisable = \"en.year\"\n", "voice = { a = 1 }\n"} {
		if _, err := ParseFile(s, "c", []byte(bad), LayerUser, true); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// accept: CFG-02#1
func TestUnknownProfileListsAvailable(t *testing.T) {
	s := testSchema(t)
	f, err := ParseFile(s, "c.toml", []byte("[profile.narrator]\nvoice = \"marin\"\n[profile.anchor]\nvoice = \"ash\"\n"), LayerUser, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Resolve(Input{Schema: s, User: f, Getenv: noEnv, Flags: Values{ProfileKey: {Raw: "narator", Layer: LayerFlag, Origin: "--profile"}}})
	if err == nil || err.Error() != `unknown profile "narator" (from --profile); available profiles: anchor, narrator` {
		t.Fatalf("err %v", err)
	}
	env := func(k string) string {
		if k == "RECITE_PROFILE" {
			return "x"
		}
		return ""
	}
	if _, err = Resolve(Input{Schema: s, Getenv: env}); err == nil || err.Error() != `unknown profile "x" (from RECITE_PROFILE); no profiles are defined (no config file)` {
		t.Fatalf("err %v", err)
	}
	// A profile overrides the top-level config, and only that: env and flags still win.
	got, err := Resolve(Input{Schema: s, User: f, Getenv: env, Flags: Values{ProfileKey: {Raw: "anchor", Layer: LayerFlag, Origin: "--profile"}}})
	if err != nil {
		t.Fatal(err)
	}
	if v := got.Get("voice"); v.Raw != "ash" || v.Source() != "profile anchor (c.toml:4)" {
		t.Fatalf("voice %+v", v)
	}
}

// accept: CFG-03#1
func TestEnvironmentValues(t *testing.T) {
	s := testSchema(t)
	env := map[string]string{"RECITE_VOICE": "marin", "RECITE_JOBS": "4", "OPENAI_API_KEY": "sk-canary"}
	got, err := Resolve(Input{Schema: s, Getenv: func(k string) string { return env[k] }})
	if err != nil {
		t.Fatal(err)
	}
	if v := got.Get("voice"); v.Raw != "marin" || v.Source() != "env (RECITE_VOICE)" {
		t.Fatalf("voice %+v", v)
	}
	for _, e := range got.Entries() {
		if strings.Contains(e.Raw, "sk-canary") {
			t.Fatalf("OPENAI_API_KEY reached %s", e.Key)
		}
	}
	env["RECITE_JOBS"] = "lots"
	// A flag overriding the variable does not hide that the variable is malformed.
	_, err = Resolve(Input{Schema: s, Getenv: func(k string) string { return env[k] }, Flags: Values{"jobs": {Raw: "2", Layer: LayerFlag}}})
	if err == nil || !strings.HasPrefix(err.Error(), "RECITE_JOBS: ") {
		t.Fatalf("err %v", err)
	}
}

func TestReadFileMissing(t *testing.T) {
	s := testSchema(t)
	p := filepath.Join(t.TempDir(), "none.toml")
	if f, err := ReadFile(s, p, LayerUser, true, false); f != nil || err != nil {
		t.Fatalf("optional missing file: %v %v", f, err)
	}
	if _, err := ReadFile(s, p, LayerUser, true, true); err == nil {
		t.Fatal("required missing file accepted")
	}
}

func TestUserConfigPath(t *testing.T) {
	env := map[string]string{"HOME": "/h"}
	get := func(k string) string { return env[k] }
	if p := UserConfigPath(get); p != filepath.Join("/h", ".config", "recite", "config.toml") {
		t.Fatal(p)
	}
	env["XDG_CONFIG_HOME"] = "/x"
	if p := UserConfigPath(get); p != filepath.Join("/x", "recite", "config.toml") {
		t.Fatal(p)
	}
}

// accept: CLI-10#1
func TestShowGoldenSixLayers(t *testing.T) {
	s := testSchema(t)
	user, err := ParseFile(s, "user.toml", []byte("voice = \"alloy\"\nmodel = \"m-user\"\n\n[profile.narrator]\nmodel = \"m-prof\"\njobs = 5\n"), LayerUser, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFile(s, "project.toml", []byte("[profile.x]\nvoice = \"ash\"\n"), LayerProject, false); err == nil {
		t.Fatal("project file with a profile accepted")
	}
	project, err := ParseFile(s, "project.toml", []byte("jobs = 4\n[verify]\nmode = \"strict\"\n"), LayerProject, false)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"RECITE_PROFILE": "narrator", "RECITE_JOBS": "6"}
	got, err := Resolve(Input{
		Schema: s, User: user, Project: project,
		Getenv: func(k string) string { return env[k] },
		Flags:  Values{"gaps.sentence": {Raw: "250ms..300ms", Layer: LayerFlag, Origin: "--gap-sentence"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := FormatText(got.Entries())
	path := filepath.Join("testdata", "show-six-layers.golden")
	if *update {
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if out != string(want) {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
	seen := map[Layer]bool{}
	for _, e := range got.Entries() {
		seen[e.Layer] = true
	}
	for _, l := range []Layer{LayerDefault, LayerUser, LayerProfile, LayerProject, LayerEnv, LayerFlag} {
		if !seen[l] {
			t.Errorf("layer %s not shown", l)
		}
	}
}
