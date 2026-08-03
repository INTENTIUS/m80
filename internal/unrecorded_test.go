package internal_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// docs/unrecorded.md is the list of every place m80 answers something it never
// observed. A list like that is worth exactly as much as its agreement with
// the code, and nothing keeps a hand-maintained one in agreement.
//
// So the source carries a marker at each deciding line:
//
//	// UNRECORDED: endpoint-pending-vm — extrapolate
//
// and this test fails when the markers and the table disagree in either
// direction — a guess added to the code without a row, or a row left behind
// after a recording made it unnecessary. The second matters more: a stale row
// tells a consumer to distrust something that is now recorded fact.

var (
	markerRe = regexp.MustCompile(`UNRECORDED: ([a-z0-9-]+) — ([a-z-]+)`)
	rowRe    = regexp.MustCompile("^\\| `([a-z0-9-]+)` \\| [^|]+ \\| ([a-z-]+) \\|")
)

// The dispositions the rule allows. A marker carrying anything else is a
// decision made outside the rule, which is the thing this file exists to stop.
var dispositions = map[string]bool{
	"refuse":           true,
	"follow-the-model": true,
	"extrapolate":      true,
	"safest-of-two":    true,
	"reuse-recorded":   true,
	"omit":             true,
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(wd)
}

func markersInSource(t *testing.T, root string) map[string]string {
	t.Helper()
	found := map[string]string{}
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range markerRe.FindAllStringSubmatch(string(body), -1) {
			id, disposition := m[1], m[2]
			if prev, dup := found[id]; dup {
				t.Errorf("UNRECORDED id %q appears twice (%s and again in %s)", id, prev, path)
			}
			found[id] = disposition
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func rowsInDoc(t *testing.T, root string) map[string]string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "docs", "unrecorded.md"))
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		if m := rowRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			rows[m[1]] = m[2]
		}
	}
	return rows
}

func TestEveryUnrecordedDecisionIsListed(t *testing.T) {
	root := repoRoot(t)
	source := markersInSource(t, root)
	doc := rowsInDoc(t, root)

	if len(source) == 0 {
		t.Fatal("no UNRECORDED markers found in internal/ — the marker format changed, or the walk is wrong")
	}

	for id, disposition := range source {
		want, listed := doc[id]
		if !listed {
			t.Errorf("%q is decided in the source and not listed in docs/unrecorded.md — add a row saying what m80 does and why", id)
			continue
		}
		if want != disposition {
			t.Errorf("%q is marked %q in the source and %q in the docs", id, disposition, want)
		}
	}

	for id := range doc {
		if _, ok := source[id]; !ok {
			t.Errorf("docs/unrecorded.md lists %q and nothing in the source decides it — if a recording made it unnecessary, delete the row", id)
		}
	}
}

func TestEveryDispositionIsOneTheRuleAllows(t *testing.T) {
	for id, disposition := range markersInSource(t, repoRoot(t)) {
		if !dispositions[disposition] {
			allowed := make([]string, 0, len(dispositions))
			for d := range dispositions {
				allowed = append(allowed, d)
			}
			sort.Strings(allowed)
			t.Errorf("%q is marked %q, which is not one of the rule's dispositions (%s)", id, disposition, strings.Join(allowed, ", "))
		}
	}
}
