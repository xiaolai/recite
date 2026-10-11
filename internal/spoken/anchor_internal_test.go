package spoken

import (
	"regexp"
	"regexp/syntax"
	"testing"
)

// Rules run at every offset of the text, so each rule regexp must be anchored at its start.
// A pattern Go does not see as anchored (for example ^ repeated inside an alternation)
// scans the whole remainder per offset and makes Run quadratic; the fuzzer timed out on it.
func TestRuleRegexpsAreAnchored(t *testing.T) {
	for name, re := range map[string]*regexp.Regexp{
		"phoneRe": phoneRe, "scaleRe": scaleRe, "monthRe": monthRe, "ampmRe": ampmRe,
	} {
		parsed, err := syntax.Parse(re.String(), syntax.Perl)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		prog, err := syntax.Compile(parsed.Simplify())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if prog.StartCond()&syntax.EmptyBeginText == 0 {
			t.Errorf("%s is not anchored at the start: %s", name, re)
		}
	}
}
