// Package images implements the MicroVM image resource: images, their
// versions, and the builds behind each version.
//
// Three state layers, each with its own enum, and they are not redundant.
// The image tracks whether the named resource is settled, a version tracks
// one build lineage, and a build tracks one attempt against one chipset
// generation — which is why the live service returns two builds per version,
// Graviton 3 and 4. KubeMicroVM's image build logs feature reads the build
// layer, so collapsing them would break it.
//
// Every shape here is taken from the recorded fixtures rather than the
// service model, because the model does not describe them: it gives no hint
// that Get returns a smaller projection than Create, that Update omits tags
// entirely, or that versionStateTimeBucket exists at all.
package images

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

// AccountID is the account m80 pretends to be. Twelve digits so the
// conformance normalizer flattens it exactly as it flattens a real one.
const AccountID = "000000000000"

// Image states, from MicrovmImageState.
const (
	StateCreating = "CREATING"
	StateCreated  = "CREATED"
	StateUpdating = "UPDATING"
	StateUpdated  = "UPDATED"
	StateDeleting = "DELETING"
)

// Version and build states share the BuildState spelling.
const (
	BuildPending    = "PENDING"
	BuildInProgress = "IN_PROGRESS"
	BuildSuccessful = "SUCCESSFUL"
	BuildFailed     = "FAILED"
)

// Version status is a separate axis from build progress.
const (
	StatusActive   = "ACTIVE"
	StatusInactive = "INACTIVE"
)

// MemoryTiers are the five the service accepts. The schema types this as an
// open integer; the service does not.
var MemoryTiers = []int{512, 1024, 2048, 4096, 8192}

// DefaultMemoryMiB is what a create without an explicit resources block gets,
// recorded.
const DefaultMemoryMiB = 2048

// namePattern is the recorded constraint, quoted verbatim in the validation
// message the service returns.
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9-_]+$`)

const namePatternSource = "[a-zA-Z0-9-_]+"

// Build is one build attempt, for one chipset generation.
type Build struct {
	BuildID           string
	ImageVersion      string
	State             string
	Architecture      string
	Chipset           string
	ChipsetGeneration string
	CreatedAt         time.Time
	StateReason       *string
}

// Version is one build lineage of an image.
type Version struct {
	Version      string
	State        string
	Status       string
	BaseImageArn string
	BaseVersion  string
	BuildRoleArn string
	CodeURI      string
	Description  *string
	MemoryMiB    int
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Deleting     bool
	Builds       []*Build
}

// Image is the named resource.
type Image struct {
	ID           string
	Name         string
	Region       string
	State        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Description  *string
	BaseImageArn string
	BuildRoleArn string
	CodeURI      string
	MemoryMiB    int
	Tags         map[string]string

	// LatestVersion is the newest minted version, which the detail
	// projection reports as imageVersion. LatestActive is the newest one that
	// finished building, which is what a client can actually run.
	LatestVersion string
	LatestActive  *string
	LatestFailed  *string

	Versions []*Version
}

func imageARN(region, name string) string {
	return "arn:aws:lambda:" + region + ":" + AccountID + ":microvm-image:" + name
}

// managedConnectorARN names one of the service-owned default connectors.
// INTERNET_EGRESS rides on every image; HTTP_INGRESS shows up on VMs. Neither
// type is in the model's NetworkConnectorType enum, both are in the
// recordings.
func managedConnectorARN(region, kind string) string {
	return "arn:aws:lambda:" + region + ":aws:network-connector:aws-network-connector:" + kind
}

// newUUID returns a v4 UUID. Only the shape matters — the conformance
// normalizer replaces any UUID with a placeholder — but real clients parse
// these, so they are well-formed.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("images: no entropy: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// epoch renders a timestamp the way the service does, as fractional seconds.
func epoch(t time.Time) float64 {
	return float64(t.UnixNano()) / 1e9
}

// stateTimeBucket reproduces versionStateTimeBucket, e.g.
// "SUCCESSFUL#26073006". The suffix is the UTC hour the state was reached, in
// YYMMDDHH — decoded from the recording, where a version that settled at
// 06:xx UTC on 2026-07-30 carried 26073006. It reads as an index key the
// service exposes by accident, but clients can see it, so m80 emits it.
func stateTimeBucket(state string, at time.Time) string {
	return state + "#" + at.UTC().Format("06010215")
}

// nextVersion returns the successor of a version string. Versions mint as
// 1.0, 2.0, 3.0 — the minor is always zero in every recording, so the major
// is what moves.
func nextVersion(current string) string {
	var major int
	if _, err := fmt.Sscanf(current, "%d.", &major); err != nil || major < 1 {
		return "1.0"
	}
	return fmt.Sprintf("%d.0", major+1)
}
