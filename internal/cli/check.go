package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/xiaolai/recite/internal/guard"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/text"
)

// FindingsSchema identifies the `check --json` document (docs/schema/findings.schema.json).
const FindingsSchema = "recite/findings@1"

// CheckReport is the `check` result (CLI-14, design §7.10). Its JSON form is published as
// docs/schema/findings.schema.json; change both together.
type CheckReport struct {
	Schema      string           `json:"schema"`
	Input       string           `json:"input"`
	Policy      string           `json:"guard_policy"`
	Language    string           `json:"language"`
	Findings    []CheckFinding   `json:"findings"`
	Ambiguous   []CheckAmbiguous `json:"ambiguous"`
	RequestLike []string         `json:"request_like"`
	Plan        CheckPlan        `json:"plan"`
	Risk        CheckRisk        `json:"risk"`
	Exit        int              `json:"exit"`
}

// CheckFinding is one guard finding at its line and column (runes) of the input.
type CheckFinding struct {
	Code     string `json:"code"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Severity string `json:"severity"`
	Action   string `json:"action"`
	Before   string `json:"before"`
	After    string `json:"after"`
}

// CheckAmbiguous is an N-AMBIGUOUS span of the display text.
type CheckAmbiguous struct {
	Code   string `json:"code"`
	Rule   string `json:"rule"`
	Line   int    `json:"line"`
	Col    int    `json:"col"`
	Text   string `json:"text"`
	Reason string `json:"reason"`
}

// CheckPlan summarises the chunk plan.
type CheckPlan struct {
	Chunks     int            `json:"chunks"`
	EstSeconds float64        `json:"est_seconds"`
	Items      []CheckChunk   `json:"items"`
	Warnings   []CheckSegWarn `json:"warnings"`
}

// CheckChunk is one planned chunk.
type CheckChunk struct {
	ID       string   `json:"id"`
	EstMS    int64    `json:"est_ms"`
	Boundary string   `json:"boundary"`
	Display  string   `json:"display"`
	Spoken   string   `json:"spoken"`
	Findings []string `json:"findings"`
}

// CheckSegWarn is a segmentation warning (S-SPLIT, S-OVERLONG).
type CheckSegWarn struct {
	Code    string  `json:"code"`
	Chunk   string  `json:"chunk"`
	Seconds float64 `json:"seconds"`
	Message string  `json:"message"`
}

// CheckRisk counts what may need attention before rendering.
type CheckRisk struct {
	Errors          int  `json:"errors"`
	Warnings        int  `json:"warnings"`
	Info            int  `json:"info"`
	Ambiguous       int  `json:"ambiguous"`
	RequestLike     int  `json:"request_like"`
	Split           int  `json:"split"`
	Overlong        int  `json:"overlong"`
	StrictWouldFail bool `json:"strict_would_fail"`
}

func setupCheck(fs *flag.FlagSet, c *CLI) func([]string) int {
	var tf textFlags
	tf.register(fs)
	speed := fs.String("speed", "1.0", "`SPEED` the estimates assume, 0.25 to 1.5")
	return func(args []string) int {
		if len(args) != 1 {
			c.errorf("recite check: takes exactly one FILE (- for stdin)")
			return ExitUsage
		}
		pre, code := c.prereadMarkdown("recite check", &tf, text.Source{File: args[0], Stdin: c.Stdin, StdinIsTTY: c.StdinIsTTY()}, args[0])
		if code != ExitOK {
			return code
		}
		g, so, st, err := c.textSettings(fs, &tf, pre.frontMatter())
		if err != nil {
			c.errorf("recite check: %v", err)
			return ExitUsage
		}
		sp, err := realtime.ParseSpeed(*speed)
		if err != nil {
			c.errorf("recite check: %v", err)
			return ExitUsage
		}
		format, marks, err := tf.inputMode(args[0])
		if err != nil {
			c.errorf("recite check: %v", err)
			return ExitUsage
		}
		g.Marks = marks
		g.Markdown = format == formatMarkdown
		md, err := tf.markdownOptions()
		if err != nil {
			c.errorf("recite check: %v", err)
			return ExitUsage
		}
		in := textInput{format: format, markdown: md, lang: st.Get(langKey).Raw, preread: pre}
		return c.runCheck(args[0], tf, in, g, so, sp)
	}
}

// runCheck is `check` (CLI-14): parse, guard, spoken form and segmentation, with no network
// and no credentials. The report goes to stdout; the exit code says whether `say` with the
// same flags would stop at the text stage (2) or go on (0).
func (c *CLI) runCheck(file string, tf textFlags, in textInput, g guard.Options, so spoken.Options, speed float64) int {
	input, code := c.readInput("recite check", in, text.Source{File: file, Stdin: c.Stdin, StdinIsTTY: c.StdinIsTTY()}, tf.maxInput)
	if code != ExitOK {
		return code
	}
	var rep CheckReport
	var err error
	if in.format == formatSpoken {
		rep, err = buildSpokenCheckReport(input, g, so, speed)
	} else {
		var doc textDoc
		doc, err = speechDoc(input, in)
		c.skippedWarning("recite check", doc.skipped)
		if err == nil {
			rep, err = buildCheckReport(doc, g, so, tf.chunkSeconds, speed)
		}
	}
	if err != nil {
		c.errorf("recite check: %v", err)
		return textErrorCode(err)
	}
	rep.Input = file
	if c.globals.JSON {
		b, err := json.Marshal(rep)
		if err != nil {
			c.errorf("recite check: encode: %v", err)
			return ExitInternal
		}
		if code := c.writeResult(writeLine(c.Stdout, b)); code != ExitOK {
			return code
		}
		return rep.Exit
	}
	if code := c.writeResult(writeCheckText(c.Stdout, rep)); code != ExitOK {
		return code
	}
	return rep.Exit
}

func writeLine(w io.Writer, b []byte) error {
	_, err := w.Write(append(b, '\n'))
	return err
}

// textStages is the outcome of the text stages of `say` on plain text: the guard, the spoken
// form and the plan. failed says `say` would stop at the text stage (exit 2); form and plan
// are set only when there is something speakable to plan.
type textStages struct {
	guard   guard.Result
	failed  bool
	lang    string
	form    spoken.Result
	planned bool
	plan    pipeline.Plan
	seg     text.Segmentation
}

// runTextStages runs guard → spoken form → segmentation with no network, as `say` would.
func runTextStages(input string, g guard.Options, so spoken.Options, budget float64, settings realtime.Settings) (textStages, error) {
	return runDocStages(plainDoc(input), g, so, budget, settings)
}

// runDocStages is runTextStages for a text document (plain text or Markdown speech text).
func runDocStages(doc textDoc, g guard.Options, so spoken.Options, budget float64, settings realtime.Settings) (textStages, error) {
	var st textStages
	gr, gerr := guard.Run(doc.text, g)
	var ge *guard.Error
	if gerr != nil && !errors.As(gerr, &ge) {
		return st, gerr
	}
	st.guard, st.failed = gr, ge != nil
	if !guard.Speakable(gr.Text) {
		return st, nil
	}
	if err := checkPauses(gr); err != nil {
		return st, err
	}
	lang := text.Resolve(gr.Text, text.ResolveOptions{FrontMatter: doc.lang})
	st.lang = lang.Code
	form, err := spoken.Normalize(gr.Text, lang.Code, markOptions(gr, so, markReader(lang.Code, so.DateStyle)))
	if err != nil {
		return st, err
	}
	st.form = form
	if len(form.Findings) > 0 && g.Policy == guard.PolicyStrict {
		st.failed = true
	}
	st.plan, st.seg, err = planDoc(doc, gr, form, budget, settings)
	if err != nil {
		return st, err
	}
	st.planned = true
	return st, nil
}

// buildCheckReport runs the text stages of `say` on doc and reports on them.
func buildCheckReport(doc textDoc, g guard.Options, so spoken.Options, budget, speed float64) (CheckReport, error) {
	if g.Policy == "" {
		g.Policy = guard.PolicyFix
	}
	rep := newCheckReport(g.Policy)
	settings := realtime.DefaultSettings()
	settings.Speed = speed
	st, err := runDocStages(doc, g, so, budget, settings)
	if err != nil {
		return rep, err
	}
	for _, f := range st.guard.Findings {
		rep.Findings = append(rep.Findings, CheckFinding{Code: string(f.Code), Line: f.Line, Col: f.Col,
			Severity: f.Severity.String(), Action: string(f.Action), Before: f.Before, After: f.After})
		switch f.Severity {
		case guard.Fatal:
			rep.Risk.Errors++
		case guard.Warn:
			rep.Risk.Warnings++
		default:
			rep.Risk.Info++
		}
	}
	strict := g
	strict.Policy = guard.PolicyStrict
	_, serr := guard.Run(doc.text, strict)
	rep.Risk.StrictWouldFail = serr != nil
	failed := st.failed
	if st.planned {
		rep.Language = st.lang
		for _, f := range st.form.Findings {
			rep.Ambiguous = append(rep.Ambiguous, CheckAmbiguous{Code: f.Code, Rule: string(f.Rule), Line: f.Line, Col: f.Col, Text: f.Text, Reason: f.Reason})
		}
		rep.Risk.Ambiguous = len(st.form.Findings)
		if len(st.form.Findings) > 0 {
			rep.Risk.StrictWouldFail = true
		}
		fillPlan(&rep, st.plan, st.seg)
	}
	if failed {
		rep.Exit = ExitUsage
	}
	return rep, nil
}

func newCheckReport(p guard.Policy) CheckReport {
	return CheckReport{Schema: FindingsSchema, Policy: string(p), Findings: []CheckFinding{}, Ambiguous: []CheckAmbiguous{},
		RequestLike: []string{}, Plan: CheckPlan{Items: []CheckChunk{}, Warnings: []CheckSegWarn{}}}
}

// buildSpokenCheckReport reports on a spoken script (TXT-15): there is no guard and no
// spoken-form stage, so the report is its plan.
func buildSpokenCheckReport(input string, g guard.Options, so spoken.Options, speed float64) (CheckReport, error) {
	if g.Policy == "" {
		g.Policy = guard.PolicyFix
	}
	rep := newCheckReport(g.Policy)
	settings := realtime.DefaultSettings()
	settings.Speed = speed
	plan, doc, err := spokenScriptPlan(input, g.Marks, so, planSettings(settings), speed)
	if err != nil {
		return rep, err
	}
	rep.Language = text.Resolve(scriptText(input), text.ResolveOptions{}).Code
	fillPlan(&rep, plan, doc.Seg)
	return rep, nil
}

func fillPlan(rep *CheckReport, plan pipeline.Plan, seg text.Segmentation) {
	var ms int64
	for _, ch := range plan.Chunks {
		codes := []string{}
		request := false
		for _, f := range ch.Findings {
			codes = append(codes, f.Code)
			request = request || f.Code == string(guard.Request)
		}
		if request {
			rep.RequestLike = append(rep.RequestLike, ch.ID)
		}
		ms += ch.EstMillis
		rep.Plan.Items = append(rep.Plan.Items, CheckChunk{ID: ch.ID, EstMS: ch.EstMillis, Boundary: string(ch.Boundary),
			Display: ch.Display, Spoken: ch.Spoken, Findings: codes})
	}
	for _, w := range seg.Warnings {
		rep.Plan.Warnings = append(rep.Plan.Warnings, CheckSegWarn{Code: w.Code, Chunk: plan.Chunks[w.Chunk].ID,
			Seconds: math.Round(w.Seconds*10) / 10, Message: w.Msg})
		switch w.Code {
		case "S-SPLIT":
			rep.Risk.Split++
		case "S-OVERLONG":
			rep.Risk.Overlong++
		}
	}
	rep.Plan.Chunks = len(plan.Chunks)
	rep.Plan.EstSeconds = math.Round(float64(ms)/100) / 10
	rep.Risk.RequestLike = len(rep.RequestLike)
}

// writeCheckText is the human form of the report.
func writeCheckText(w io.Writer, rep CheckReport) error {
	var b strings.Builder
	b.WriteString("findings:\n")
	if len(rep.Findings) == 0 {
		b.WriteString("  none\n")
	}
	for _, f := range rep.Findings {
		fmt.Fprintf(&b, "  %s\n", guard.Finding{Code: guard.Code(f.Code), Line: f.Line, Col: f.Col, Action: guard.Action(f.Action), Before: f.Before, After: f.After})
	}
	b.WriteString("ambiguous spans:\n")
	if len(rep.Ambiguous) == 0 {
		b.WriteString("  none\n")
	}
	for _, a := range rep.Ambiguous {
		fmt.Fprintf(&b, "  %s\n", spoken.Finding{Code: a.Code, Rule: spoken.RuleID(a.Rule), Line: a.Line, Col: a.Col, Text: a.Text, Reason: a.Reason})
	}
	fmt.Fprintf(&b, "request-like chunks: %s\n", listOrNone(rep.RequestLike))
	fmt.Fprintf(&b, "plan: %d chunk(s), about %.1f s of speech\n", rep.Plan.Chunks, rep.Plan.EstSeconds)
	for _, ch := range rep.Plan.Items {
		fmt.Fprintf(&b, "  %s  %5.1f s  %-9s  %s\n", ch.ID, float64(ch.EstMS)/1000, ch.Boundary, preview(ch.Display))
	}
	for _, w := range rep.Plan.Warnings {
		fmt.Fprintf(&b, "  %s %s: %s\n", w.Chunk, w.Code, w.Message)
	}
	r := rep.Risk
	fmt.Fprintf(&b, "risk: %d error(s), %d warning(s), %d info, %d ambiguous, %d request-like, %d split, %d overlong; --guard strict would %s\n",
		r.Errors, r.Warnings, r.Info, r.Ambiguous, r.RequestLike, r.Split, r.Overlong, map[bool]string{true: "fail", false: "pass"}[r.StrictWouldFail])
	_, err := io.WriteString(w, b.String())
	return err
}

func listOrNone(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

// preview is the chunk's display text on one line, shortened.
func preview(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 60
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return fmt.Sprintf("%q", s)
}
