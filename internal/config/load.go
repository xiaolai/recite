package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ProfileKey is the setting that selects a [profile.NAME] table (--profile, RECITE_PROFILE).
const ProfileKey = "profile"

// UserConfigPath is $XDG_CONFIG_HOME/recite/config.toml, else ~/.config/recite/config.toml
// (design §13, on every OS). "" when neither variable is set.
func UserConfigPath(getenv func(string) string) string {
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "recite", "config.toml")
	}
	if h := getenv("HOME"); h != "" {
		return filepath.Join(h, ".config", "recite", "config.toml")
	}
	return ""
}

// ReadFile reads and parses the settings file at path. A missing file is (nil, nil) unless
// required, as for an explicit --config.
func ReadFile(s *Schema, path string, layer Layer, profiles, required bool) (*File, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && !required {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return ParseFile(s, path, data, layer, profiles)
}

// Input is everything Resolve merges. User and Project may be nil.
type Input struct {
	Schema      *Schema
	User        *File
	Project     *File
	FrontMatter Values
	Getenv      func(string) string
	// Flags holds the values set on the command line, tagged LayerFlag.
	Flags Values
}

// Resolve merges defaults, the user file, the selected profile, the project file, front
// matter, the environment and the flags (CFG-04). Every environment value is validated
// whether or not a flag overrides it, so a malformed variable always fails (CFG-03).
func Resolve(in Input) (Settings, error) {
	env, err := EnvValues(in.Schema, in.Getenv)
	if err != nil {
		return Settings{}, err
	}
	layers := map[Layer]Values{LayerEnv: env, LayerFlag: in.Flags, LayerFrontMatter: in.FrontMatter}
	if in.User != nil {
		layers[LayerUser] = in.User.Top
	}
	if in.Project != nil {
		layers[LayerProject] = in.Project.Top
	}
	name, origin := "", ""
	if v, ok := env[ProfileKey]; ok {
		name, origin = v.Raw, v.Origin
	}
	if v, ok := in.Flags[ProfileKey]; ok {
		name, origin = v.Raw, v.Origin
	}
	if name != "" {
		prof, ok := in.User.profile(name)
		if !ok {
			return Settings{}, unknownProfile(name, origin, in.User)
		}
		layers[LayerProfile] = prof
	}
	return Merge(in.Schema, layers)
}

func (f *File) profile(name string) (Values, bool) {
	if f == nil {
		return nil, false
	}
	p, ok := f.Profiles[name]
	return p, ok
}

func unknownProfile(name, origin string, f *File) error {
	names := f.ProfileNames()
	if len(names) == 0 {
		if f == nil {
			return fmt.Errorf("unknown profile %q (from %s); no profiles are defined (no config file)", name, origin)
		}
		return fmt.Errorf("unknown profile %q (from %s); %s defines no profiles", name, origin, f.Path)
	}
	return fmt.Errorf("unknown profile %q (from %s); available profiles: %s", name, origin, strings.Join(names, ", "))
}

// EnvValues reads every key that has an environment variable. An empty variable counts as
// unset. A malformed value is an error naming the variable.
func EnvValues(s *Schema, getenv func(string) string) (Values, error) {
	out := Values{}
	for _, k := range s.keys {
		if k.Env == "" {
			continue
		}
		raw := getenv(k.Env)
		if raw == "" {
			continue
		}
		if k.Check != nil {
			if err := k.Check(raw); err != nil {
				return nil, fmt.Errorf("%s: %w", k.Env, err)
			}
		}
		out[k.Name] = Value{Raw: raw, Layer: LayerEnv, Origin: k.Env}
	}
	return out, nil
}
