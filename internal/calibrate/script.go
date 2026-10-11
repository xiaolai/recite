package calibrate

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Script is the writing system of a calibration corpus (VER-11 calibrates Latin and Han).
type Script string

// Scripts.
const (
	Latin Script = "latin" // words separated by spaces, as written
	Han   Script = "han"   // passages segmented into words by spaces; see Unsegment
)

// ParseScript reads a script name.
func ParseScript(s string) (Script, error) {
	switch sc := Script(strings.ToLower(strings.TrimSpace(s))); sc {
	case Latin, Han:
		return sc, nil
	}
	return "", fmt.Errorf("calibrate: script %q: want latin or han", s)
}

// Language is the language code of the script's corpus, for the spoken-form normaliser.
func (s Script) Language() string {
	if s == Han {
		return "zh"
	}
	return "en"
}

// text is the passage as it is rendered and verified: Latin passages as written, Han passages
// with their word-segmenting spaces removed.
func (s Script) text(passage string) string {
	if s == Han {
		return Unsegment(passage)
	}
	return passage
}

// Unsegment joins the space-separated words of a segmented passage as Chinese is written: a
// space survives only between two ASCII letters or digits ("GPS 定位" keeps none, "3 km" keeps
// its space).
func Unsegment(passage string) string {
	var b strings.Builder
	prev := rune(0)
	for i, w := range strings.Fields(passage) {
		first := []rune(w)[0]
		if i > 0 && asciiAlnum(prev) && asciiAlnum(first) {
			b.WriteByte(' ')
		}
		b.WriteString(w)
		r := []rune(w)
		prev = r[len(r)-1]
	}
	return b.String()
}

func asciiAlnum(r rune) bool {
	return r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r))
}

// CorruptScript applies corruption k to a passage of script sc, choosing among the possible
// edits with rng, and returns the edited text as it is rendered (a Han result is unsegmented).
// It returns ErrNotApplicable when the passage offers no edit of that kind.
func CorruptScript(sc Script, passage string, k Kind, rng *rand.Rand) (string, error) {
	switch sc {
	case Latin:
		return Corrupt(passage, k, rng)
	case Han:
		return corruptHan(passage, k, rng)
	}
	return "", fmt.Errorf("calibrate: unknown script %q", sc)
}

var (
	hanPreambles = []string{"好的，下面 是 这 段 文字。", "当然，内容 如下。", "没问题，我 来 读 这 段 话。"}
	hanNumbers   = []string{"三", "五", "七", "九", "十", "十一", "十二", "二十", "三十", "四十", "五十", "六十", "八十", "一百"}
	// The people and places of passages-zh.txt; a name changes into another of its own kind.
	hanPeople = []string{"林秋禾", "周慕远", "沈青梧", "陈守义", "许晚晴", "唐知秋", "赵云舒", "顾长庚", "何小满", "苏念安",
		"叶听澜", "方立春", "宋怀瑾", "孟冬生", "韩书言", "杜若溪", "吴启明", "钱半夏", "郑远山"}
	hanPlaces = []string{"青石镇", "白沙渡", "柳湾村", "南塘", "北岭", "枫桥镇", "落雁湖", "石门街", "云溪", "桐花巷",
		"东岸码头", "西坡"}
	hanAntonyms = map[string]string{
		"早": "晚", "晚": "早", "大": "小", "小": "大", "多": "少", "少": "多", "高": "低", "低": "高",
		"冷": "热", "热": "冷", "暖": "凉", "凉": "暖", "新": "旧", "旧": "新", "长": "短", "短": "长",
		"快": "慢", "慢": "快", "远": "近", "近": "远", "硬": "软", "软": "硬", "深": "浅", "浅": "深",
		"总是": "从不", "从来": "偶尔", "最好": "最差", "安静": "吵闹", "吵闹": "安静", "便宜": "昂贵",
		"快乐": "痛苦", "好听": "难听", "凉快": "闷热", "得意": "后悔", "喜欢": "讨厌", "记得": "忘了",
		"早上": "晚上", "晚上": "早上", "白天": "夜里", "夜里": "白天", "冬天": "夏天", "夏天": "冬天",
		"春天": "秋天", "秋天": "春天", "前面": "后面", "最后": "最初", "第一": "最后", "开门": "关门",
		"关门": "开门", "上山": "下山", "下山": "上山", "平安": "危险", "有名": "无名", "准时": "随意",
	}
	hanReplacements = []string{"石头", "镜子", "火车", "雨伞", "银器", "灯笼"}
	hanFunction     = set(`的 了 在 是 和 也 都 就 还 又 把 被 给 从 到 对 着 过 地 得 他 她 我 你 它 他们 她们 我们 你们
		这 那 这个 那个 一个 没有 不 很 但 但是 因为 所以 如果 可是 却 而 每 每个 什么 自己 只有 然后 后来 已经 一直`)
)

