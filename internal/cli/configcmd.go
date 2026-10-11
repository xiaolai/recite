package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/xiaolai/recite/internal/config"
)

// setupConfig is `config show|path` (CLI-10). It takes every setting flag of `say`, so
// `config show --voice marin` reports what `say --voice marin` would use.
func setupConfig(fs *flag.FlagSet, c *CLI) func([]string) int {
	var all settingFlags
	all.register(fs)
	return func(args []string) int {
		if len(args) != 1 {
			c.errorf("recite config: takes one subcommand: show or path")
			return ExitUsage
		}
		switch args[0] {
		case "show":
			return c.configShow(fs)
		case "path":
			return c.configPathCmd()
		}
		c.errorf("recite config: unknown subcommand %q; use show or path", args[0])
		return ExitUsage
	}
}

type settingRow struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Layer  string `json:"layer"`
	Detail string `json:"detail,omitempty"`
	Origin string `json:"origin,omitempty"`
}

func (c *CLI) configShow(fs *flag.FlagSet) int {
	st, err := c.resolveSettings(fs)
	if err != nil {
		c.errorf("recite config show: %v", err)
		return ExitUsage
	}
	if c.globals.JSON {
		rows := []settingRow{}
		for _, e := range st.Entries() {
			rows = append(rows, settingRow{Key: e.Key, Value: e.Raw, Layer: e.Layer.String(), Detail: e.Detail, Origin: e.Origin})
		}
		return c.emit(struct {
			Settings []settingRow `json:"settings"`
		}{rows}, ExitOK)
	}
	_, err = io.WriteString(c.Stdout, config.FormatText(st.Entries()))
	return c.writeResult(err)
}

// configPathCmd prints the resolved file paths. It reads no settings file, so it still
// works when the file is broken and the user needs to find it.
func (c *CLI) configPathCmd() int {
	path, _ := c.configPath()
	state := "missing"
	if path == "" {
		state = "no HOME or XDG_CONFIG_HOME"
	} else if _, err := os.Stat(path); err == nil {
		state = "found"
	} else if !errors.Is(err, fs.ErrNotExist) {
		state = "unreadable"
	}
	cache := c.Getenv("RECITE_CACHE_DIR")
	if cache == "" {
		cache = defaultCacheDir(c.Getenv)
	} else if err := absPath(cache); err != nil {
		c.errorf("recite config path: RECITE_CACHE_DIR: %v", err)
		return ExitUsage
	}
	if c.globals.JSON {
		return c.emit(struct {
			Config      string `json:"config"`
			ConfigState string `json:"config_state"`
			Cache       string `json:"cache"`
		}{path, state, cache}, ExitOK)
	}
	_, err := fmt.Fprintf(c.Stdout, "config  %s (%s)\ncache   %s\n", path, state, cache)
	return c.writeResult(err)
}
