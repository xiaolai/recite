// Command acceptcheck enforces that every acceptance bullet of a done feature
// is proved by a test carrying an `// accept: <ID>#<n>` comment (FND-03).
//
// A feature is done when every work package of the implementation plan that
// lists it is marked DONE. Bullets completed by a later work package (plan
// §1.7) are listed in deferred.txt and stay exempt only while that work
// package is not DONE.
package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Paths of the real repository, relative to the module root.
const (
	ledgerPath   = "dev-docs/feature-ledger.md"
	planPath     = "dev-docs/implementation-plan.md"
	deferredPath = "tools/acceptcheck/deferred.txt"
)

func main() {
	os.Exit(run(paths{
		ledger:   ledgerPath,
		plan:     planPath,
		deferred: deferredPath,
		src:      ".",
	}, os.Stdout, os.Stderr))
}

type paths struct {
	ledger, plan, deferred, src string
}

// run returns 0 on success, 1 when the gate finds problems, and 2 when an
// input cannot be read or parsed or the report cannot be written.
func run(p paths, stdout, stderr io.Writer) int {
	code, msg := evaluate(p)
	w := stdout
	if code != 0 {
		w = stderr
	}
	if _, err := io.WriteString(w, msg); err != nil {
		return 2
	}
	return code
}

// evaluate returns the exit code and the report to print.
func evaluate(p paths) (code int, report string) {
	fail := func(err error) (int, string) {
		return 2, fmt.Sprintf("acceptcheck: %v\n", err)
	}
	ledger, err := parseLedger(p.ledger)
	if err != nil {
		return fail(err)
	}
	plan, err := parsePlan(p.plan, ledger)
	if err != nil {
		return fail(err)
	}
	deferred, err := parseDeferred(p.deferred)
	if err != nil {
		return fail(err)
	}
	refs, err := scanRefs(p.src)
	if err != nil {
		return fail(err)
	}

	res := check(ledger, plan, deferred, refs)
	if len(res.problems) > 0 {
		return 1, fmt.Sprintf("%s\nacceptcheck: %d problem(s)\n",
			strings.Join(res.problems, "\n"), len(res.problems))
	}
	return 0, fmt.Sprintf("acceptcheck: %d done features, %d bullets referenced, %d deferred, OK\n",
		res.doneFeatures, res.referenced, res.deferred)
}

// ledger maps a feature ID to its number of acceptance bullets; order keeps
// the ledger order for deterministic output.
type ledger struct {
	bullets map[string]int
	order   []string
}

func (l ledger) hasBullet(id string) bool {
	feat, n, ok := splitBullet(id)
	if !ok {
		return false
	}
	count, ok := l.bullets[feat]
	return ok && n >= 1 && n <= count
}

var (
	featureHeading = regexp.MustCompile(`^### ([A-Z][A-Z0-9]*-[0-9]+) · `)
	bulletID       = regexp.MustCompile(`^([A-Z][A-Z0-9]*-[0-9]+)#([0-9]+)$`)
)

func splitBullet(id string) (feature string, n int, ok bool) {
	m := bulletID.FindStringSubmatch(id)
	if m == nil {
		return "", 0, false
	}
	if _, err := fmt.Sscanf(m[2], "%d", &n); err != nil {
		return "", 0, false
	}
	return m[1], n, true
}

func parseLedger(path string) (ledger, error) {
	lines, err := readLines(path)
	if err != nil {
		return ledger{}, err
	}
	l := ledger{bullets: map[string]int{}}
	var cur string // feature whose heading was seen last
	inAccept := false
	finish := func() error {
		if cur != "" && l.bullets[cur] == 0 {
			return fmt.Errorf("%s: feature %s has no acceptance bullets", path, cur)
		}
		return nil
	}
	for i, line := range lines {
		if strings.HasPrefix(line, "#") {
			if err := finish(); err != nil {
				return ledger{}, err
			}
			cur, inAccept = "", false
			if m := featureHeading.FindStringSubmatch(line); m != nil {
				if _, dup := l.bullets[m[1]]; dup {
					return ledger{}, fmt.Errorf("%s:%d: duplicate feature %s", path, i+1, m[1])
				}
				cur = m[1]
				l.bullets[cur] = 0
				l.order = append(l.order, cur)
			}
			continue
		}
		if cur == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "- **Accept:**"):
			inAccept = true
		case strings.HasPrefix(line, "- **"):
			inAccept = false
		case inAccept && strings.HasPrefix(line, "  - "):
			l.bullets[cur]++
		}
	}
	if err := finish(); err != nil {
		return ledger{}, err
	}
	if len(l.order) == 0 {
		return ledger{}, fmt.Errorf("%s: no features found", path)
	}
	return l, nil
}

// plan holds every work package and, per feature, the work packages that list it.
type plan struct {
	done     map[string]bool     // WP id → DONE
	claimers map[string][]string // feature → WP ids
}

var (
	wpHeading  = regexp.MustCompile(`^### (WP-[A-Z0-9.]+) · `)
	featureRef = regexp.MustCompile(`\b[A-Z][A-Z0-9]*-[0-9]+\b`)
)

