// Package vms implements the MicroVM resource: run, get, list, suspend,
// resume, terminate, and the three timers that bound a VM's life.
//
// Two recorded facts shape everything here. VM ids are microvm-<uuid>, not
// the mv-… the docs guessed, and every VM carries managed default connectors
// on both directions — INTERNET_EGRESS out and HTTP_INGRESS in — neither of
// which appears in the model's NetworkConnectorType enum.
//
// Every mutable field of every VM is guarded by the service mutex. The
// transitions run on clock callbacks, which under clock.Real are separate
// goroutines, while handlers read the same fields to build a response. Tests
// use clock.Test, whose callbacks run on the test goroutine, so -race cannot
// see that collision; it is real in the shipped binary regardless.
package vms

import (
	"net"
	"strings"
	"sync"
	"time"

	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/store"
)

// VM states, from MicrovmState. There is no RESUMING: recorded, a resume goes
// SUSPENDED straight to RUNNING with nothing sampled between.
const (
	StatePending     = "PENDING"
	StateRunning     = "RUNNING"
	StateSuspending  = "SUSPENDING"
	StateSuspended   = "SUSPENDED"
	StateTerminating = "TERMINATING"
	StateTerminated  = "TERMINATED"
)

// MaximumDurationSeconds is the eight-hour session cap every VM reports, and
// the deadline the service enforces against it whatever state the VM is in.
const MaximumDurationSeconds = 28800

// MaximumDuration is MaximumDurationSeconds as a duration.
const MaximumDuration = MaximumDurationSeconds * time.Second

// IdlePolicy is echoed back as given. autoResumeEnabled is required whenever
// the policy is present at all — recorded, and the model marks no member of
// it required.
//
// The policy is written once, at Run, and read thereafter; nothing mutates it
// in place, which is why a VM snapshot can share the pointer.
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

	// LastActivity is when the endpoint last saw traffic. The idle timer
	// measures from here, not from when it happened to be armed.
	LastActivity time.Time

	// Marker is the state marker: a monotonic counter bumped by every
	// endpoint request and never reset, so a client that reads it through the
	// endpoint stub (#12) across a suspend and resume can prove the VM's
	// state survived rather than being rebuilt.
	Marker uint64

	// MemoryMiB is what this VM allocates, copied from its image at launch
	// so the account ceiling can be totalled without asking images again.
	MemoryMiB int

	// Tags is set through the tags API. No recorded VM response carries a
	// tags member, so it never reaches the wire.
	Tags map[string]string

	// stateSeq is bumped on every state change. A timer captures it when
	// armed and does nothing if it no longer matches, which is how a stale
	// idle or suspend-cap timer from an earlier RUNNING or SUSPENDED period
	// stays harmless — clock.Clock has no cancel, by design.
	stateSeq uint64
}

// Terminal reports whether the VM can still change state. Mutating a
// terminated VM is a 400 ValidationException, recorded — not either of the
// conflict types the model offers.
//
// Callers outside this package read it off a Snapshot; inside, it is only
// safe under the service mutex.
func (v *VM) Terminal() bool {
	return v.State == StateTerminated
}

type Service struct {
	clock clock.Clock
	store *store.Store

	// Transition is one hop of a VM state machine: PENDING to RUNNING,
	// SUSPENDING to SUSPENDED, TERMINATING to TERMINATED.
	Transition time.Duration

	// mu guards every mutable field of every VM this service owns. One lock
	// rather than one per VM: an emulator has no contention worth splitting,
	// and a single lock is one fewer ordering rule to get wrong.
	mu sync.Mutex
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

// Snapshot copies a VM's mutable state under the lock, so a handler renders
// one consistent view rather than reading fields a transition is midway
// through changing.
func (s *Service) Snapshot(vm *VM) VM {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *vm
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

// Snapshots is List with every VM copied under one acquisition of the lock,
// so a list response cannot show two VMs from different instants.
func (s *Service) Snapshots(region string) []VM {
	vms := s.List(region)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]VM, 0, len(vms))
	for _, vm := range vms {
		out = append(out, *vm)
	}
	return out
}

