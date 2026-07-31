package images

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/intentius/m80/internal/api"
	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/managedimages"
	"github.com/intentius/m80/internal/store"
)

const (
	region  = "us-east-1"
	baseArn = "arn:aws:lambda:us-east-1:aws:microvm-image:al2023-1"
	roleArn = "arn:aws:iam::123456789012:role/build"
	codeURI = "s3://bucket/code.zip"
	hop     = time.Second
)

type fakeVMs struct{ running bool }

func (f fakeVMs) HasRunningVMs(string, string) bool { return f.running }

type harness struct {
	srv *api.Server
	svc *Service
	clk *clock.Test
}

func newHarness(t *testing.T, vms VMChecker) *harness {
	t.Helper()
	clk := clock.NewTest(time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC))
	st := store.New()
	srv := api.NewServer(clk, st, "test")
	managedimages.Register(srv)
	svc := NewService(clk, st, hop)
	Register(srv, svc, vms)
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

func createBody(name string) map[string]any {
	return map[string]any{
		"name":         name,
		"baseImageArn": baseArn,
		"buildRoleArn": roleArn,
		"codeArtifact": map[string]any{"uri": codeURI},
	}
}

func (h *harness) createImage(t *testing.T, name string) string {
	t.Helper()
	rec, doc := h.do("POST", "/2025-09-09/microvm-images", createBody(name))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: status %d (%s)", name, rec.Code, rec.Body.String())
	}
	return doc["imageArn"].(string)
}

// settle walks the whole build chain: version PENDING → IN_PROGRESS →
// SUCCESSFUL, then the image one hop later.
func (h *harness) settle() { h.clk.Advance(4 * hop) }

// The three layers must be observable separately, or KubeMicroVM's build
// logs feature has nothing to read.
func TestBuildAdvancesThroughEveryState(t *testing.T) {
	h := newHarness(t, nil)
	arn := h.createImage(t, "img")

	_, doc := h.do("GET", "/2025-09-09/microvm-images/"+arn, nil)
	if doc["state"] != StateCreating {
		t.Fatalf("image state %v, want CREATING", doc["state"])
	}
	_, v := h.do("GET", "/2025-09-09/microvm-images/"+arn+"/versions/1.0", nil)
	if v["state"] != BuildPending {
		t.Fatalf("version state %v, want PENDING", v["state"])
	}

	h.clk.Advance(hop)
	_, v = h.do("GET", "/2025-09-09/microvm-images/"+arn+"/versions/1.0", nil)
	if v["state"] != BuildInProgress {
		t.Fatalf("version state %v, want IN_PROGRESS", v["state"])
	}

	h.clk.Advance(hop)
	_, v = h.do("GET", "/2025-09-09/microvm-images/"+arn+"/versions/1.0", nil)
	if v["state"] != BuildSuccessful {
		t.Fatalf("version state %v, want SUCCESSFUL", v["state"])
	}
	// The image settles a hop after its version, so a client polling the
	// image does not see CREATED before the build is usable.
	_, doc = h.do("GET", "/2025-09-09/microvm-images/"+arn, nil)
	if doc["state"] != StateCreating {
		t.Fatalf("image settled early: %v", doc["state"])
	}

	h.clk.Advance(hop)
	_, doc = h.do("GET", "/2025-09-09/microvm-images/"+arn, nil)
	if doc["state"] != StateCreated {
		t.Fatalf("image state %v, want CREATED", doc["state"])
	}
	if doc["latestActiveImageVersion"] != "1.0" {
		t.Errorf("latestActiveImageVersion %v", doc["latestActiveImageVersion"])
	}
	if h.clk.Pending() != 0 {
		t.Errorf("%d timers left; the machine did not come to rest", h.clk.Pending())
	}
}

// A CREATING image reports no active version: there is nothing runnable yet.
func TestLatestActiveIsNullWhileCreating(t *testing.T) {
	h := newHarness(t, nil)
	_, doc := h.do("POST", "/2025-09-09/microvm-images", createBody("img"))
	if doc["latestActiveImageVersion"] != nil {
		t.Errorf("latestActiveImageVersion %v, want null", doc["latestActiveImageVersion"])
	}
	if doc["state"] != StateCreating {
		t.Errorf("state %v", doc["state"])
	}
}

func TestFailureInjectionMarksVersionFailed(t *testing.T) {
	h := newHarness(t, nil)
	h.svc.FailNextBuild("img")
	arn := h.createImage(t, "img")
	h.settle()

	_, v := h.do("GET", "/2025-09-09/microvm-images/"+arn+"/versions/1.0", nil)
	if v["state"] != BuildFailed {
		t.Fatalf("version state %v, want FAILED", v["state"])
	}
	_, doc := h.do("GET", "/2025-09-09/microvm-images/"+arn, nil)
	if doc["latestFailedImageVersion"] != "1.0" {
		t.Errorf("latestFailedImageVersion %v", doc["latestFailedImageVersion"])
	}
	if doc["latestActiveImageVersion"] != nil {
		t.Errorf("a failed build left an active version: %v", doc["latestActiveImageVersion"])
	}
	// The lever is one-shot, so a suite running several images is not
	// poisoned by one injection.
	_, b := h.do("GET", "/2025-09-09/microvm-images/"+arn+"/versions/1.0/builds", nil)
	items := b["items"].([]any)
	if reason := items[0].(map[string]any)["stateReason"]; reason == nil {
		t.Error("failed build carries no stateReason")
	}
}

