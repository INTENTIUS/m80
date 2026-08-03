package connectors

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/store"
)

type Service struct {
	clock clock.Clock
	store *store.Store

	// Transition is one hop of the connector state machine: PENDING to
	// ACTIVE, DELETING to gone.
	Transition time.Duration

	mu sync.Mutex
	// failNext arms the injection lever, keyed by connector name so one
	// failing connector does not poison a suite running several. Same shape
	// as the images build lever, for the same reason.
	failNext map[string]string // name -> reason code
}

func NewService(c clock.Clock, s *store.Store, transition time.Duration) *Service {
	return &Service{clock: c, store: s, Transition: transition, failNext: map[string]string{}}
}

func (s *Service) collection(region string) *store.Collection[*Connector] {
	return store.CollectionOf[*Connector](s.store.Region(region), "network-connectors")
}

// FailNext arms the injection lever: the next connector created with this
// name settles into FAILED carrying the given reason code, instead of ACTIVE.
//
// The seven reason codes are the point. Each is a realistic VPC failure that
// a test cannot provoke against real AWS on demand — you cannot ask EC2 to
// run a subnet out of addresses — so an emulator that cannot produce them
// leaves the consumer's whole error-handling path untested.
func (s *Service) FailNext(name, reasonCode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext[name] = reasonCode
}

func (s *Service) takeFailFlag(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	code, ok := s.failNext[name]
	if ok {
		delete(s.failNext, name)
	}
	return code, ok
}

// Get resolves a connector by id, name or ARN. The model's identifier shape
// admits all three, so a client that kept the ARN from create can read back
// with it.
func (s *Service) Get(region, identifier string) (*Connector, bool) {
	c := s.collection(region)
	if conn, ok := c.Get(identifier); ok {
		return conn, true
	}
	if i := strings.LastIndex(identifier, ":network-connector:"); i >= 0 {
		if conn, ok := c.Get(identifier[i+len(":network-connector:"):]); ok {
			return conn, true
		}
	}
	for _, key := range c.Keys() {
		if conn, ok := c.Get(key); ok && conn.Name == identifier {
			return conn, true
		}
	}
	return nil, false
}

// ByName finds a connector by name, for the create-time uniqueness check.
func (s *Service) ByName(region, name string) (*Connector, bool) {
	c := s.collection(region)
	for _, key := range c.Keys() {
		if conn, ok := c.Get(key); ok && conn.Name == name {
			return conn, true
		}
	}
	return nil, false
}

// List returns connectors sorted by id so responses are stable.
func (s *Service) List(region string) []*Connector {
	c := s.collection(region)
	keys := c.Keys()
	sortStrings(keys)
	out := make([]*Connector, 0, len(keys))
	for _, k := range keys {
		if conn, ok := c.Get(k); ok {
			out = append(out, conn)
		}
	}
	return out
}

// Snapshot copies a connector's mutable state under the lock, so a handler
// renders one consistent view rather than reading fields a transition is
// midway through changing. Same reasoning as the vms package.
func (s *Service) Snapshot(conn *Connector) Connector {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *conn
}

// Snapshots is List with every connector copied under one acquisition.
func (s *Service) Snapshots(region string) []Connector {
	conns := s.List(region)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Connector, 0, len(conns))
	for _, c := range conns {
		out = append(out, *c)
	}
	return out
}

// Create makes a connector in PENDING and schedules it to ACTIVE, or to
// FAILED when the injection lever is armed for its name.
func (s *Service) Create(region, account, name, operatorRole, clientToken string, cfg VpcEgress) *Connector {
	id := "nc-" + newUUID()
	conn := &Connector{
		ID:           id,
		Region:       region,
		Account:      account,
		Name:         name,
		Arn:          "arn:aws:lambda:" + region + ":" + account + ":network-connector:" + id,
		OperatorRole: operatorRole,
		Config:       cfg,
		State:        StatePending,
		ClientToken:  clientToken,
	}
	s.collection(region).Put(id, conn)

	code, injected := s.takeFailFlag(name)
	s.clock.After(s.Transition, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if conn.State != StatePending {
			return
		}
		now := s.clock.Now()
		conn.LastModified = &now
		if injected {
			conn.State = StateFailed
			reason := "Connector failed by m80 failure injection"
			conn.StateReason = &reason
			c := code
			conn.StateReasonCode = &c
			return
		}
		conn.State = StateActive
		// Recorded on the first read of a freshly created connector.
		reason := "Initial creation"
		conn.StateReason = &reason
	})
	return conn
}

