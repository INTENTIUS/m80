package tokens

import (
	"encoding/base64"
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
	vmID   = "microvm-11111111-2222-4333-8444-555555555555"
	vmHost = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee.lambda-microvm.us-east-1.on.aws"
)

// stubVMs stands in for the vms package: tokens only ever sees primitives.
type stubVMs struct {
	state      string
	autoResume bool
	missing    bool
	marker     uint64
	woken      int
}

func (s *stubVMs) LookupEndpoint(host string) (string, string, bool) {
	if s.missing || !strings.HasPrefix(host, vmHost) {
		return "", "", false
	}
	return region, vmID, true
}

func (s *stubVMs) IsEndpointHost(host string) bool {
	return strings.HasPrefix(host, vmHost)
}

func (s *stubVMs) LookupID(id string) (string, bool) {
	if s.missing || id != vmID {
		return "", false
	}
	return region, true
}

func (s *stubVMs) Status(reg, id string) (string, bool, bool) {
	if s.missing || reg != region || id != vmID {
		return "", false, false
	}
	return s.state, s.autoResume, true
}

func (s *stubVMs) RecordTraffic(reg, id string) (uint64, bool) {
	if s.missing || reg != region || id != vmID {
		return 0, false
	}
	s.marker++
	return s.marker, true
}

func (s *stubVMs) Wake(reg, id string) {
	s.woken++
	s.state = "RUNNING"
}

type harness struct {
	srv  *api.Server
	svc  *Service
	ep   *Endpoint
	vms  *stubVMs
	clk  *clock.Test
	body []byte
}

func newHarness(t *testing.T, state string) *harness {
	t.Helper()
	clk := clock.NewTest(time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC))
	srv := api.NewServer(clk, store.New(), "test")
	svc := NewService(clk)
	source := &stubVMs{state: state}
	Register(srv, svc, source)
	ep := NewEndpoint(svc, source, nil)
	srv.Intercept = ep.Intercept
	return &harness{srv: srv, svc: svc, ep: ep, vms: source, clk: clk}
}

func (h *harness) post(path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	var r *http.Request
	if body != nil {
		raw, _ := json.Marshal(body)
		r = httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
	} else {
		r = httptest.NewRequest("POST", path, nil)
	}
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=AKID/20260730/"+region+"/lambda/aws4_request, SignedHeaders=host, Signature=x")
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, r)
	var doc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	return rec, doc
}

// issue mints a token the normal way, through the API.
func (h *harness) issue(t *testing.T, minutes int, allowed []any) string {
	t.Helper()
	rec, doc := h.post("/2025-09-09/microvms/"+vmID+"/auth-token", map[string]any{
		"expirationInMinutes": minutes,
		"allowedPorts":        allowed,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("auth-token: status %d (%s)", rec.Code, rec.Body.String())
	}
	tok, _ := doc["authToken"].(map[string]any)
	v, _ := tok[HeaderName].(string)
	if v == "" {
		t.Fatalf("no %s in %v", HeaderName, doc)
	}
	return v
}

func allPorts() []any { return []any{map[string]any{"allPorts": map[string]any{}}} }

// hit calls the VM endpoint by hostname.
func (h *harness) hit(token, host string) (*httptest.ResponseRecorder, map[string]any) {
	r := httptest.NewRequest("GET", "http://"+host+"/", nil)
	r.Host = host
	if token != "" {
		r.Header.Set(HeaderName, token)
	}
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, r)
	var doc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	return rec, doc
}

// The recorded token is a JWE in compact serialization: five parts, with an
// empty encrypted-key segment because alg is "dir", and a real header
// carrying a kid. A client that parses the header must find it where the
// service puts it.
func TestTokenHasRecordedJWEShape(t *testing.T) {
	h := newHarness(t, "RUNNING")
	v := h.issue(t, 60, allPorts())

	parts := strings.Split(v, ".")
	if len(parts) != 5 {
		t.Fatalf("token has %d parts, want 5: %q", len(parts), v)
	}
	if parts[1] != "" {
		t.Errorf("encrypted-key segment %q, want empty for alg dir", parts[1])
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("header is not base64url: %v", err)
	}
	var hdr struct{ Kid, Alg, Enc string }
	if err := json.Unmarshal(raw, &hdr); err != nil {
		t.Fatalf("header is not JSON: %v (%s)", err, raw)
	}
	if hdr.Alg != "dir" || hdr.Enc != "A256GCM" {
		t.Errorf("header alg/enc %q/%q, want dir/A256GCM", hdr.Alg, hdr.Enc)
	}
	if len(hdr.Kid) != 36 {
		t.Errorf("kid %q is not a uuid", hdr.Kid)
	}
	for _, p := range parts[2:] {
		if _, err := base64.RawURLEncoding.DecodeString(p); err != nil {
			t.Errorf("segment %q is not base64url", p)
		}
	}
}