func TestUpdateMintsANewVersion(t *testing.T) {
	h := newHarness(t, nil)
	arn := h.createImage(t, "img")
	h.settle()

	rec, doc := h.do("PUT", "/2025-09-09/microvm-images/"+arn, createBody("img"))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status %d", rec.Code)
	}
	if doc["imageVersion"] != "2.0" {
		t.Errorf("imageVersion %v, want 2.0", doc["imageVersion"])
	}
	if doc["state"] != StateUpdating {
		t.Errorf("state %v, want UPDATING", doc["state"])
	}
	// The prior version stays active until the rebuild lands.
	if doc["latestActiveImageVersion"] != "1.0" {
		t.Errorf("latestActiveImageVersion %v, want 1.0", doc["latestActiveImageVersion"])
	}

	h.settle()
	_, doc = h.do("GET", "/2025-09-09/microvm-images/"+arn, nil)
	if doc["state"] != StateUpdated || doc["latestActiveImageVersion"] != "2.0" {
		t.Errorf("after update: state %v, active %v", doc["state"], doc["latestActiveImageVersion"])
	}
	_, list := h.do("GET", "/2025-09-09/microvm-images/"+arn+"/versions", nil)
	if n := len(list["items"].([]any)); n != 2 {
		t.Errorf("%d versions, want both retained", n)
	}
}

