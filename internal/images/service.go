package images

import (
	"strings"
	"sync"
	"time"

	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/store"
)

// Service owns image state and the transitions between states.
//
// Every transition is scheduled on the clock rather than applied inline, so
// a build is observably PENDING then IN_PROGRESS then SUCCESSFUL the way the
// real one is. A test clock makes that instant; the running binary uses a
// short wall-clock delay so a demo looks like the service instead of like a
// mock.
type Service struct {
	clock clock.Clock
	store *store.Store

	// BuildDelay is one hop of the build state machine. Three hops separate
	// a create from a runnable image.
	BuildDelay time.Duration

	mu sync.Mutex
	// failNext forces the next build to FAILED, the compensation-testing
	// lever. Keyed by image name so one failing image does not poison a
	// suite running several.
	failNext map[string]bool
}

func NewService(c clock.Clock, s *store.Store, buildDelay time.Duration) *Service {
	return &Service{clock: c, store: s, BuildDelay: buildDelay, failNext: map[string]bool{}}
}

func (s *Service) collection(region string) *store.Collection[*Image] {
	return store.CollectionOf[*Image](s.store.Region(region), "images")
}

// FailNextBuild arms the failure-injection lever for an image name.
func (s *Service) FailNextBuild(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext[name] = true
}

func (s *Service) takeFailFlag(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext[name] {
		delete(s.failNext, name)
		return true
	}
	return false
}

// Get returns an image by name or ARN.
func (s *Service) Get(region, identifier string) (*Image, bool) {
	return s.collection(region).Get(nameOf(region, identifier))
}

// ResolveRunnable implements the vms package's ImageResolver: an image that
// exists but has no version that finished building cannot back a VM, so a
// run against a still-CREATING image is a miss rather than a VM that would
// never start.
func (s *Service) ResolveRunnable(region, identifier string) (string, string, bool) {
	img, ok := s.Get(region, identifier)
	if !ok || img.LatestActive == nil {
		return "", "", false
	}
	return imageARN(region, img.Name), *img.LatestActive, true
}

// List returns images sorted by name, so responses are stable across calls.
func (s *Service) List(region string) []*Image {
	c := s.collection(region)
	keys := c.Keys()
	sortStrings(keys)
	out := make([]*Image, 0, len(keys))
	for _, k := range keys {
		if img, ok := c.Get(k); ok {
			out = append(out, img)
		}
	}
	return out
}

// Create mints an image and its first version, then starts the build.
func (s *Service) Create(region, name string, spec Spec) *Image {
	now := s.clock.Now()
	img := &Image{
		ID:           newUUID(),
		Name:         name,
		Region:       region,
		State:        StateCreating,
		CreatedAt:    now,
		UpdatedAt:    now,
		Description:  spec.Description,
		BaseImageArn: spec.BaseImageArn,
		BuildRoleArn: spec.BuildRoleArn,
		CodeURI:      spec.CodeURI,
		MemoryMiB:    spec.MemoryMiB,
		Tags:         map[string]string{},
	}
	v := s.mintVersion(img, "1.0", spec)
	img.LatestVersion = v.Version
	s.collection(region).Put(name, img)
	s.startBuild(img, v, StateCreated)
	return img
}

// Update is a full replace that mints a new version, recorded: the PUT moves
// the image through UPDATING and a rebuild appears as 2.0.
func (s *Service) Update(img *Image, spec Spec) *Version {
	now := s.clock.Now()
	img.State = StateUpdating
	img.UpdatedAt = now
	img.Description = spec.Description
	img.BaseImageArn = spec.BaseImageArn
	img.BuildRoleArn = spec.BuildRoleArn
	img.CodeURI = spec.CodeURI
	img.MemoryMiB = spec.MemoryMiB

	v := s.mintVersion(img, nextVersion(img.LatestVersion), spec)
	img.LatestVersion = v.Version
	s.startBuild(img, v, StateUpdated)
	return v
}

// Spec is the mutable half of an image, shared by create and update.
type Spec struct {
	BaseImageArn string
	BuildRoleArn string
	CodeURI      string
	Description  *string
	MemoryMiB    int
}

