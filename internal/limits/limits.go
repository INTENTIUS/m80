// Package limits is m80's throttle and quota emulation: the levers a
// QuotaGuard-style client needs in order to be tested offline.
//
// Two of these behaviors are recorded and one is not, and the difference
// matters. The account memory ceiling is recorded — six concurrent RunMicrovm
// calls against a fresh account yielded two running VMs and four 402s — so it
// is on by default at the recorded ceiling. Throttling was never observed at
// all: the memory ceiling fires first and hides it, so every throttle here is
// implemented from the model and is off by default. An emulator that throttles
// by surprise is worse than one that never does.
//
// The throttle shape depends on which service the operation belongs to, and
// that is the model rather than a choice. ThrottleReason rides
// TooManyRequestsException.Reason in Lambda Core and classic Lambda; the
// Lambda Microvms model's TooManyRequestsException carries only Type and
// message, with no Reason member at all. So a chosen reason is observable on
// the connector and tags operations and is simply not expressible on the
// MicroVM ones, which throttle as ThrottlingException instead.
package limits

import (
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/intentius/m80/internal/api"
	"github.com/intentius/m80/internal/clock"
)

// ThrottleReasons is the ThrottleReason enum, shared by Lambda Core and
// classic Lambda. ConcurrentSnapshotCreateLimitExceeded is the one
// KubeMicroVM's QuotaGuard testing cares about.
var ThrottleReasons = []string{
	"ConcurrentInvocationLimitExceeded",
	"FunctionInvocationRateLimitExceeded",
	"ReservedFunctionConcurrentInvocationLimitExceeded",
	"ReservedFunctionInvocationRateLimitExceeded",
	"CallerRateLimitExceeded",
	"ConcurrentSnapshotCreateLimitExceeded",
}

func ValidThrottleReason(s string) bool {
	for _, r := range ThrottleReasons {
		if r == s {
			return true
		}
	}
	return false
}

// DefaultAccountMemoryMiB is the recorded base ceiling. Six concurrent
// RunMicrovm calls on a fresh account left two VMs running at the 2048 MiB
// default tier and rejected the other four, which puts the account's base
// allocated-memory ceiling here.
const DefaultAccountMemoryMiB = 4096

// QuotaMessage is the recorded 402 message, verbatim.
const QuotaMessage = "The base maximum allocated memory limit has been reached for this account."

// Config is the knob set. The zero value throttles nothing and caps nothing,
// which is what a caller who has not asked for either should get.
type Config struct {
	// RequestsPerInterval and Interval bound the request rate. Zero
	// RequestsPerInterval means no rate limit.
	RequestsPerInterval int
	IntervalSeconds     int

	// Reason rides TooManyRequestsException.Reason on the operations whose
	// model carries it. Empty means the member is omitted.
	Reason string

	// RetryAfterSeconds rides the Retry-After header. Zero omits it.
	RetryAfterSeconds int

	// MaxAccountMemoryMiB caps total allocated memory across non-terminal
	// VMs. Zero means uncapped.
	MaxAccountMemoryMiB int

	// MaxConcurrentSnapshotCreates caps in-flight image builds. Zero means
	// uncapped. Exceeding it is the one quota that maps onto a named
	// ThrottleReason.
	MaxConcurrentSnapshotCreates int

	// MaxMicrovms caps non-terminal VM count. Zero means uncapped. The
	// recording found the binding limit to be memory rather than count, so
	// this is off by default and exists for a tester who wants the simpler
	// axis.
	MaxMicrovms int
}

// Service applies the configured limits.
type Service struct {
	clock clock.Clock

	mu  sync.Mutex
	cfg Config
	// window counts requests in the current interval. A fixed window rather
	// than a token bucket: a test that wants the Nth call throttled should be
	// able to say so and count, not solve for a refill rate.
	windowStart int64
	windowCount int
}

func NewService(c clock.Clock, cfg Config) *Service {
	return &Service{clock: c, cfg: cfg}
}

// SetConfig replaces the configuration, for a test that wants to arm a
// throttle partway through.
func (s *Service) SetConfig(cfg Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	s.windowCount = 0
}

func (s *Service) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// allow reports whether the request fits inside the current rate window.
func (s *Service) allow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.RequestsPerInterval <= 0 {
		return true
	}
	interval := int64(s.cfg.IntervalSeconds)
	if interval <= 0 {
		interval = 1
	}
	now := s.clock.Now().Unix()
	if bucket := now / interval; bucket != s.windowStart {
		s.windowStart = bucket
		s.windowCount = 0
	}
	s.windowCount++
	return s.windowCount <= s.cfg.RequestsPerInterval
}

