package connectors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/intentius/m80/internal/api"
	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/images"
	"github.com/intentius/m80/internal/store"
)

const (
	region = "us-east-1"
	hop    = time.Second
	role   = "arn:aws:iam::000000000000:role/m80-conformance-connector-operator"
)

// Two different accounts in one emulator's ARNs would be a bug no fixture
// would catch, since normalization flattens every 12-digit account to the
// same placeholder before comparing.
func TestAccountMatchesImages(t *testing.T) {
	if AccountID != images.AccountID {
		t.Fatalf("connectors account %q, images account %q", AccountID, images.AccountID)
	}
}

type harness struct {
	srv *api.Server
	svc *Service
	clk *clock.Test
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clk := clock.NewTest(time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC))
	st := store.New()
	srv := api.NewServer(clk, st, "test")
	svc := NewService(clk, st, hop)
	Register(srv, svc)
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

func createBody(name string, over ...func(map[string]any)) map[string]any {
	body := map[string]any{
		"Name": name,
		"Configuration": map[string]any{
			"VpcEgressConfiguration": map[string]any{
				"SubnetIds":                      []any{"subnet-00000000000000001"},
				"SecurityGroupIds":               []any{"sg-00000000000000001"},
				"NetworkProtocol":                "IPv4",
				"AssociatedComputeResourceTypes": []any{"MicroVm"},
			},
		},
		"ClientToken":  "m80-token-01",
		"OperatorRole": role,
	}
	for _, f := range over {
		f(body)
	}
	return body
}

func vpc(body map[string]any) map[string]any {
	return body["Configuration"].(map[string]any)["VpcEgressConfiguration"].(map[string]any)
}

func (h *harness) create(t *testing.T, name string) (string, map[string]any) {
	t.Helper()
	rec, doc := h.do("POST", "/2026-04-04/network-connectors", createBody(name))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create: status %d (%s)", rec.Code, rec.Body.String())
	}
	return doc["Id"].(string), doc
}

// Create answers 202, not 200 — it is asynchronous, like delete.
func TestCreateIsAcceptedAndPending(t *testing.T) {
	h := newHarness(t)
	id, doc := h.create(t, "conn")

	if !strings.HasPrefix(id, "nc-") {
		t.Errorf("Id %q does not start with nc-", id)
	}
	if n := len(strings.TrimPrefix(id, "nc-")); n != 36 {
		t.Errorf("Id %q: uuid part is %d chars, want 36", id, n)
	}
	if doc["State"] != StatePending {
		t.Errorf("State %v, want PENDING", doc["State"])
	}
	if arn, _ := doc["Arn"].(string); !strings.HasSuffix(arn, ":network-connector:"+id) {
		t.Errorf("Arn %q", arn)
	}
	// Create's recorded member set is exactly these six.
	want := map[string]bool{"Arn": true, "Name": true, "Id": true,
		"Configuration": true, "OperatorRole": true, "State": true}
	for k := range doc {
		if !want[k] {
			t.Errorf("create response carries unrecorded member %q", k)
		}
	}
	for k := range want {
		if _, has := doc[k]; !has {
			t.Errorf("create response missing %q", k)
		}
	}
}

func TestConnectorSettlesToActive(t *testing.T) {
	h := newHarness(t)
	id, _ := h.create(t, "conn")

	h.clk.Advance(hop)
	rec, doc := h.do("GET", "/2026-04-04/network-connectors/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status %d", rec.Code)
	}
	if doc["State"] != StateActive {
		t.Fatalf("State %v, want ACTIVE", doc["State"])
	}
	// Recorded on the first read of a freshly created connector.
	if doc["StateReason"] != "Initial creation" {
		t.Errorf("StateReason %v, want \"Initial creation\"", doc["StateReason"])
	}
	if _, has := doc["LastModified"]; !has {
		t.Error("LastModified missing on an ACTIVE connector")
	}
	// Absent, not null: a connector that has never been updated carries no
	// update status at all in the recorded response.
	for _, member := range []string{"LastUpdateStatus", "LastUpdateStatusReason",
		"LastUpdateStatusReasonCode", "StateReasonCode", "Version"} {
		if _, has := doc[member]; has {
			t.Errorf("%s present on a never-updated connector; recorded response omits it", member)
		}
	}
}

