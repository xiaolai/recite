package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/xiaolai/recite/internal/config"
	"github.com/xiaolai/recite/internal/pipeline"
	"github.com/xiaolai/recite/internal/realtime"
	"github.com/xiaolai/recite/internal/spoken"
	"github.com/xiaolai/recite/internal/text"
)

// voicesKey is the front-matter speaker map of a dialogue script (TXT-06, TXT-08):
// NAME = "voice" or NAME = { voice = "…", style = "…" }.
const voicesKey = "voices"

// speakerVoice is one speaker of the map.
type speakerVoice struct {
	Voice string `json:"voice"`
	Style string `json:"style,omitempty"`
}

// speakerMap is the parsed [voices] table.
type speakerMap map[string]speakerVoice

// names are the speakers in sorted order.
func (m speakerMap) names() []string {
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// parseVoices validates a [voices] table: valid speaker names, known voices, one-line styles.
func parseVoices(t map[string]any) (speakerMap, error) {
	out := speakerMap{}
	var errs []error
	names := make([]string, 0, len(t))
	for n := range t {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, name := range names {
		if !text.ValidSpeakerName(name) {
			errs = append(errs, fmt.Errorf("speaker %q: want 1 to %d letters, digits, '_', '-' or '.', with no spaces", name, text.MaxSpeakerName))
			continue
		}
		var sv speakerVoice
		switch v := t[name].(type) {
		case string:
			sv.Voice = v
		case map[string]any:
			for _, k := range sortedFieldNames(v) {
				s, ok := v[k].(string)
				if !ok {
					errs = append(errs, fmt.Errorf("speaker %q: %s must be a string", name, k))
					continue
				}
				switch k {
				case "voice":
					sv.Voice = s
				case "style":
					sv.Style = s
				default:
					errs = append(errs, fmt.Errorf("speaker %q: unknown field %q (fields: voice, style)", name, k))
				}
			}
		default:
			errs = append(errs, fmt.Errorf("speaker %q: want a voice such as \"cedar\" or a table { voice = \"cedar\", style = \"…\" }", name))
			continue
		}
		var err error
		if sv.Voice, err = realtime.ParseVoice(sv.Voice); err != nil {
			errs = append(errs, fmt.Errorf("speaker %q: %w", name, err))
			continue
		}
		if sv.Style, err = realtime.ParseStyle(sv.Style); err != nil {
			errs = append(errs, fmt.Errorf("speaker %q: %w", name, err))
			continue
		}
		out[name] = sv
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

func sortedFieldNames(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// encodeVoices is the canonical text of a [voices] table: JSON with sorted keys.
func encodeVoices(t map[string]any) (string, error) {
	m, err := parseVoices(t)
	if err != nil {
		return "", err
	}
	if len(m) == 0 {
		return "", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("voices: encode: %w", err)
	}
	return string(b), nil
}

// parseVoicesText reads the canonical text; "" is no speakers.
func parseVoicesText(s string) (speakerMap, error) {
	if s == "" {
		return nil, nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("voices: %w", err)
	}
	return parseVoices(raw)
}

// voicesOf is the effective speaker map of resolved settings.
func voicesOf(st config.Settings) (speakerMap, error) {
	v := st.Get(voicesKey)
	m, err := parseVoicesText(v.Raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", v.Source(), err)
	}
	return m, nil
}

// scriptDoc reads a dialogue script (TXT-06): its front matter is a settings layer already
// read, and its lines become one paragraph each, labels and directives removed.
func scriptDoc(input string, in textInput) (textDoc, error) {
	fm, body, err := text.SplitFrontMatter(input)
	if err != nil {
		return textDoc{}, err
	}
	first := 1
	if fm != nil {
		first = fm.Line + strings.Count(fm.TOML, "\n") + 1
	}
	d, err := text.ParseDialogue(body, first, in.voices.names())
	if err != nil {
		return textDoc{}, err
	}
	return textDoc{text: d.Text, breaks: d.Breaks, lang: in.lang, directed: &d, voices: in.voices}, nil
}

// directedDoc takes the directives out of a text or Markdown document (TXT-07, --directives),
// moving its block breaks to the new text.
func directedDoc(doc textDoc) (textDoc, error) {
	d, err := text.ExtractDirectives(doc.text)
	if err != nil {
		return textDoc{}, err
	}
	out := doc
	out.text, out.directed = d.Text, &d
	if doc.breaks != nil {
		out.breaks = make([]text.Break, 0, len(doc.breaks)+len(d.Breaks))
		for _, b := range doc.breaks {
			out.breaks = append(out.breaks, text.Break{Display: d.Offset(b.Display), Kind: b.Kind})
		}
		out.breaks = append(out.breaks, d.Breaks...)
	}
	if strings.TrimSpace(out.text) == "" {
		return out, text.ErrNothingToSpeak
	}
	return out, nil
}

// directedInput is the planner's view of a document's directives, in display coordinates
// (through the guard's edits): the voice of each speaker or direction span, and the sections.
func directedInput(doc textDoc, voices speakerMap, runStyle string, display func(int) int) ([]pipeline.VoiceSpan, []pipeline.SectionStart) {
	if doc.directed == nil {
		return nil, nil
	}
	var vs []pipeline.VoiceSpan
	for _, sp := range doc.directed.Spans {
		v := pipeline.VoiceSpan{Display: spoken.Range{Start: display(sp.Start), End: display(sp.End)}, Speaker: sp.Speaker}
		var styles []string
		if runStyle != "" {
			styles = append(styles, runStyle)
		}
		if sp.Speaker != "" {
			sv := voices[sp.Speaker]
			v.Voice = sv.Voice
			if sv.Style != "" {
				styles = append(styles, sv.Style)
			}
		}
		if sp.Direction != "" {
			styles = append(styles, sp.Direction)
		}
		v.Style = strings.Join(styles, "; ")
		vs = append(vs, v)
	}
	var ss []pipeline.SectionStart
	for _, sc := range doc.directed.Sections {
		ss = append(ss, pipeline.SectionStart{At: display(sc.At), Title: sc.Title})
	}
	return vs, ss
}
