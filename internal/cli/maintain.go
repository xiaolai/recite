package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/xiaolai/recite/internal/cache"
	"github.com/xiaolai/recite/internal/exitcode"
)

// setupCache is `recite cache ls|prune|clear` (CLI-08). It never prompts: clear needs --yes.
func setupCache(fs *flag.FlagSet, c *CLI) func([]string) int {
	var olderThan, maxSize string
	var yes bool
	fs.StringVar(&olderThan, "older-than", "", "prune: remove takes not used in this period, such as 30d")
	fs.StringVar(&maxSize, "max-size", "", "prune: then remove the least recently used takes until the cache fits, such as 2GB")
	fs.BoolVar(&yes, "yes", false, "clear: confirm removing every take")
	return func(args []string) int {
		if len(args) != 1 {
			c.errorf("recite cache: takes one subcommand: ls, prune or clear")
			return ExitUsage
		}
		root, err := c.cacheRoot()
		if err != nil {
			c.errorf("recite cache: %v", err)
			return ExitUsage
		}
		switch args[0] {
		case "ls":
			return c.cacheLs(root)
		case "prune":
			if olderThan == "" && maxSize == "" {
				c.errorf("recite cache prune: give --older-than, --max-size or both")
				return ExitUsage
			}
			var cutoff time.Time
			if olderThan != "" {
				d, err := parseAge(olderThan)
				if err != nil {
					c.errorf("recite cache prune: --older-than: %v", err)
					return ExitUsage
				}
				cutoff = c.Now().Add(-d)
			}
			limit := int64(-1)
			if maxSize != "" {
				if limit, err = parseSize(maxSize); err != nil {
					c.errorf("recite cache prune: --max-size: %v", err)
					return ExitUsage
				}
			}
			return c.cacheRemove(root, "prune", func(items []cache.Item) []cache.Item { return cache.PrunePlan(items, cutoff, limit) })
		case "clear":
			if !yes {
				c.errorf("recite cache clear: removes every take; run it again with --yes to confirm")
				return ExitUsage
			}
			return c.cacheRemove(root, "clear", func(items []cache.Item) []cache.Item { return items })
		}
		c.errorf("recite cache: unknown subcommand %q; use ls, prune or clear", args[0])
		return ExitUsage
	}
}

// cacheRoot is the take cache root: RECITE_CACHE_DIR, else the platform default.
func (c *CLI) cacheRoot() (string, error) {
	root := c.Getenv("RECITE_CACHE_DIR")
	if root == "" {
		root = defaultCacheDir(c.Getenv)
	} else if err := absPath(root); err != nil {
		return "", fmt.Errorf("RECITE_CACHE_DIR: %w", err)
	}
	if root == "" {
		return "", errors.New("no cache directory: set HOME, XDG_CACHE_HOME or RECITE_CACHE_DIR")
	}
	return root, nil
}

func (c *CLI) cacheLs(root string) int {
	items, err := cache.Scan(root)
	if err != nil {
		c.errorf("recite cache ls: %v", err)
		return exitcode.Internal
	}
	s := cache.Summarize(items)
	if c.globals.JSON {
		ev := struct {
			Event  string     `json:"event"`
			Dir    string     `json:"dir"`
			Count  int        `json:"count"`
			Bytes  int64      `json:"bytes"`
			Oldest *time.Time `json:"oldest_access"`
			Newest *time.Time `json:"newest_access"`
		}{Event: "cache", Dir: cache.TakesDir(root), Count: s.Count, Bytes: s.Size}
		if s.Count > 0 {
			o, n := s.Oldest.UTC(), s.Newest.UTC()
			ev.Oldest, ev.Newest = &o, &n
		}
		return c.emit(ev, ExitOK)
	}
	out := fmt.Sprintf("dir     %s\ntakes   %d\nsize    %s\n", cache.TakesDir(root), s.Count, formatSize(s.Size))
	if s.Count > 0 {
		out += fmt.Sprintf("oldest  %s (last used)\nnewest  %s (last used)\n", stamp(s.Oldest), stamp(s.Newest))
	}
	_, err = io.WriteString(c.Stdout, out)
	return c.writeResult(err)
}

// cacheRemove removes the takes pick selects and reports what it reclaimed.
func (c *CLI) cacheRemove(root, verb string, pick func([]cache.Item) []cache.Item) int {
	items, err := cache.Scan(root)
	if err != nil {
		c.errorf("recite cache %s: %v", verb, err)
		return exitcode.Internal
	}
	var n int
	var freed int64
	for _, it := range pick(items) {
		if err := cache.Remove(root, it.Key); err != nil {
			c.errorf("recite cache %s: %v", verb, err)
			return exitcode.Internal
		}
		n++
		freed += it.Size
	}
	left := len(items) - n
	if c.globals.JSON {
		return c.emit(struct {
			Event   string `json:"event"`
			Action  string `json:"action"`
			Removed int    `json:"removed"`
			Freed   int64  `json:"freed_bytes"`
			Left    int    `json:"left"`
		}{"cache_removed", verb, n, freed, left}, ExitOK)
	}
	_, err = fmt.Fprintf(c.Stdout, "removed %d takes (%s), %d left\n", n, formatSize(freed), left)
	return c.writeResult(err)
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

// parseAge reads a period such as 30d, 12h or 90m: a whole number and a unit (d, h, m).
func parseAge(s string) (time.Duration, error) {
	units := map[byte]time.Duration{'d': 24 * time.Hour, 'h': time.Hour, 'm': time.Minute}
	if len(s) < 2 {
		return 0, fmt.Errorf("%q: want a number and a unit (d, h or m), such as 7d", s)
	}
	u, ok := units[s[len(s)-1]]
	n, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
	if !ok || err != nil || n <= 0 || n > math.MaxInt64/int64(u) {
		return 0, fmt.Errorf("%q: want a positive number and a unit (d, h or m), such as 7d", s)
	}
	return time.Duration(n) * u, nil
}

// sizeUnits are decimal (KB = 1000) and binary (KiB = 1024), longest suffix first.
var sizeUnits = []struct {
	suffix string
	mult   float64
}{
	{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30}, {"TIB", 1 << 40},
	{"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12}, {"B", 1},
}

// parseSize reads a size such as 2GB, 500MiB or 1048576 (bytes).
func parseSize(s string) (int64, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	mult := 1.0
	for _, u := range sizeUnits {
		if strings.HasSuffix(t, u.suffix) {
			t, mult = strings.TrimSpace(strings.TrimSuffix(t, u.suffix)), u.mult
			break
		}
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || f < 0 || math.IsNaN(f) || f*mult >= math.MaxInt64 {
		return 0, fmt.Errorf("%q: want a size such as 2GB, 500MB or 1GiB", s)
	}
	return int64(f * mult), nil
}

// formatSize renders bytes in decimal units, matching what --max-size reads.
func formatSize(n int64) string {
	switch {
	case n >= 1e9:
		return strconv.FormatFloat(float64(n)/1e9, 'f', 2, 64) + " GB"
	case n >= 1e6:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 2, 64) + " MB"
	case n >= 1e3:
		return strconv.FormatFloat(float64(n)/1e3, 'f', 2, 64) + " KB"
	}
	return strconv.FormatInt(n, 10) + " B"
}
