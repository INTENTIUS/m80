package managedimages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/intentius/m80/internal/api"
	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/store"
)

func newServer() *api.Server {
	srv := api.NewServer(clock.NewTest(time.Unix(0, 0).UTC()), store.New(), "test")
	Register(srv)
	return srv
}

func signedFor(region, method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260730/"+region+"/lambda/aws4_request, SignedHeaders=host, Signature=abc")
	return r
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, rec.Body.String())
	}
	return doc
}

// The catalog is identical in every account but not every region, and a
// client signing for eu-west-1 must not be handed us-east-2 ARNs.
func TestListIsRegionScoped(t *testing.T) {
	srv := newServer()
	for _, region := range []string{"us-east-2", "eu-west-1"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, signedFor(region, "GET", "/2025-09-09/managed-microvm-images"))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", region, rec.Code)
		}
		items := decode(t, rec)["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("%s: %d items", region, len(items))
		}
		want := "arn:aws:lambda:" + region + ":aws:microvm-image:al2023-1"
		if got := items[0].(map[string]any)["imageArn"]; got != want {
			t.Errorf("%s: arn %v, want %v", region, got, want)
		}
	}
}

// The account segment is the literal "aws", which is what marks these as
// service-owned; a numeric account there would be a different resource.
func TestManagedARNsUseTheAwsAccount(t *testing.T) {
	if got := arn("us-east-2", "al2023-1"); got != "arn:aws:lambda:us-east-2:aws:microvm-image:al2023-1" {
		t.Errorf("arn %q", got)
	}
}

func TestNextTokenIsPresentAndNull(t *testing.T) {
	srv := newServer()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, signedFor("us-east-2", "GET", "/2025-09-09/managed-microvm-images"))
	doc := decode(t, rec)
	tok, ok := doc["nextToken"]
	if !ok {
		t.Fatal("nextToken member missing; a client checking for it finds nothing")
	}
	if tok != nil {
		t.Errorf("nextToken %v, want null", tok)
	}
}

// Recorded order is newest first. A client taking items[0] as current would
// silently get the wrong base image if this flipped.
func TestVersionsAreNewestFirst(t *testing.T) {
	srv := newServer()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, signedFor("us-east-2",
		"GET", "/2025-09-09/managed-microvm-images/arn:aws:lambda:us-east-2:aws:microvm-image:al2023-1/versions"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	items := decode(t, rec)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("%d versions", len(items))
	}
	if v := items[0].(map[string]any)["imageVersion"]; v != "1" {
		t.Errorf("first version %v, want 1", v)
	}
	if v := items[1].(map[string]any)["imageVersion"]; v != "0" {
		t.Errorf("second version %v, want 0", v)
	}
	for _, it := range items {
		if s := it.(map[string]any)["status"]; s != "AVAILABLE" {
			t.Errorf("status %v", s)
		}
	}
}

func TestVersionsAcceptBareName(t *testing.T) {
	srv := newServer()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, signedFor("us-east-2", "GET", "/2025-09-09/managed-microvm-images/al2023-1/versions"))
	if rec.Code != http.StatusOK {
		t.Errorf("status %d", rec.Code)
	}
}

// An ARN for the right image but the wrong region is a miss, which is the
// same isolation property the list test covers from the other side.
func TestVersionsRejectWrongRegionARN(t *testing.T) {
	srv := newServer()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, signedFor("eu-west-1",
		"GET", "/2025-09-09/managed-microvm-images/arn:aws:lambda:us-east-2:aws:microvm-image:al2023-1/versions"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("X-Amzn-Errortype"); got != "ResourceNotFoundException" {
		t.Errorf("error type %q", got)
	}
}

func TestVersionsUnknownImageIs404(t *testing.T) {
	srv := newServer()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, signedFor("us-east-2", "GET", "/2025-09-09/managed-microvm-images/nope/versions"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

// Has is what CreateMicrovmImage validates baseImageArn against (#8).
func TestHas(t *testing.T) {
	for _, tc := range []struct {
		region, arn string
		want        bool
	}{
		{"us-east-2", "arn:aws:lambda:us-east-2:aws:microvm-image:al2023-1", true},
		{"eu-west-1", "arn:aws:lambda:eu-west-1:aws:microvm-image:al2023-1", true},
		{"us-east-2", "arn:aws:lambda:eu-west-1:aws:microvm-image:al2023-1", false},
		{"us-east-2", "arn:aws:lambda:us-east-2:aws:microvm-image:nope", false},
		{"us-east-2", "al2023-1", false},
		{"us-east-2", "", false},
	} {
		if got := Has(tc.region, tc.arn); got != tc.want {
			t.Errorf("Has(%q, %q) = %v, want %v", tc.region, tc.arn, got, tc.want)
		}
	}
}

func TestNames(t *testing.T) {
	if names := Names(); len(names) != 1 || names[0] != "al2023-1" {
		t.Errorf("names %v", names)
	}
}

func TestTrimARN(t *testing.T) {
	if got := TrimARN("arn:aws:lambda:us-east-2:aws:microvm-image:al2023-1"); got != "al2023-1" {
		t.Errorf("got %q", got)
	}
	if got := TrimARN("al2023-1"); got != "al2023-1" {
		t.Errorf("passthrough got %q", got)
	}
}
