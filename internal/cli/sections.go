package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaolai/recite/internal/audio"
	"github.com/xiaolai/recite/internal/manifest"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/subtitle"
	"github.com/xiaolai/recite/internal/text"
)

// section is one Markdown heading and the chunks it covers: first is the heading's first
// chunk, last the section's last chunk. The text before the first heading belongs to no
// section: it has no chapter, and split output puts it in the first file.
type section struct {
	title       string
	first, last int
}

// sectionsOf finds the sections of a plan (OUT-04): a heading is always its own block, so it
// starts a chunk, and every chunk after a section break starts one; the heading's title is
// the display text of the chunks up to its first block break (a long heading may be cut).
func sectionsOf(chunks []pipeline.Chunk, headingFirst bool) []section {
	var out []section
	for i := range chunks {
		starts := headingFirst
		if i > 0 {
			starts = chunks[i-1].Boundary == text.BoundarySection
		}
		if !starts {
			continue
		}
		if n := len(out); n > 0 {
			out[n-1].last = i - 1
		}
		var title []string
		for k := i; k < len(chunks); k++ {
			title = append(title, strings.TrimSpace(chunks[k].Display))
			if !inSentence(chunks[k].Boundary) {
				break
			}
		}
		out = append(out, section{title: strings.Join(title, " "), first: i, last: len(chunks) - 1})
	}
	return out
}

// inSentence reports a cut that leaves its block unfinished.
func inSentence(b text.Boundary) bool {
	switch b {
	case text.BoundaryChar, text.BoundaryWord, text.BoundaryClause, text.BoundarySentence:
		return true
	}
	return false
}

// manifestSections are the sections as the manifest records them, with no file paths.
func manifestSections(secs []section) []manifest.Section {
	out := make([]manifest.Section, len(secs))
	for i, s := range secs {
		out[i] = manifest.Section{Title: s.title, First: s.first}
	}
	return out
}

// chaptersFlag is --chapters: unset means on for m4a and m4b when there are sections.
type chaptersFlag struct{ set, on bool }

func (f *chaptersFlag) String() string   { return fmt.Sprint(f.on) }
func (f *chaptersFlag) IsBoolFlag() bool { return true }
func (f *chaptersFlag) Set(s string) error {
	switch strings.ToLower(s) {
	case "true", "1", "on":
		f.on = true
	case "false", "0", "off":
		f.on = false
	default:
		return fmt.Errorf("want true or false, not %q", s)
	}
	f.set = true
	return nil
}

// extras are the outputs beyond the audio, decided before the network: subtitles, chapter
// markers and split files (OUT-03, OUT-04, OUT-05).
type extras struct {
	sections  []section
	subtitles subtitle.Format // "" writes none
	chapters  bool            // markers in the m4a/m4b
	split     bool            // target is a directory of one file per section
	ext       string          // the audio extension of split files
}

// planExtras checks the subtitle, chapter and split settings against the output and the
// sections, before any request. A non-zero code has been reported.
func (c *CLI) planExtras(req SayRequest, target string, enc audio.Encoding, plan pipeline.Plan, doc textDoc) (extras, int) {
	x := extras{sections: sectionsOf(plan.Chunks, doc.headingFirst), subtitles: req.Subtitles, split: req.SplitSections, ext: enc.Format.Ext()}
	fail := func(format string, a ...any) (extras, int) {
		c.errorf("%s: "+format, append([]any{req.prefix()}, a...)...)
		return extras{}, ExitUsage
	}
	stream := target == stdoutTarget
	switch {
	case stream && x.subtitles != "":
		return fail("-o - and --subtitles exclude each other: subtitles are written next to an output file")
	case stream && x.split:
		return fail("-o - and --split-sections exclude each other: split output is a directory of files")
	case x.split && len(x.sections) == 0:
		return fail("--split-sections needs sections: Markdown headings")
	case x.split && req.Chapters.set && req.Chapters.on:
		return fail("--split-sections and --chapters exclude each other: each file is one section")
	}
	if req.Chapters.set && req.Chapters.on {
		switch {
		case stream:
			return fail("-o - and --chapters exclude each other: chapters are written once the timeline is known")
		case !audio.ChapterFormat(enc.Format):
			return fail("--chapters needs m4a or m4b output, not %s; the manifest lists the sections for every format", enc.Format)
		case enc.Tool != audio.ToolFFmpeg:
			return fail("--chapters needs ffmpeg, which is not on PATH; install ffmpeg (e.g. `brew install ffmpeg` or your package manager)")
		}
	}
	on := req.Chapters.on || !req.Chapters.set
	x.chapters = on && !stream && !x.split && audio.ChapterFormat(enc.Format) && len(x.sections) > 0
	if x.chapters && enc.Tool != audio.ToolFFmpeg {
		c.Logger.Warn("chapter markers skipped: they need ffmpeg; the manifest still lists the sections", "encoder", enc.Tool)
		x.chapters = false
	}
	if req.Chapters.set && req.Chapters.on && len(x.sections) == 0 {
		c.Logger.Warn("--chapters: the input has no sections (Markdown headings); no chapter markers")
	}
	return x, ExitOK
}

// subtitlePath is the subtitle file next to the audio at path.
func subtitlePath(path string, f subtitle.Format) string {
	return strings.TrimSuffix(path, filepath.Ext(path)) + f.Ext()
}

