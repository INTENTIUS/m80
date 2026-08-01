package limits

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/intentius/m80/internal/api"
	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/store"
)

func harness(t *testing.T, cfg Config) (*api.Server, *Service, *clock.Test) {
	t.Helper()
	clk := clock.NewTest(time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC))
	srv := api.NewServer(clk, store.New(), "test")
	svc := NewService(clk, cfg)
	srv.Gate = svc.Gate
	// A trivial handler on two operations from different service families,
	// so the throttle's per-family shape is observable.
	srv.Register("GetMicrovm", func(w http.ResponseWriter, r *http.Request) {
		api.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	srv.Register("GetNetworkConnector", func(w http.ResponseWriter, r *http.Request) {
		api.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	return srv, svc, clk
}

func get(srv *api.Server, path string) (*httptest.ResponseRecorder, map[string]any) {
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=AKID/20260730/us-east-1/lambda/aws4_request, SignedHeaders=host, Signature=x")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	var doc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	return rec, doc
}

const (
	vmPath   = "/2025-09-09/microvms/microvm-1"
	connPath = "/2026-04-04/network-connectors/nc-1"
)

// Throttling was never observed live — the memory ceiling fires first and
// hides it — so an emulator that throttles by surprise would be inventing
// behavior. Off unless asked for.
func TestThrottlingIsOffByDefault(t *testing.T) {
	srv, _, _ := harness(t, Config{})
	for i := 0; i < 50; i++ {
		if rec, _ := get(srv, vmPath); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200 with no throttle configured", i, rec.Code)
		}
	}
}

// The MicroVM family throttles as ThrottlingException. Its model's
// TooManyRequestsException has no Reason member at all, so a configured
// reason is simply not expressible here.
func TestMicrovmFamilyThrottlesAsThrottlingException(t *testing.T) {
	srv, _, _ := harness(t, Config{
		RequestsPerInterval: 2, IntervalSeconds: 60,
		Reason: "ConcurrentSnapshotCreateLimitExceeded", RetryAfterSeconds: 7,
	})
	for i := 0; i < 2; i++ {
		if rec, _ := get(srv, vmPath); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200 inside the window", i, rec.Code)
		}
	}

	rec, doc := get(srv, vmPath)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("X-Amzn-Errortype"); got != "ThrottlingException" {
		t.Errorf("error type %q, want ThrottlingException", got)
	}
	if got := rec.Header().Get("Retry-After"); got != "7" {
		t.Errorf("Retry-After %q, want 7", got)
	}
	if doc["retryAfterSeconds"] != float64(7) {
		t.Errorf("retryAfterSeconds %v", doc["retryAfterSeconds"])
	}
	if _, has := doc["Reason"]; has {
		t.Error("ThrottlingException carries Reason; the MicroVMs model has no such member")
	}
	for _, m := range []string{"quotaCode", "serviceCode"} {
		if _, has := doc[m]; !has {
			t.Errorf("ThrottlingException missing %q", m)
		}
	}
}

// Lambda Core and classic Lambda both carry Reason on
// TooManyRequestsException, so a chosen ThrottleReason is observable there —
// which is the whole point for a QuotaGuard-style client.
func TestConnectorFamilyThrottlesWithAChosenReason(t *testing.T) {
	srv, _, _ := harness(t, Config{
		RequestsPerInterval: 1, IntervalSeconds: 60,
		Reason: "ConcurrentSnapshotCreateLimitExceeded",
	})
	if rec, _ := get(srv, connPath); rec.Code != http.StatusOK {
		t.Fatalf("first request: status %d", rec.Code)
	}

	rec, doc := get(srv, connPath)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("X-Amzn-Errortype"); got != "TooManyRequestsException" {
		t.Errorf("error type %q, want TooManyRequestsException", got)
	}
	if doc["Reason"] != "ConcurrentSnapshotCreateLimitExceeded" {
		t.Errorf("Reason %v, want the configured reason", doc["Reason"])
	}
	if doc["Type"] != "User" {
		t.Errorf("Type %v", doc["Type"])
	}
}

// An unconfigured reason omits the member rather than sending an empty
// string, which a client switching on the enum would have to special-case.
func TestReasonOmittedWhenUnset(t *testing.T) {
	srv, _, _ := harness(t, Config{RequestsPerInterval: 1, IntervalSeconds: 60})
	get(srv, connPath)
	_, doc := get(srv, connPath)
	if _, has := doc["Reason"]; has {
		t.Errorf("Reason %v present with none configured", doc["Reason"])
	}
}

// The window is fixed rather than a token bucket: a test that wants the Nth
// call throttled should be able to say so and count.
func TestWindowResetsOnTheClock(t *testing.T) {
	srv, _, clk := harness(t, Config{RequestsPerInterval: 2, IntervalSeconds: 60})
	get(srv, vmPath)
	get(srv, vmPath)
	if rec, _ := get(srv, vmPath); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third request: status %d, want 429", rec.Code)
	}

	clk.Advance(61 * time.Second)
	if rec, _ := get(srv, vmPath); rec.Code != http.StatusOK {
		t.Errorf("status %d after the window rolled, want 200", rec.Code)
	}
}

// An unimplemented operation must still report as unimplemented rather than
// as throttled — the conformance runner distinguishes the two, and a throttle
// masking a 501 would make coverage look better than it is.
func TestGateDoesNotMask501(t *testing.T) {
	clk := clock.NewTest(time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC))
	srv := api.NewServer(clk, store.New(), "test")
	srv.Gate = NewService(clk, Config{RequestsPerInterval: 1, IntervalSeconds: 60}).Gate
	// ListMicrovms is routed but has no handler registered here.
	for i := 0; i < 3; i++ {
		rec, _ := get(srv, "/2025-09-09/microvms")
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("request %d: status %d, want 501", i, rec.Code)
		}
	}
}