func (s *Service) mintVersion(img *Image, version string, spec Spec) *Version {
	now := s.clock.Now()
	v := &Version{
		Version:      version,
		State:        BuildPending,
		Status:       StatusActive,
		BaseImageArn: spec.BaseImageArn,
		BaseVersion:  "1.0",
		BuildRoleArn: spec.BuildRoleArn,
		CodeURI:      spec.CodeURI,
		Description:  spec.Description,
		MemoryMiB:    spec.MemoryMiB,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	// Two builds per version, one per Graviton generation, newest first —
	// recorded. A single build would look right until a client counted them.
	for _, gen := range []string{"4", "3"} {
		v.Builds = append(v.Builds, &Build{
			BuildID:           newUUID(),
			ImageVersion:      version,
			State:             BuildPending,
			Architecture:      "ARM_64",
			Chipset:           "GRAVITON",
			ChipsetGeneration: gen,
			CreatedAt:         now,
		})
	}
	img.Versions = append(img.Versions, v)
	return v
}

// startBuild walks PENDING → IN_PROGRESS → SUCCESSFUL, then settles the
// image into settledState. Each hop is a separate timer so every state is
// observable to a poller rather than collapsing into one jump.
func (s *Service) startBuild(img *Image, v *Version, settledState string) {
	fail := s.takeFailFlag(img.Name)

	s.clock.After(s.BuildDelay, func() {
		v.State = BuildInProgress
		v.UpdatedAt = s.clock.Now()
		for _, b := range v.Builds {
			b.State = BuildInProgress
		}

		s.clock.After(s.BuildDelay, func() {
			now := s.clock.Now()
			v.UpdatedAt = now
			if fail {
				v.State = BuildFailed
				for _, b := range v.Builds {
					b.State = BuildFailed
					reason := "build failed by m80 failure injection"
					b.StateReason = &reason
				}
				failed := v.Version
				img.LatestFailed = &failed
			} else {
				v.State = BuildSuccessful
				for _, b := range v.Builds {
					b.State = BuildSuccessful
				}
				active := v.Version
				img.LatestActive = &active
			}

			// The image settles one hop after its version does, which is why
			// a poll on the image sees CREATED only after the build lands.
			s.clock.After(s.BuildDelay, func() {
				img.State = settledState
				img.UpdatedAt = s.clock.Now()
			})
		})
	})
}

// Version returns a version of an image by its version string.
func (img *Image) Version(version string) (*Version, bool) {
	for _, v := range img.Versions {
		if v.Version == version {
			return v, true
		}
	}
	return nil, false
}

// ActiveVersions counts versions that are not draining, which is what makes
// an image deletable or not.
func (img *Image) ActiveVersions() int {
	n := 0
	for _, v := range img.Versions {
		if !v.Deleting {
			n++
		}
	}
	return n
}

// Building reports whether any version is still working, which the service
// refuses to delete over.
func (img *Image) Building() bool {
	for _, v := range img.Versions {
		if v.State == BuildPending || v.State == BuildInProgress {
			return true
		}
	}
	return false
}

// DeleteVersion starts a version draining. Recorded as asynchronous: the
// response says DELETING and the version lingers.
func (s *Service) DeleteVersion(img *Image, v *Version) {
	v.Deleting = true
	v.UpdatedAt = s.clock.Now()
	s.clock.After(s.BuildDelay, func() {
		remaining := img.Versions[:0]
		for _, existing := range img.Versions {
			if existing != v {
				remaining = append(remaining, existing)
			}
		}
		img.Versions = remaining
	})
}

// Delete starts an image draining. The name stays reserved for the window,
// recorded: a create reusing it during the window is refused as already
// existing.
func (s *Service) Delete(img *Image) {
	img.State = StateDeleting
	img.UpdatedAt = s.clock.Now()
	s.clock.After(s.BuildDelay, func() {
		s.collection(img.Region).Delete(img.Name)
	})
}

// nameOf accepts either a bare name or a full ARN, since the service takes
// ARNs in image paths and humans type names.
func nameOf(region, identifier string) string {
	prefix := imageARN(region, "")
	if len(identifier) > len(prefix) && identifier[:len(prefix)] == prefix {
		return identifier[len(prefix):]
	}
	return identifier
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Tags and SetTags implement the tags package's Resource over image ARNs.
// The tag set lives on the image so it appears in the image's own responses;
// a copy kept in the tags package would be a second truth.
func (s *Service) Tags(region, arn string) (map[string]string, bool) {
	img, ok := s.Get(region, arn)
	if !ok || !strings.Contains(arn, ":microvm-image:") {
		return nil, false
	}
	if img.Tags == nil {
		return map[string]string{}, true
	}
	return img.Tags, true
}

func (s *Service) SetTags(region, arn string, tags map[string]string) bool {
	img, ok := s.Get(region, arn)
	if !ok || !strings.Contains(arn, ":microvm-image:") {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	img.Tags = tags
	return true
}