func parsePlan(path string, l ledger) (plan, error) {
	lines, err := readLines(path)
	if err != nil {
		return plan{}, err
	}
	p := plan{done: map[string]bool{}, claimers: map[string][]string{}}
	var cur string
	header := false // still in the lines right under the heading
	inFeatures := false
	for i, line := range lines {
		if strings.HasPrefix(line, "#") {
			cur, header, inFeatures = "", false, false
			if m := wpHeading.FindStringSubmatch(line); m != nil {
				if _, dup := p.done[m[1]]; dup {
					return plan{}, fmt.Errorf("%s:%d: duplicate work package %s", path, i+1, m[1])
				}
				cur, header = m[1], true
				p.done[cur] = false
			}
			continue
		}
		if cur == "" {
			continue
		}
		if inFeatures && strings.HasPrefix(line, "  ") && strings.TrimSpace(line) != "" {
			if err := addClaims(&p, l, cur, line, path, i+1); err != nil {
				return plan{}, err
			}
			continue
		}
		inFeatures = false
		if strings.HasPrefix(line, "- **") {
			header = false
		}
		if header && strings.HasPrefix(line, "**Status:** DONE") {
			p.done[cur] = true
		}
		if rest, ok := strings.CutPrefix(line, "- **Features:**"); ok {
			inFeatures = true
			if err := addClaims(&p, l, cur, rest, path, i+1); err != nil {
				return plan{}, err
			}
		}
	}
	if len(p.done) == 0 {
		return plan{}, fmt.Errorf("%s: no work packages found", path)
	}
	return p, nil
}

// addClaims records the feature IDs on one Features line. Tokens followed by
// "." are section or WP numbers (WP-1.4), not features.
func addClaims(p *plan, l ledger, wp, text, path string, lineNo int) error {
	for _, loc := range featureRef.FindAllStringIndex(text, -1) {
		if loc[1] < len(text) && text[loc[1]] == '.' && loc[1]+1 < len(text) && text[loc[1]+1] >= '0' && text[loc[1]+1] <= '9' {
			continue
		}
		id := text[loc[0]:loc[1]]
		if strings.HasPrefix(id, "WP-") {
			continue
		}
		if _, ok := l.bullets[id]; !ok {
			return fmt.Errorf("%s:%d: %s lists unknown feature %s", path, lineNo, wp, id)
		}
		if !slices.Contains(p.claimers[id], wp) {
			p.claimers[id] = append(p.claimers[id], wp)
		}
	}
	return nil
}

type deferral struct {
	bullet, wp string
	line       int
}

func parseDeferred(path string) ([]deferral, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	var out []deferral
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		f := strings.Fields(t)
		if len(f) < 3 {
			return nil, fmt.Errorf("%s:%d: want <BULLET-ID> <WP-id> <reason>, got %q", path, i+1, line)
		}
		out = append(out, deferral{bullet: f[0], wp: f[1], line: i + 1})
	}
	return out, nil
}

// refs maps a bullet ID to the files referencing it.
type refs map[string][]string

var acceptComment = regexp.MustCompile(`^\s*// accept:\s*(.+)$`)

func scanRefs(root string) (refs, error) {
	r := refs{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (d.Name() == ".git" || d.Name() == "testdata" || d.Name() == "dev-docs") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		lines, err := readLines(path)
		if err != nil {
			return err
		}
		for i, line := range lines {
			m := acceptComment.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			for _, id := range strings.FieldsFunc(m[1], func(c rune) bool { return c == ' ' || c == ',' || c == '\t' }) {
				r[id] = append(r[id], fmt.Sprintf("%s:%d", filepath.ToSlash(path), i+1))
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", root, err)
	}
	return r, nil
}

type result struct {
	problems                           []string
	doneFeatures, referenced, deferred int
}

func check(l ledger, p plan, defs []deferral, r refs) result {
	var res result

	ids := make([]string, 0, len(r))
	for id := range r {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if !l.hasBullet(id) {
			for _, at := range r[id] {
				res.problems = append(res.problems, fmt.Sprintf("%s: reference to nonexistent bullet %s", at, id))
			}
		}
	}

	active := map[string]bool{}
	for _, d := range defs {
		ok := true
		if !l.hasBullet(d.bullet) {
			res.problems = append(res.problems, fmt.Sprintf("deferred.txt:%d: unknown bullet %s", d.line, d.bullet))
			ok = false
		}
		done, known := p.done[d.wp]
		if !known {
			res.problems = append(res.problems, fmt.Sprintf("deferred.txt:%d: unknown work package %s", d.line, d.wp))
			ok = false
		}
		if ok && !done {
			active[d.bullet] = true
		}
	}

	for _, feat := range l.order {
		wps := p.claimers[feat]
		if len(wps) == 0 || slices.ContainsFunc(wps, func(wp string) bool { return !p.done[wp] }) {
			continue
		}
		res.doneFeatures++
		for n := 1; n <= l.bullets[feat]; n++ {
			id := fmt.Sprintf("%s#%d", feat, n)
			switch {
			case len(r[id]) > 0:
				res.referenced++
			case active[id]:
				res.deferred++
			default:
				res.problems = append(res.problems, fmt.Sprintf("%s: done feature has no // accept: reference for %s", feat, id))
			}
		}
	}
	return res
}

func readLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"), nil
}