// Run creates a VM in PENDING and schedules it into RUNNING. The eight-hour
// session cap is armed here and never re-armed: it bounds total life from
// launch, regardless of how the VM spends it.
func (s *Service) Run(region, imageArn, imageVersion string, memoryMiB int, idle *IdlePolicy) *VM {
	id := "microvm-" + newUUID()
	now := s.clock.Now()
	vm := &VM{
		ID:           id,
		Region:       region,
		ImageArn:     imageArn,
		ImageVersion: imageVersion,
		State:        StatePending,
		MemoryMiB:    memoryMiB,
		StartedAt:    now,
		LastActivity: now,
		IdlePolicy:   idle,
		// The endpoint hostname is a bare UUID, not the microvm- prefixed id.
		Endpoint: newUUID() + ".lambda-microvm." + region + ".on.aws",
	}
	s.collection(region).Put(id, vm)

	seq := vm.stateSeq
	s.clock.After(s.Transition, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if vm.stateSeq != seq || vm.State != StatePending {
			return
		}
		s.enterRunningLocked(vm)
	})
	s.armSessionCap(vm)
	return vm
}

// Suspend walks a VM to SUSPENDED through SUSPENDING.
//
// UNRECORDED: suspend-non-running — safest-of-two
//
// Suspending an already suspending or suspended VM is a no-op answered 200,
// and so is suspending one on its way to TERMINATED. Neither was recorded, and
// between inventing an error type and being idempotent, idempotent is the
// safer guess for a consumer whose reconcile loop may re-issue the call.
// PENDING is allowed through for the same reason.
func (s *Service) Suspend(vm *VM) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch vm.State {
	case StatePending, StateRunning:
		s.beginSuspendLocked(vm)
	}
}

// Resume returns a suspended VM to RUNNING with no state in between.
//
// Recorded 2026-07-30: a five-second poll across a full cycle saw SUSPENDED
// then RUNNING and nothing else, while the same poll did catch PENDING on the
// initial launch — so the two paths genuinely differ and this is not a
// sampling artifact. The enum has no RESUMING to occupy anyway.
//
// A VM still in SUSPENDING resumes too: its pending transition finds a
// changed stateSeq and does nothing.
func (s *Service) Resume(vm *VM) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch vm.State {
	case StateSuspending, StateSuspended:
		s.enterRunningLocked(vm)
	}
}

// Touch records endpoint traffic: it bumps the state marker and resets the
// idle timer's reference point. #12's endpoint stub is the caller; it returns
// the marker so the stub can serve it back.
func (s *Service) Touch(vm *VM) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	vm.LastActivity = s.clock.Now()
	vm.Marker++
	return vm.Marker
}