// Type is on the list summary and on none of the single-connector responses —
// recorded, and the model agrees.
func TestTypeAppearsOnlyOnTheListSummary(t *testing.T) {
	h := newHarness(t)
	id, createDoc := h.create(t, "conn")
	h.clk.Advance(hop)

	if _, has := createDoc["Type"]; has {
		t.Error("create response carries Type")
	}
	_, getDoc := h.do("GET", "/2026-04-04/network-connectors/"+id, nil)
	if _, has := getDoc["Type"]; has {
		t.Error("get response carries Type")
	}

	_, listDoc := h.do("GET", "/2026-04-04/network-connectors", nil)
	items := listDoc["NetworkConnectors"].([]any)
	if len(items) != 1 {
		t.Fatalf("%d connectors listed", len(items))
	}
	item := items[0].(map[string]any)
	if item["Type"] != TypeVpcEgress {
		t.Errorf("Type %v, want VPC_EGRESS", item["Type"])
	}
	// The summary is six members; Configuration and OperatorRole are not on it.
	for _, member := range []string{"Configuration", "OperatorRole", "StateReason"} {
		if _, has := item[member]; has {
			t.Errorf("list summary carries %q", member)
		}
	}
}

// The model carries NextMarker and the recorded response omits it entirely.
// A client looping until the marker is null would otherwise never stop.
func TestListOmitsNextMarker(t *testing.T) {
	h := newHarness(t)
	_, listDoc := h.do("GET", "/2026-04-04/network-connectors", nil)
	if _, has := listDoc["NextMarker"]; has {
		t.Error("list carries NextMarker; the recorded response omits it")
	}
}

