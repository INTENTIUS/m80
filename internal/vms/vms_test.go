package vms

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/intentius/m80/internal/api"
	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/store"
)

const (
	region = "us-east-1"
	imgArn = "arn:aws:lambda:us-east-1:000000000000:microvm-image:img"
	hop    = time.Second
)

// stubImages resolves one image, or nothing when runnable is false — which is
// how a still-building image looks to vms.
type stubImages struct{ runnable bool }

func (s stubImages) ResolveRunnable(region, identifier string) (string, string, bool) {
	if !s.runnable {
		return "", "", false
	}
	if identifier != imgArn && identifier != "img" {
		return "", "", false
	}
	return imgArn, "1.0", true
}

type harness struct {
	srv *api.Server
	svc *Service
	clk *clock.Test
}

func newHarness(t *testing.T, images ImageResolver) *harness {
	t.Helper()
	clk := clock.NewTest(time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC))
	st := store.New()
	srv := api.NewServer(clk, st, "test")
	svc := NewService(clk, st, hop)
	Register(srv, svc, images)
	return &harness{srv: srv, svc: svc, clk: clk}
}

func (h *harness) do(method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	var r *http.Request
	if body != nil {
		raw, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=AKID/20260730/"+region+"/lambda/aws4_request, SignedHeaders=host, Signature=x")
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, r)
	var doc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	return rec, doc
}

func (h *harness) run(t *testing.T) (string, map[string]any) {
	t.Helper()
	rec, doc := h.do("POST", "/2025-09-09/microvms", map[string]any{"imageIdentifier": imgArn})
	if rec.Code != http.StatusOK {
		t.Fatalf("run: status %d (%s)", rec.Code, rec.Body.String())
	}
	return doc["microvmId"].(string), doc
}

// runIdle launches a VM carrying an idle policy and settles it into RUNNING,
// which is where every timer test starts.
func (h *harness) runIdle(t *testing.T, maxIdleSec, suspendedSec int) string {
	t.Helper()
	policy := map[string]any{"autoResumeEnabled": false}
	if maxIdleSec > 0 {
		policy["maxIdleDurationSeconds"] = maxIdleSec
	}
	if suspendedSec > 0 {
		policy["suspendedDurationSeconds"] = suspendedSec
	}
	rec, doc := h.do("POST", "/2025-09-09/microvms", map[string]any{
		"imageIdentifier": imgArn,
		"idlePolicy":      policy,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("run: status %d (%s)", rec.Code, rec.Body.String())
	}
	h.clk.Advance(hop)
	return doc["microvmId"].(string)
}

func (h *harness) state(t *testing.T, id string) string {
	t.Helper()
	rec, doc := h.do("GET", "/2025-09-09/microvms/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get %s: status %d", id, rec.Code)
	}
	s, _ := doc["state"].(string)
	return s
}

func (h *harness) vm(t *testing.T, id string) *VM {
	t.Helper()
	vm, ok := h.svc.Get(region, id)
	if !ok {
		t.Fatalf("VM %s not in the store", id)
	}
	return vm
}

// VM ids are microvm-<uuid>; the mv-… in the early docs was a guess, and the
// wrong shape made the live API gateway answer with an HTML 502.
func TestVMIdShape(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id, _ := h.run(t)
	if !strings.HasPrefix(id, "microvm-") {
		t.Fatalf("id %q does not start with microvm-", id)
	}
	if n := len(strings.TrimPrefix(id, "microvm-")); n != 36 {
		t.Errorf("id %q: uuid part is %d chars, want 36", id, n)
	}
}

func TestRunSettlesPendingToRunning(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id, doc := h.run(t)
	if doc["state"] != StatePending {
		t.Fatalf("state %v, want PENDING", doc["state"])
	}
	h.clk.Advance(hop)
	_, doc = h.do("GET", "/2025-09-09/microvms/"+id, nil)
	if doc["state"] != StateRunning {
		t.Fatalf("state %v, want RUNNING", doc["state"])
	}
}

