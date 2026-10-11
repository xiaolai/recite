package spoken

import (
	"strings"

	"github.com/xiaolai/recite/internal/numwords"
)

// usStates maps USPS state codes to the names said aloud.
var usStates = map[string]string{
	"AL": "Alabama", "AK": "Alaska", "AZ": "Arizona", "AR": "Arkansas", "CA": "California",
	"CO": "Colorado", "CT": "Connecticut", "DE": "Delaware", "DC": "D C", "FL": "Florida",
	"GA": "Georgia", "HI": "Hawaii", "ID": "Idaho", "IL": "Illinois", "IN": "Indiana", "IA": "Iowa",
	"KS": "Kansas", "KY": "Kentucky", "LA": "Louisiana", "ME": "Maine", "MD": "Maryland",
	"MA": "Massachusetts", "MI": "Michigan", "MN": "Minnesota", "MS": "Mississippi", "MO": "Missouri",
	"MT": "Montana", "NE": "Nebraska", "NV": "Nevada", "NH": "New Hampshire", "NJ": "New Jersey",
	"NM": "New Mexico", "NY": "New York", "NC": "North Carolina", "ND": "North Dakota", "OH": "Ohio",
	"OK": "Oklahoma", "OR": "Oregon", "PA": "Pennsylvania", "RI": "Rhode Island", "SC": "South Carolina",
	"SD": "South Dakota", "TN": "Tennessee", "TX": "Texas", "UT": "Utah", "VT": "Vermont",
	"VA": "Virginia", "WA": "Washington", "WV": "West Virginia", "WI": "Wisconsin", "WY": "Wyoming",
}

// zipEnd returns the end of a ZIP or ZIP+4 code at i ("10019", "78701-1234"), or -1.
func zipEnd(s string, i int) int {
	if !isDigit(byteAt(s, i)) || !wordStart(s, i) || digits(s, i)-i != 5 {
		return -1
	}
	j := i + 5
	if byteAt(s, j) == '-' && digits(s, j+1)-j-1 == 4 {
		j += 5
	}
	if !wordEnd(s, j) || byteAt(s, j) == '-' || byteAt(s, j) == '.' && isDigit(byteAt(s, j+1)) {
		return -1
	}
	return j
}

// sayZip reads a ZIP code digit by digit, as it is said and as transcripts give it.
func sayZip(z string) string {
	a, b, plus4 := strings.Cut(z, "-")
	w := strings.Join(numwords.Digits(a), " ")
	if plus4 {
		w += " dash " + strings.Join(numwords.Digits(b), " ")
	}
	return w
}

// afterCityComma reports a ", " at k preceded by a capitalised word: the city of
// "New York, NY 10019".
func afterCityComma(s string, k int) bool {
	return k >= 2 && s[k-2:k] == ", " && capitalised(prevWord(s, k-2))
}

// enAddress reads the end of a US address. A state code between a city comma and a ZIP code
// is the state name ("New York, NY 10019" is "New York, New York one zero zero one nine");
// a ZIP code after a city comma and a state name or code reads digit by digit, so the
// transcript "New York, New York, 10019" reads the same. Outside that shape nothing changes:
// a bare "NY" or "10019" is not an address.
func enAddress(s string, i int, _ *Options) (Match, bool) {
	if !wordStart(s, i) {
		return Match{}, false
	}
	if j := i + 2; j < len(s) && usStates[s[i:j]] != "" && s[j] == ' ' && afterCityComma(s, i) {
		if e := zipEnd(s, j+1); e > 0 {
			return Match{End: e, Spoken: usStates[s[i:j]] + " " + sayZip(s[j+1:e])}, true
		}
	}
	e := zipEnd(s, i)
	if e < 0 {
		return Match{}, false
	}
	k := strings.TrimSuffix(strings.TrimRight(s[:i], " "), ",")
	if len(k) == i {
		return Match{}, false
	}
	if c := k[max(len(k)-2, 0):]; usStates[c] != "" && wordStart(s, len(k)-2) && afterCityComma(s, len(k)-2) {
		return Match{End: e, Spoken: sayZip(s[i:e])}, true
	}
	for _, name := range usStates {
		if p := len(k) - len(name); strings.HasSuffix(k, name) && wordStart(s, p) && afterCityComma(s, p) {
			return Match{End: e, Spoken: sayZip(s[i:e])}, true
		}
	}
	return Match{}, false
}
