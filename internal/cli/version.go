package cli

import (
	"encoding/json"
	"flag"
	"fmt"

	"github.com/xiaolai/recite/internal/buildinfo"
)

func setupVersion(_ *flag.FlagSet, c *CLI) func([]string) int {
	return func(args []string) int {
		if len(args) > 0 {
			c.errorf("recite version: takes no arguments")
			return ExitUsage
		}
		info := buildinfo.Get()
		if c.globals.JSON {
			b, err := json.Marshal(info)
			if err != nil {
				c.errorf("recite version: %v", err)
				return 1
			}
			_, err = fmt.Fprintf(c.Stdout, "%s\n", b)
			return c.writeResult(err)
		}
		_, err := fmt.Fprintf(c.Stdout, "recite %s\ncommit: %s\nbuilt:  %s\ngo:     %s\n",
			info.Version, info.Commit, info.Date, info.GoVersion)
		return c.writeResult(err)
	}
}