// LookupEndpoint resolves the VM whose per-VM endpoint hostname this is, for
// the endpoint stub (#12). The region is read out of the hostname —
// <uuid>.lambda-microvm.<region>.on.aws — and only that region is searched,
// so two regions minting the same endpoint uuid could not cross over.
//
// The scan is linear in one region's VMs. A reverse index would be faster and
// would be a second thing to keep in step with every create and delete, which
// for an emulator is the worse trade.
func (s *Service) LookupEndpoint(host string) (region, id string, ok bool) {
	region, shaped := endpointRegion(host)
	if !shaped {
		return "", "", false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	for _, vm := range s.List(region) {
		if strings.EqualFold(vm.Endpoint, host) {
			return region, vm.ID, true
		}
	}
	return "", "", false
}

// IsEndpointHost reports whether a hostname is shaped like a per-VM endpoint,
// whether or not a VM currently answers to it. The endpoint has to claim
// those hosts either way: real AWS answers an unknown endpoint host with the
// same rejection as a token for the wrong VM, so falling through to the
// control-plane mux and getting its 404 would be the wrong answer.
func (s *Service) IsEndpointHost(host string) bool {
	_, shaped := endpointRegion(host)
	return shaped
}

// endpointRegion reads the region out of <uuid>.lambda-microvm.<region>.on.aws
// and reports whether the hostname has that shape at all.
func endpointRegion(host string) (region string, ok bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	parts := strings.Split(host, ".")
	if len(parts) != 5 || parts[1] != "lambda-microvm" || parts[3] != "on" || parts[4] != "aws" {
		return "", false
	}
	return parts[2], true
}

// LookupID finds which region holds a VM, for the endpoint's path-prefix form
// where there is no signed request to read a region out of.
func (s *Service) LookupID(id string) (region string, ok bool) {
	for _, name := range s.store.Regions() {
		if _, found := s.collection(name).Get(id); found {
			return name, true
		}
	}
	return "", false
}

// Status reports what the endpoint stub needs to decide how to answer: the
// VM's state, and whether its idle policy lets endpoint traffic wake it.
func (s *Service) Status(region, id string) (state string, autoResume bool, ok bool) {
	vm, found := s.Get(region, id)
	if !found {
		return "", false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return vm.State, vm.IdlePolicy != nil && vm.IdlePolicy.AutoResumeEnabled, true
}

// RecordTraffic is Touch by id: the endpoint stub has resolved a VM from a
// hostname, not from a signed control-plane request.
func (s *Service) RecordTraffic(region, id string) (marker uint64, ok bool) {
	vm, found := s.Get(region, id)
	if !found {
		return 0, false
	}
	return s.Touch(vm), true
}

// Wake is Resume by id, for the auto-resume path: a SUSPENDED VM whose idle
// policy has autoResumeEnabled comes back when its endpoint is called.
func (s *Service) Wake(region, id string) {
	vm, found := s.Get(region, id)
	if !found {
		return
	}
	s.Resume(vm)
}

// UNRECORDED: terminating-visible — follow-the-model
//
// Terminate walks the VM to TERMINATED through TERMINATING. The recording
// never sampled TERMINATING at a five-second poll, but it is in the enum and
// a faster client can see it, so m80 goes through it rather than jumping.
func (s *Service) Terminate(vm *VM) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.terminateLocked(vm)
}

// HasRunningVMs implements the images package's VMChecker: an image cannot be
// deleted while anything is running off it.
func (s *Service) HasRunningVMs(region, imageArn string) bool {
	vms := s.List(region)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, vm := range vms {
		if vm.ImageArn != imageArn {
			continue
		}
		if !vm.Terminal() {
			return true
		}
	}
	return false
}

func (s *Service) setStateLocked(vm *VM, state string) {
	vm.State = state
	vm.stateSeq++
}

// enterRunningLocked is the one way into RUNNING, from launch or from resume,
// so the idle timer is armed identically on both paths.
func (s *Service) enterRunningLocked(vm *VM) {
	s.setStateLocked(vm, StateRunning)
	vm.LastActivity = s.clock.Now()
	s.armIdleLocked(vm)
}

func (s *Service) beginSuspendLocked(vm *VM) {
	s.setStateLocked(vm, StateSuspending)
	seq := vm.stateSeq
	s.clock.After(s.Transition, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if vm.stateSeq != seq {
			return
		}
		s.setStateLocked(vm, StateSuspended)
		s.armSuspendCapLocked(vm)
	})
}

func (s *Service) terminateLocked(vm *VM) {
	if vm.State == StateTerminated || vm.State == StateTerminating {
		return
	}
	s.setStateLocked(vm, StateTerminating)
	seq := vm.stateSeq
	s.clock.After(s.Transition, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if vm.stateSeq != seq {
			return
		}
		s.setStateLocked(vm, StateTerminated)
		now := s.clock.Now()
		vm.TerminatedAt = &now
		// UNRECORDED: cap-terminated-reason — reuse-recorded
		// Recorded: a cleanly terminated VM reports exactly this, trailing
		// period included. A VM the suspend cap or the session cap ended
		// reports it too — the service's wording for those paths was never
		// recorded, and guessing a different string would put an invented
		// value on the wire.
		reason := "Success."
		vm.StateReason = &reason
	})
}

