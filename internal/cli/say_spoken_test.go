package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xiaolai/recite/internal/exitcode"
)

// sentScript is the text inside the markers of the request that reached the fake.
func sentScript(t *testing.T, e *sayEnv) string {
	t.Helper()
	var ev struct {
		Response struct {
			Input []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		} `json:"response"`
	}
	raw := sentText(t, e.srv)
	if err := json.Unmarshal([]byte(raw), &ev); err != nil || len(ev.Response.Input) != 1 || len(ev.Response.Input[0].Content) != 1 {
		t.Fatalf("request %s: %v", raw, err)
	}
	return ev.Response.Input[0].Content[0].Text
}

// The spoken form, not the display text, goes to the model; the flags turn the
// stage off, disable one rule and change the date style.
// accept: TXT-10#2
func TestSaySendsSpokenForm(t *testing.T) {
	const in = "Paid $3.5M on 2026-10-11."
	cases := []struct {
		args []string
		want string
	}{
		{nil, "Paid three point five million dollars on October eleventh, twenty twenty-six."},
		{[]string{"--normalize", "off"}, in},
		{[]string{"--normalize-disable", "en.currency.scale"}, "Paid $3.5M on October eleventh, twenty twenty-six."},
		{[]string{"--normalize-disable", "en.date.iso,en.currency.scale"}, "Paid $3.5M on two oh two six, one oh, one one."},
		{[]string{"--date-style", "uk"}, "Paid three point five million dollars on the eleventh of October, twenty twenty-six."},
	}
	for _, c := range cases {
		e := newSayEnv(t)
		r := e.run(append([]string{in, "-o", "a.wav"}, c.args...)...)
		if r.code != exitcode.OK {
			t.Fatalf("%v: code %d stderr %q", c.args, r.code, r.stderr)
		}
		if got := sentScript(t, e); got != "<<<\n"+c.want+"\n>>>" {
			t.Errorf("%v: sent %q", c.args, got)
		}
	}
}

// An ambiguous span is reported like a guard finding and sent as written;
// under --guard strict it stops the run before any network call.
func TestSayAmbiguityFinding(t *testing.T) {
	e := newSayEnv(t)
	r := e.run("Take 1/2 now.", "-o", "a.wav")
	if r.code != exitcode.OK || !strings.Contains(r.stderr, `1:6 N-AMBIGUOUS en.fraction: "1/2" left as written (fraction or date)`) {
		t.Fatalf("%+v", r)
	}
	if got := sentScript(t, e); got != "<<<\nTake 1/2 now.\n>>>" {
		t.Fatalf("sent %q", got)
	}
	e = newSayEnv(t)
	e.mode = "logged-out"
	r = e.run("Take 1/2 now.", "--guard", "strict", "-o", "a.wav")
	if r.code != exitcode.Usage || !strings.Contains(r.stderr, "N-AMBIGUOUS") || len(e.srv.MintRequests()) != 0 {
		t.Fatalf("strict: %+v", r)
	}
	r = e.run("Take 1/2 now.", "-q", "-o", "b.wav")
	if strings.Contains(r.stderr, "N-AMBIGUOUS") {
		t.Fatalf("-q printed the finding: %q", r.stderr)
	}
}

func TestSaySpokenFlagErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--normalize", "on"}, {"--date-style", "eu"}, {"--normalize-disable", "en.nope"},
	} {
		e := newSayEnv(t)
		r := e.run(append([]string{"Hello", "-o", "a.wav"}, args...)...)
		if r.code != exitcode.Usage || len(e.srv.MintRequests()) != 0 {
			t.Errorf("%v: %+v", args, r)
		}
	}
}