func corruptHan(passage string, k Kind, rng *rand.Rand) (string, error) {
	w := hanFields(passage)
	var out []string
	var err error
	switch k {
	case WordSwap:
		out, err = wordSwap(w, rng)
	case ClauseDrop:
		out, err = clauseDrop(w, rng)
	case Preamble:
		out = append(hanFields(hanPreambles[rng.IntN(len(hanPreambles))]), w...)
	case NumberChange:
		out, err = replaceWhere(w, rng, func(w []string, i int) bool {
			_, c, _ := split(w[i])
			return isHanNumber(c) || isDigits(c)
		}, func(c string) string {
			if isDigits(c) {
				n, _ := strconv.Atoi(c)
				return strconv.Itoa(n + 1 + rng.IntN(9))
			}
			return pick(hanNumbers, c, rng)
		})
	case NameChange:
		out, err = replaceWhere(w, rng, func(w []string, i int) bool {
			_, c, _ := split(w[i])
			return hanPool(c) != nil
		}, func(c string) string { return pick(hanPool(c), c, rng) })
	case ContentWord:
		out, err = hanContentWord(w, rng)
	default:
		return "", fmt.Errorf("calibrate: unknown corruption %q", k)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", k, err)
	}
	return Unsegment(strings.Join(out, " ")), nil
}

// hanFields splits a segmented passage into words, each with its trailing punctuation; a
// Chinese punctuation mark also ends a word when no space follows it ("下雨，我们").
func hanFields(passage string) []string {
	var out []string
	for _, f := range strings.Fields(passage) {
		start := 0
		for i, r := range f {
			if strings.ContainsRune("，；。！？：、", r) {
				if end := i + utf8.RuneLen(r); end < len(f) {
					out, start = append(out, f[start:end]), end
				}
			}
		}
		out = append(out, f[start:])
	}
	return out
}

// isHanNumber is a word written wholly in Han numerals.
func isHanNumber(c string) bool {
	return c != "" && strings.IndexFunc(c, func(r rune) bool { return !strings.ContainsRune("零一二三四五六七八九十百千万两", r) }) < 0
}

func hanPool(name string) []string {
	for _, pool := range [][]string{hanPeople, hanPlaces} {
		for _, p := range pool {
			if p == name {
				return pool
			}
		}
	}
	return nil
}

func hanContentWord(w []string, rng *rand.Rand) ([]string, error) {
	// An antonym changes the meaning for certain; a replacement noun is the fallback.
	out, err := replaceWhere(w, rng, func(w []string, i int) bool {
		_, c, _ := split(w[i])
		_, ok := hanAntonyms[c]
		return ok
	}, func(c string) string { return hanAntonyms[c] })
	if !errors.Is(err, ErrNotApplicable) {
		return out, err
	}
	return replaceWhere(w, rng, func(w []string, i int) bool {
		_, c, _ := split(w[i])
		return alpha(c) && len([]rune(c)) >= 2 && !hanFunction[c] && hanPool(c) == nil && !isHanNumber(c)
	}, func(c string) string { return pick(hanReplacements, c, rng) })
}
