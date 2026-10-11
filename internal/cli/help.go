package cli

import (
	"fmt"
	"io"
	"strings"
)

func (c *CLI) writeHelp(w io.Writer) {
	var b strings.Builder
	b.WriteString("recite turns text into verified speech.\n\n")
	b.WriteString("Usage:\n")
	b.WriteString("  recite [global flags] <command> [flags] [arguments]\n")
	b.WriteString("  recite [flags] TEXT...       same as: recite say TEXT...\n\n")
	b.WriteString("Commands:\n")
	for _, cmd := range c.commands() {
		fmt.Fprintf(&b, "  %-9s %s\n", cmd.name, cmd.summary)
	}
	b.WriteString("\nGlobal flags:\n")
	b.WriteString(globalDefaults())
	b.WriteString("\nRun \"recite help <command>\" for a command's flags.\n")
	_, _ = io.WriteString(w, b.String()) // help output has no error channel
}

func (c *CLI) writeCommandHelp(w io.Writer, cmd command) {
	var b strings.Builder
	usage := "recite " + cmd.name + " [flags]"
	if cmd.args != "" {
		usage += " " + cmd.args
	}
	fmt.Fprintf(&b, "Usage: %s\n\n%s\n", usage, cmd.summary)
	fs := newFlagSet("recite " + cmd.name)
	cmd.setup(fs, c)
	var own strings.Builder
	fs.SetOutput(&own)
	fs.PrintDefaults()
	if own.Len() > 0 {
		b.WriteString("\nFlags:\n")
		b.WriteString(own.String())
	}
	b.WriteString("\nGlobal flags:\n")
	b.WriteString(globalDefaults())
	_, _ = io.WriteString(w, b.String()) // help output has no error channel
}

func globalDefaults() string {
	g := defaultGlobals()
	fs := newFlagSet("recite")
	g.register(fs)
	var b strings.Builder
	fs.SetOutput(&b)
	fs.PrintDefaults()
	return b.String()
}
