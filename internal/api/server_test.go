package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/store"
)

type inventoryOp struct {
	Operation string `json:"operation"`
	Method    string `json:"method"`
	URI       string `json:"uri"`
}

func loadInventory(t *testing.T) []inventoryOp {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "conformance", "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Operations []inventoryOp `json:"operations"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Operations
}

// The route table and the conformance inventory are two statements of the
// same fact. If they drift, the suite tests paths the emulator does not serve
// and the coverage number stops meaning anything.
func TestRoutesMatchInventory(t *testing.T) {
	inv := loadInventory(t)
	if len(Routes) != len(inv) {
		t.Errorf("%d routes, %d inventory operations", len(Routes), len(inv))
	}

	byOp := map[string]Route{}
	for _, r := range Routes {
		if _, dup := byOp[r.Operation]; dup {
			t.Errorf("operation %s routed twice", r.Operation)
		}
		byOp[r.Operation] = r
	}

	for _, op := range inv {
		r, ok := byOp[op.Operation]
		if !ok {
			t.Errorf("%s is in the inventory with no route", op.Operation)
			continue
		}
		if r.Method != op.Method {
			t.Errorf("%s: route method %s, inventory %s", op.Operation, r.Method, op.Method)
		}
		// The inventory writes {param}; ServeMux patterns use the same
		// spelling, so these compare directly.
		if r.Pattern != op.URI {
			t.Errorf("%s: route %s, inventory %s", op.Operation, r.Pattern, op.URI)
		}
		delete(byOp, op.Operation)
	}
	for op := range byOp {
		t.Errorf("%s is routed but not in the inventory", op)
	}
}

func newTestServer() *Server {
	return NewServer(clock.NewTest(time.Unix(0, 0).UTC()), store.New(), "test")
}

// 501 rather than 404 is the whole point of routing everything up front: the
// conformance runner reads 501 as "not implemented yet" and 404 as a wrong
// answer.
func TestUnimplementedOperationsAnswer501(t *testing.T) {
	srv := newTestServer()
	for _, tc := range []struct{ method, path string }{
		{"POST", "/2025-09-09/microvm-images"},
		{"GET", "/2025-09-09/microvm-images/arn:aws:lambda:us-east-2:123456789012:microvm-image:img"},
		{"POST", "/2025-09-09/microvms/microvm-abc/suspend"},
		{"GET", "/2026-04-04/network-connectors"},
		{"DELETE", "/2017-03-31/tags/arn:aws:lambda:us-east-2:123456789012:microvm-image:img"},
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s: got %d, want 501", tc.method, tc.path, rec.Code)
		}
	}
}

// Image identifiers are full ARNs and the service accepts the colons raw.
// A pattern that could not carry them would break every image route.
func TestARNPathSegmentRoutes(t *testing.T) {
	srv := newTestServer()
	got := ""
	srv.Register("GetMicrovmImage", func(w http.ResponseWriter, r *http.Request) {
		got = r.PathValue("imageIdentifier")
		w.WriteHeader(http.StatusOK)
	})
	arn := "arn:aws:lambda:us-east-2:123456789012:microvm-image:m80-conf-image"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/2025-09-09/microvm-images/"+arn, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got != arn {
		t.Errorf("path value %q, want %q", got, arn)
	}
}

func TestRegisteredHandlerTakesOver(t *testing.T) {
	srv := newTestServer()
	srv.Register("ListMicrovmImages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
	})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/2025-09-09/microvm-images", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	// The sibling POST on the same path must still be unimplemented.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/2025-09-09/microvm-images", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("POST status %d, want 501", rec.Code)
	}
}

func TestRegisterUnknownOperationPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering an unknown operation did not panic")
		}
	}()
	newTestServer().Register("GetMicrovmImageTypo", func(http.ResponseWriter, *http.Request) {})
}

func TestHealthReportsCoverage(t *testing.T) {
	srv := newTestServer()
	srv.Register("ListMicrovmImages", func(w http.ResponseWriter, r *http.Request) {})
	srv.Store.Region("us-east-2")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/_m80/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var doc struct {
		Version  string `json:"version"`
		Coverage struct {
			Implemented int      `json:"implemented"`
			Total       int      `json:"total"`
			Operations  []string `json:"operations"`
			Pending     []string `json:"notImplementedYet"`
		} `json:"coverage"`
		Regions []string `json:"regions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Coverage.Implemented != 1 || doc.Coverage.Total != len(Routes) {
		t.Errorf("coverage %d/%d", doc.Coverage.Implemented, doc.Coverage.Total)
	}
	if len(doc.Coverage.Pending) != len(Routes)-1 {
		t.Errorf("%d pending, want %d", len(doc.Coverage.Pending), len(Routes)-1)
	}
	if len(doc.Regions) != 1 || doc.Regions[0] != "us-east-2" {
		t.Errorf("regions %v", doc.Regions)
	}
	if doc.Version != "test" {
		t.Errorf("version %q", doc.Version)
	}
}

func TestUnknownPathIs404(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/2025-09-09/nonsense", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404 for a path the service does not have", rec.Code)
	}
}

func TestUnimplementedBodyIsJSON(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", "/2025-09-09/microvms", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type %q", ct)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("501 body is not JSON: %v", err)
	}
	if doc["__type"] != "NotImplemented" {
		t.Errorf("__type %v", doc["__type"])
	}
}
