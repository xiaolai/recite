package config

import (
	"fmt"
	"strings"
)

// FormatText renders entries as the aligned `config show` table: key, effective value and
// source. An empty value prints as "".
func FormatText(entries []Entry) string {
	rows := [][3]string{{"KEY", "VALUE", "SOURCE"}}
	for _, e := range entries {
		v := e.Raw
		if v == "" {
			v = `""`
		}
		rows = append(rows, [3]string{e.Key, v, e.Source()})
	}
	var w0, w1 int
	for _, r := range rows {
		w0, w1 = max(w0, len(r[0])), max(w1, len(r[1]))
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%-*s  %-*s  %s\n", w0, r[0], w1, r[1], r[2])
	}
	return b.String()
}