func TestUpdateReportsNoChangesDetected(t *testing.T) {
	h := newHarness(t)
	id, _ := h.create(t, "conn")
	h.clk.Advance(hop)

	rec, doc := h.do("PUT", "/2026-04-04/network-connectors/"+id, createBody("conn"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("update: status %d (%s)", rec.Code, rec.Body.String())
	}
	if doc["LastUpdateStatus"] != UpdateSuccessful {
		t.Errorf("LastUpdateStatus %v, want Successful", doc["LastUpdateStatus"])
	}
	// Recorded: the live service answered an update that changed nothing with
	// exactly this.
	if doc["LastUpdateStatusReason"] != "No configuration changes detected" {
		t.Errorf("LastUpdateStatusReason %v", doc["LastUpdateStatusReason"])
	}
	// Update's recorded member set omits StateReason even though Get has it.
	if _, has := doc["StateReason"]; has {
		t.Error("update response carries StateReason; the recorded one does not")
	}
}

func TestUpdateReplacesConfiguration(t *testing.T) {
	h := newHarness(t)
	id, _ := h.create(t, "conn")
	h.clk.Advance(hop)

	body := createBody("conn", func(b map[string]any) {
		vpc(b)["SubnetIds"] = []any{"subnet-00000000000000009"}
		vpc(b)["NetworkProtocol"] = "DualStack"
	})
	_, doc := h.do("PUT", "/2026-04-04/network-connectors/"+id, body)
	if doc["LastUpdateStatusReason"] == "No configuration changes detected" {
		t.Error("a real change reported as no changes detected")
	}

	_, got := h.do("GET", "/2026-04-04/network-connectors/"+id, nil)
	cfg := got["Configuration"].(map[string]any)["VpcEgressConfiguration"].(map[string]any)
	if cfg["NetworkProtocol"] != "DualStack" {
		t.Errorf("NetworkProtocol %v, want the update to have replaced it", cfg["NetworkProtocol"])
	}
	subnets := cfg["SubnetIds"].([]any)
	if len(subnets) != 1 || subnets[0] != "subnet-00000000000000009" {
		t.Errorf("SubnetIds %v — update replaces rather than merges", subnets)
	}
}

// Delete is asynchronous and answers 202, so the connector stays readable
// through the DELETING window and disappears after it.
func TestDeleteIsAsynchronous(t *testing.T) {
	h := newHarness(t)
	id, _ := h.create(t, "conn")
	h.clk.Advance(hop)

	rec, doc := h.do("DELETE", "/2026-04-04/network-connectors/"+id, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("delete: status %d", rec.Code)
	}
	if doc["State"] != StateDeleting {
		t.Errorf("State %v, want DELETING", doc["State"])
	}

	if rec, _ := h.do("GET", "/2026-04-04/network-connectors/"+id, nil); rec.Code != http.StatusOK {
		t.Error("connector unreadable during the DELETING window")
	}
	h.clk.Advance(hop)
	if rec, _ := h.do("GET", "/2026-04-04/network-connectors/"+id, nil); rec.Code != http.StatusNotFound {
		t.Errorf("status %d after the delete drained, want 404", rec.Code)
	}
}

// One injected failure per reason code. None of these can be provoked against
// real AWS on demand — you cannot ask EC2 to run a subnet out of addresses —
// so without the lever the consumer's whole error path stays untested.
func TestInjectedFailurePerReasonCode(t *testing.T) {
	for _, code := range ReasonCodes {
		t.Run(code, func(t *testing.T) {
			h := newHarness(t)
			h.svc.FailNext("conn", code)
			id, _ := h.create(t, "conn")
			h.clk.Advance(hop)

			_, doc := h.do("GET", "/2026-04-04/network-connectors/"+id, nil)
			if doc["State"] != StateFailed {
				t.Fatalf("State %v, want FAILED", doc["State"])
			}
			if doc["StateReasonCode"] != code {
				t.Errorf("StateReasonCode %v, want %s", doc["StateReasonCode"], code)
			}
			if _, has := doc["StateReason"]; !has {
				t.Error("FAILED connector carries no StateReason")
			}
		})
	}
}

func TestReasonCodesMatchTheModelEnum(t *testing.T) {
	// NetworkConnectorStateReasonCode, shared verbatim with
	// NetworkConnectorLastUpdateStatusReasonCode.
	want := []string{"DisallowedByVpcEncryptionControl", "Ec2RequestLimitExceeded",
		"InsufficientRolePermissions", "InternalError", "InvalidSecurityGroup",
		"InvalidSubnet", "SubnetOutOfIPAddresses"}
	if len(ReasonCodes) != len(want) {
		t.Fatalf("%d reason codes, want %d", len(ReasonCodes), len(want))
	}
	for i, c := range want {
		if ReasonCodes[i] != c {
			t.Errorf("reason code %d is %q, want %q", i, ReasonCodes[i], c)
		}
		if !ValidReasonCode(c) {
			t.Errorf("%q not accepted by ValidReasonCode", c)
		}
	}
	if ValidReasonCode("NotAReasonCode") {
		t.Error("ValidReasonCode accepted a made-up code")
	}
}

// The lever is armed once and consumed, so one failing connector does not
// poison a suite running several.
func TestInjectionLeverIsConsumed(t *testing.T) {
	h := newHarness(t)
	h.svc.FailNext("conn", "InvalidSubnet")

	id1, _ := h.create(t, "conn")
	h.clk.Advance(hop)
	if _, doc := h.do("GET", "/2026-04-04/network-connectors/"+id1, nil); doc["State"] != StateFailed {
		t.Fatalf("first connector State %v, want FAILED", doc["State"])
	}

	id2, _ := h.create(t, "other")
	h.clk.Advance(hop)
	if _, doc := h.do("GET", "/2026-04-04/network-connectors/"+id2, nil); doc["State"] != StateActive {
		t.Errorf("second connector State %v, want ACTIVE — the lever leaked", doc["State"])
	}
}

// Four members are modeled optional and enforced anyway, each recorded live.
// A client written from the model alone gets these four 400s in a row.
func TestModeledOptionalButEnforced(t *testing.T) {
	cases := []struct {
		name string
		mut  func(map[string]any)
		want string
	}{
		{"no ClientToken", func(b map[string]any) { delete(b, "ClientToken") },
			"ClientToken is a required field"},
		{"no OperatorRole", func(b map[string]any) { delete(b, "OperatorRole") },
			"NetworkConnectorOperatorRole is required for VPC_EGRESS connector type"},
		{"no NetworkProtocol", func(b map[string]any) { delete(vpc(b), "NetworkProtocol") },
			"cannot be null or empty"},
		{"no AssociatedComputeResourceTypes", func(b map[string]any) {
			delete(vpc(b), "AssociatedComputeResourceTypes")
		}, "cannot be null or empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			rec, doc := h.do("POST", "/2026-04-04/network-connectors", createBody("conn", c.mut))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", rec.Code)
			}
			if got := rec.Header().Get("X-Amzn-Errortype"); got != "InvalidParameterValueException" {
				t.Errorf("error type %q", got)
			}
			if msg, _ := doc["message"].(string); !strings.Contains(msg, c.want) {
				t.Errorf("message %q, want it to mention %q", msg, c.want)
			}
		})
	}
}

