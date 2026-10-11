// Package config merges defaults, user config, profile, project config, environment and flags, and validates the result.
package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Layer is a source of settings, in increasing precedence (design §13, CFG-04).
type Layer int

// Layers, lowest first. A value from a later layer replaces one from an earlier layer.
const (
	LayerDefault Layer = iota
	LayerUser
	LayerProfile
	LayerProject
	LayerFrontMatter
	LayerEnv
	LayerFlag
	numLayers
)

// Layers lists every layer, lowest precedence first.
func Layers() []Layer {
	out := make([]Layer, 0, numLayers)
	for l := range numLayers {
		out = append(out, l)
	}
	return out
}

func (l Layer) String() string {
	switch l {
	case LayerDefault:
		return "default"
	case LayerUser:
		return "user"
	case LayerProfile:
		return "profile"
	case LayerProject:
		return "project"
	case LayerFrontMatter:
		return "front matter"
	case LayerEnv:
		return "env"
	case LayerFlag:
		return "flag"
	}
	return fmt.Sprintf("layer(%d)", int(l))
}

// Key describes one setting. Values are carried as text in the form the matching flag
// accepts, so the flag parser and every layer agree on one syntax.
type Key struct {
	Name    string // dotted file key, e.g. "verify.mode"
	Default string
	Flag    string // flag name without dashes; "" when no flag sets it
	Env     string // environment variable; "" when none
	NoFile  bool   // set only by the environment or a flag, never by a file
	List    bool   // a TOML array of strings, carried comma-joined
	// Check validates a value; nil accepts any value.
	Check func(string) error
}

// Schema is the set of known keys, in display order.
type Schema struct {
	keys     []Key
	byName   map[string]int
	reserved map[string]string
}

// NewSchema builds a schema. reserved maps keys the design names but this version does
// not implement to the reason, so a file using them fails loudly rather than being ignored.
func NewSchema(keys []Key, reserved map[string]string) (*Schema, error) {
	s := &Schema{keys: slices.Clone(keys), byName: map[string]int{}, reserved: reserved}
	for i, k := range keys {
		if k.Name == "" {
			return nil, fmt.Errorf("config: key %d has no name", i)
		}
		if _, dup := s.byName[k.Name]; dup {
			return nil, fmt.Errorf("config: duplicate key %q", k.Name)
		}
		s.byName[k.Name] = i
	}
	return s, nil
}

// Keys returns the keys in display order.
func (s *Schema) Keys() []Key { return slices.Clone(s.keys) }

// Lookup returns the key named name.
func (s *Schema) Lookup(name string) (Key, bool) {
	i, ok := s.byName[name]
	if !ok {
		return Key{}, false
	}
	return s.keys[i], true
}

// isSection reports whether name is a table prefix of some key, such as "verify".
func (s *Schema) isSection(name string) bool {
	for _, k := range s.keys {
		if !k.NoFile && strings.HasPrefix(k.Name, name+".") {
			return true
		}
	}
	return false
}

// Value is one setting's effective text and where it came from.
type Value struct {
	Raw   string
	Layer Layer
	// Origin locates the value inside its layer: "PATH:LINE" for a file, the variable name
	// for the environment, "--flag" for a flag, "" for a default.
	Origin string
	// Detail qualifies the layer, such as the profile name.
	Detail string
}

// Source is the human form of where v came from, e.g. "profile narrator (config.toml:12)".
func (v Value) Source() string {
	s := v.Layer.String()
	if v.Detail != "" {
		s += " " + v.Detail
	}
	if v.Origin != "" {
		s += " (" + v.Origin + ")"
	}
	return s
}

// Values is one layer's settings by key name.
type Values map[string]Value

// Settings is the merged result: one value per known key.
type Settings struct {
	schema *Schema
	values map[string]Value
}

// Merge resolves layers by precedence: for every key, the value from the highest layer that
// sets it wins, else the schema default. Each input map must hold values of the layer it is
// given under; a value claiming another layer is a programming error.
func Merge(s *Schema, layers map[Layer]Values) (Settings, error) {
	out := Settings{schema: s, values: make(map[string]Value, len(s.keys))}
	for _, k := range s.keys {
		out.values[k.Name] = Value{Raw: k.Default, Layer: LayerDefault}
	}
	for _, l := range Layers() {
		vals := layers[l]
		names := make([]string, 0, len(vals))
		for name := range vals {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			v := vals[name]
			if v.Layer != l {
				return Settings{}, fmt.Errorf("config: value for %q claims layer %s inside layer %s", name, v.Layer, l)
			}
			if _, ok := s.byName[name]; !ok {
				return Settings{}, fmt.Errorf("config: unknown key %q in layer %s", name, l)
			}
			out.values[name] = v
		}
	}
	return out, nil
}

// Get returns the effective value of a known key; it panics on an unknown key, which is a
// programming error.
func (st Settings) Get(name string) Value {
	v, ok := st.values[name]
	if !ok {
		panic("config: unknown key " + name)
	}
	return v
}

// Entry is one row of `config show`.
type Entry struct {
	Key string
	Value
}

// Entries lists every setting in schema order.
func (st Settings) Entries() []Entry {
	out := make([]Entry, 0, len(st.schema.keys))
	for _, k := range st.schema.keys {
		out = append(out, Entry{Key: k.Name, Value: st.values[k.Name]})
	}
	return out
}
