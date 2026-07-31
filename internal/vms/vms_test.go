package vms

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