// Model constraints come back through a validation layer in front of the
// service, in AWS's standard wording and as ValidationException — a type the
// Lambda Core model does not list on any connector operation. Recorded by the
// too-many-subnets probe; the sibling constraints follow its format.
//
// The member path is camelCase even though every wire member is PascalCase,
// which is the detail a hand-written emulator would never guess.
func TestModelConstraintsAnswerValidationException(t *testing.T) {
	many := func(prefix string, n int) []any {
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, prefix+"-0000000000000000"+string(rune('a'+i%26)))
		}
		return out
	}
	cases := []struct {
		name string
		mut  func(map[string]any)
		want string
	}{
		{"no subnets", func(b map[string]any) { vpc(b)["SubnetIds"] = []any{} },
			"at 'configuration.vpcEgressConfiguration.subnetIds' failed to satisfy constraint: Member must have length greater than or equal to 1"},
		{"17 subnets", func(b map[string]any) { vpc(b)["SubnetIds"] = many("subnet", 17) },
			"at 'configuration.vpcEgressConfiguration.subnetIds' failed to satisfy constraint: Member must have length less than or equal to 16"},
		{"6 security groups", func(b map[string]any) { vpc(b)["SecurityGroupIds"] = many("sg", 6) },
			"at 'configuration.vpcEgressConfiguration.securityGroupIds' failed to satisfy constraint: Member must have length less than or equal to 5"},
		{"bad protocol", func(b map[string]any) { vpc(b)["NetworkProtocol"] = "IPv6" },
			"at 'configuration.vpcEgressConfiguration.networkProtocol' failed to satisfy constraint: Member must satisfy enum value set: [IPv4, DualStack]"},
		{"bad compute type", func(b map[string]any) {
			vpc(b)["AssociatedComputeResourceTypes"] = []any{"Function"}
		}, "failed to satisfy constraint: Member must satisfy enum value set: [MicroVm]"},
		{"two compute types", func(b map[string]any) {
			vpc(b)["AssociatedComputeResourceTypes"] = []any{"MicroVm", "MicroVm"}
		}, "failed to satisfy constraint: Member must have length less than or equal to 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			rec, doc := h.do("POST", "/2026-04-04/network-connectors", createBody("conn", c.mut))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", rec.Code)
			}
			if got := rec.Header().Get("X-Amzn-Errortype"); got != "ValidationException" {
				t.Errorf("error type %q, want ValidationException", got)
			}
			msg, _ := doc["message"].(string)
			if !strings.HasPrefix(msg, "1 validation error detected: Value '") {
				t.Errorf("message %q does not use the recorded validation wording", msg)
			}
			if !strings.Contains(msg, c.want) {
				t.Errorf("message %q, want it to contain %q", msg, c.want)
			}
		})
	}
}

// A missing Configuration is service logic rather than a model constraint —
// the model marks it required but the union has no constraint to violate.
func TestMissingConfigurationIsInvalidParameter(t *testing.T) {
	h := newHarness(t)
	rec, doc := h.do("POST", "/2026-04-04/network-connectors",
		createBody("conn", func(b map[string]any) { delete(b, "Configuration") }))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if msg, _ := doc["message"].(string); !strings.Contains(msg, "VpcEgressConfiguration") {
		t.Errorf("message %q", msg)
	}
}

