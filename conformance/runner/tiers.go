package runner

import (
	"encoding/json"
	"os"
)

// Tier selects how strictly a response is compared to its fixture.
type Tier string

const (
	// TierAll compares every member. This is the bar m80 holds itself to,
	// because exactness costs a purpose-built emulator almost nothing.
	TierAll Tier = "all"
	// TierLoadBearing ignores members nothing branches on and the prose of
	// error messages, keeping status codes, error types, states, and every
	// echoed spec field. This is the bar for a target whose job is narrower,
	// like the floci CloudFormation module.
	TierLoadBearing Tier = "load-bearing"
)

// Tiers is the committed classification, loaded from conformance/tiers.json.
// The reason strings are not decoration: a member lands in cosmeticMembers
// only with an argument for why no consumer reads it, and a test re-derives
// the always-null set from the fixtures so the list cannot drift silently.
type Tiers struct {
	CosmeticMembers           map[string]string `json:"cosmeticMembers"`
	ProseMembers              map[string]string `json:"proseMembers"`
	RetainedDespiteAlwaysNull map[string]string `json:"retainedDespiteAlwaysNull"`
}

func LoadTiers(path string) (*Tiers, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t Tiers
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// ApplyTier rewrites a normalized body for comparison at the given tier.
// Cosmetic members are removed entirely, so a target that omits them and one
// that sends them as null compare equal. Prose members keep their key and
// lose their text, so a missing message is still a divergence while different
// wording is not.
func (t *Tiers) ApplyTier(body []byte, tier Tier) []byte {
	if t == nil || tier != TierLoadBearing {
		return body
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body
	}
	doc = t.strip(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

func (t *Tiers) strip(node any) any {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			if _, cosmetic := t.CosmeticMembers[k]; cosmetic {
				delete(v, k)
				continue
			}
			if _, prose := t.ProseMembers[k]; prose {
				v[k] = "PROSE"
				continue
			}
			v[k] = t.strip(child)
		}
		return v
	case []any:
		for i, child := range v {
			v[i] = t.strip(child)
		}
		return v
	default:
		return node
	}
}