func TestTokensAreDistinct(t *testing.T) {
	h := newHarness(t, "RUNNING")
	if a, b := h.issue(t, 60, allPorts()), h.issue(t, 60, allPorts()); a == b {
		t.Error("two issued tokens are identical")
	}
}

// Recorded: a suspended VM returns a full token rather than a conflict, which
// is the order a client that means to wake a VM by calling it has to work in.
func TestSuspendedVMStillIssuesTokens(t *testing.T) {
	h := newHarness(t, "SUSPENDED")
	rec, doc := h.post("/2025-09-09/microvms/"+vmID+"/auth-token", map[string]any{
		"expirationInMinutes": 60,
		"allowedPorts":        allPorts(),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 against a SUSPENDED VM", rec.Code)
	}
	if tok, _ := doc["authToken"].(map[string]any); tok[HeaderName] == "" {
		t.Error("no token issued")
	}
}

func TestTerminatedVMIssuesNoToken(t *testing.T) {
	h := newHarness(t, "TERMINATED")
	rec, doc := h.post("/2025-09-09/microvms/"+vmID+"/auth-token", map[string]any{
		"expirationInMinutes": 60,
		"allowedPorts":        allPorts(),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if msg, _ := doc["message"].(string); !strings.Contains(msg, "has been terminated") {
		t.Errorf("message %q", msg)
	}
}

func TestMissingVMIs404(t *testing.T) {
	h := newHarness(t, "RUNNING")
	h.vms.missing = true
	rec, _ := h.post("/2025-09-09/microvms/"+vmID+"/auth-token", map[string]any{
		"expirationInMinutes": 60,
		"allowedPorts":        allPorts(),
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

// expirationInMinutes and allowedPorts are both required by the model, and
// expiry is documented at a maximum of 60.
func TestAuthTokenRequestValidation(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"no expiry", map[string]any{"allowedPorts": allPorts()}, "expirationInMinutes"},
		{"no ports", map[string]any{"expirationInMinutes": 60}, "allowedPorts"},
		{"empty ports", map[string]any{"expirationInMinutes": 60, "allowedPorts": []any{}}, "allowedPorts"},
		{"expiry zero", map[string]any{"expirationInMinutes": 0, "allowedPorts": allPorts()}, "greater than or equal to 1"},
		{"expiry over max", map[string]any{"expirationInMinutes": 61, "allowedPorts": allPorts()}, "less than or equal to 60"},
		{"port out of range", map[string]any{"expirationInMinutes": 60,
			"allowedPorts": []any{map[string]any{"port": 70000}}}, "between 1 and 65535"},
		{"range inverted", map[string]any{"expirationInMinutes": 60,
			"allowedPorts": []any{map[string]any{"range": map[string]any{"startPort": 900, "endPort": 100}}}},
			"less than or equal to endPort"},
		// PortSpecification is a union: exactly one member may be set.
		{"union with two members", map[string]any{"expirationInMinutes": 60,
			"allowedPorts": []any{map[string]any{"port": 80, "allPorts": map[string]any{}}}}, "Exactly one of"},
		{"union with none", map[string]any{"expirationInMinutes": 60,
			"allowedPorts": []any{map[string]any{}}}, "Exactly one of"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, "RUNNING")
			rec, doc := h.post("/2025-09-09/microvms/"+vmID+"/auth-token", c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", rec.Code)
			}
			if got := rec.Header().Get("X-Amzn-Errortype"); got != "ValidationException" {
				t.Errorf("error type %q", got)
			}
			if msg, _ := doc["message"].(string); !strings.Contains(msg, c.want) {
				t.Errorf("message %q, want it to mention %q", msg, c.want)
			}
		})
	}
}

// The shell token can only ever fail. SHELL_INGRESS is absent from the
// service model entirely, so no request could make it succeed.
func TestShellAuthTokenIsAlwaysTheRecordedRejection(t *testing.T) {
	h := newHarness(t, "RUNNING")
	rec, doc := h.post("/2025-09-09/microvms/"+vmID+"/shell-auth-token",
		map[string]any{"expirationInMinutes": 60})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if got := rec.Header().Get("X-Amzn-Errortype"); got != "ValidationException" {
		t.Errorf("error type %q, want ValidationException", got)
	}
	want := "Shell access requires SHELL_INGRESS network connector to be configured on the MicroVM."
	if doc["message"] != want {
		t.Errorf("message %q, want the recorded %q", doc["message"], want)
	}
}

func TestShellAuthTokenStillValidatesExpiry(t *testing.T) {
	h := newHarness(t, "RUNNING")
	rec, doc := h.post("/2025-09-09/microvms/"+vmID+"/shell-auth-token", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if msg, _ := doc["message"].(string); !strings.Contains(msg, "expirationInMinutes") {
		t.Errorf("message %q, want the required-member error before the shell rejection", msg)
	}
}

func TestEndpointServesRunningVMWithValidToken(t *testing.T) {
	h := newHarness(t, "RUNNING")
	tok := h.issue(t, 60, allPorts())

	rec, doc := h.hit(tok, vmHost)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
	}
	if doc["microvmId"] != vmID {
		t.Errorf("microvmId %v", doc["microvmId"])
	}
	if doc["stateMarker"] != float64(1) {
		t.Errorf("stateMarker %v, want 1 on the first request", doc["stateMarker"])
	}
	if got := rec.Header().Get("X-M80-State-Marker"); got != "1" {
		t.Errorf("marker header %q, want 1", got)
	}
}

// The marker counts endpoint traffic, which is what makes it evidence the VM
// kept its state rather than being rebuilt.
func TestEndpointAdvancesTheMarker(t *testing.T) {
	h := newHarness(t, "RUNNING")
	tok := h.issue(t, 60, allPorts())
	for i := 1; i <= 3; i++ {
		_, doc := h.hit(tok, vmHost)
		if doc["stateMarker"] != float64(i) {
			t.Fatalf("request %d: stateMarker %v", i, doc["stateMarker"])
		}
	}
}

// Recorded: a missing token and an unparseable one are the same failure, and
// both are 403. The 401 m80 used to answer for a missing header was a guess,
// and a reasonable one — a client retrying with a fresh token would want them
// apart — but AWS does not distinguish them.
func TestEndpointRejectsMissingAndBadTokensIdentically(t *testing.T) {
	h := newHarness(t, "RUNNING")
	h.issue(t, 60, allPorts()) // a valid token exists, just not the one presented

	for _, presented := range []string{"", "not-a-token"} {
		rec, _ := h.hit(presented, vmHost)
		if rec.Code != http.StatusForbidden {
			t.Errorf("token %q: status %d, want 403", presented, rec.Code)
		}
		if got := rec.Body.String(); got != "Request missing authentication" {
			t.Errorf("token %q: body %q", presented, got)
		}
	}
}

// A hostname shaped like an endpoint but naming no VM is refused the same way
// a token for the wrong VM is, rather than falling through to the control
// plane's 404. Recorded against a made-up uuid under the real domain.
func TestEndpointUnknownHostRejectsLikeAMismatchedToken(t *testing.T) {
	h := newHarness(t, "RUNNING")
	tok := h.issue(t, 60, allPorts())
	h.vms.missing = true

	rec, _ := h.hit(tok, vmHost)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403", rec.Code)
	}
	if got := rec.Body.String(); got != "Token authentication failed" {
		t.Errorf("body %q", got)
	}
}

func TestEndpointRejectsExpiredToken(t *testing.T) {
	h := newHarness(t, "RUNNING")
	tok := h.issue(t, 60, allPorts())
	if rec, _ := h.hit(tok, vmHost); rec.Code != http.StatusOK {
		t.Fatalf("token rejected while still valid: %d", rec.Code)
	}

	h.clk.Advance(61 * time.Minute)
	if rec, _ := h.hit(tok, vmHost); rec.Code != http.StatusForbidden {
		t.Errorf("expired token: status %d, want 403", rec.Code)
	}
}

// allowedPorts does not gate the endpoint the control plane hands out. A
// token granting only 8080 was recorded serving 443, so the earlier
// enforcement here — reasonable as it looked — would have failed requests
// real AWS answers. The grant is still parsed and validated at issue time,
// because the control plane does reject a malformed one.
func TestEndpointDoesNotEnforceAllowedPorts(t *testing.T) {
	h := newHarness(t, "RUNNING")
	tok := h.issue(t, 60, []any{map[string]any{"port": 8080}})

	for _, host := range []string{vmHost, vmHost + ":8080", vmHost + ":9000"} {
		if rec, _ := h.hit(tok, host); rec.Code != http.StatusOK {
			t.Errorf("host %s: status %d, want 200", host, rec.Code)
		}
	}
}

// A token is scoped to the VM it was issued for.
func TestEndpointRejectsAnotherVMsToken(t *testing.T) {
	h := newHarness(t, "RUNNING")
	other := h.svc.Issue(region, "microvm-99999999-9999-4999-8999-999999999999",
		time.Hour, true, nil, false)
	if rec, _ := h.hit(other.Value, vmHost); rec.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403 for another VM's token", rec.Code)
	}
}