// Lambda Core answers not-found with a capital Message — its own member style,
// not the lowercase message Lambda Microvms uses — and echoes the ARN it built
// from the identifier rather than the identifier itself. Both recorded.
func TestNotFoundUsesCapitalMessageAndEchoesAnArn(t *testing.T) {
	h := newHarness(t)
	rec, doc := h.do("GET", "/2026-04-04/network-connectors/nc-m80-does-not-exist", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if _, has := doc["message"]; has {
		t.Error("body carries lowercase message; Lambda Core records a capital Message")
	}
	msg, _ := doc["Message"].(string)
	want := "Network connector not found for: arn:aws:lambda:" + region + ":" + AccountID +
		":network-connector:nc-m80-does-not-exist"
	if msg != want {
		t.Errorf("Message %q, want %q", msg, want)
	}
}

// Zero security groups is legal — the model's minimum is 0 — and must
// serialize as [] rather than null.
func TestZeroSecurityGroupsIsLegal(t *testing.T) {
	h := newHarness(t)
	rec, doc := h.do("POST", "/2026-04-04/network-connectors",
		createBody("conn", func(b map[string]any) { vpc(b)["SecurityGroupIds"] = []any{} }))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
	}
	sgs := vpc(doc)["SecurityGroupIds"]
	if sgs == nil {
		t.Error("SecurityGroupIds is null, want []")
	}
}

// ClientToken is an idempotency token: a retry with the same one returns the
// existing connector rather than a duplicate.
func TestClientTokenIsIdempotent(t *testing.T) {
	h := newHarness(t)
	id, _ := h.create(t, "conn")

	rec, doc := h.do("POST", "/2026-04-04/network-connectors", createBody("conn"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry: status %d", rec.Code)
	}
	if doc["Id"] != id {
		t.Errorf("retry minted a new connector %v, want the existing %s", doc["Id"], id)
	}

	// A different token against the same name is a genuine collision.
	rec, _ = h.do("POST", "/2026-04-04/network-connectors",
		createBody("conn", func(b map[string]any) { b["ClientToken"] = "different" }))
	if rec.Code != http.StatusConflict {
		t.Errorf("status %d, want 409", rec.Code)
	}
	if got := rec.Header().Get("X-Amzn-Errortype"); got != "ResourceConflictException" {
		t.Errorf("error type %q", got)
	}
}

// The model's identifier admits an id, a name or an ARN, so a client that
// kept the ARN from create can read back with it.
func TestLookupByIdNameAndArn(t *testing.T) {
	h := newHarness(t)
	id, doc := h.create(t, "conn")
	arn := doc["Arn"].(string)
	h.clk.Advance(hop)

	for _, ident := range []string{id, "conn", arn} {
		rec, got := h.do("GET", "/2026-04-04/network-connectors/"+ident, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("lookup by %q: status %d", ident, rec.Code)
			continue
		}
		if got["Id"] != id {
			t.Errorf("lookup by %q returned %v", ident, got["Id"])
		}
	}
}

func TestMissingConnectorIs404(t *testing.T) {
	h := newHarness(t)
	for _, m := range []struct{ method, path string }{
		{"GET", "/2026-04-04/network-connectors/nc-nope"},
		{"DELETE", "/2026-04-04/network-connectors/nc-nope"},
	} {
		rec, _ := h.do(m.method, m.path, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", m.method, rec.Code)
		}
		if got := rec.Header().Get("X-Amzn-Errortype"); got != "ResourceNotFoundException" {
			t.Errorf("%s: error type %q", m.method, got)
		}
	}
	rec, _ := h.do("PUT", "/2026-04-04/network-connectors/nc-nope", createBody("conn"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("PUT: status %d, want 404", rec.Code)
	}
}

func TestConnectorsAreRegionScoped(t *testing.T) {
	h := newHarness(t)
	h.create(t, "conn")

	r := httptest.NewRequest("GET", "/2026-04-04/network-connectors", nil)
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=AKID/20260730/eu-west-1/lambda/aws4_request, SignedHeaders=host, Signature=x")
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, r)
	var doc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	if n := len(doc["NetworkConnectors"].([]any)); n != 0 {
		t.Errorf("eu-west-1 sees %d us-east-1 connectors", n)
	}
}
