package spoken

import (
	"regexp"
	"strings"
	"unicode"
)

// Chinese is the Chinese column of design §7.7: numbers become 汉字, digit by
// digit for years, phone numbers and codes, positional otherwise, with 两 where
// it is conventional. English terms are left as written. Order matters where
// two rules can start at the same position: the more specific rule comes first.
var Chinese = Table{
	{"zh.phone", zhPhone},
	{"zh.digits", zhCodeDigits},
	{"zh.date.iso", zhDateISO},
	{"zh.digitgroups", zhDigitGroups},
	{"zh.elapsed", zhElapsed},
	{"zh.time", zhTime},
	{"zh.duration", zhDuration},
	{"zh.ratio", zhRatio},
	{"zh.currency", zhCurrency},
	{"zh.version", zhVersion},
	{"zh.range", zhRange},
	{"zh.year", zhYear},
	{"zh.fraction", zhFraction},
	{"zh.percent", zhPercent},
	{"zh.temp.below-zero", zhTempBelowZero},
	{"zh.temp", zhTemp},
	{"zh.unit", zhUnit},
	{"zh.number", zhNumber},
	{"zh.roman", zhRoman},
	{"zh.symbol", zhSymbol},
}

// --- reading numbers ---

var hanDigit = [10]string{"零", "一", "二", "三", "四", "五", "六", "七", "八", "九"}

// zhDigits reads ds digit by digit; zero is 〇 in years and 零 elsewhere.
func zhDigits(ds, zero string) string {
	var b strings.Builder
	for k := 0; k < len(ds); k++ {
		if ds[k] == '0' {
			b.WriteString(zero)
		} else {
			b.WriteString(hanDigit[ds[k]-'0'])
		}
	}
	return b.String()
}

// zhInt reads an integer digit string positionally (一千二百八十, 一万零五, 两千).
// A leading zero ("05") or more than sixteen digits is read digit by digit.
func zhInt(ds string) string {
	if len(ds) > 1 && ds[0] == '0' || len(ds) > 16 {
		return zhDigits(ds, "零")
	}
	if strings.Trim(ds, "0") == "" {
		return "零"
	}
	groupUnits := []string{"", "万", "亿", "万亿"}
	var groups []string
	for len(ds) > 0 {
		k := max(len(ds)-4, 0)
		groups = append([]string{ds[k:]}, groups...)
		ds = ds[:k]
	}
	var b strings.Builder
	zero := false
	for gi, g := range groups {
		g = strings.Repeat("0", 4-len(g)) + g
		if g == "0000" {
			zero = zero || b.Len() > 0
			continue
		}
		if b.Len() > 0 && (zero || g[0] == '0') {
			b.WriteString("零")
		}
		zero = false
		b.WriteString(zhGroup(g))
		b.WriteString(groupUnits[len(groups)-1-gi])
	}
	out := b.String()
	if strings.HasPrefix(out, "一十") {
		out = strings.TrimPrefix(out, "一") // 十二, 十万: not 一十二
	}
	for _, u := range []string{"百", "千", "万", "亿"} {
		if strings.HasPrefix(out, "二"+u) {
			return "两" + strings.TrimPrefix(out, "二") // 两千, 两万
		}
	}
	return out
}

// zhGroup reads four digits (with leading zeros already accounted for by the caller).
func zhGroup(g string) string {
	units := [4]string{"千", "百", "十", ""}
	var b strings.Builder
	zero := false
	for k := 0; k < 4; k++ {
		d := g[k] - '0'
		if d == 0 {
			zero = zero || b.Len() > 0
			continue
		}
		if zero {
			b.WriteString("零")
			zero = false
		}
		b.WriteString(hanDigit[d] + units[k])
	}
	return b.String()
}

// zhNum reads a scanned number: 负 for a sign, 点 and digits for a fraction.
func zhNum(n number, trimFrac bool) string {
	w := zhInt(n.Int)
	frac := n.Frac
	if trimFrac {
		frac = strings.TrimRight(frac, "0")
	}
	if frac != "" {
		w += "点" + zhDigits(frac, "零")
	}
	if n.Neg {
		w = "负" + w
	}
	return w
}

