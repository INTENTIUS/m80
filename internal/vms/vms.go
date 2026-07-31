// Package vms implements the MicroVM resource: run, get, list, terminate.
//
// Suspend, resume and the idle timers are #11; this package owns the states
// either side of them and the storage they share.
//
// Two recorded facts shape everything here. VM ids are microvm-<uuid>, not
// the mv-… the docs guessed, and every VM carries managed default connectors
// on both directions — INTERNET_EGRESS out and HTTP_INGRESS in — neither of
// which appears in the model's NetworkConnectorType enum.
package vms

import (
	"time"

	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/store"
)

// VM states, from MicrovmState.
const (
	StatePending     = "PENDING"
	StateRunning     = "RUNNING"
	StateSuspending  = "SUSPENDING"
	StateSuspended   = "SUSPENDED"
	StateTerminating = "TERMINATING"
	StateTerminated  = "TERMINATED"
)

// MaximumDurationSeconds is the eight-hour session cap every VM reports.
const MaximumDurationSeconds = 28800

// IdlePolicy is echoed back as given. autoResumeEnabled is required whenever
// the policy is present at all — recorded, and the model marks no member of
// it required.
type IdlePolicy struct {
	AutoResumeEnabled        bool `json:"autoResumeEnabled"`
	MaxIdleDurationSeconds   *int `json:"maxIdleDurationSeconds,omitempty"`
	SuspendedDurationSeconds *int `json:"suspendedDurationSeconds,omitempty"`
}

type VM struct {
	ID           string
	Region       string
	ImageArn     string
	ImageVersion string
	State        string
	StartedAt    time.Time
	TerminatedAt *time.Time
	StateReason  *string
	IdlePolicy   *IdlePolicy
	Endpoint     string
}

// Terminal reports whether the VM can still change state. Mutating a
// terminated VM is a 400 ValidationException, recorded — not either of the
// conflict types the model offers.
func (v *VM) Terminal() bool {
	return v.State == StateTerminated
}

type Service struct {
	clock clock.Clock
	store *store.Store

	// Transition is one hop of a VM state machine: PENDING to RUNNING,
	// TERMINATING to TERMINATED.
	Transition time.Duration
}

func NewService(c clock.Clock, s *store.Store, transition time.Duration) *Service {
	return &Service{clock: c, store: s, Transition: transition}
}

func (s *Service) collection(region string) *store.Collection[*VM] {
	return store.CollectionOf[*VM](s.store.Region(region), "microvms")
}

func (s *Service) Get(region, id string) (*VM, bool) {
	return s.collection(region).Get(id)
}

// List returns VMs sorted by id so responses are stable. Terminated VMs stay
// listed — recorded, and the reason a recorded ListMicrovms fixture can never
// match a fresh emulator.
func (s *Service) List(region string) []*VM {
	c := s.collection(region)
	keys := c.Keys()
	sortStrings(keys)
	out := make([]*VM, 0, len(keys))
	for _, k := range keys {
		if vm, ok := c.Get(k); ok {
			out = append(out, vm)
		}
	}
	return out
}

// Run creates a VM in PENDING and schedules it into RUNNING.
func (s *Service) Run(region, imageArn, imageVersion string, idle *IdlePolicy) *VM {
	id := "microvm-" + newUUID()
	vm := &VM{
		ID:           id,
		Region:       region,
		ImageArn:     imageArn,
		ImageVersion: imageVersion,
		State:        StatePending,
		StartedAt:    s.clock.Now(),
		IdlePolicy:   idle,
		// The endpoint hostname is a bare UUID, not the microvm- prefixed id.
		Endpoint: newUUID() + ".lambda-microvm." + region + ".on.aws",
	}
	s.collection(region).Put(id, vm)
	s.clock.After(s.Transition, func() {
		if vm.State == StatePending {
			vm.State = StateRunning
		}
	})
	return vm
}

// Terminate walks the VM to TERMINATED through TERMINATING. The recording
// never sampled TERMINATING at a five-second poll, but it is in the enum and
// a faster client can see it, so m80 goes through it rather than jumping.
func (s *Service) Terminate(vm *VM) {
	if vm.Terminal() || vm.State == StateTerminating {
		return
	}
	vm.State = StateTerminating
	s.clock.After(s.Transition, func() {
		vm.State = StateTerminated
		now := s.clock.Now()
		vm.TerminatedAt = &now
		// Recorded: a cleanly terminated VM reports exactly this, trailing
		// period included.
		reason := "Success."
		vm.StateReason = &reason
	})
}

// HasRunningVMs implements the images package's VMChecker: an image cannot be
// deleted while anything is running off it.
func (s *Service) HasRunningVMs(region, imageArn string) bool {
	for _, vm := range s.List(region) {
		if vm.ImageArn != imageArn {
			continue
		}
		if !vm.Terminal() {
			return true
		}
	}
	return false
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
