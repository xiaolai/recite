package config

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// File is one parsed TOML settings file.
type File struct {
	Path string
	// Top holds the top-level settings, Profiles the [profile.NAME] tables.
	Top      map[string]Value
	Profiles map[string]map[string]Value
}

// ProfileNames lists the file's profiles, sorted.
func (f *File) ProfileNames() []string {
	if f == nil {
		return nil
	}
	names := make([]string, 0, len(f.Profiles))
	for n := range f.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// FileError is an invalid settings file. Line is 1-based, 0 when unknown.
type FileError struct {
	Path string
	Line int
	Msg  string
}

func (e *FileError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", e.Path, e.Line, e.Msg)
	}
	return e.Path + ": " + e.Msg
}

// ParseFile parses data, read from path, against s. Values are tagged with layer; the
// [profile.NAME] tables are accepted only when profiles is set, and their values are tagged
// LayerProfile. Every problem is a *FileError naming the line; when there are several, they
// are joined in line order.
func ParseFile(s *Schema, path string, data []byte, layer Layer, profiles bool) (*File, error) {
	var root map[string]any
	if _, err := toml.Decode(string(data), &root); err != nil {
		var pe toml.ParseError
		if errors.As(err, &pe) {
			return nil, &FileError{Path: path, Line: pe.Position.Line, Msg: pe.Message}
		}
		return nil, &FileError{Path: path, Msg: err.Error()}
	}
	p := &fileParser{s: s, path: path, data: data}
	f := &File{Path: path, Top: map[string]Value{}, Profiles: map[string]map[string]Value{}}
	for _, name := range sortedKeys(root) {
		v := root[name]
		if name == "profile" && profiles {
			p.profiles(f, v)
			continue
		}
		p.walk(f.Top, nil, nil, name, v, layer, "")
	}
	if len(p.errs) > 0 {
		sort.SliceStable(p.errs, func(i, j int) bool { return p.errs[i].Line < p.errs[j].Line })
		errs := make([]error, len(p.errs))
		for i, e := range p.errs {
			errs[i] = e
		}
		return nil, errors.Join(errs...)
	}
	return f, nil
}

type fileParser struct {
	s    *Schema
	path string
	data []byte
	errs []*FileError
}

func (p *fileParser) fail(path []string, format string, a ...any) {
	p.errs = append(p.errs, &FileError{Path: p.path, Line: keyLine(p.data, path), Msg: fmt.Sprintf(format, a...)})
}

func (p *fileParser) profiles(f *File, v any) {
	tables, ok := v.(map[string]any)
	if !ok {
		p.fail([]string{"profile"}, "profile must be a table of [profile.NAME] tables")
		return
	}
	for _, name := range sortedKeys(tables) {
		body, ok := tables[name].(map[string]any)
		if !ok {
			p.fail([]string{"profile", name}, "profile.%s must be a table", name)
			continue
		}
		vals := map[string]Value{}
		f.Profiles[name] = vals
		for _, k := range sortedKeys(body) {
			p.walk(vals, []string{"profile", name}, nil, k, body[k], LayerProfile, name)
		}
	}
}

// walk records the value of key name under the table path keyPath. docPrefix is the part of
// the document path that is not part of the key (a profile's "profile.NAME").
func (p *fileParser) walk(dst map[string]Value, docPrefix, keyPath []string, name string, v any, layer Layer, detail string) {
	kp := append(append([]string{}, keyPath...), name)
	doc := append(append([]string{}, docPrefix...), kp...)
	full := strings.Join(kp, ".")
	if why, ok := p.s.reserved[full]; ok {
		p.fail(doc, "key %q is not supported by this version (%s)", full, why)
		return
	}
	if k, ok := p.s.Lookup(full); ok && !k.NoFile {
		raw, err := toText(v, k.List)
		if err == nil && k.Check != nil {
			err = k.Check(raw)
		}
		if err != nil {
			p.fail(doc, "%s: %v", full, err)
			return
		}
		dst[full] = Value{Raw: raw, Layer: layer, Origin: fmt.Sprintf("%s:%d", p.path, keyLine(p.data, doc)), Detail: detail}
		return
	}
	if t, ok := v.(map[string]any); ok && p.s.isSection(full) {
		for _, sub := range sortedKeys(t) {
			p.walk(dst, docPrefix, kp, sub, t[sub], layer, detail)
		}
		return
	}
	p.fail(doc, "unknown key %q", full)
}

// toText renders a TOML value in the syntax the matching flag takes.
func toText(v any, list bool) (string, error) {
	if list {
		arr, ok := v.([]any)
		if !ok {
			return "", fmt.Errorf("want an array of strings, not %s", tomlType(v))
		}
		parts := make([]string, len(arr))
		for i, e := range arr {
			s, ok := e.(string)
			if !ok || strings.Contains(s, ",") {
				return "", fmt.Errorf("want an array of strings without commas")
			}
			parts[i] = s
		}
		return strings.Join(parts, ","), nil
	}
	switch x := v.(type) {
	case string:
		return x, nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(x), nil
	}
	return "", fmt.Errorf("want a string or number, not %s", tomlType(v))
}

func tomlType(v any) string {
	switch v.(type) {
	case map[string]any:
		return "a table"
	case []any, []map[string]any:
		return "an array"
	}
	return fmt.Sprintf("a %T", v)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// errLineProbe is returned by lineProbe so the decoder reports where the probed key sits.
var errLineProbe = errors.New("line probe")

type lineProbe struct{}

func (lineProbe) UnmarshalTOML(any) error { return errLineProbe }

// keyLine returns the line of the key at path in data, 0 when unknown. The TOML decoder
// knows every key's position but exposes it only on a decode error, so this decodes into a
// struct that has exactly that key, typed to fail.
func keyLine(data []byte, path []string) int {
	t := reflect.TypeOf(lineProbe{})
	for i := len(path) - 1; i >= 0; i-- {
		t = reflect.StructOf([]reflect.StructField{{
			Name: "F",
			Type: t,
			Tag:  reflect.StructTag("toml:" + strconv.Quote(path[i])),
		}})
	}
	_, err := toml.Decode(string(data), reflect.New(t).Interface())
	var pe toml.ParseError
	// ParseError does not unwrap, so the probe is recognised by its message.
	if errors.As(err, &pe) && pe.Message == errLineProbe.Error() {
		return pe.Position.Line
	}
	return 0
}
