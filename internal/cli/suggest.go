package cli

// distance is the optimal-string-alignment edit distance: insertions,
// deletions, substitutions and adjacent transpositions each cost 1, so the
// common typo "sya" is one edit from "say".
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ra)][len(rb)]
}

// nearest returns the command closest to word, and its distance.
// Ties go to the earlier command in registry order, which is deterministic.
func (c *CLI) nearest(word string) (string, int) {
	best, bestD := "", -1
	for _, cmd := range c.commands() {
		if d := distance(word, cmd.name); bestD < 0 || d < bestD {
			best, bestD = cmd.name, d
		}
	}
	return best, bestD
}

// looksLikeTypo reports whether a first positional argument is a mistyped
// command rather than text for the default `say`. Because any word can be
// text, only a single lowercase ASCII word exactly one edit from a command counts.
func (c *CLI) looksLikeTypo(arg string) (string, bool) {
	if arg == "" {
		return "", false
	}
	for _, r := range arg {
		if r < 'a' || r > 'z' {
			return "", false
		}
	}
	name, d := c.nearest(arg)
	return name, d == 1
}