// PUT is a full replace: omitting a required member is a validation error,
// not a merge with what is stored.
func TestUpdateIsFullReplace(t *testing.T) {
	h := newHarness(t, nil)
	arn := h.createImage(t, "img")
	h.settle()

	rec, doc := h.do("PUT", "/2025-09-09/microvm-images/"+arn, map[string]any{"description": "only this"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	msg, _ := doc["message"].(string)
	for _, member := range []string{"baseImageArn", "codeArtifact", "buildRoleArn"} {
		if !strings.Contains(msg, member) {
			t.Errorf("message does not name %s: %s", member, msg)
		}
	}
	if !strings.HasPrefix(msg, "3 validation errors detected") {
		t.Errorf("message should report all three at once: %s", msg)
	}
}

func TestVersionPatchDuringUpdateIs409(t *testing.T) {
	h := newHarness(t, nil)
	arn := h.createImage(t, "img")
	h.settle()
	h.do("PUT", "/2025-09-09/microvm-images/"+arn, createBody("img"))

	rec, doc := h.do("PATCH", "/2025-09-09/microvm-images/"+arn+"/versions/1.0",
		map[string]any{"status": StatusInactive})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	if got := rec.Header().Get("X-Amzn-Errortype"); got != "ConflictException" {
		t.Errorf("error type %q", got)
	}
	if msg, _ := doc["message"].(string); !strings.Contains(msg, StateUpdating) {
		t.Errorf("message %q", msg)
	}
}

func TestDeleteRefusedWhileBuilding(t *testing.T) {
	h := newHarness(t, nil)
	arn := h.createImage(t, "img")

	rec, doc := h.do("DELETE", "/2025-09-09/microvm-images/"+arn, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if msg, _ := doc["message"].(string); !strings.Contains(msg, "current state") {
		t.Errorf("message %q", msg)
	}
}

func TestDeleteRefusedWithRunningVMs(t *testing.T) {
	h := newHarness(t, fakeVMs{running: true})
	arn := h.createImage(t, "img")
	h.settle()

	rec, doc := h.do("DELETE", "/2025-09-09/microvm-images/"+arn, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if msg, _ := doc["message"].(string); msg != "Cannot delete microvm image with running microvms." {
		t.Errorf("message %q", msg)
	}
}

// Delete is asynchronous and the name stays reserved through the window, so
// a create reusing it is refused rather than resurrecting the image.
func TestNameStaysReservedThroughDeleteWindow(t *testing.T) {
	h := newHarness(t, nil)
	arn := h.createImage(t, "img")
	h.settle()

	rec, doc := h.do("DELETE", "/2025-09-09/microvm-images/"+arn, nil)
	if rec.Code != http.StatusOK || doc["state"] != StateDeleting {
		t.Fatalf("delete: %d %v", rec.Code, doc)
	}

	rec, doc = h.do("POST", "/2025-09-09/microvm-images", createBody("img"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create during delete window: status %d, want 400", rec.Code)
	}
	if msg, _ := doc["message"].(string); !strings.Contains(msg, "already exists") {
		t.Errorf("message %q", msg)
	}

	// Once it drains the name frees up.
	h.clk.Advance(2 * hop)
	rec, _ = h.do("POST", "/2025-09-09/microvm-images", createBody("img"))
	if rec.Code != http.StatusCreated {
		t.Errorf("create after drain: status %d", rec.Code)
	}
}

func TestMemoryTierEnforced(t *testing.T) {
	h := newHarness(t, nil)
	body := createBody("img")
	body["resources"] = []any{map[string]any{"minimumMemoryInMiB": 3000}}
	rec, doc := h.do("POST", "/2025-09-09/microvm-images", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 for an off-tier size", rec.Code)
	}
	if msg, _ := doc["message"].(string); !strings.Contains(msg, "minimumMemoryInMiB") {
		t.Errorf("message %q", msg)
	}

	for _, tier := range MemoryTiers {
		body := createBody("img-" + itoa(tier))
		body["resources"] = []any{map[string]any{"minimumMemoryInMiB": tier}}
		if rec, _ := h.do("POST", "/2025-09-09/microvm-images", body); rec.Code != http.StatusCreated {
			t.Errorf("tier %d rejected: %d", tier, rec.Code)
		}
	}
}

func TestUnknownBaseImageRejected(t *testing.T) {
	h := newHarness(t, nil)
	body := createBody("img")
	body["baseImageArn"] = "arn:aws:lambda:us-east-1:aws:microvm-image:not-a-real-base"
	rec, _ := h.do("POST", "/2025-09-09/microvm-images", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
}

// Two builds per version, one per Graviton generation, newest first.
func TestVersionHasOneBuildPerChipsetGeneration(t *testing.T) {
	h := newHarness(t, nil)
	arn := h.createImage(t, "img")
	h.settle()

	_, doc := h.do("GET", "/2025-09-09/microvm-images/"+arn+"/versions/1.0/builds", nil)
	items := doc["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("%d builds, want 2", len(items))
	}
	gens := []string{
		items[0].(map[string]any)["chipsetGeneration"].(string),
		items[1].(map[string]any)["chipsetGeneration"].(string),
	}
	if gens[0] != "4" || gens[1] != "3" {
		t.Errorf("generations %v, want 4 then 3", gens)
	}
	for _, it := range items {
		m := it.(map[string]any)
		if m["architecture"] != "ARM_64" || m["chipset"] != "GRAVITON" {
			t.Errorf("build %v is not ARM_64/GRAVITON", m)
		}
		if _, has := m["snapshotBuild"]; has {
			t.Error("list item carries snapshotBuild; only Get does")
		}
	}

	buildID := items[0].(map[string]any)["buildId"].(string)
	_, one := h.do("GET", "/2025-09-09/microvm-images/"+arn+"/versions/1.0/builds/"+buildID, nil)
	if _, has := one["snapshotBuild"]; !has {
		t.Error("Get build is missing snapshotBuild")
	}
}

// Get returns a smaller projection than Create, and tags differ between them:
// null on create, {} on get. Both are recorded.
func TestProjectionsDifferBetweenCreateAndGet(t *testing.T) {
	h := newHarness(t, nil)
	_, created := h.do("POST", "/2025-09-09/microvm-images", createBody("img"))
	if _, has := created["tags"]; !has {
		t.Error("create response omits tags; it should be present and null")
	}
	if created["tags"] != nil {
		t.Errorf("create tags %v, want null", created["tags"])
	}
	if _, has := created["codeArtifact"]; !has {
		t.Error("create response is missing codeArtifact")
	}

	arn := created["imageArn"].(string)
	_, got := h.do("GET", "/2025-09-09/microvm-images/"+arn, nil)
	tags, ok := got["tags"].(map[string]any)
	if !ok || tags == nil {
		t.Errorf("get tags %v, want {}", got["tags"])
	}
	if _, has := got["codeArtifact"]; has {
		t.Error("get returns the full detail shape; it should be the summary")
	}
}

func TestImagesAreRegionScoped(t *testing.T) {
	h := newHarness(t, nil)
	h.createImage(t, "img")

	r := httptest.NewRequest("GET", "/2025-09-09/microvm-images", nil)
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=AKID/20260730/eu-west-1/lambda/aws4_request, SignedHeaders=host, Signature=x")
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, r)
	var doc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	if n := len(doc["items"].([]any)); n != 0 {
		t.Errorf("eu-west-1 sees %d us-east-1 images", n)
	}
}

func TestMissingImageIs404(t *testing.T) {
	h := newHarness(t, nil)
	rec, _ := h.do("GET", "/2025-09-09/microvm-images/"+imageARN(region, "ghost"), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("X-Amzn-Errortype"); got != "ResourceNotFoundException" {
		t.Errorf("error type %q", got)
	}
}

func TestNextVersion(t *testing.T) {
	for in, want := range map[string]string{
		"1.0": "2.0", "2.0": "3.0", "9.0": "10.0", "": "1.0", "garbage": "1.0",
	} {
		if got := nextVersion(in); got != want {
			t.Errorf("nextVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStateTimeBucket(t *testing.T) {
	at := time.Date(2026, 7, 30, 6, 13, 0, 0, time.UTC)
	if got := stateTimeBucket(BuildSuccessful, at); got != "SUCCESSFUL#26073006" {
		t.Errorf("got %q, want SUCCESSFUL#26073006", got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