// zhMeasures follow a count; a lone 2 before them is read 两 (两个, 两点, 两万).
var zhMeasures = []string{"个", "位", "名", "人", "次", "回", "遍", "趟", "天", "周", "年", "岁",
	"小时", "分钟", "秒钟", "种", "类", "件", "本", "张", "只", "条", "台", "家", "辆", "架", "艘", "倍",
	"份", "套", "句", "块", "元", "角", "点", "斤", "公斤", "公里", "千克", "克", "米", "升", "百", "千",
	"万", "亿", "杯", "瓶", "碗", "双", "对", "项", "组", "批", "场", "部", "篇", "页", "所", "座",
	"间", "栋", "首", "门", "颗", "粒", "支", "把", "片", "头", "匹", "棵", "朵", "册", "步", "美元",
	"欧元", "英镑", "港元", "摄氏度", "度"}

// liang reports whether n, between before and after, is a lone 2 counting what follows:
// not an ordinal (第2点) and not a school year (2年级).
func liang(n number, before, after string) bool {
	if n.Int != "2" || n.Frac != "" || n.Neg || strings.HasSuffix(before, "第") || strings.HasPrefix(after, "年级") {
		return false
	}
	for _, m := range zhMeasures {
		if strings.HasPrefix(after, m) {
			return true
		}
	}
	return false
}

// count reads n as a count of what follows at j.
func count(s string, start int, n number, j int, trimFrac bool) string {
	if liang(n, s[:start], s[j:]) {
		return "两"
	}
	return zhNum(n, trimFrac)
}

// --- boundaries ---

func isHan(r rune) bool { return unicode.Is(unicode.Han, r) }