// Update replaces the configuration. The model says the update carries the
// full VpcEgressConfiguration and replaces what was there, so this is not a
// merge.
//
// The recorded LastUpdateStatusReason is "No configuration changes detected",
// which the live service answered to an update that changed nothing. m80
// reports it the same way when the incoming configuration equals the stored
// one, and reports a plain success otherwise — the service's wording for a
// real change was never recorded, so the alternative is inventing one.
func (s *Service) Update(conn *Connector, cfg *VpcEgress, operatorRole string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	unchanged := cfg == nil || sameConfig(conn.Config, *cfg)
	if cfg != nil {
		conn.Config = *cfg
	}
	if operatorRole != "" {
		conn.OperatorRole = operatorRole
	}
	now := s.clock.Now()
	conn.LastModified = &now

	status := UpdateSuccessful
	conn.LastUpdateStatus = &status
	reason := "Configuration updated"
	if unchanged {
		reason = "No configuration changes detected"
	}
	conn.LastUpdateStatusReason = &reason
}

// Delete moves the connector to DELETING and schedules its removal. The
// delete is asynchronous and answers 202, which is why the connector stays
// readable through the window.
func (s *Service) Delete(conn *Connector) {
	s.mu.Lock()
	if conn.State == StateDeleting {
		s.mu.Unlock()
		return
	}
	conn.State = StateDeleting
	now := s.clock.Now()
	conn.LastModified = &now
	region, id := conn.Region, conn.ID
	s.mu.Unlock()

	s.clock.After(s.Transition, func() {
		s.mu.Lock()
		gone := conn.State == StateDeleting
		s.mu.Unlock()
		if gone {
			s.collection(region).Delete(id)
		}
	})
}

// InUse reports whether anything is running that references this connector.
// Nothing does yet: VMs carry managed default connectors, which are
// service-owned ARNs rather than entries in this collection, so no recorded
// case has a user connector in use. The hook exists because the recorded
// image rule — delete refuses while VMs run — is the shape this would take,
// and #18's UAT is where it would first bite.
func (s *Service) InUse(region, id string) bool { return false }

func sameConfig(a, b VpcEgress) bool {
	return a.NetworkProtocol == b.NetworkProtocol &&
		sameStrings(a.SubnetIds, b.SubnetIds) &&
		sameStrings(a.SecurityGroupIds, b.SecurityGroupIds) &&
		sameStrings(a.AssociatedComputeResourceTypes, b.AssociatedComputeResourceTypes)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("connectors: no entropy: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return strings.Join([]string{h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]}, "-")
}

// Tags and SetTags implement the tags package's Resource over connector ARNs.
// As with VMs, no recorded connector response carries a tags member, so they
// are stored and never surfaced.
func (s *Service) Tags(region, arn string) (map[string]string, bool) {
	if !strings.Contains(arn, ":network-connector:") {
		return nil, false
	}
	conn, ok := s.Get(region, arn)
	if !ok {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyTags(conn.Tags), true
}

// copyTags hands out a copy rather than the service's own map — see the same
// function in internal/vms for why a read accessor returning the live map is
// a write path nobody asked for.
func copyTags(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (s *Service) SetTags(region, arn string, tags map[string]string) bool {
	if !strings.Contains(arn, ":network-connector:") {
		return false
	}
	conn, ok := s.Get(region, arn)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conn.Tags = tags
	return true
}