// Both directions carry a managed default connector, and HTTP_INGRESS is not
// in the model's enum at all.
func TestRunCarriesManagedDefaultConnectors(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	_, doc := h.run(t)
	egress := doc["egressNetworkConnectors"].([]any)
	ingress := doc["ingressNetworkConnectors"].([]any)
	if len(egress) != 1 || !strings.HasSuffix(egress[0].(string), ":INTERNET_EGRESS") {
		t.Errorf("egress %v", egress)
	}
	if len(ingress) != 1 || !strings.HasSuffix(ingress[0].(string), ":HTTP_INGRESS") {
		t.Errorf("ingress %v", ingress)
	}
	if doc["maximumDurationInSeconds"] != float64(MaximumDurationSeconds) {
		t.Errorf("maximumDurationInSeconds %v, want 28800", doc["maximumDurationInSeconds"])
	}
	if ep, _ := doc["endpoint"].(string); !strings.Contains(ep, ".lambda-microvm."+region+".on.aws") {
		t.Errorf("endpoint %q", ep)
	}
}

// Terminate answers 200 with an empty object, not the VM.
func TestTerminateReturnsEmptyObject(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id, _ := h.run(t)
	h.clk.Advance(hop)

	rec, doc := h.do("DELETE", "/2025-09-09/microvms/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if len(doc) != 0 {
		t.Errorf("body %v, want {}", doc)
	}
}

func TestTerminateWalksThroughTerminating(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id, _ := h.run(t)
	h.clk.Advance(hop)
	h.do("DELETE", "/2025-09-09/microvms/"+id, nil)

	_, doc := h.do("GET", "/2025-09-09/microvms/"+id, nil)
	if doc["state"] != StateTerminating {
		t.Fatalf("state %v, want TERMINATING", doc["state"])
	}
	h.clk.Advance(hop)
	_, doc = h.do("GET", "/2025-09-09/microvms/"+id, nil)
	if doc["state"] != StateTerminated {
		t.Fatalf("state %v, want TERMINATED", doc["state"])
	}
	if doc["stateReason"] != "Success." {
		t.Errorf("stateReason %v, want \"Success.\"", doc["stateReason"])
	}
	if doc["terminatedAt"] == nil {
		t.Error("terminatedAt not set on a terminated VM")
	}
}

