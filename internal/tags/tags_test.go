package tags

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
	region  = "us-east-1"
	imgArn  = "arn:aws:lambda:us-east-1:000000000000:microvm-image:img"
	vmArn   = "arn:aws:lambda:us-east-1:000000000000:microvm:microvm-1111"
	nopeArn = "arn:aws:lambda:us-east-1:000000000000:microvm-image:nope"
)

// stubResource stands in for one owning package: it answers for the ARNs it
// knows and reports a miss for everything else, which is how the registry
// decides who owns what.
type stubResource struct {
	arn  string
	tags map[string]string
}

func (s *stubResource) Tags(reg, arn string) (map[string]string, bool) {
	if reg != region || arn != s.arn {
		return nil, false
	}
	if s.tags == nil {
		return map[string]string{}, true
	}
	return s.tags, true
}

func (s *stubResource) SetTags(reg, arn string, t map[string]string) bool {
	if reg != region || arn != s.arn {
		return false
	}
	s.tags = t
	return true
}

type harness struct {
	srv *api.Server
	img *stubResource
	vm  *stubResource
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clk := clock.NewTest(time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC))
	srv := api.NewServer(clk, store.New(), "test")
	img := &stubResource{arn: imgArn}
	vm := &stubResource{arn: vmArn}
	Register(srv, img, vm)
	return &harness{srv: srv, img: img, vm: vm}
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

// Recorded: tag and untag answer 204 with an empty body. The fixtures for
// both steps are zero-byte files, which is what no content looks like on disk.
func TestTagAndUntagAnswer204WithNoBody(t *testing.T) {
	h := newHarness(t)

	rec, _ := h.do("POST", "/2017-03-31/tags/"+imgArn, map[string]any{
		"Tags": map[string]string{"m80": "conformance"},
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("tag: status %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("tag body %q, want empty", rec.Body.String())
	}

	rec, _ = h.do("DELETE", "/2017-03-31/tags/"+imgArn+"?tagKeys=m80", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("untag: status %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("untag body %q, want empty", rec.Body.String())
	}
}

func TestListTagsRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/2017-03-31/tags/"+imgArn, map[string]any{
		"Tags": map[string]string{"m80": "conformance", "owner": "kubemicrovm"},
	})

	rec, doc := h.do("GET", "/2017-03-31/tags/"+imgArn, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	got, _ := doc["Tags"].(map[string]any)
	if got["m80"] != "conformance" || got["owner"] != "kubemicrovm" {
		t.Errorf("Tags %v", got)
	}
}

// An untagged resource answers with an empty object rather than null, so a
// client can index it without a nil check.
func TestListTagsOnUntaggedResourceIsEmptyObject(t *testing.T) {
	h := newHarness(t)
	rec, doc := h.do("GET", "/2017-03-31/tags/"+imgArn, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	got, ok := doc["Tags"].(map[string]any)
	if !ok {
		t.Fatalf("Tags %v, want an object", doc["Tags"])
	}
	if len(got) != 0 {
		t.Errorf("Tags %v, want empty", got)
	}
}

// Tagging merges rather than replaces: it adds and overwrites the keys it
// names and leaves the rest alone, which is what untag exists for.
func TestTagMergesRatherThanReplaces(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/2017-03-31/tags/"+imgArn, map[string]any{
		"Tags": map[string]string{"a": "1", "b": "2"},
	})
	h.do("POST", "/2017-03-31/tags/"+imgArn, map[string]any{
		"Tags": map[string]string{"b": "changed", "c": "3"},
	})

	_, doc := h.do("GET", "/2017-03-31/tags/"+imgArn, nil)
	got := doc["Tags"].(map[string]any)
	want := map[string]string{"a": "1", "b": "changed", "c": "3"}
	if len(got) != len(want) {
		t.Fatalf("Tags %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("tag %q = %v, want %q", k, got[k], v)
		}
	}
}

func TestUntagRemovesOnlyTheNamedKeys(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/2017-03-31/tags/"+imgArn, map[string]any{
		"Tags": map[string]string{"a": "1", "b": "2", "c": "3"},
	})

	// The SDK repeats the parameter; a human with curl comma-separates it.
	h.do("DELETE", "/2017-03-31/tags/"+imgArn+"?tagKeys=a&tagKeys=b", nil)
	_, doc := h.do("GET", "/2017-03-31/tags/"+imgArn, nil)
	got := doc["Tags"].(map[string]any)
	if len(got) != 1 || got["c"] != "3" {
		t.Errorf("Tags %v, want only c", got)
	}

	h.do("POST", "/2017-03-31/tags/"+imgArn, map[string]any{
		"Tags": map[string]string{"d": "4", "e": "5"},
	})
	h.do("DELETE", "/2017-03-31/tags/"+imgArn+"?tagKeys=d,e", nil)
	_, doc = h.do("GET", "/2017-03-31/tags/"+imgArn, nil)
	got = doc["Tags"].(map[string]any)
	if len(got) != 1 || got["c"] != "3" {
		t.Errorf("Tags %v after comma-separated untag, want only c", got)
	}
}

