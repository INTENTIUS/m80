package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func loadCommittedTiers(t *testing.T) *Tiers {
	t.Helper()
	tiers, err := LoadTiers(filepath.Join("..", "tiers.json"))
	if err != nil {
		t.Fatal(err)
	}
	return tiers
}

// Every member that is null in every recorded response is a candidate for
// cosmetic. Some are promoted back — a failed build populates
// latestFailedImageVersion, the recordings just never had one — but the
// promotion has to be deliberate and written down. This test fails when a
// re-recording introduces a new always-null member that nobody has classified,
// so the tiering cannot rot behind a passing suite.
func TestEveryAlwaysNullMemberIsClassified(t *testing.T) {
	tiers := loadCommittedTiers(t)

	seen, nulls := map[string]int{}, map[string]int{}
	var walk func(any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			for k, child := range v {
				seen[k]++
				if child == nil {
					nulls[k]++
				}
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}

	fixtures, err := filepath.Glob(filepath.Join("..", "fixtures", "*", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		if filepath.Ext(f) != ".json" || len(f) > 10 && f[len(f)-10:] == ".meta.json" {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var doc any
		if json.Unmarshal(raw, &doc) != nil {
			continue
		}
		walk(doc)
	}
	if len(seen) == 0 {
		t.Fatal("no fixtures read; the corpus path is wrong")
	}

	var unclassified []string
	for member, n := range seen {
		if nulls[member] != n {
			continue
		}
		_, cosmetic := tiers.CosmeticMembers[member]
		_, retained := tiers.RetainedDespiteAlwaysNull[member]
		if !cosmetic && !retained {
			unclassified = append(unclassified, member)
		}
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Errorf("always-null members with no tier decision: %v\n"+
			"add each to cosmeticMembers with a reason no consumer reads it, "+
			"or to retainedDespiteAlwaysNull with a reason one does",
			unclassified)
	}
}

// A cosmetic classification must carry an argument, not just a name.
func TestEveryTierEntryHasAReason(t *testing.T) {
	tiers := loadCommittedTiers(t)
	for _, group := range []map[string]string{
		tiers.CosmeticMembers, tiers.ProseMembers, tiers.RetainedDespiteAlwaysNull,
	} {
		for member, reason := range group {
			if len(reason) < 15 {
				t.Errorf("%s: reason %q is too thin to be an argument", member, reason)
			}
		}
	}
}

// A member cannot be both ignored and required.
func TestNoMemberIsBothCosmeticAndRetained(t *testing.T) {
	tiers := loadCommittedTiers(t)
	for member := range tiers.CosmeticMembers {
		if _, both := tiers.RetainedDespiteAlwaysNull[member]; both {
			t.Errorf("%s is classified both cosmetic and retained", member)
		}
	}
}

func TestApplyTier(t *testing.T) {
	tiers := loadCommittedTiers(t)
	const sparse = `{"state":"CREATED","hooks":null,"logging":null,"message":"one wording"}`
	const full = `{"state":"CREATED","message":"a completely different wording"}`

	// At the load-bearing tier a target that omits cosmetic members equals
	// one that sends them null, and wording does not matter.
	a := tiers.ApplyTier([]byte(sparse), TierLoadBearing)
	b := tiers.ApplyTier([]byte(full), TierLoadBearing)
	if !jsonEqual(a, b) {
		t.Errorf("load-bearing tier still separates them:\n  %s\n  %s", a, b)
	}

	// At the full tier they differ, which is the bar m80 holds itself to.
	if jsonEqual(tiers.ApplyTier([]byte(sparse), TierAll), tiers.ApplyTier([]byte(full), TierAll)) {
		t.Error("full tier ignored a missing member or differing wording")
	}

	// The load-bearing state must survive; the tier drops decoration, not
	// the thing the whole comparison exists for.
	var doc map[string]any
	if err := json.Unmarshal(a, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["state"] != "CREATED" {
		t.Errorf("state was stripped: %v", doc)
	}
	if _, present := doc["message"]; !present {
		t.Error("message key was dropped; a missing message should still be a divergence")
	}
}

// A nil Tiers or the full tier must be a no-op, so recording runs and m80's
// own runs are untouched.
func TestApplyTierIsNoOpByDefault(t *testing.T) {
	const body = `{"state":"CREATED","hooks":null}`
	var nilTiers *Tiers
	if got := string(nilTiers.ApplyTier([]byte(body), TierLoadBearing)); got != body {
		t.Errorf("nil tiers changed the body: %s", got)
	}
	tiers := loadCommittedTiers(t)
	if got := string(tiers.ApplyTier([]byte(body), TierAll)); got != body {
		t.Errorf("full tier changed the body: %s", got)
	}
}