// armIdleLocked starts the idle countdown for the VM's current RUNNING
// period. No policy, or no maxIdleDurationSeconds in it, means no idle
// suspend at all.
func (s *Service) armIdleLocked(vm *VM) {
	if vm.IdlePolicy == nil || vm.IdlePolicy.MaxIdleDurationSeconds == nil {
		return
	}
	window := time.Duration(*vm.IdlePolicy.MaxIdleDurationSeconds) * time.Second
	s.armIdleAfterLocked(vm, window, vm.stateSeq)
}

// armIdleAfterLocked schedules the idle check. Because the clock has no
// cancel, traffic does not reset the timer; the timer fires, finds activity
// newer than it expected, and re-arms for the remainder. Same behavior, one
// less thing for the clock to model.
func (s *Service) armIdleAfterLocked(vm *VM, d time.Duration, seq uint64) {
	s.clock.After(d, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if vm.stateSeq != seq || vm.State != StateRunning {
			return
		}
		if vm.IdlePolicy == nil || vm.IdlePolicy.MaxIdleDurationSeconds == nil {
			return
		}
		window := time.Duration(*vm.IdlePolicy.MaxIdleDurationSeconds) * time.Second
		if idle := s.clock.Now().Sub(vm.LastActivity); idle < window {
			s.armIdleAfterLocked(vm, window-idle, seq)
			return
		}
		s.beginSuspendLocked(vm)
	})
}

// armSuspendCapLocked bounds how long a VM may sit in SUSPENDED before the
// service reclaims it.
func (s *Service) armSuspendCapLocked(vm *VM) {
	if vm.IdlePolicy == nil || vm.IdlePolicy.SuspendedDurationSeconds == nil {
		return
	}
	window := time.Duration(*vm.IdlePolicy.SuspendedDurationSeconds) * time.Second
	seq := vm.stateSeq
	s.clock.After(window, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if vm.stateSeq != seq || vm.State != StateSuspended {
			return
		}
		s.terminateLocked(vm)
	})
}

// armSessionCap bounds total session life at eight hours. It carries no
// stateSeq guard: unlike the other two it is not scoped to a state, and a VM
// that suspended and resumed six times still dies at the same wall time.
func (s *Service) armSessionCap(vm *VM) {
	s.clock.After(MaximumDuration, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.terminateLocked(vm)
	})
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Tags and SetTags implement the tags package's Resource over VM ARNs.
//
// No recorded VM response carries a tags member, so these are stored and
// never surfaced on the wire. That is deliberate: ListTags against a VM ARN
// has to work, and inventing a tags member on GetMicrovm to show them would
// be a divergence on every read.
func (s *Service) Tags(region, arn string) (map[string]string, bool) {
	id, ok := vmIDFromARN(arn)
	if !ok {
		return nil, false
	}
	vm, found := s.Get(region, id)
	if !found {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if vm.Tags == nil {
		return map[string]string{}, true
	}
	return vm.Tags, true
}

func (s *Service) SetTags(region, arn string, tags map[string]string) bool {
	id, ok := vmIDFromARN(arn)
	if !ok {
		return false
	}
	vm, found := s.Get(region, id)
	if !found {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vm.Tags = tags
	return true
}

func vmIDFromARN(arn string) (string, bool) {
	i := strings.LastIndex(arn, ":microvm:")
	if i < 0 {
		return "", false
	}
	return arn[i+len(":microvm:"):], true
}

// Allocated reports the memory currently allocated across non-terminal VMs
// and how many there are, for the account quota check. A terminated VM has
// given its memory back.
func (s *Service) Allocated(region string) (memoryMiB, count int) {
	vms := s.List(region)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, vm := range vms {
		if vm.State == StateTerminated {
			continue
		}
		memoryMiB += vm.MemoryMiB
		count++
	}
	return memoryMiB, count
}
