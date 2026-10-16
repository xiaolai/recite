package verify

import "strings"

// Spelling variants the scorer compares as one word. Verification judges what was spoken, and
// STT writes American spelling whatever the source used ("colour" is heard as "color"), so a
// faithful take of British text would lose its content words to the spelling alone. The table
// is enumerated on purpose: an open rule (any "-our" to "-or") would fold "four" into "for"
// and "hour" into "hor", and an open joining rule would let a dropped word hide inside a
// compound. Each entry is a pair that is pronounced the same.
//
// Both sides of the comparison are folded, so the direction does not matter; the American
// form is the one kept.
var spellingVariants = func() map[string]string {
	m := map[string]string{}
	// British stem → American stem, each with the listed endings.
	stems := []struct {
		british, american string
		endings           string
	}{
		// -our / -or
		{"colour", "color", "_ s ed ing ful less"},
		{"harbour", "harbor", "_ s ed ing master masters"},
		{"neighbour", "neighbor", "_ s ly hood hoods ing"},
		{"favour", "favor", "_ s ed ing able ite ites"},
		{"honour", "honor", "_ s ed ing able"},
		{"labour", "labor", "_ s ed ing er ers"},
		{"flavour", "flavor", "_ s ed ing"},
		{"humour", "humor", "_ s ed"},
		{"rumour", "rumor", "_ s ed"},
		{"vapour", "vapor", "_ s"},
		{"behaviour", "behavior", "_ s"},
		{"odour", "odor", "_ s"},
		{"armour", "armor", "_ ed"},
		{"endeavour", "endeavor", "_ s ed ing"},
		{"parlour", "parlor", "_ s"},
		{"vigour", "vigor", "_"},
		{"rigour", "rigor", "_"},
		{"splendour", "splendor", "_"},
		{"clamour", "clamor", "_ s ed ing"},
		{"savour", "savor", "_ s ed ing"},
		{"valour", "valor", "_"},
		// -re / -er
		{"centre", "center", "_ s"},
		{"centred", "centered", "_"},
		{"theatre", "theater", "_ s"},
		{"metre", "meter", "_ s"},
		{"litre", "liter", "_ s"},
		{"fibre", "fiber", "_ s"},
		{"calibre", "caliber", "_"},
		{"sombre", "somber", "_"},
		{"lustre", "luster", "_"},
		{"meagre", "meager", "_"},
		{"sabre", "saber", "_ s"},
		{"spectre", "specter", "_ s"},
		// -ise / -ize
		{"organis", "organiz", "e es ed ing ation ations"},
		{"realis", "realiz", "e es ed ing ation"},
		{"recognis", "recogniz", "e es ed ing"},
		{"apologis", "apologiz", "e es ed ing"},
		{"criticis", "criticiz", "e es ed ing"},
		{"emphasis", "emphasiz", "e es ed ing"},
		{"memoris", "memoriz", "e es ed ing"},
		{"summaris", "summariz", "e es ed ing"},
		{"prioritis", "prioritiz", "e es ed ing"},
		{"specialis", "specializ", "e es ed ing"},
		{"authoris", "authoriz", "e es ed ing ation"},
		{"utilis", "utiliz", "e es ed ing"},
		{"finalis", "finaliz", "e es ed ing"},
		{"minimis", "minimiz", "e es ed ing"},
		{"maximis", "maximiz", "e es ed ing"},
		{"characteris", "characteriz", "e es ed ing"},
		{"sympathis", "sympathiz", "e es ed ing"},
		{"civilis", "civiliz", "e ed ation"},
		{"analys", "analyz", "e ed ing"},
		{"paralys", "paralyz", "e ed ing"},
		// doubled l
		{"travell", "travel", "ed er ers ing"},
		{"cancell", "cancel", "ed ing"},
		{"labell", "label", "ed ing"},
		{"modell", "model", "ed ing"},
		{"quarrell", "quarrel", "ed ing"},
		{"fuell", "fuel", "ed ing"},
		{"levell", "level", "ed ing"},
		{"signall", "signal", "ed ing"},
		{"counsell", "counsel", "ed ing or ors"},
		{"marvell", "marvel", "ed ing ous"},
	}
	for _, s := range stems {
		for _, e := range strings.Fields(s.endings) {
			if e == "_" {
				e = ""
			}
			if s.british+e != s.american+e {
				m[s.british+e] = s.american + e
			}
		}
	}
	for _, p := range [][2]string{
		{"grey", "gray"}, {"greys", "grays"}, {"greying", "graying"}, {"greyer", "grayer"},
		{"draught", "draft"}, {"draughts", "drafts"}, {"draughty", "drafty"},
		{"plough", "plow"}, {"ploughs", "plows"}, {"ploughed", "plowed"}, {"ploughing", "plowing"},
		{"cheque", "check"}, {"cheques", "checks"},
		{"tyre", "tire"}, {"tyres", "tires"},
		{"kerb", "curb"}, {"kerbs", "curbs"},
		{"programme", "program"}, {"programmes", "programs"},
		{"catalogue", "catalog"}, {"catalogues", "catalogs"},
		{"dialogue", "dialog"}, {"dialogues", "dialogs"},
		{"defence", "defense"}, {"offence", "offense"}, {"offences", "offenses"},
		{"licence", "license"}, {"licences", "licenses"},
		{"mould", "mold"}, {"mouldy", "moldy"}, {"smoulder", "smolder"}, {"smouldering", "smoldering"},
		{"moustache", "mustache"}, {"sceptic", "skeptic"}, {"sceptical", "skeptical"},
		{"storey", "story"}, {"storeys", "stories"},
		{"manoeuvre", "maneuver"}, {"manoeuvres", "maneuvers"},
		{"ageing", "aging"}, {"judgement", "judgment"}, {"judgements", "judgments"},
		{"acknowledgement", "acknowledgment"},
		{"enrol", "enroll"}, {"fulfil", "fulfill"}, {"skilful", "skillful"}, {"wilful", "willful"},
		{"jewellery", "jewelry"},
	} {
		m[p[0]] = p[1]
	}
	return m
}()