// Untag is idempotent: removing a key that is not there is not an error,
// which is what a reconciler converging on a desired tag set needs.
func TestUntagIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/2017-03-31/tags/"+imgArn, map[string]any{"Tags": map[string]string{"a": "1"}})

	for i := 0; i < 3; i++ {
		rec, _ := h.do("DELETE", "/2017-03-31/tags/"+imgArn+"?tagKeys=a", nil)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("untag %d: status %d", i, rec.Code)
		}
	}
}

// The registry routes an ARN to whichever package owns it, and a VM's tags
// must not land on an image.
func TestRegistryRoutesByOwner(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/2017-03-31/tags/"+imgArn, map[string]any{"Tags": map[string]string{"on": "image"}})
	h.do("POST", "/2017-03-31/tags/"+vmArn, map[string]any{"Tags": map[string]string{"on": "vm"}})

	if h.img.tags["on"] != "image" {
		t.Errorf("image tags %v", h.img.tags)
	}
	if h.vm.tags["on"] != "vm" {
		t.Errorf("vm tags %v", h.vm.tags)
	}

	_, doc := h.do("GET", "/2017-03-31/tags/"+vmArn, nil)
	if doc["Tags"].(map[string]any)["on"] != "vm" {
		t.Errorf("list on the VM ARN returned %v", doc["Tags"])
	}
}

func TestUnknownResourceIs404(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/2017-03-31/tags/" + nopeArn, nil},
		{"POST", "/2017-03-31/tags/" + nopeArn, map[string]any{"Tags": map[string]string{"a": "1"}}},
		{"DELETE", "/2017-03-31/tags/" + nopeArn + "?tagKeys=a", nil},
	} {
		rec, _ := h.do(c.method, c.path, c.body)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", c.method, rec.Code)
		}
		if got := rec.Header().Get("X-Amzn-Errortype"); got != "ResourceNotFoundException" {
			t.Errorf("%s: error type %q", c.method, got)
		}
	}
}

func TestTagValidation(t *testing.T) {
	h := newHarness(t)

	// Tags is a required member.
	rec, doc := h.do("POST", "/2017-03-31/tags/"+imgArn, map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if msg, _ := doc["message"].(string); !strings.Contains(msg, "Tags") {
		t.Errorf("message %q", msg)
	}

	// TagKeys is required on untag; the query string carrying none is the
	// realistic way to get this wrong.
	rec, doc = h.do("DELETE", "/2017-03-31/tags/"+imgArn, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if msg, _ := doc["message"].(string); !strings.Contains(msg, "TagKeys") {
		t.Errorf("message %q", msg)
	}

	// Model constraints: key 1..128, value at most 256.
	rec, _ = h.do("POST", "/2017-03-31/tags/"+imgArn, map[string]any{
		"Tags": map[string]string{strings.Repeat("k", MaxKeyLength+1): "v"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("over-long key: status %d, want 400", rec.Code)
	}
	rec, _ = h.do("POST", "/2017-03-31/tags/"+imgArn, map[string]any{
		"Tags": map[string]string{"k": strings.Repeat("v", MaxValueLength+1)},
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("over-long value: status %d, want 400", rec.Code)
	}
	// An empty value is legal — the model's minimum is 0.
	rec, _ = h.do("POST", "/2017-03-31/tags/"+imgArn, map[string]any{
		"Tags": map[string]string{"k": ""},
	})
	if rec.Code != http.StatusNoContent {
		t.Errorf("empty value: status %d, want 204", rec.Code)
	}
}

// A caller must not be able to mutate a resource's tag map by holding onto
// what ListTags handed back.
func TestReturnedTagsAreACopy(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/2017-03-31/tags/"+imgArn, map[string]any{"Tags": map[string]string{"a": "1"}})

	got, _ := h.img.Tags(region, imgArn)
	before := len(got)
	h.do("POST", "/2017-03-31/tags/"+imgArn, map[string]any{"Tags": map[string]string{"b": "2"}})
	if len(got) != before {
		t.Error("the map handed out earlier was mutated in place by a later tag call")
	}
}
