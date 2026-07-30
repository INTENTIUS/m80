package runner

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Normalize redacts environment-specific values so fixtures recorded in one
// account compare cleanly everywhere: account IDs, ARN region/account fields,
// timestamps, UUIDs, and any field whose name suggests a credential.
func Normalize(body []byte) []byte {
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body
	}
	doc = normalizeNode("", doc)
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

var (
	reARN       = regexp.MustCompile(`arn:([a-z0-9-]*):([a-z0-9-]*):([a-z0-9-]*):(\d{12}):`)
	reAccount   = regexp.MustCompile(`\b\d{12}\b`)
	reTimestamp = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})\b`)
	reUUID      = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	reSecretKey = regexp.MustCompile(`(?i)(token|secret|credential|password)`)
)

func normalizeNode(key string, node any) any {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			v[k] = normalizeNode(k, child)
		}
		return v
	case []any:
		for i, child := range v {
			v[i] = normalizeNode(key, child)
		}
		return v
	case string:
		if reSecretKey.MatchString(key) {
			return "REDACTED"
		}
		s := reARN.ReplaceAllString(v, "arn:$1:$2:REGION:ACCOUNT:")
		s = reAccount.ReplaceAllString(s, "ACCOUNT")
		s = reTimestamp.ReplaceAllString(s, "TIMESTAMP")
		s = reUUID.ReplaceAllString(s, "UUID")
		return s
	case float64:
		// Epoch-second timestamps: anything that parses as a plausible date
		// in this century gets flattened.
		if key != "" && strings.Contains(strings.ToLower(key), "time") {
			return "TIMESTAMP"
		}
		return v
	default:
		return v
	}
}
