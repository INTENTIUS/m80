package runner

import (
	"encoding/json"
	"regexp"
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
	// Region redacts in every ARN — including AWS-owned ones (account "aws",
	// e.g. managed base images) — or fixtures recorded in one region fail
	// compares everywhere else. The account keeps "aws" but flattens 12-digit
	// ids.
	reARN       = regexp.MustCompile(`arn:([a-z0-9-]*):([a-z0-9-]*):[a-z0-9-]+:(\d{12}|aws):`)
	reAccount   = regexp.MustCompile(`\b\d{12}\b`)
	reTimestamp = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})\b`)
	reUUID      = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	reSecretKey = regexp.MustCompile(`(?i)(token|secret|credential|password)`)
	reTimeKey   = regexp.MustCompile(`(?i)(time|createdat|updatedat|startedat|terminatedat|expir)`)

	// Short AWS resource ids carry no conformance signal and never match
	// across environments: a recorded sg-f0e6979f can never equal an
	// emulator's generated one. Runs before the UUID rule so a connector id
	// of nc-<uuid> flattens to nc-ID rather than nc-UUID, which is what
	// makes live and emulator connectors comparable at all. The UUID and ID
	// tails are matched too: fixtures on disk were normalized when written,
	// so a live connector already reads nc-UUID there and has to land on the
	// same placeholder as the emulator's raw id.
	reShortID = regexp.MustCompile(`\b(sg|subnet|vpc|nc|eni|rtb|igw|acl|nat|vpce)-(?:[0-9a-fA-F][0-9a-fA-F-]{7,}|UUID|ID)\b`)

	// Regions appear outside ARNs too — the per-VM endpoint hostname is
	// <uuid>.lambda-microvm.<region>.on.aws — so ARN-only region redaction
	// leaves fixtures pinned to the recording region.
	reRegion = regexp.MustCompile(`\b(af|ap|ca|cn|eu|il|me|sa|us)-(gov-)?(central|north|northeast|northwest|south|southeast|southwest|east|west)-[0-9]\b`)

	// versionStateTimeBucket packs a state and the UTC hour it was reached
	// into one string, e.g. "SUCCESSFUL#26073006". The state half is real
	// conformance signal and stays; the YYMMDDHH half is wall clock, so a
	// recording can only ever equal a target that ran in the same hour of
	// the same day. Keeping it would make the step pass or fail by clock.
	reStateBucket = regexp.MustCompile(`^([A-Z_]+)#\d{8}$`)
)

// arnRedact flattens the region and any 12-digit account in an ARN prefix,
// keeping the literal "aws" account of AWS-owned resources.
func arnRedact(match string) string {
	parts := reARN.FindStringSubmatch(match)
	account := "ACCOUNT"
	if parts[3] == "aws" {
		account = "aws"
	}
	return "arn:" + parts[1] + ":" + parts[2] + ":REGION:" + account + ":"
}

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
		s := reStateBucket.ReplaceAllString(v, "${1}#TIMESTAMP")
		s = reARN.ReplaceAllStringFunc(s, arnRedact)
		s = reShortID.ReplaceAllString(s, "${1}-ID")
		s = reRegion.ReplaceAllString(s, "REGION")
		s = reAccount.ReplaceAllString(s, "ACCOUNT")
		s = reTimestamp.ReplaceAllString(s, "TIMESTAMP")
		s = reUUID.ReplaceAllString(s, "UUID")
		return s
	case float64:
		// Epoch-second timestamps hide under many key names (createdAt,
		// startedAt, …) — flatten by key, observed live in the managed-image
		// catalog during the preflight.
		if reTimeKey.MatchString(key) {
			return "TIMESTAMP"
		}
		return v
	default:
		return v
	}
}
