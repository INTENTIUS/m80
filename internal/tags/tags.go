// Package tags implements the three tagging operations, which ride the
// classic Lambda tags routes at /2017-03-31/tags/{Resource} rather than
// either MicroVM URI family. A third path prefix on the same listener.
//
// Tags are stored by the package that owns the resource, not here. A tag set
// belongs to the thing it is on: an image's tags have to appear in the
// image's own responses, and a copy kept here would be a second truth that
// drifts the first time anything reads the wrong one. This package routes an
// ARN to its owner and does nothing else.
//
// KubeMicroVM tags for ownership and chant's lifecycle model reads ownership
// markers from tags, so this small surface is load-bearing for both.
package tags

import (
	"net/http"
	"strings"
)

// Resource is a kind of taggable thing. Each resource package implements it
// over its own ARNs and reports a miss for anything else.
type Resource interface {
	// Tags returns the resource's tags, or false when the ARN names nothing
	// in this region.
	Tags(region, arn string) (map[string]string, bool)
	// SetTags replaces them, reporting whether the resource existed.
	SetTags(region, arn string, tags map[string]string) bool
}

// Model constraints on a tag.
const (
	MaxKeyLength   = 128
	MaxValueLength = 256
)

// registry resolves an ARN to whichever resource package owns it.
type registry struct{ resources []Resource }

func (g registry) get(region, arn string) (map[string]string, bool) {
	for _, r := range g.resources {
		if t, ok := r.Tags(region, arn); ok {
			return t, true
		}
	}
	return nil, false
}

func (g registry) set(region, arn string, t map[string]string) bool {
	for _, r := range g.resources {
		if _, ok := r.Tags(region, arn); ok {
			return r.SetTags(region, arn, t)
		}
	}
	return false
}

// tagKeys reads the TagKeys list, which rides the query string as tagKeys.
// The SDK repeats the parameter; a human with curl is more likely to comma
// separate it, and both are accepted because neither costs anything.
func tagKeys(r *http.Request) []string {
	var out []string
	for _, v := range r.URL.Query()["tagKeys"] {
		for _, k := range strings.Split(v, ",") {
			if k = strings.TrimSpace(k); k != "" {
				out = append(out, k)
			}
		}
	}
	return out
}

// copyTags returns a copy, so a caller cannot mutate a resource's tag map by
// holding onto what it was handed.
func copyTags(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
