package cli

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/xiaolai/recite/internal/config"
	"github.com/xiaolai/recite/internal/exitcode"
	"github.com/xiaolai/recite/internal/logx"
	"github.com/xiaolai/recite/internal/usage"
)

// renderExcluded are the `say` flags `render` does not take: the project names each output's
// input and audio file, and each output's manifest goes next to its audio.
var renderExcluded = map[string]bool{"f": true, "file": true, "o": true, "output": true, "manifest": true}

// setupRender is `render PROJECT.toml` (RUN-07, CLI-13). Each output runs through `say` itself,
// so verification, the manifest, loudness, chapters, subtitles and the shared take cache
// behave exactly as for one file. Its flags are those of `say`, applied to every output above
// the project's settings (CFG-04).
func setupRender(fs *flag.FlagSet, c *CLI) func([]string) int {
	sayFS := newFlagSet("recite say")
	setupSay(sayFS, c)
	forward := map[string]bool{}
	sayFS.VisitAll(func(f *flag.Flag) {
		if !renderExcluded[f.Name] {
			fs.Var(f.Value, f.Name, f.Usage)
			forward[f.Name] = true
		}
	})
	var failFast bool
	fs.BoolVar(&failFast, "fail-fast", false, "stop at the first output that fails (default: render every output)")
	run := func(args []string) int {
		if len(args) != 1 {
			c.errorf("recite render: want one PROJECT.toml, got %d argument(s)", len(args))
			return ExitUsage
		}
		schema := settingSchema(c.Getenv, fs.Lookup("model-unchecked").Value.String() == "true")
		p, err := readProject(schema, args[0])
		if err != nil {
			c.errorf("recite render: %v", err)
			return c.renderDone(renderEvent{Project: args[0], Exit: ExitUsage})
		}
		var fwd []string
		formatGiven := false
		fs.Visit(func(f *flag.Flag) {
			if forward[f.Name] {
				fwd = append(fwd, "-"+f.Name+"="+f.Value.String())
				formatGiven = formatGiven || f.Name == "format"
			}
		})
		dryRun := fs.Lookup("dry-run").Value.String() == "true"
		return c.renderDone(c.renderProject(p, fwd, formatGiven, dryRun, failFast))
	}
	return run
}

// renderProject runs the outputs in order. A failed output does not stop the next unless
// failFast; an interrupt always stops. The exit code is the most severe (design §12.3).
func (c *CLI) renderProject(p *project, fwd []string, formatGiven, dryRun, failFast bool) renderEvent {
	ev := renderEvent{Project: p.path, DryRun: dryRun}
	if dryRun {
		ev.Plan = &renderPlan{}
	}
	stopped := false
	for i, o := range p.outputs {
		row := renderOutput{Input: o.input, Output: o.path}
		if stopped {
			row.Skipped = true
			ev.Outputs = append(ev.Outputs, row)
			continue
		}
		c.Logger.Info("render output", "n", i+1, "of", len(p.outputs), "input", o.input, "output", o.path)
		args := []string{"-f", o.inputPath, "-o", o.outputPath}
		if o.format != "" && !formatGiven {
			args = append(args, "-format="+string(o.format))
		}
		args = append(args, fwd...)
		code, plan := c.renderOne(p, o, args, dryRun)
		row.Exit = ptr(code)
		row.Plan = plan
		ev.Plan.add(plan)
		ev.Outputs = append(ev.Outputs, row)
		ev.Exit = exitcode.Worst(ev.Exit, code)
		if code == exitcode.Interrupted || (failFast && code != ExitOK) {
			stopped = true
		}
	}
	return ev
}

// renderOne runs one output through the `say` command with the project layer in place. Under
// --dry-run it returns the output's plan instead of printing it, for the aggregate.
func (c *CLI) renderOne(p *project, o projectOutput, args []string, dryRun bool) (int, *planEvent) {
	sfs := newFlagSet("recite render")
	run := setupSay(sfs, c)
	if _, err := parseInterspersed(sfs, args); err != nil {
		// The arguments come from flags render already parsed and a validated project.
		c.errorf("recite render: %s: %v", o.input, err)
		return ExitInternal, nil
	}
	say := c.Say
	var plan *planEvent
	c.project = &config.File{Path: p.path, Top: o.settings}
	c.written = nil
	c.Say = func(req SayRequest) int {
		req.Command = "render"
		if !dryRun {
			return say(req)
		}
		job, code := c.prepareSay(req)
		if code != ExitOK {
			return code
		}
		ev := c.estimate(req, job, true)
		plan = &ev
		if c.globals.JSON {
			c.event(ev)
		}
		return ExitOK
	}
	defer func() { c.Say, c.project = say, nil }()
	return run(nil), plan
}

// renderEvent is the last line of `render --json` and the source of the summary table.
type renderEvent struct {
	Event   string         `json:"event"`
	Project string         `json:"project,omitempty"`
	DryRun  bool           `json:"dry_run"`
	Exit    int            `json:"exit"`
	Outputs []renderOutput `json:"outputs"`
	Plan    *renderPlan    `json:"plan,omitempty"`
}

