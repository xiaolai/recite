package guard

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// G-REQUEST (TXT-14) flags sentences that read like a request to the model.
// It never changes text: a false flag costs at most one extra render attempt,
// so the patterns favour recall.
var (
	reIgnore     = regexp.MustCompile(`(?i)\b(ignore|disregard|forget)\b.{0,60}?\b(instructions?|rules|prompts?|directions|guidelines|everything)\b`)
	reYou        = regexp.MustCompile(`(?i)\byou(?:r|rs|rself)?\b`)
	reModalYou   = regexp.MustCompile(`(?i)\b(?:can|could|would|will|won't|do|did|are|have|should)\s+you\b`)
	reWantYou    = regexp.MustCompile(`(?i)\b(?:want|need|like)\s+you\s+to\b`)
	reFirstWord  = regexp.MustCompile(`^[\p{L}']+`)
	reZhRequest  = regexp.MustCompile(`帮我|帮忙|请你|请帮|麻烦你|你(?:能|可以|会不会|会|知道|觉得|认为|帮|给我|来|说说|告诉|写|讲|解释|翻译|是谁|叫什么|有没有|愿意|怎么看)|忽略.{0,20}(?:指令|指示|说明|规则)`)
	reZhOpening  = regexp.MustCompile(`^(?:请|告诉我|给我|写一|讲一|翻译|解释|总结|列出|帮|说说|介绍一下|推荐)`)
	reZhQuestion = regexp.MustCompile(`你.*[吗呢？?]$`)
)

// imperativeVerbs open a sentence addressed to an assistant.
var imperativeVerbs = map[string]bool{
	"tell": true, "write": true, "give": true, "explain": true, "describe": true, "translate": true,
	"summarize": true, "summarise": true, "list": true, "show": true, "help": true, "answer": true,
	"make": true, "create": true, "generate": true, "draft": true, "compose": true, "sing": true,
	"act": true, "pretend": true, "imagine": true, "ignore": true, "forget": true, "disregard": true,
	"respond": true, "reply": true, "calculate": true, "compute": true, "find": true, "recommend": true,
	"suggest": true, "define": true, "convert": true, "rewrite": true, "fix": true, "check": true,
	"remind": true, "book": true, "schedule": true, "please": true, "let's": true, "repeat": true,
	"stop": true, "continue": true, "send": true, "search": true, "look": true, "say": true,
}

// ignoreRequest is reIgnore with both keywords whole Unicode words: its inner \b, like every
// \b, knows only ASCII letters ("Ignoreé" is not "ignore").
func ignoreRequest(s string) bool {
	for _, loc := range reIgnore.FindAllStringSubmatchIndex(s, -1) {
		if bounded(s, loc[2], loc[3]) && bounded(s, loc[4], loc[5]) {
			return true
		}
	}
	return false
}

// RequestLike reports whether sentence reads like a request to the model:
// a question to "you", an imperative opening, "ignore … instructions", or a
// Chinese 请/帮我/你 + verb pattern.
func RequestLike(sentence string) bool {
	s := strings.TrimFunc(sentence, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) && !strings.ContainsRune("?？'", r)
	})
	if s == "" {
		return false
	}
	if ignoreRequest(s) || matchBounded(reModalYou, s) || matchBounded(reWantYou, s) {
		return true
	}
	if last, _ := utf8.DecodeLastRuneInString(s); (last == '?' || last == '？') && matchBounded(reYou, s) {
		return true
	}
	if w := reFirstWord.FindString(s); imperativeVerbs[strings.ToLower(w)] {
		return true
	}
	return reZhRequest.MatchString(s) || reZhOpening.MatchString(s) || reZhQuestion.MatchString(s)
}

// sentences splits s into sentence ranges at . ! ? 。！？ and newlines.
func sentences(s string) [][2]int {
	var out [][2]int
	start := 0
	for i, r := range s {
		if strings.ContainsRune(".!?。！？\n", r) {
			end := i + utf8.RuneLen(r)
			// "3.5" and "v1.2" do not end a sentence.
			if r == '.' && end < len(s) {
				if next, _ := utf8.DecodeRuneInString(s[end:]); !unicode.IsSpace(next) {
					continue
				}
			}
			out = append(out, [2]int{start, end})
			start = end
		}
	}
	if start < len(s) {
		out = append(out, [2]int{start, len(s)})
	}
	return out
}

func findRequest(s string) []match {
	var ms []match
	for _, sp := range sentences(s) {
		text := s[sp[0]:sp[1]]
		trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
		if !RequestLike(trimmed) {
			continue
		}
		start := sp[0] + len(text) - len(trimmed)
		end := start + len(strings.TrimRightFunc(trimmed, unicode.IsSpace))
		ms = append(ms, mk(Request, ActFlag, start, end, "", rules[Request].sev))
	}
	return ms
}
