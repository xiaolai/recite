package verify

import "testing"

// A say pair's written term in the transcript is read as its rewrite: any case, whole words
// only in spaced scripts, substrings in Chinese.
func TestApplyEquivalents(t *testing.T) {
	eqs := []SayPair{{"SQL", "sequel"}, {"重庆", "chong qing"}, {"New York", "NY"}, {"York", "yorke"}}
	for _, c := range []struct{ in, want string }{
		{"wrote the SQL query", "wrote the sequel query"},
		{"wrote the sql query, Sql too", "wrote the sequel query, sequel too"},
		{"SQLite and MySQL", "SQLite and MySQL"},
		{"去重庆市", "去chong qing市"},
		{"in New York and York", "in NY and yorke"},
		{"wrote the sickle query", "wrote the sickle query"},
		{"Ünï SQL\xff", "Ünï sequel\xff"},
	} {
		if got := applyEquivalents(c.in, eqs); got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
	if got := applyEquivalents("SQL", nil); got != "SQL" {
		t.Errorf("no pairs: %q", got)
	}
}

// A pair whose term the reference keeps as written does not rewrite the transcript.
func TestSayPairsForReference(t *testing.T) {
	eqs := []SayPair{{"SQL", "sequel"}, {"C++", "cee plus plus"}}
	got := sayPairsFor("Keep SQL, then cee plus plus.", eqs)
	if len(got) != 1 || got[0].Written != "C++" {
		t.Errorf("%+v", got)
	}
}