// renderOutput is one output's row; Exit is nil when --fail-fast skipped it.
type renderOutput struct {
	Input   string     `json:"input"`
	Output  string     `json:"output"`
	Exit    *int       `json:"exit"`
	Skipped bool       `json:"skipped"`
	Plan    *planEvent `json:"-"`
}

// renderPlan is the dry-run estimate summed over the outputs (CLI-13). Outputs render one
// after another, so their wall times add up. The price is null when any output has none.
type renderPlan struct {
	Chunks         int      `json:"chunks"`
	Cached         int      `json:"cached"`
	EstSeconds     float64  `json:"est_seconds"`
	EstWallSeconds float64  `json:"est_wall_seconds"`
	EstimateUSD    *float64 `json:"estimate_usd"`
	PriceNote      string   `json:"price_note"`
	unpriced       bool
}

func (r *renderPlan) add(p *planEvent) {
	if r == nil {
		return
	}
	if p == nil {
		r.unpriced = true // an output that failed to plan has no estimate to add
		r.EstimateUSD = nil
		return
	}
	r.Chunks += p.Chunks
	r.Cached += p.Cached
	r.EstSeconds = round3(r.EstSeconds + p.EstSeconds)
	r.EstWallSeconds = round3(r.EstWallSeconds + p.EstWallSeconds)
	r.PriceNote = p.PriceNote
	switch {
	case r.unpriced:
	case p.EstimateUSD == nil:
		r.unpriced, r.EstimateUSD = true, nil
	case r.EstimateUSD == nil:
		r.EstimateUSD = ptr(*p.EstimateUSD)
	default:
		r.EstimateUSD = ptr(*r.EstimateUSD + *p.EstimateUSD)
	}
}

// renderDone reports the run: the render event under --json, else the summary table on
// stdout. It returns the exit code.
func (c *CLI) renderDone(ev renderEvent) int {
	ev.Event = "render"
	if ev.Outputs == nil {
		ev.Outputs = []renderOutput{}
	}
	if ev.Plan != nil && ev.Plan.PriceNote == "" {
		ev.Plan.PriceNote = usage.ListPrices.Label()
	}
	if c.globals.JSON {
		c.event(ev)
		return ev.Exit
	}
	if len(ev.Outputs) == 0 {
		return ev.Exit
	}
	var b strings.Builder
	// The table goes through a tabwriter into a strings.Builder: no write can fail.
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	if ev.DryRun {
		_, _ = fmt.Fprintln(tw, "#\tinput\toutput\tchunks\tcached\taudio\twall time\tresult")
	} else {
		_, _ = fmt.Fprintln(tw, "#\tinput\toutput\texit\tresult")
	}
	for i, o := range ev.Outputs {
		if ev.DryRun {
			chunks, cached, audioLen, wall := "-", "-", "-", "-"
			if o.Plan != nil {
				chunks, cached = strconv.Itoa(o.Plan.Chunks), strconv.Itoa(o.Plan.Cached)
				audioLen, wall = clock(o.Plan.EstSeconds), clock(o.Plan.EstWallSeconds)
			}
			_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", i+1, o.Input, o.Output, chunks, cached, audioLen, wall, outcome(o))
			continue
		}
		code := "-"
		if o.Exit != nil {
			code = strconv.Itoa(*o.Exit)
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", i+1, o.Input, o.Output, code, outcome(o))
	}
	_ = tw.Flush()
	if p := ev.Plan; p != nil {
		price := "unknown (an output has no list price)"
		if p.EstimateUSD != nil {
			price = fmt.Sprintf("$%.4f (%s)", *p.EstimateUSD, p.PriceNote)
		}
		fmt.Fprintf(&b, "\ntotal:      %d chunks, %d cached\n", p.Chunks, p.Cached)
		fmt.Fprintf(&b, "audio:      about %s\n", clock(p.EstSeconds))
		fmt.Fprintf(&b, "wall time:  about %s, outputs one after another\n", clock(p.EstWallSeconds))
		fmt.Fprintf(&b, "price:      %s\n", price)
	}
	fmt.Fprintf(&b, "exit:       %d\n", ev.Exit)
	if _, err := io.WriteString(c.Stdout, logx.Scrub(b.String())); err != nil {
		c.errorf("recite: write output: %v", err)
		return exitcode.Worst(ev.Exit, ExitInternal)
	}
	return ev.Exit
}

// outcome is a row's result in words.
func outcome(o renderOutput) string {
	switch {
	case o.Skipped:
		return "skipped (--fail-fast)"
	case o.Exit == nil:
		return "-"
	case *o.Exit == ExitOK:
		return "ok"
	}
	for _, m := range exitcode.Meanings {
		if m.Code == *o.Exit {
			return m.Meaning
		}
	}
	return "exit " + strconv.Itoa(*o.Exit)
}