// Recorded and surprising: a terminal-state mutation is a plain 400
// ValidationException, not either conflict type the model offers.
func TestMutatingTerminatedVMIs400Validation(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id, _ := h.run(t)
	h.clk.Advance(hop)
	h.do("DELETE", "/2025-09-09/microvms/"+id, nil)
	h.clk.Advance(hop)

	rec, doc := h.do("DELETE", "/2025-09-09/microvms/"+id, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if got := rec.Header().Get("X-Amzn-Errortype"); got != "ValidationException" {
		t.Errorf("error type %q, want ValidationException", got)
	}
	if msg, _ := doc["message"].(string); !strings.Contains(msg, "has been terminated") {
		t.Errorf("message %q", msg)
	}
}

// Terminated VMs stay listed. This is why a recorded ListMicrovms fixture can
// never match a fresh emulator.
func TestTerminatedVMsStayListed(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id, _ := h.run(t)
	h.clk.Advance(hop)
	h.do("DELETE", "/2025-09-09/microvms/"+id, nil)
	h.clk.Advance(hop)

	_, doc := h.do("GET", "/2025-09-09/microvms", nil)
	items := doc["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("%d items, want the terminated VM retained", len(items))
	}
	item := items[0].(map[string]any)
	if item["state"] != StateTerminated {
		t.Errorf("state %v", item["state"])
	}
	// List is a five-member summary, not the detail shape.
	if _, has := item["endpoint"]; has {
		t.Error("list item carries endpoint; it should be the summary")
	}
}

// An image with nothing built cannot back a VM: better a miss than a VM that
// would never start.
func TestRunAgainstUnrunnableImageIs404(t *testing.T) {
	h := newHarness(t, stubImages{runnable: false})
	rec, _ := h.do("POST", "/2025-09-09/microvms", map[string]any{"imageIdentifier": imgArn})
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

// autoResumeEnabled is required whenever idlePolicy is present, though the
// model marks no member of it required.
func TestIdlePolicyRequiresAutoResumeEnabled(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	rec, doc := h.do("POST", "/2025-09-09/microvms", map[string]any{
		"imageIdentifier": imgArn,
		"idlePolicy":      map[string]any{"maxIdleDurationSeconds": 900},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if msg, _ := doc["message"].(string); !strings.Contains(msg, "autoResumeEnabled") {
		t.Errorf("message %q", msg)
	}
}

func TestIdlePolicyEchoedWhenGiven(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	_, doc := h.do("POST", "/2025-09-09/microvms", map[string]any{
		"imageIdentifier": imgArn,
		"idlePolicy": map[string]any{
			"autoResumeEnabled":        false,
			"maxIdleDurationSeconds":   900,
			"suspendedDurationSeconds": 1800,
		},
	})
	policy, ok := doc["idlePolicy"].(map[string]any)
	if !ok {
		t.Fatalf("idlePolicy %v", doc["idlePolicy"])
	}
	if policy["maxIdleDurationSeconds"] != float64(900) || policy["autoResumeEnabled"] != false {
		t.Errorf("idlePolicy %v", policy)
	}

	// Absent on the request means null on the response, not an empty object.
	_, doc = h.do("POST", "/2025-09-09/microvms", map[string]any{"imageIdentifier": imgArn})
	if doc["idlePolicy"] != nil {
		t.Errorf("idlePolicy %v, want null", doc["idlePolicy"])
	}
}

// The rule images enforces on delete comes from here.
func TestHasRunningVMs(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	if h.svc.HasRunningVMs(region, imgArn) {
		t.Error("reported running VMs with none created")
	}
	id, _ := h.run(t)
	h.clk.Advance(hop)
	if !h.svc.HasRunningVMs(region, imgArn) {
		t.Error("a RUNNING VM was not reported")
	}
	if h.svc.HasRunningVMs(region, "arn:aws:lambda:us-east-1:000000000000:microvm-image:other") {
		t.Error("a VM was attributed to the wrong image")
	}

	h.do("DELETE", "/2025-09-09/microvms/"+id, nil)
	h.clk.Advance(hop)
	if h.svc.HasRunningVMs(region, imgArn) {
		t.Error("a TERMINATED VM still blocks image deletion")
	}
}

func TestVMsAreRegionScoped(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	h.run(t)

	r := httptest.NewRequest("GET", "/2025-09-09/microvms", nil)
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=AKID/20260730/eu-west-1/lambda/aws4_request, SignedHeaders=host, Signature=x")
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, r)
	var doc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	if n := len(doc["items"].([]any)); n != 0 {
		t.Errorf("eu-west-1 sees %d us-east-1 VMs", n)
	}
}

func TestMissingVMIs404(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	rec, _ := h.do("GET", "/2025-09-09/microvms/microvm-00000000-0000-0000-0000-000000000000", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

// SUSPENDING was never sampled live at a five-second poll, but it is in the
// enum and a faster client can see it, so m80 goes through it.
func TestSuspendWalksThroughSuspending(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id := h.runIdle(t, 0, 0)

	rec, doc := h.do("POST", "/2025-09-09/microvms/"+id+"/suspend", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("suspend: status %d (%s)", rec.Code, rec.Body.String())
	}
	if len(doc) != 0 {
		t.Errorf("suspend body %v, want {}", doc)
	}
	if got := h.state(t, id); got != StateSuspending {
		t.Fatalf("state %v, want SUSPENDING", got)
	}
	h.clk.Advance(hop)
	if got := h.state(t, id); got != StateSuspended {
		t.Fatalf("state %v, want SUSPENDED", got)
	}
}

// The recorded asymmetry: the same five-second poll that caught PENDING on
// the initial launch saw nothing at all between SUSPENDED and RUNNING. There
// is no RESUMING in the enum, and resume does not go back through PENDING.
func TestResumeGoesStraightToRunningWithoutPending(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id := h.runIdle(t, 0, 0)
	h.do("POST", "/2025-09-09/microvms/"+id+"/suspend", nil)
	h.clk.Advance(hop)

	rec, doc := h.do("POST", "/2025-09-09/microvms/"+id+"/resume", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume: status %d (%s)", rec.Code, rec.Body.String())
	}
	if len(doc) != 0 {
		t.Errorf("resume body %v, want {}", doc)
	}
	// RUNNING on the very next read, with no transition hop advanced.
	if got := h.state(t, id); got != StateRunning {
		t.Fatalf("state %v immediately after resume, want RUNNING", got)
	}
}

// A resume that arrives while the suspend is still settling wins: the pending
// transition finds a changed generation and does nothing.
func TestResumeDuringSuspendingCancelsTheSuspend(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id := h.runIdle(t, 0, 0)
	h.do("POST", "/2025-09-09/microvms/"+id+"/suspend", nil)
	if got := h.state(t, id); got != StateSuspending {
		t.Fatalf("state %v, want SUSPENDING", got)
	}

	h.do("POST", "/2025-09-09/microvms/"+id+"/resume", nil)
	if got := h.state(t, id); got != StateRunning {
		t.Fatalf("state %v, want RUNNING", got)
	}
	// The suspend's transition is still scheduled; it must not land.
	h.clk.Advance(hop * 4)
	if got := h.state(t, id); got != StateRunning {
		t.Fatalf("state %v after the stale transition came due, want RUNNING", got)
	}
}

// Suspending something already suspended is a no-op answered 200. Unrecorded,
// and idempotence is the safer guess than an invented error for a reconciler
// that may re-issue the call.
func TestSuspendIsIdempotent(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id := h.runIdle(t, 0, 0)
	h.do("POST", "/2025-09-09/microvms/"+id+"/suspend", nil)
	h.clk.Advance(hop)

	rec, _ := h.do("POST", "/2025-09-09/microvms/"+id+"/suspend", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if got := h.state(t, id); got != StateSuspended {
		t.Errorf("state %v, want SUSPENDED", got)
	}
}

func TestIdleTimerSuspendsAfterMaxIdle(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id := h.runIdle(t, 900, 0)

	h.clk.Advance(899 * time.Second)
	if got := h.state(t, id); got != StateRunning {
		t.Fatalf("state %v one second short of the idle window, want RUNNING", got)
	}
	h.clk.Advance(time.Second)
	if got := h.state(t, id); got != StateSuspending {
		t.Fatalf("state %v at the idle window, want SUSPENDING", got)
	}
	h.clk.Advance(hop)
	if got := h.state(t, id); got != StateSuspended {
		t.Fatalf("state %v, want SUSPENDED", got)
	}
}

// Endpoint traffic resets the countdown. The clock has no cancel, so the
// armed timer fires, finds newer activity than it expected and re-arms for
// the remainder — the VM must not suspend on that first firing.
func TestEndpointTrafficDefersIdleSuspend(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id := h.runIdle(t, 900, 0)
	vm := h.vm(t, id)

	h.clk.Advance(400 * time.Second)
	h.svc.Touch(vm)

	// The original timer comes due here and must decline to act.
	h.clk.Advance(500 * time.Second)
	if got := h.state(t, id); got != StateRunning {
		t.Fatalf("state %v after traffic reset the window, want RUNNING", got)
	}
	// 900s after the touch, not after the arming.
	h.clk.Advance(400 * time.Second)
	if got := h.state(t, id); got != StateSuspending {
		t.Fatalf("state %v 900s after the last traffic, want SUSPENDING", got)
	}
}

func TestNoIdlePolicyMeansNoIdleSuspend(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id, _ := h.run(t)
	h.clk.Advance(hop)

	h.clk.Advance(4 * time.Hour)
	if got := h.state(t, id); got != StateRunning {
		t.Errorf("state %v with no idlePolicy, want RUNNING", got)
	}
}

func TestSuspendCapTerminatesSuspendedVM(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id := h.runIdle(t, 900, 1800)
	h.do("POST", "/2025-09-09/microvms/"+id+"/suspend", nil)
	h.clk.Advance(hop)
	if got := h.state(t, id); got != StateSuspended {
		t.Fatalf("state %v, want SUSPENDED", got)
	}

	h.clk.Advance(1799 * time.Second)
	if got := h.state(t, id); got != StateSuspended {
		t.Fatalf("state %v one second short of the suspend cap, want SUSPENDED", got)
	}
	h.clk.Advance(2*time.Second + hop)
	if got := h.state(t, id); got != StateTerminated {
		t.Fatalf("state %v past the suspend cap, want TERMINATED", got)
	}
}

// A resume restarts the suspend cap. The first suspension's timer is stale
// and must not reclaim a VM that has since suspended a second time.
func TestSuspendCapDoesNotFireOnAStaleSuspension(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id := h.runIdle(t, 0, 1800)

	h.do("POST", "/2025-09-09/microvms/"+id+"/suspend", nil)
	h.clk.Advance(hop)
	h.clk.Advance(1000 * time.Second)
	h.do("POST", "/2025-09-09/microvms/"+id+"/resume", nil)

	// Suspend again; the first suspension's cap comes due 800s from now and
	// must be inert, because this suspension has its own full 1800s.
	h.do("POST", "/2025-09-09/microvms/"+id+"/suspend", nil)
	h.clk.Advance(hop)
	h.clk.Advance(1000 * time.Second)
	if got := h.state(t, id); got != StateSuspended {
		t.Fatalf("state %v: a stale suspend cap reclaimed the VM early", got)
	}
}

// Eight hours bounds total session life regardless of how the VM spent it.
func TestSessionCapTerminatesRegardlessOfState(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id := h.runIdle(t, 0, 0)
	h.do("POST", "/2025-09-09/microvms/"+id+"/suspend", nil)
	h.clk.Advance(hop)

	h.clk.Advance(MaximumDuration - 10*time.Second)
	if got := h.state(t, id); got != StateSuspended {
		t.Fatalf("state %v short of the session cap, want SUSPENDED", got)
	}
	h.clk.Advance(10*time.Second + hop)
	if got := h.state(t, id); got != StateTerminated {
		t.Fatalf("state %v at the eight-hour cap, want TERMINATED", got)
	}
}

// The point of the marker: state that survives a suspend and resume, which is
// what #12's endpoint stub serves back to prove the VM was not rebuilt.
func TestMarkerSurvivesSuspendResume(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id := h.runIdle(t, 900, 1800)
	vm := h.vm(t, id)

	h.svc.Touch(vm)
	h.svc.Touch(vm)
	if got := h.svc.Snapshot(vm).Marker; got != 2 {
		t.Fatalf("marker %d after two requests, want 2", got)
	}

	h.do("POST", "/2025-09-09/microvms/"+id+"/suspend", nil)
	h.clk.Advance(hop)
	h.do("POST", "/2025-09-09/microvms/"+id+"/resume", nil)

	if got := h.svc.Snapshot(vm).Marker; got != 2 {
		t.Fatalf("marker %d across suspend and resume, want it preserved at 2", got)
	}
	if got := h.svc.Touch(vm); got != 3 {
		t.Errorf("marker %d on the next request, want it to keep counting at 3", got)
	}
}

// The marker is m80's own instrumentation and must not leak onto a modeled
// response, where it would be an invented member.
func TestMarkerIsNotOnTheWire(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id := h.runIdle(t, 0, 0)
	h.svc.Touch(h.vm(t, id))

	_, doc := h.do("GET", "/2025-09-09/microvms/"+id, nil)
	for _, member := range []string{"marker", "Marker", "stateMarker", "lastActivity"} {
		if _, has := doc[member]; has {
			t.Errorf("GetMicrovm response carries %q", member)
		}
	}
}

// Case 82: suspending a terminated VM is a plain 400 ValidationException, not
// either conflict type the model offers. Resume takes the same path.
func TestSuspendAndResumeOnTerminatedVMAre400(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id := h.runIdle(t, 0, 0)
	h.do("DELETE", "/2025-09-09/microvms/"+id, nil)
	h.clk.Advance(hop)
	if got := h.state(t, id); got != StateTerminated {
		t.Fatalf("state %v, want TERMINATED", got)
	}

	for _, action := range []string{"suspend", "resume"} {
		rec, doc := h.do("POST", "/2025-09-09/microvms/"+id+"/"+action, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", action, rec.Code)
		}
		if got := rec.Header().Get("X-Amzn-Errortype"); got != "ValidationException" {
			t.Errorf("%s: error type %q, want ValidationException", action, got)
		}
		if msg, _ := doc["message"].(string); !strings.Contains(msg, "has been terminated") {
			t.Errorf("%s: message %q", action, msg)
		}
	}
}

func TestSuspendOnMissingVMIs404(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	rec, _ := h.do("POST", "/2025-09-09/microvms/microvm-00000000-0000-0000-0000-000000000000/suspend", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

// Every other test here drives clock.Test, whose callbacks run on the test
// goroutine, so -race never sees the transitions and handlers touching a VM
// at once. This one runs the real clock and reads while they fire, which is
// the arrangement the shipped binary is actually in.
func TestTransitionsAndHandlersDoNotRace(t *testing.T) {
	st := store.New()
	clk := clock.Real{}
	srv := api.NewServer(clk, st, "test")
	svc := NewService(clk, st, time.Millisecond)
	Register(srv, svc, stubImages{runnable: true})

	get := func(path string) {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization",
			"AWS4-HMAC-SHA256 Credential=AKID/20260730/"+region+"/lambda/aws4_request, SignedHeaders=host, Signature=x")
		srv.ServeHTTP(httptest.NewRecorder(), r)
	}
	post := func(path string, body any) {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
		r.Header.Set("Authorization",
			"AWS4-HMAC-SHA256 Credential=AKID/20260730/"+region+"/lambda/aws4_request, SignedHeaders=host, Signature=x")
		srv.ServeHTTP(httptest.NewRecorder(), r)
	}

	idle := 1
	vm := svc.Run(region, imgArn, "1.0", &IdlePolicy{
		AutoResumeEnabled:      false,
		MaxIdleDurationSeconds: &idle,
	})

	var wg sync.WaitGroup
	deadline := time.Now().Add(150 * time.Millisecond)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				get("/2025-09-09/microvms/" + vm.ID)
				get("/2025-09-09/microvms")
				svc.Touch(vm)
				svc.HasRunningVMs(region, imgArn)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for time.Now().Before(deadline) {
			post("/2025-09-09/microvms/"+vm.ID+"/suspend", nil)
			post("/2025-09-09/microvms/"+vm.ID+"/resume", nil)
		}
	}()
	wg.Wait()
}

// A suspended VM still blocks its image's deletion; only a terminal one frees
// it.
func TestSuspendedVMStillBlocksImageDeletion(t *testing.T) {
	h := newHarness(t, stubImages{runnable: true})
	id := h.runIdle(t, 0, 0)
	h.do("POST", "/2025-09-09/microvms/"+id+"/suspend", nil)
	h.clk.Advance(hop)

	if !h.svc.HasRunningVMs(region, imgArn) {
		t.Error("a SUSPENDED VM did not block image deletion")
	}
}
