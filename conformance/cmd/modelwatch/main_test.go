package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The real models are fetched over the network; these keep the parsers under
// test offline, since a CI run that silently degrades to "could not fetch"
// would report no drift forever.
const serviceModelJSON = `{
  "metadata": {"serviceId": "Lambda Microvms", "signingName": "lambda"},
  "operations": {
    "RunMicrovm": {
      "name": "RunMicrovm",
      "http": {"method": "POST", "requestUri": "/2025-09-09/microvms", "responseCode": 200},
      "errors": [{"shape": "ValidationException"}, {"shape": "AccessDeniedException"}]
    }
  }
}`

const smithyModelJSON = `{
  "shapes": {
    "com.amazonaws.lambdamicrovms#RunMicrovm": {
      "type": "operation",
      "traits": {"smithy.api#http": {"method": "POST", "uri": "/2025-09-09/microvms", "code": 200}}
    },
    "com.amazonaws.lambdamicrovms#RunMicrovmRequest": {"type": "structure"}
  }
}`

func TestOpsFromServiceModel(t *testing.T) {
	ops, signing, err := opsFromServiceModel([]byte(serviceModelJSON))
	if err != nil {
		t.Fatal(err)
	}
	if signing != "lambda" {
		t.Errorf("signingName: %q", signing)
	}
	op, ok := ops["RunMicrovm"]
	if !ok {
		t.Fatalf("RunMicrovm missing from %v", ops)
	}
	if op.Method != "POST" || op.URI != "/2025-09-09/microvms" || op.ResponseCode != 200 {
		t.Errorf("routing: %+v", op)
	}
	if op.Service != "Lambda Microvms" {
		t.Errorf("service: %q", op.Service)
	}
	// Sorted, so an upstream reordering is not reported as drift.
	if !sameStrings(op.Errors, []string{"AccessDeniedException", "ValidationException"}) {
		t.Errorf("errors not sorted: %v", op.Errors)
	}
}

func TestOpsFromSmithySkipsNonOperations(t *testing.T) {
	ops, err := opsFromSmithy([]byte(smithyModelJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("want only the operation shape, got %v", ops)
	}
	op, ok := ops["RunMicrovm"] // namespace stripped
	if !ok {
		t.Fatalf("RunMicrovm missing from %v", ops)
	}
	if op.Method != "POST" || op.URI != "/2025-09-09/microvms" {
		t.Errorf("routing: %+v", op)
	}
}

func TestSameStrings(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b []string
		want bool
	}{
		{"equal", []string{"a", "b"}, []string{"a", "b"}, true},
		{"different length", []string{"a"}, []string{"a", "b"}, false},
		{"different member", []string{"a", "c"}, []string{"a", "b"}, false},
		{"both empty", nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameStrings(tc.a, tc.b); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFetchReadsLocalPath(t *testing.T) {
	// Local paths keep the watcher usable against a checkout, which is how
	// the offline CI job and any bisect run exercise it.
	dir := filepath.Join(t.TempDir(), "model.json")
	if err := os.WriteFile(dir, []byte(serviceModelJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := fetch(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := opsFromServiceModel(raw); err != nil {
		t.Fatal(err)
	}
}