func TestEndpointRejectsShellToken(t *testing.T) {
	h := newHarness(t, "RUNNING")
	shell := h.svc.Issue(region, vmID, time.Hour, true, nil, true)
	if rec, _ := h.hit(shell.Value, vmHost); rec.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403 for a shell token on the HTTP endpoint", rec.Code)
	}
}

// The auto-resume path, and the one guess that turned out right: calling a
// suspended VM's endpoint wakes it when its idle policy allows, and the VM
// reads RUNNING afterwards.
func TestEndpointAutoResumesSuspendedVM(t *testing.T) {
	h := newHarness(t, "RUNNING")
	tok := h.issue(t, 60, allPorts())
	h.vms.state, h.vms.autoResume = "SUSPENDED", true

	rec, doc := h.hit(tok, vmHost)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 with auto-resume enabled", rec.Code)
	}
	if h.vms.woken != 1 {
		t.Errorf("woken %d times, want 1", h.vms.woken)
	}
	if doc["state"] != "RUNNING" {
		t.Errorf("state %v, want RUNNING after the wake", doc["state"])
	}
}

func TestEndpointRefusesSuspendedVMWithoutAutoResume(t *testing.T) {
	h := newHarness(t, "RUNNING")
	tok := h.issue(t, 60, allPorts())
	h.vms.state, h.vms.autoResume = "SUSPENDED", false

	rec, _ := h.hit(tok, vmHost)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status %d, want 502", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body %q, want empty", rec.Body.String())
	}
	if h.vms.woken != 0 {
		t.Error("woke a VM whose idle policy does not allow it")
	}
}