// The recorded 402. Payment Required is the surprising part — not the 429 or
// 400 anyone would guess — and every detail member comes back null, so a
// client cannot branch on which quota was hit.
func TestQuotaExceededMatchesTheRecordedShape(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteQuotaExceeded(rec, "")
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status %d, want 402", rec.Code)
	}
	if got := rec.Header().Get("X-Amzn-Errortype"); got != "ServiceQuotaExceededException" {
		t.Errorf("error type %q", got)
	}
	var doc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	if doc["message"] != QuotaMessage {
		t.Errorf("message %q, want the recorded %q", doc["message"], QuotaMessage)
	}
	for _, m := range []string{"quotaCode", "serviceCode", "resourceId", "resourceType"} {
		v, has := doc[m]
		if !has {
			t.Errorf("%s missing; the recorded body carries it", m)
		}
		if v != nil {
			t.Errorf("%s = %v, recorded as null", m, v)
		}
	}
}

// Recorded: six concurrent RunMicrovm calls on a fresh account left two VMs
// running at the 2048 MiB default tier and rejected four.
func TestAccountMemoryCeilingMatchesTheRecordedBurst(t *testing.T) {
	svc := NewService(clock.Real{}, Config{MaxAccountMemoryMiB: DefaultAccountMemoryMiB})
	allocated := 0
	admitted := 0
	for i := 0; i < 6; i++ {
		if svc.AllowMemory(allocated, 2048) {
			allocated += 2048
			admitted++
		}
	}
	if admitted != 2 {
		t.Errorf("%d of 6 admitted, want 2 — the recorded burst", admitted)
	}
}

func TestQuotasAreUncappedAtZero(t *testing.T) {
	svc := NewService(clock.Real{}, Config{})
	if !svc.AllowMemory(1<<20, 1<<20) {
		t.Error("memory capped with MaxAccountMemoryMiB unset")
	}
	if !svc.AllowMicrovm(10000) {
		t.Error("VM count capped with MaxMicrovms unset")
	}
	if !svc.AllowSnapshotCreate(10000) {
		t.Error("snapshot creates capped with MaxConcurrentSnapshotCreates unset")
	}
}

func TestCountAndSnapshotCaps(t *testing.T) {
	svc := NewService(clock.Real{}, Config{MaxMicrovms: 2, MaxConcurrentSnapshotCreates: 1})
	if !svc.AllowMicrovm(1) {
		t.Error("second VM rejected under a cap of 2")
	}
	if svc.AllowMicrovm(2) {
		t.Error("third VM admitted under a cap of 2")
	}
	if !svc.AllowSnapshotCreate(0) {
		t.Error("first build rejected under a cap of 1")
	}
	if svc.AllowSnapshotCreate(1) {
		t.Error("second concurrent build admitted under a cap of 1")
	}
}

// The snapshot cap is the one quota that maps onto a named ThrottleReason,
// but CreateMicrovmImage is a MicroVM-family operation whose
// TooManyRequestsException has no Reason member — so the reason is named in
// the message rather than in a member that does not exist.
func TestSnapshotThrottleNamesTheReasonInTheMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteSnapshotThrottle(rec, 5)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("X-Amzn-Errortype"); got != "ThrottlingException" {
		t.Errorf("error type %q", got)
	}
	if got := rec.Header().Get("Retry-After"); got != "5" {
		t.Errorf("Retry-After %q", got)
	}
	var doc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	msg, _ := doc["message"].(string)
	if msg == "" || msg != "Rate exceeded: ConcurrentSnapshotCreateLimitExceeded" {
		t.Errorf("message %q", msg)
	}
}

func TestThrottleReasonsMatchTheModelEnum(t *testing.T) {
	want := []string{
		"ConcurrentInvocationLimitExceeded",
		"FunctionInvocationRateLimitExceeded",
		"ReservedFunctionConcurrentInvocationLimitExceeded",
		"ReservedFunctionInvocationRateLimitExceeded",
		"CallerRateLimitExceeded",
		"ConcurrentSnapshotCreateLimitExceeded",
	}
	if len(ThrottleReasons) != len(want) {
		t.Fatalf("%d reasons, want %d", len(ThrottleReasons), len(want))
	}
	for i, r := range want {
		if ThrottleReasons[i] != r {
			t.Errorf("reason %d is %q, want %q", i, ThrottleReasons[i], r)
		}
		if !ValidThrottleReason(r) {
			t.Errorf("%q rejected by ValidThrottleReason", r)
		}
	}
	if ValidThrottleReason("NotAReason") {
		t.Error("ValidThrottleReason accepted a made-up reason")
	}
}

// The 402 body is recorded and names no number and no knob, so the only place
// an operator can learn why a run was rejected is the log. This is the line
// that would have saved twenty UAT cases' worth of diagnosis.
func TestExplainQuotaNamesTheNumbersAndTheKnob(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	ExplainQuota(log, 4096, 2048, 4096)

	out := buf.String()
	for _, want := range []string{
		"account memory ceiling reached",
		"allocatedMiB=4096",
		"requestedMiB=2048",
		"ceilingMiB=4096",
		"-max-account-memory-mib",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log line is missing %q:\n%s", want, out)
		}
	}
}

// A nil logger is the zero value a caller that never wired one has, and it
// must not panic on the rejection path.
func TestExplainQuotaToleratesNilLogger(t *testing.T) {
	ExplainQuota(nil, 1, 2, 3)
}