// Gate is the hook api.Server consults before every operation handler. It
// reports whether it answered the request itself.
func (s *Service) Gate(operation string, w http.ResponseWriter, r *http.Request) bool {
	if s.allow() {
		return false
	}
	s.writeThrottle(w, r)
	return true
}

// writeThrottle answers in whichever shape the operation's own service model
// defines. The MicroVM family has no Reason member to put a reason in.
func (s *Service) writeThrottle(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config()
	if cfg.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(cfg.RetryAfterSeconds))
	}

	if microvmFamily(r.URL.Path) {
		// ThrottlingException: 429, message plus the quota codes, and a
		// Retry-After header. No Reason exists on this one.
		body := map[string]any{
			"message":     "Rate exceeded",
			"quotaCode":   nil,
			"serviceCode": nil,
		}
		if cfg.RetryAfterSeconds > 0 {
			body["retryAfterSeconds"] = cfg.RetryAfterSeconds
		}
		api.WriteError(w, http.StatusTooManyRequests, "ThrottlingException", body)
		return
	}

	// TooManyRequestsException: 429, and this one does carry Reason.
	body := map[string]any{
		"Type":    "User",
		"message": "Rate exceeded",
	}
	if cfg.Reason != "" {
		body["Reason"] = cfg.Reason
	}
	if cfg.RetryAfterSeconds > 0 {
		body["retryAfterSeconds"] = cfg.RetryAfterSeconds
	}
	api.WriteError(w, http.StatusTooManyRequests, "TooManyRequestsException", body)
}

// microvmFamily reports whether a path belongs to Lambda Microvms. Connectors
// live under /2026-04-04/ and tags under /2017-03-31/, and both of those
// models carry Reason.
func microvmFamily(path string) bool { return strings.HasPrefix(path, "/2025-09-09/") }

// AllowMemory reports whether allocating another wantMiB on top of
// currentMiB stays inside the account ceiling.
func (s *Service) AllowMemory(currentMiB, wantMiB int) bool {
	cfg := s.Config()
	if cfg.MaxAccountMemoryMiB <= 0 {
		return true
	}
	return currentMiB+wantMiB <= cfg.MaxAccountMemoryMiB
}

// AllowMicrovm reports whether another non-terminal VM fits under the count
// cap, which is off unless a tester asks for it.
func (s *Service) AllowMicrovm(current int) bool {
	cfg := s.Config()
	if cfg.MaxMicrovms <= 0 {
		return true
	}
	return current+1 <= cfg.MaxMicrovms
}

// AllowSnapshotCreate reports whether another build fits under the concurrent
// snapshot-create cap.
func (s *Service) AllowSnapshotCreate(inFlight int) bool {
	cfg := s.Config()
	if cfg.MaxConcurrentSnapshotCreates <= 0 {
		return true
	}
	return inFlight+1 <= cfg.MaxConcurrentSnapshotCreates
}

// WriteQuotaExceeded answers the recorded 402.
//
// The status is the surprising part: Payment Required, not the 429 or 400
// anyone would guess. The model names the error and says nothing about its
// status, so this is only knowable by provoking it. Every detail member comes
// back null, so a client cannot branch on which quota was hit — only the
// message says.
func WriteQuotaExceeded(w http.ResponseWriter, message string) {
	if message == "" {
		message = QuotaMessage
	}
	api.WriteError(w, http.StatusPaymentRequired, "ServiceQuotaExceededException", map[string]any{
		"message":      message,
		"quotaCode":    nil,
		"resourceId":   nil,
		"resourceType": nil,
		"serviceCode":  nil,
	})
}

// WriteSnapshotThrottle answers a concurrent-snapshot-create rejection. It is
// the one quota that maps onto a named ThrottleReason, but CreateMicrovmImage
// is a MicroVM-family operation whose TooManyRequestsException has no Reason
// member — so the reason is named in the message instead of a member that
// does not exist.
func WriteSnapshotThrottle(w http.ResponseWriter, retryAfterSeconds int) {
	if retryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
	}
	body := map[string]any{
		"message":     "Rate exceeded: ConcurrentSnapshotCreateLimitExceeded",
		"quotaCode":   nil,
		"serviceCode": nil,
	}
	if retryAfterSeconds > 0 {
		body["retryAfterSeconds"] = retryAfterSeconds
	}
	api.WriteError(w, http.StatusTooManyRequests, "ThrottlingException", body)
}