// A terminated VM answers 502 with an empty body, not the 410 m80 guessed.
// PENDING is not recorded and shares the answer, since every unavailable VM
// that was observed gave this one.
func TestEndpointOnUnavailableVMIsBadGateway(t *testing.T) {
	for _, state := range []string{"TERMINATED", "PENDING"} {
		h := newHarness(t, "RUNNING")
		tok := h.issue(t, 60, allPorts())
		h.vms.state = state

		rec, _ := h.hit(tok, vmHost)
		if rec.Code != http.StatusBadGateway {
			t.Errorf("%s: status %d, want 502", state, rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("%s: body %q, want empty", state, rec.Body.String())
		}
	}
}

// The path-prefix form exists so a caller can reach the endpoint without
// forging a Host header or overriding DNS.
func TestEndpointReachableByPathPrefix(t *testing.T) {
	h := newHarness(t, "RUNNING")
	tok := h.issue(t, 60, allPorts())

	r := httptest.NewRequest("GET", PathPrefix+vmID+"/anything", nil)
	r.Header.Set(HeaderName, tok)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
	}
	var doc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	if doc["microvmId"] != vmID {
		t.Errorf("microvmId %v", doc["microvmId"])
	}
}

// A host that is not a VM endpoint has to fall through to the control plane,
// or the intercept would swallow the whole API.
func TestControlPlaneStillReachable(t *testing.T) {
	h := newHarness(t, "RUNNING")
	r := httptest.NewRequest("GET", "/_m80/health", nil)
	r.Host = "localhost:4290"
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("health: status %d, the endpoint intercept swallowed it", rec.Code)
	}
}

func TestUnknownEndpointHostFallsThrough(t *testing.T) {
	h := newHarness(t, "RUNNING")
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "somewhere-else.lambda-microvm.us-east-1.on.aws"
	h.vms.missing = true
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, r)
	// No VM endpoint and no control-plane route: the mux answers, not the stub.
	if rec.Code == http.StatusOK {
		t.Errorf("status %d, want the control-plane mux to reject it", rec.Code)
	}
}

// The stub stands in for the user's own image, so its body has to be theirs.
func TestStubBodyIsConfigurable(t *testing.T) {
	h := newHarness(t, "RUNNING")
	tok := h.issue(t, 60, allPorts())
	h.ep.SetBody([]byte(`{"mine":true}`))

	rec, doc := h.hit(tok, vmHost)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if doc["mine"] != true {
		t.Errorf("body %v, want the configured payload", doc)
	}
	// The marker still has to reach a caller who replaced the body.
	if got := rec.Header().Get("X-M80-State-Marker"); got != "1" {
		t.Errorf("marker header %q, want it present alongside a custom body", got)
	}
}
