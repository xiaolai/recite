package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/config"
)

// maxProjectBytes bounds a project file; a real one is a few kilobytes.
const maxProjectBytes = 1 << 20

// project is a parsed project file (RUN-07): shared [defaults] and the outputs in file order.
type project struct {
	path    string
	outputs []projectOutput
}

// projectOutput is one [[output]]: its input and audio paths, both resolved against the
// project's directory, and its settings with the per-output overrides over the defaults.
type projectOutput struct {
	input, path string // as written in the file, for the summary
	inputPath   string
	outputPath  string
	format      audio.Format // "" means from the output's extension
	settings    config.Values
}

// The keys of a project file that are not settings.
const (
	projectDefaults = "defaults"
	projectOutputs  = "output"
	outputInput     = "input"
	outputPath      = "path"
	outputFormat    = "format"
)

// readProject reads and validates the project file at path against schema. Everything is
// checked before any output runs: unknown keys, invalid values, and input or output paths
// that are absolute or leave the project's directory are errors (exit 2).
func readProject(schema *config.Schema, path string) (*project, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read project: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxProjectBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read project: %w", err)
	}
	if len(data) > maxProjectBytes {
		return nil, fmt.Errorf("%s: a project file is limited to %d bytes", path, maxProjectBytes)
	}
	return parseProject(schema, path, data)
}

func parseProject(schema *config.Schema, path string, data []byte) (*project, error) {
	var root map[string]any
	if _, err := toml.Decode(string(data), &root); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var errs []error
	fail := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s: "+format, append([]any{path}, a...)...))
	}
	keys := make([]string, 0, len(root))
	for k := range root {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k != projectDefaults && k != projectOutputs {
			fail("unknown key %q (a project has [%s] and [[%s]])", k, projectDefaults, projectOutputs)
		}
	}
	defaults := map[string]any{}
	if v, ok := root[projectDefaults]; ok {
		t, ok := v.(map[string]any)
		if !ok {
			fail("%s must be a table", projectDefaults)
		} else {
			defaults = t
		}
	}
	var tables []map[string]any
	if v, ok := root[projectOutputs]; ok {
		if tables, ok = v.([]map[string]any); !ok {
			fail("%s must be an array of [[%s]] tables", projectOutputs, projectOutputs)
		}
	}
	if len(tables) == 0 && len(errs) == 0 {
		fail("no [[%s]]: a project lists at least one output", projectOutputs)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	dir := filepath.Dir(path)
	defFormat, defSettings, err := projectTable(schema, path, data, []string{projectDefaults}, defaults, "defaults", false)
	if err != nil {
		errs = append(errs, err)
	}
	p := &project{path: path}
	seen := map[string]int{}
	for i, t := range tables {
		n := i + 1
		where := fmt.Sprintf("output %d", n)
		format, settings, err := projectTable(schema, path, data, []string{projectOutputs}, t, where, true)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		o := projectOutput{format: defFormat, settings: config.Values{}}
		if format != "" {
			o.format = format
		}
		for k, v := range defSettings {
			o.settings[k] = v
		}
		for k, v := range settings {
			o.settings[k] = v
		}
		var perr error
		if o.input, o.inputPath, perr = projectPath(dir, t, outputInput); perr != nil {
			errs = append(errs, fmt.Errorf("%s: %s: %w", path, where, perr))
			continue
		}
		if o.path, o.outputPath, perr = projectPath(dir, t, outputPath); perr != nil {
			errs = append(errs, fmt.Errorf("%s: %s: %w", path, where, perr))
			continue
		}
		if prev, dup := seen[o.outputPath]; dup {
			errs = append(errs, fmt.Errorf("%s: %s: path %q is also the path of output %d", path, where, o.path, prev))
			continue
		}
		seen[o.outputPath] = n
		p.outputs = append(p.outputs, o)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return p, nil
}

// projectTable splits a [defaults] or [[output]] table into its format and its settings.
// The input and path keys are allowed only in an output.
func projectTable(schema *config.Schema, path string, data []byte, prefix []string, t map[string]any, where string, output bool) (audio.Format, config.Values, error) {
	rest := make(map[string]any, len(t))
	var format audio.Format
	var errs []error
	for k, v := range t {
		switch k {
		case outputFormat:
			s, ok := v.(string)
			f, err := audio.ParseFormat(s)
			if !ok || err != nil {
				errs = append(errs, fmt.Errorf("%s: %s: format: want one of the output formats, not %v", path, where, v))
				continue
			}
			format = f
		case outputInput, outputPath:
			if !output {
				errs = append(errs, fmt.Errorf("%s: %s: key %q belongs in each [[%s]]", path, where, k, projectOutputs))
			}
		default:
			rest[k] = v
		}
	}
	vals, err := config.ParseTable(schema, path, data, prefix, rest, config.LayerProject, where)
	if err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", where, err))
	}
	return format, vals, errors.Join(errs...)
}

// projectPath reads the path at key of an output: a relative path that stays inside the
// project's directory, which it is resolved against. A project file names its own files, so
// one that reaches outside its directory is refused rather than trusted.
func projectPath(dir string, t map[string]any, key string) (written, resolved string, err error) {
	v, ok := t[key]
	if !ok {
		return "", "", fmt.Errorf("missing %q", key)
	}
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", "", fmt.Errorf("%s: want a non-empty path", key)
	}
	local := filepath.FromSlash(s)
	if !filepath.IsLocal(local) {
		return "", "", fmt.Errorf("%s %q: want a relative path inside the project's directory", key, s)
	}
	return s, filepath.Join(dir, local), nil
}