func isASCIIAlnum(r rune) bool {
	return r < 0x80 && (r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
}

// isNumeral is a digit or a Chinese numeral. Boundary checks treat both alike,
// so a neighbour already rewritten to 汉字 does not change a later decision.
func isNumeral(r rune) bool {
	return r >= '0' && r <= '9' || strings.ContainsRune("零〇一二两三四五六七八九十百千万亿", r)
}

// glued reports whether r, or the separator r preceded by p, attaches the next
// number to a term ("A4", "COVID-19", "3.5", "12:30").
func glued(p, r rune) bool {
	if isASCIIAlnum(r) || isNumeral(r) {
		return true
	}
	return strings.ContainsRune(".:/-_,", r) && (isASCIIAlnum(p) || isNumeral(p))
}

// termBefore reports whether a letter, digit or numeral touches i from the left, so a
// symbol or prefix at i belongs to a term. Numerals count because a digit there may
// already have been read as 汉字 by an earlier pass.
func termBefore(s string, i int) bool {
	p := prevRune(s, i)
	return isASCIIAlnum(p) || isNumeral(p)
}

// zhStart reports whether a number may start at i.
func zhStart(s string, i int) bool {
	return i == 0 || !glued(prevRune(s, i-len(string(prevRune(s, i)))), prevRune(s, i))
}

// signAt returns the length of a minus sign at i that cannot be a hyphen, or 0.
func signAt(s string, i int) int {
	if !zhStart(s, i) || prevRune(s, i) == ')' {
		return 0
	}
	for _, m := range []string{"-", "−"} {
		if strings.HasPrefix(s[i:], m) && isDigit(byteAt(s, i+len(m))) {
			return len(m)
		}
	}
	return 0
}

// zhScan reads [sign] number at i, honouring the start boundary.
func zhScan(s string, i int, signed bool) (number, bool) {
	j := i
	neg := false
	if signed {
		if k := signAt(s, i); k > 0 {
			neg, j = true, i+k
		}
	}
	if !isDigit(byteAt(s, j)) || j == i && !zhStart(s, i) {
		return number{}, false
	}
	n, ok := scanNumber(s, j, false)
	n.Neg = neg
	return n, ok
}

// numberEnd reports whether a number may end at j: no digit after it, and no
// separator that would continue it ("3.5.1", "12:30", "1/2").
func numberEnd(s string, j int) bool {
	if isDigit(byteAt(s, j)) {
		return false
	}
	if c := byteAt(s, j); (c == '.' || c == ':' || c == '/' || c == ',') && isDigit(byteAt(s, j+1)) {
		return false
	}
	return true
}

// --- quantities: a number with a unit, percentage or temperature ---

type qtyKind int

const (
	kindNone qtyKind = iota
	kindPercent
	kindTemp
	kindUnit
)

type suffix struct {
	text, say string
	kind      qtyKind
	ascii     bool // must not be followed by an ASCII letter
}

// suffixes are tried in order, longest first where one is a prefix of another.
var suffixes = []suffix{
	{"%", "百分之", kindPercent, false}, {"％", "百分之", kindPercent, false}, {"‰", "千分之", kindPercent, false},
	{"℃", "摄氏度", kindTemp, false}, {"°C", "摄氏度", kindTemp, true}, {"℉", "华氏度", kindTemp, false},
	{"°F", "华氏度", kindTemp, true}, {"°", "度", kindTemp, false},
	{"km/h", "公里每小时", kindUnit, true}, {"m/s", "米每秒", kindUnit, true}, {"km²", "平方公里", kindUnit, false},
	{"m²", "平方米", kindUnit, false}, {"㎡", "平方米", kindUnit, false}, {"kg", "公斤", kindUnit, true},
	{"km", "公里", kindUnit, true}, {"cm", "厘米", kindUnit, true}, {"mm", "毫米", kindUnit, true},
	{"mg", "毫克", kindUnit, true}, {"ml", "毫升", kindUnit, true}, {"mL", "毫升", kindUnit, true},
	{"g", "克", kindUnit, true}, {"m", "米", kindUnit, true}, {"L", "升", kindUnit, true},
}

type qty struct {
	n     number
	suf   *suffix
	start int
	end   int
}

func suffixAt(s string, j int) *suffix {
	for k := range suffixes {
		x := &suffixes[k]
		if !strings.HasPrefix(s[j:], x.text) {
			continue
		}
		e := j + len(x.text)
		if x.ascii && (isASCIIAlnum(nextRune(s, e)) || nextRune(s, e) == '/') {
			continue
		}
		return x
	}
	return nil
}

func scanQty(s string, i int, signed bool) (qty, bool) {
	n, ok := zhScan(s, i, signed)
	if !ok {
		return qty{}, false
	}
	q := qty{n: n, start: i, end: n.End}
	if x := suffixAt(s, n.End); x != nil {
		q.suf, q.end = x, n.End+len(x.text)
	} else if !numberEnd(s, n.End) {
		return qty{}, false
	}
	return q, true
}

// say reads q with suffix x (q's own unless it has none).
func (q qty) say(s string, x *suffix) string {
	if x == nil {
		return count(s, q.start, q.n, q.end, false)
	}
	abs := q.n
	abs.Neg = false
	switch x.kind {
	case kindPercent:
		w := x.say + zhNum(abs, false)
		if q.n.Neg {
			w = "负" + w
		}
		return w
	case kindTemp:
		w := zhNum(abs, false) + x.say
		if q.n.Neg {
			w = "零下" + w
		}
		return w
	}
	if liang(q.n, s[:q.start], x.say) {
		return "两" + x.say
	}
	return zhNum(q.n, false) + x.say
}

func zhQty(kind qtyKind, neg bool) func(string, int, *Options) (Match, bool) {
	return func(s string, i int, _ *Options) (Match, bool) {
		q, ok := scanQty(s, i, true)
		if !ok || q.suf == nil || q.suf.kind != kind || neg && !q.n.Neg {
			return Match{}, false
		}
		return Match{End: q.end, Spoken: q.say(s, q.suf)}, true
	}
}

var (
	zhPercent       = zhQty(kindPercent, false)
	zhTempBelowZero = zhQty(kindTemp, true)
	zhTemp          = zhQty(kindTemp, false)
	zhUnit          = zhQty(kindUnit, false)
)

// --- ranges ---

var rangeSeps = []string{"-", "–", "—", "~", "～"}

func zhRange(s string, i int, _ *Options) (Match, bool) {
	l, ok := scanQty(s, i, true)
	if !ok {
		return Match{}, false
	}
	sep := ""
	for _, x := range rangeSeps {
		if strings.HasPrefix(s[l.end:], x) {
			sep = x
		}
	}
	if sep == "" {
		return Match{}, false
	}
	k := l.end + len(sep)
	rn, ok := scanNumber(s, k, false)
	if g := signAt(s, k); !ok && sep != "-" && sep != "–" && g > 0 {
		rn, ok = scanNumber(s, k+g, false)
		rn.Neg = true
	}
	if !ok {
		return Match{}, false
	}
	r := qty{n: rn, start: k, end: rn.End}
	if x := suffixAt(s, rn.End); x != nil {
		r.suf, r.end = x, rn.End+len(x.text)
	} else if !numberEnd(s, rn.End) {
		return Match{}, false
	}
	// "3-5-7" is a code or a list, not a range.
	for _, x := range rangeSeps {
		if strings.HasPrefix(s[r.end:], x) && isDigit(byteAt(s, r.end+len(x))) {
			return Match{End: r.end, Ambiguous: "a code or a range"}, true
		}
	}
	if l.suf != nil && r.suf != nil && l.suf.say != r.suf.say {
		return Match{}, false
	}
	if l.suf == nil && r.suf == nil && l.n.plain(s, i) && r.n.plain(s, k) && len(l.n.Int) == 4 {
		switch {
		case strings.HasPrefix(s[r.end:], "年") && len(r.n.Int) == 4:
			return Match{End: r.end, Spoken: zhDigits(l.n.Int, "〇") + "到" + zhDigits(r.n.Int, "〇")}, true
		case sep == "-" && len(r.n.Int) <= 2:
			return Match{End: r.end, Ambiguous: "year and month, or a range"}, true
		}
	}
	x := r.suf
	if x == nil {
		x = l.suf
	}
	if x == nil {
		return Match{End: r.end, Spoken: l.say(s, nil) + "到" + count(s, k, r.n, r.end, false)}, true
	}
	if x.kind == kindUnit {
		return Match{End: r.end, Spoken: zhNum(l.n, false) + "到" + r.say(s, x)}, true
	}
	return Match{End: r.end, Spoken: l.say(s, x) + "到" + r.say(s, x)}, true
}

// --- years, dates, times ---

func zhYear(s string, i int, _ *Options) (Match, bool) {
	n, ok := zhScan(s, i, false)
	if !ok || !n.plain(s, i) || len(n.Int) != 4 || !strings.HasPrefix(s[n.End:], "年") {
		return Match{}, false
	}
	return Match{End: n.End, Spoken: zhDigits(n.Int, "〇")}, true
}

var zhDateRe = regexp.MustCompile(`^(\d{4})([-/.])(\d{1,2})([-/.])(\d{1,2})`)

func zhDateISO(s string, i int, _ *Options) (Match, bool) {
	if !isDigit(s[i]) || !zhStart(s, i) {
		return Match{}, false
	}
	m := zhDateRe.FindStringSubmatch(s[i:])
	if m == nil || m[2] != m[4] {
		return Match{}, false
	}
	j := i + len(m[0])
	if !numberEnd(s, j) || strings.ContainsAny(string(byteAt(s, j)), "-/.") && isDigit(byteAt(s, j+1)) {
		return Match{}, false
	}
	mo, _ := atoi(m[3])
	d, _ := atoi(m[5])
	if !validDate(mo, d) {
		return Match{End: j, Ambiguous: "not a valid date"}, true
	}
	return Match{End: j, Spoken: zhDigits(m[1], "〇") + "年" + zhInt(itoa(mo)) + "月" + zhInt(itoa(d)) + "日"}, true
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for ; v > 0; v /= 10 {
		b = append([]byte{byte('0' + v%10)}, b...)
	}
	return string(b)
}

var zhTimeRe = regexp.MustCompile(`^(\d{1,2}):(\d{2})(?::(\d{2}))?`)

func zhTime(s string, i int, _ *Options) (Match, bool) {
	if !isDigit(s[i]) || !zhStart(s, i) {
		return Match{}, false
	}
	m := zhTimeRe.FindStringSubmatch(s[i:])
	if m == nil {
		return Match{}, false
	}
	j := i + len(m[0])
	if !numberEnd(s, j) {
		return Match{}, false
	}
	h, _ := atoi(m[1])
	mi, _ := atoi(m[2])
	sec, hasSec := atoi(m[3])
	if h > 24 || mi > 59 || hasSec && sec > 59 {
		return Match{}, false
	}
	w := zhInt(itoa(h))
	if h == 2 {
		w = "两"
	}
	w += "点"
	if mi > 0 || hasSec {
		w += zhClock(mi) + "分"
	}
	if hasSec {
		w += zhClock(sec) + "秒"
	}
	return Match{End: j, Spoken: w}, true
}

// zhClock reads minutes or seconds: 零五 for 05.
func zhClock(v int) string {
	if v == 0 {
		return "零"
	}
	if v < 10 {
		return "零" + hanDigit[v]
	}
	return zhInt(itoa(v))
}

var zhRatioRe = regexp.MustCompile(`^(\d+):(\d+)`)

// zhRatio reads a colon that is not a clock time as 比 (比分3:1).
func zhRatio(s string, i int, _ *Options) (Match, bool) {
	if !isDigit(s[i]) || !zhStart(s, i) {
		return Match{}, false
	}
	m := zhRatioRe.FindStringSubmatch(s[i:])
	if m == nil || len(m[1]) > 6 || len(m[2]) > 6 {
		return Match{}, false
	}
	j := i + len(m[0])
	if !numberEnd(s, j) {
		return Match{}, false
	}
	return Match{End: j, Spoken: zhInt(m[1]) + "比" + zhInt(m[2])}, true
}

// --- currency ---

var zhCurrencies = []struct{ sym, unit string }{
	{"HK$", "港元"}, {"US$", "美元"}, {"¥", "元"}, {"￥", "元"}, {"$", "美元"}, {"€", "欧元"}, {"£", "英镑"},
}

// zhSuffixCurrencies follow the amount: 1,280.50元.
var zhSuffixCurrencies = []string{"元", "美元", "欧元", "英镑", "港元", "日元"}

var zhScales = []struct{ text, say string }{
	{"万亿", "万亿"}, {"千万", "千万"}, {"百万", "百万"}, {"万", "万"}, {"亿", "亿"},
	{"bn", "十亿"}, {"K", "千"}, {"k", "千"}, {"M", "百万"}, {"B", "十亿"},
}

func scaleAt(s string, j int) (string, int) {
	for _, x := range zhScales {
		if strings.HasPrefix(s[j:], x.text) {
			e := j + len(x.text)
			if isASCIIAlnum(rune(x.text[0])) && isASCIIAlnum(nextRune(s, e)) {
				continue
			}
			return x.say, e
		}
	}
	return "", j
}

// amount reads an amount: trailing zeros of the fraction are not said (一千二百八十点五元).
func amount(s string, start int, n number, next string) string {
	if liang(n, s[:start], next) {
		return "两"
	}
	return zhNum(n, true)
}

func zhCurrency(s string, i int, _ *Options) (Match, bool) {
	j := i
	neg := false
	for _, m := range []string{"-", "−"} {
		if strings.HasPrefix(s[i:], m) && zhStart(s, i) && prevRune(s, i) != ')' {
			neg, j = true, i+len(m)
		}
	}
	for _, c := range zhCurrencies {
		if !strings.HasPrefix(s[j:], c.sym) || termBefore(s, i) {
			continue
		}
		a := j + len(c.sym)
		n, ok := scanNumber(s, a, false)
		if !ok {
			return Match{}, false
		}
		n.Neg = neg
		scale, e := scaleAt(s, n.End)
		w := amount(s, a, n, scale+c.unit)
		if scale == "" && !numberEnd(s, e) {
			return Match{}, false
		}
		// ¥10-20 and ¥10-¥20: a range of amounts.
		for _, sep := range rangeSeps {
			if !strings.HasPrefix(s[e:], sep) {
				continue
			}
			b := e + len(sep)
			b += len(c.sym) * boolInt(strings.HasPrefix(s[b:], c.sym))
			m, ok := scanNumber(s, b, false)
			if !ok {
				break
			}
			scale2, e2 := scaleAt(s, m.End)
			if scale2 == "" && !numberEnd(s, e2) {
				break
			}
			if scale2 == "" {
				scale2 = scale
			}
			return Match{End: e2, Spoken: w + scale + "到" + amount(s, b, m, scale2+c.unit) + scale2 + c.unit}, true
		}
		return Match{End: e, Spoken: w + scale + c.unit}, true
	}
	if neg || !isDigit(s[i]) {
		return Match{}, false
	}
	n, ok := zhScan(s, i, false)
	if !ok {
		return Match{}, false
	}
	scale, e := scaleAt(s, n.End)
	if scale != "" && isASCIIAlnum(rune(s[n.End])) {
		return Match{}, false // 5K, 3M: not money without a currency
	}
	for _, u := range zhSuffixCurrencies {
		if strings.HasPrefix(s[e:], u) && (n.Frac != "" || scale != "") {
			return Match{End: e + len(u), Spoken: amount(s, i, n, scale+u) + scale + u}, true
		}
	}
	return Match{}, false
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- versions ---

// zhVersion reads v2.0 as 版本二点零 (just 二点零 next to 版本 or 版), and a bare
// dotted version 1.2.3 as 一点二点三. Four parts of at most 255 are an IP address,
// read digit by digit.
func zhVersion(s string, i int, _ *Options) (Match, bool) {
	j := i
	v := s[i] == 'v' || s[i] == 'V'
	if v {
		if termBefore(s, i) || !isDigit(byteAt(s, i+1)) {
			return Match{}, false
		}
		j++
	} else if !isDigit(s[i]) || !zhStart(s, i) {
		return Match{}, false
	}
	var parts []string
	for {
		e := digits(s, j)
		if e == j {
			return Match{}, false
		}
		parts = append(parts, s[j:e])
		j = e
		if byteAt(s, j) != '.' || !isDigit(byteAt(s, j+1)) {
			break
		}
		j++
	}
	if !v && len(parts) < 3 || isDigit(byteAt(s, j)) || isASCIIAlnum(nextRune(s, j)) && !v {
		return Match{}, false
	}
	ip := !v && len(parts) == 4
	words := make([]string, len(parts))
	for k, p := range parts {
		val, ok := atoi(p)
		ip = ip && ok && val <= 255
		words[k] = zhInt(p)
	}
	if ip {
		for k, p := range parts {
			words[k] = zhDigits(p, "零")
		}
	}
	w := strings.Join(words, "点")
	if v && !strings.HasSuffix(strings.TrimRight(s[:i], " "), "版本") && !strings.HasPrefix(s[j:], "版") {
		w = "版本" + w
	}
	return Match{End: j, Spoken: w}, true
}

// --- digit strings: phone numbers and codes ---

var zhPhoneRe = regexp.MustCompile(`^(?:(\+86)[ -]?)?(?:(1[3-9]\d)(\d{4})(\d{4})|(1[3-9]\d)-(\d{4})-(\d{4})|(0\d{2,3})-(\d{7,8})|(\d{3})-(\d{3})-(\d{4}))`)

func zhPhone(s string, i int, _ *Options) (Match, bool) {
	if c := s[i]; c != '+' && !isDigit(c) || !zhStart(s, i) {
		return Match{}, false
	}
	m := zhPhoneRe.FindStringSubmatchIndex(s[i:])
	if m == nil {
		return Match{}, false
	}
	j := i + m[1]
	if !numberEnd(s, j) || byteAt(s, j) == '-' && isDigit(byteAt(s, j+1)) {
		return Match{}, false
	}
	var groups []string
	for g := 1; g < len(m)/2; g++ {
		if m[2*g] < 0 {
			continue
		}
		ds := s[i+m[2*g] : i+m[2*g+1]]
		if g == 1 {
			groups = append(groups, "加"+zhDigits(ds[1:], "零"))
			continue
		}
		groups = append(groups, zhDigits(ds, "零"))
	}
	return Match{End: j, Spoken: strings.Join(groups, " ")}, true
}

// codeCues precede numbers that are read digit by digit (尾号1234).
var codeCues = []string{"号码", "编号", "尾号", "账号", "卡号", "密码", "验证码", "工号", "学号", "邮编",
	"单号", "房间", "车次", "航班"}

func zhCodeDigits(s string, i int, _ *Options) (Match, bool) {
	if !isDigit(s[i]) || !zhStart(s, i) {
		return Match{}, false
	}
	j := digits(s, i)
	if j-i < 3 || !numberEnd(s, j) {
		return Match{}, false
	}
	before := strings.TrimRight(s[:i], " ：:是为")
	for _, c := range codeCues {
		if strings.HasSuffix(before, c) {
			return Match{End: j, Spoken: zhDigits(s[i:j], "零")}, true
		}
	}
	return Match{}, false
}

// --- fractions, plain numbers ---

func zhFraction(s string, i int, _ *Options) (Match, bool) {
	a, ok := zhScan(s, i, false)
	if !ok || !a.plain(s, i) || byteAt(s, a.End) != '/' {
		return Match{}, false
	}
	b, ok := scanNumber(s, a.End+1, false)
	if !ok || !b.plain(s, a.End+1) || !numberEnd(s, b.End) || byteAt(s, b.End) == '/' {
		return Match{}, false
	}
	if prevRune(s, i) == '第' {
		return Match{End: b.End, Ambiguous: "two ordinals or a fraction"}, true
	}
	if strings.Trim(b.Int, "0") == "" {
		return Match{End: b.End, Ambiguous: "zero denominator"}, true
	}
	return Match{End: b.End, Spoken: zhInt(b.Int) + "分之" + zhInt(a.Int)}, true
}

func zhNumber(s string, i int, _ *Options) (Match, bool) {
	n, ok := zhScan(s, i, true)
	if !ok || !numberEnd(s, n.End) {
		return Match{}, false
	}
	return Match{End: n.End, Spoken: count(s, i, n, n.End, false)}, true
}

// --- Roman numerals in numbering, symbols ---

// romanCounters follow 第 and a Roman numeral that numbers them (第IV章).
var romanCounters = "章节卷部篇回集季条款课册届次期"

func zhRoman(s string, i int, _ *Options) (Match, bool) {
	if prevRune(s, i) != '第' {
		return Match{}, false
	}
	j := i
	for j < len(s) && romanValues[s[j]] != 0 {
		j++
	}
	if j == i || !strings.ContainsRune(romanCounters, nextRune(s, j)) {
		return Match{}, false
	}
	v := parseRoman(s[i:j])
	if v == 0 {
		return Match{}, false
	}
	return Match{End: j, Spoken: zhInt(itoa(v))}, true
}

var zhSymbols = map[rune]string{
	'≥': "大于等于", '≤': "小于等于", '≠': "不等于", '×': "乘", '÷': "除以", '→': "到", '±': "正负", '≈': "约等于",
}

// zhSymbol reads math symbols; + = < > only between numbers, where they cannot be
// part of a term such as C++.
func zhSymbol(s string, i int, _ *Options) (Match, bool) {
	r := nextRune(s, i)
	j := i + len(string(r))
	if w, ok := zhSymbols[r]; ok {
		return Match{End: j, Spoken: w}, true
	}
	var w string
	switch r {
	case '+':
		w = "加"
	case '=':
		w = "等于"
	case '<':
		w = "小于"
	case '>':
		w = "大于"
	default:
		return Match{}, false
	}
	p := strings.TrimRight(s[:i], " ")
	q := strings.TrimLeft(s[j:], " ")
	if p == "" || q == "" || !isNumeral(prevRune(p, len(p))) || !isNumeral(nextRune(q, 0)) {
		return Match{}, false
	}
	return Match{End: j, Spoken: w}, true
}