// compoundVariants are specific compounds written as one word or as two: the pair of folded
// tokens and the joined word. Only these join; a general joining rule could swallow a word.
var compoundVariants = map[[2]string]string{
	{"harbor", "master"}:  "harbormaster",
	{"harbor", "masters"}: "harbormasters",
	{"weather", "vane"}:   "weathervane",
	{"weather", "vanes"}:  "weathervanes",
	{"coast", "guard"}:    "coastguard",
	{"coast", "guards"}:   "coastguards",
	{"head", "master"}:    "headmaster",
	{"head", "mistress"}:  "headmistress",
	{"school", "master"}:  "schoolmaster",
	{"post", "master"}:    "postmaster",
	{"station", "master"}: "stationmaster",
	{"light", "house"}:    "lighthouse",
	{"any", "more"}:       "anymore",
	{"half", "way"}:       "halfway",
	{"all", "together"}:   "altogether",
	{"can", "not"}:        "cannot",
	{"part", "way"}:       "partway",
}

// hanPronouns fold to 他: 他, 她 and 它 are all read tā, so a recogniser cannot tell them apart
// from the audio and writes whichever it likes. Folding gives up their written identity: a
// take that says the right sound is accepted whichever character STT chose, and a source whose
// meaning hangs on 她 versus 他 is not checked on that point. Other homophones (的/地/得,
// names) are not folded: they are checked as written.
var hanPronouns = map[string]string{"她": "他", "它": "他"}

// foldVariants rewrites canonical tokens to the forms the comparison uses: listed British
// spellings to American ones, then listed two-word compounds to one token spanning both.
func foldVariants(toks []CanonToken) []CanonToken {
	out, _ := foldVariantsRaw(toks)
	return out
}

// foldVariantsRaw is foldVariants that also returns, for each folded token, the text it had
// before folding (a joined compound gives its words joined by a space).
func foldVariantsRaw(toks []CanonToken) ([]CanonToken, []string) {
	out := make([]CanonToken, 0, len(toks))
	raw := make([]string, 0, len(toks))
	for _, t := range toks {
		r := t.Text
		if v, ok := spellingVariants[t.Text]; ok {
			t.Text = v
		}
		if n := len(out); n > 0 {
			if j, ok := compoundVariants[[2]string{out[n-1].Text, t.Text}]; ok {
				out[n-1] = CanonToken{Text: j, Start: out[n-1].Start, End: t.End}
				raw[n-1] += " " + r
				continue
			}
		}
		out = append(out, t)
		raw = append(raw, r)
	}
	return out, raw
}