// splitFiles are the audio paths of split output, in section order.
func (x extras) splitFiles(dir string) []string {
	out := make([]string, len(x.sections))
	for i, s := range x.sections {
		out[i] = filepath.Join(dir, subtitle.FileName(i, len(x.sections), s.title, x.ext))
	}
	return out
}

// preflightExtras fails before the network when a subtitle or split file exists and --force is
// absent, or when the split directory is not a directory.
func (x extras) preflight(target string, force bool) error {
	paths := []string{target}
	if x.split {
		fi, err := os.Stat(target)
		switch {
		case err == nil && !fi.IsDir():
			return usagef("--split-sections: %s is not a directory", target)
		case errors.Is(err, fs.ErrNotExist):
			if fi, err := os.Stat(filepath.Dir(target)); err != nil || !fi.IsDir() {
				return usagef("output directory %s does not exist", filepath.Dir(target))
			}
			return nil // created at publication; nothing in it can exist yet
		case err != nil:
			return fmt.Errorf("check output %s: %w", target, err)
		}
		paths = x.splitFiles(target)
		for _, p := range paths {
			if err := preflight(p, force); err != nil {
				return err
			}
		}
	}
	if x.subtitles != "" {
		for _, p := range paths {
			if err := preflight(subtitlePath(p, x.subtitles), force); err != nil {
				return err
			}
		}
	}
	return nil
}

// chapterMarkers are the sections on the assembly timeline: each chapter runs from its
// heading to the next heading, the last one to the end. Text before the first heading gets a
// lead-in chapter titled from its first sentence, as an MP4 chapter track starts at zero.
func chapterMarkers(secs []section, chunks []pipeline.Chunk, spans []audio.Span) []subtitle.Chapter {
	var out []subtitle.Chapter
	if len(secs) > 0 && secs[0].first > 0 {
		out = append(out, subtitle.Chapter{Title: subtitle.LeadIn(chunks[0].Display), End: sampleTime(spans[secs[0].first].Start)})
	}
	for i, s := range secs {
		end := spans[len(spans)-1].End
		if i+1 < len(secs) {
			end = spans[secs[i+1].first].Start
		}
		out = append(out, subtitle.Chapter{Title: s.title, Start: sampleTime(spans[s.first].Start), End: sampleTime(end)})
	}
	return out
}

func sampleTime(samples int64) time.Duration {
	return time.Duration(samples * int64(time.Second) / audio.SampleRate)
}

// cuesOf times the cues of chunks [from, to] in assembly coordinates.
func cuesOf(chunks []pipeline.Chunk, spans []audio.Span, from, to int) ([]subtitle.Cue, error) {
	in := make([]subtitle.Chunk, 0, to-from+1)
	for i := from; i <= to; i++ {
		in = append(in, subtitle.Chunk{Text: chunks[i].Display, Start: spans[i].Start, End: spans[i].End})
	}
	return subtitle.Cues(in, audio.SampleRate)
}

// publishAudio writes the assembled audio and its extras: one file (with chapter markers when
// on) or one file per section, and the subtitles of each. It returns the manifest sections.
func (c *CLI) publishAudio(ctx context.Context, req SayRequest, job sayJob, pcm audio.PCM, spans []audio.Span) ([]manifest.Section, error) {
	x := job.extras
	write := func(path string, enc audio.Encoding, p audio.PCM) error {
		return publishOutput(path, req.Force, func(f *os.File) error {
			if enc.Native() {
				return audio.Write(f, enc.Format, p)
			}
			return audio.EncodeFile(ctx, enc, p, f.Name())
		})
	}
	subs := func(path string, from, to int, offset int64) error {
		if x.subtitles == "" {
			return nil
		}
		cues, err := cuesOf(job.plan.Chunks, spans, from, to)
		if err != nil {
			return err
		}
		cues = subtitle.Shift(cues, sampleTime(offset))
		return publishOutput(subtitlePath(path, x.subtitles), req.Force, func(f *os.File) error {
			return subtitle.Write(f, x.subtitles, cues)
		})
	}
	secs := manifestSections(x.sections)
	if !x.split {
		enc := job.enc
		if x.chapters {
			enc.Chapters = subtitle.FFMetadata(chapterMarkers(x.sections, job.plan.Chunks, spans))
		}
		if err := write(job.target, enc, pcm); err != nil {
			return nil, err
		}
		return secs, subs(job.target, 0, len(spans)-1, 0)
	}
	if err := os.Mkdir(job.target, 0o777); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("create output directory: %w", err)
	}
	for i, path := range x.splitFiles(job.target) {
		s := x.sections[i]
		// The first file also holds the text before the first heading; a file ends with its
		// last chunk, without the gap that follows.
		from := 0
		if i > 0 {
			from = s.first
		}
		start, end := spans[from].Start, spans[s.last].End
		if err := write(path, job.enc, pcm[start*audio.BytesPerSample:end*audio.BytesPerSample]); err != nil {
			return nil, err
		}
		if err := subs(path, from, s.last, start); err != nil {
			return nil, err
		}
		secs[i].Path = path
		c.Logger.Info("wrote section", "path", path, "seconds", sampleTime(end-start).Seconds())
	}
	return secs, nil
}
