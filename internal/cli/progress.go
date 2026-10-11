package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/verify"
)

// progressInterval is how often the plain (non-TTY) progress line is printed.
const progressInterval = 10 * time.Second

// eraseLine is the ANSI "erase to end of line" sequence the TTY line uses.
const eraseLine = "\x1b[K"

// progress is the stderr progress of `say` (CLI-07, design §14.3): one updating line on a
// TTY, a plain line every progressInterval otherwise. ANSI codes are used only on a TTY with
// colour allowed; without them the line is overwritten with a carriage return and padding.
type progress struct {
	w           io.Writer
	tty, ansi   bool
	now         func() time.Time
	start, last time.Time
	total       int
	estTotal    float64 // estimated seconds of every chunk
	width       int     // length of the last TTY line, for padding without ANSI
	printed     bool

	done, verified, retried, failed int
	audioS, estDone                 float64
}

// newProgress returns the progress of a run of total chunks, or nil when it is off (-q).
func (c *CLI) newProgress(total int, estTotal float64) *progress {
	if c.globals.Quiet {
		return nil
	}
	tty := c.StderrIsTTY()
	now := c.Now()
	return &progress{w: c.Stderr, tty: tty, ansi: tty && c.colorAllowed(), now: c.Now,
		start: now, last: now, total: total, estTotal: estTotal}
}

// colorAllowed reports whether ANSI codes may be written: not under --no-color or NO_COLOR
// (no-color.org: any non-empty value).
func (c *CLI) colorAllowed() bool {
	return !c.globals.NoColor && c.Getenv("NO_COLOR") == ""
}

// chunk records a delivered chunk whose estimated length is est seconds.
func (p *progress) chunk(r verify.ChunkResult, est float64) {
	if p == nil {
		return
	}
	p.done++
	p.estDone += est
	p.audioS += float64(len(r.PCM)) / audio.BytesPerSecond
	if len(r.Attempts) > 1 {
		p.retried++
	}
	if r.State == verify.StateVerified {
		p.verified++
	} else {
		p.failed++
	}
	now := p.now()
	switch {
	case p.tty:
		p.draw(p.line(now))
	case now.Sub(p.last) >= progressInterval:
		p.last = now
		_, _ = io.WriteString(p.w, p.line(now)+"\n")
	}
}

// finish ends the TTY line so later output starts on a fresh line.
func (p *progress) finish() {
	if p != nil && p.tty && p.printed {
		_, _ = io.WriteString(p.w, "\n")
	}
}

func (p *progress) draw(line string) {
	pad := ""
	if p.ansi {
		pad = eraseLine
	} else if n := p.width - len(line); n > 0 {
		pad = strings.Repeat(" ", n)
	}
	p.width, p.printed = len(line), true
	_, _ = io.WriteString(p.w, "\r"+line+pad)
}

// line is the progress text: chunks done/total, verified, retried, failed, audio produced,
// real-time factor and ETA (design §14.3). The factor and ETA need elapsed time.
func (p *progress) line(now time.Time) string {
	s := fmt.Sprintf("recite: chunks %d/%d, verified %d, retried %d, failed %d, audio %s",
		p.done, p.total, p.verified, p.retried, p.failed, clock(p.audioS))
	elapsed := now.Sub(p.start).Seconds()
	if elapsed <= 0 || p.audioS <= 0 {
		return s
	}
	rtf := p.audioS / elapsed
	s += fmt.Sprintf(", %.1fx real time", rtf)
	if left := p.estTotal - p.estDone; left > 0 {
		s += ", ETA " + clock(left/rtf)
	}
	return s
}

// clock formats seconds as 1h02m03s, 2m03s or 3s.
func clock(sec float64) string {
	d := time.Duration(sec * float64(time.Second)).Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// stderrIsTTY reports whether w is the process's stderr and a terminal.
func stderrIsTTY(w io.Writer) bool {
	if w != io.Writer(os.Stderr) {
		return false
	}
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
