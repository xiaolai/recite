package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/text"
)

// scriptHeader opens every spoken script `prepare` writes.
const scriptHeader = text.CommentPrefix + " " + text.KeyRecite + ": spoken script; edit the text, then render it with: recite say -f FILE --input-format spoken"

func setupPrepare(fs *flag.FlagSet, c *CLI) func([]string) int {
	var tf textFlags
	tf.register(fs)
	var output string
	var force, withFindings bool
	fs.StringVar(&output, "o", "", "write the spoken script to `PATH`, or - for stdout (required)")
	fs.StringVar(&output, "output", "", "write the spoken script to `PATH`, or - for stdout (required)")
	fs.BoolVar(&force, "force", false, "overwrite the output")
	fs.BoolVar(&withFindings, "with-findings", false, "add a comment per finding next to its chunk")
	return func(args []string) int {
		if len(args) != 1 {
			c.errorf("recite prepare: takes exactly one FILE (- for stdin)")
			return ExitUsage
		}
		pre, code := c.prereadMarkdown("recite prepare", &tf, text.Source{File: args[0], Stdin: c.Stdin, StdinIsTTY: c.StdinIsTTY()}, args[0])
		if code != ExitOK {
			return code
		}
		g, so, st, err := c.textSettings(fs, &tf, pre.frontMatter())
		if err != nil {
			c.errorf("recite prepare: %v", err)
			return ExitUsage
		}
		format, marks, err := tf.inputMode(args[0])
		if err != nil {
			c.errorf("recite prepare: %v", err)
			return ExitUsage
		}
		if format == formatSpoken {
			c.errorf("recite prepare: the input is already a spoken script; render it with say --input-format spoken")
			return ExitUsage
		}
		g.Marks = marks
		g.Markdown = format == formatMarkdown
		md, err := tf.markdownOptions()
		if err != nil {
			c.errorf("recite prepare: %v", err)
			return ExitUsage
		}
		if output == "" {
			c.errorf("recite prepare: -o PATH is required (- writes to stdout)")
			return ExitUsage
		}
		in := textInput{format: format, markdown: md, lang: st.Get(langKey).Raw, preread: pre}
		return c.runPrepare(args[0], output, force, withFindings, tf, in, g, so)
	}
}

// runPrepare is `prepare` (CLI-15, design §7.10): the text stages of `say`, written as an
// editable spoken script that `say --input-format spoken` renders to the same plan.
func (c *CLI) runPrepare(file, output string, force, withFindings bool, tf textFlags, in textInput, g guard.Options, so spoken.Options) int {
	input, code := c.readInput("recite prepare", in, text.Source{File: file, Stdin: c.Stdin, StdinIsTTY: c.StdinIsTTY()}, tf.maxInput)
	if code != ExitOK {
		return code
	}
	doc, err := speechDoc(input, in)
	c.skippedWarning("recite prepare", doc.skipped)
	if err != nil {
		c.errorf("recite prepare: %v", err)
		return textErrorCode(err)
	}
	st, err := runDocStages(doc, g, so, tf.chunkSeconds, realtime.DefaultSettings())
	if err != nil {
		c.errorf("recite prepare: %v", err)
		return textErrorCode(err)
	}
	if st.failed || !c.globals.Quiet {
		for _, f := range st.guard.Findings {
			c.errorf("%s", f)
		}
		for _, f := range st.form.Findings {
			c.errorf("%s", f)
		}
	}
	if st.failed || !st.planned {
		c.errorf("recite prepare: the text stage fails as `say` would; fix the input or change --guard")
		return ExitUsage
	}
	script, err := spokenScript(st, withFindings)
	if err != nil {
		c.errorf("recite prepare: %v", err)
		return ExitUsage
	}
	if output == stdoutTarget {
		return c.writeResult(writeString(c.Stdout, script))
	}
	if err := preflight(output, force); err != nil {
		return c.reportFailure(err)
	}
	if err := publishOutput(output, force, func(f *os.File) error { return writeString(f, script) }); err != nil {
		return c.reportFailure(err)
	}
	c.Logger.Info("wrote spoken script", "path", output, "chunks", len(st.plan.Chunks))
	return ExitOK
}

func writeString(w io.Writer, s string) error {
	_, err := io.WriteString(w, s)
	return err
}

// spokenScript writes the plan as a spoken script: a header, then per chunk its display
// comment, its join when it is not a paragraph break, its findings when asked, and its
// spoken form with the marks that still matter re-written as marks. The output is a pure
// function of the plan.
func spokenScript(st textStages, withFindings bool) (string, error) {
	marks := spokenMarks(st.guard, st.form)
	var b strings.Builder
	b.WriteString(scriptHeader + "\n")
	for i, ch := range st.plan.Chunks {
		body, err := chunkScript(ch.Spoken, st.seg.Chunks[i].Spoken, marks)
		if err != nil {
			return "", fmt.Errorf("chunk %s: %w", ch.ID, err)
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "%s %s: %s\n", text.CommentPrefix, text.KeyDisplay, strconv.Quote(ch.Display))
		switch ch.Boundary {
		case text.BoundaryParagraph, text.BoundaryPause, text.BoundaryEnd:
		default:
			fmt.Fprintf(&b, "%s %s: %s\n", text.CommentPrefix, text.KeyJoin, ch.Boundary)
		}
		if withFindings {
			for _, f := range ch.Findings {
				fmt.Fprintf(&b, "%s %s: %s %s %s\n", text.CommentPrefix, text.KeyFinding, f.At, f.Code, f.Action)
			}
		}
		b.WriteString(body)
		if ch.Boundary == text.BoundaryPause {
			b.WriteString(text.Mark{Kind: text.MarkPause, Pause: ch.Pause}.String())
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// chunkScript is a chunk's spoken form with its marks re-written so that reading it back
// gives the same spoken form, instructions and protected ranges: say, spell and digits as
// {{say:SPOKEN|TEXT}}; hint, emph and lang as themselves; num, date, fraction and verbatim
// as their words, which no longer need a mark.
func chunkScript(spokenText string, r spoken.Range, marks []text.SpokenMark) (string, error) {
	var b strings.Builder
	pos := 0
	plain := func(s string) error {
		if strings.Contains(s, "{{") {
			return fmt.Errorf(`its spoken form holds "{{", which a spoken script would read as a speech mark`)
		}
		b.WriteString(s)
		return nil
	}
	for _, sm := range marks {
		if sm.Spoken.Start < r.Start || sm.Spoken.End > r.End {
			continue
		}
		a, z := sm.Spoken.Start-r.Start, sm.Spoken.End-r.Start
		if err := plain(spokenText[pos:a]); err != nil {
			return "", err
		}
		said := spokenText[a:z]
		m := sm.Mark
		switch m.Kind {
		case text.MarkSay, text.MarkSpell, text.MarkDigits:
			b.WriteString(text.Mark{Kind: text.MarkSay, Value: said, Text: m.Text}.String())
		case text.MarkHint, text.MarkEmph, text.MarkLang:
			b.WriteString(text.Mark{Kind: m.Kind, Value: m.Value, Text: said}.String())
		default:
			if err := plain(said); err != nil {
				return "", err
			}
		}
		pos = z
	}
	if err := plain(spokenText[pos:]); err != nil {
		return "", err
	}
	out := b.String()
	if why := text.CheckScriptText(out, false); why != "" {
		return "", fmt.Errorf("its spoken form cannot be written as a script paragraph: %s", why)
	}
	return out, nil
}
