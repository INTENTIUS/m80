package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// stub implements just enough of the wire protocol: create returns a name,
// get returns state, everything else is 501.
func stub() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/2025-09-09/microvm-images":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"img1","state":"CREATING","createdTime":"2026-07-29T10:00:00Z"}`))
		case r.Method == "GET" && r.URL.Path == "/2025-09-09/microvm-images/img1":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"img1","state":"CREATED"}`))
		case r.Method == "GET" && r.URL.Path == "/2025-09-09/microvm-images/ghost":
			w.Header().Set("X-Amzn-Errortype", "ResourceNotFoundException:http://internal")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"no such image"}`))
		default:
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = w.Write([]byte(`{"__type":"NotImplemented"}`))
		}
	}))
}

func scenarioFiles(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	scenario := Scenario{
		ID:   "images-basic",
		Tags: []string{"documented-only", "subset:floci"},
		Steps: []Step{
			{
				Name: "create", Operation: "CreateMicrovmImage",
				Method: "POST", Path: "/2025-09-09/microvm-images",
				Body:    json.RawMessage(`{"name":"img1","baseImageArn":"arn:aws:lambda:us-east-1:aws:microvm-image:al2023-1"}`),
				Expect:  Expect{Status: 201, BodyMatch: json.RawMessage(`{"state":"CREATING"}`)},
				Capture: map[string]string{"imageName": "name"},
			},
			{
				Name: "get-until-created", Operation: "GetMicrovmImage",
				Method: "GET", Path: "/2025-09-09/microvm-images/${imageName}",
				Expect: Expect{Status: 200},
				Until:  &Until{Path: "state", Equals: "CREATED", TimeoutSec: 5, IntervalSec: 0.01},
			},
			{
				Name: "get-missing", Operation: "GetMicrovmImage",
				Method: "GET", Path: "/2025-09-09/microvm-images/ghost",
				Expect: Expect{Status: 404, ErrorType: "ResourceNotFoundException"},
			},
		},
	}
	writeScenario(t, dir, "10-images.json", scenario)

	writeScenario(t, dir, "20-vms.json", Scenario{
		ID:   "vms-basic",
		Tags: []string{"documented-only"},
		Steps: []Step{
			{
				Name: "run", Operation: "RunMicrovm",
				Method: "POST", Path: "/2025-09-09/microvms",
				Body:   json.RawMessage(`{"imageIdentifier":"img1"}`),
				Expect: Expect{Status: 200},
			},
			{
				Name: "never-reached", Operation: "GetMicrovm",
				Method: "GET", Path: "/2025-09-09/microvms/x",
				Expect: Expect{Status: 200},
			},
		},
	})
	return dir
}

func writeScenario(t *testing.T, dir, name string, s Scenario) {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestRunner(t *testing.T, endpoint, cases, fixtures string, record bool, tags []string) *Runner {
	t.Helper()
	return New(Config{
		Endpoint:    endpoint,
		CasesDir:    cases,
		FixturesDir: fixtures,
		Record:      record,
		TagFilter:   tags,
		Credentials: aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"},
	})
}

func outcomes(results []StepResult) map[string]Outcome {
	m := map[string]Outcome{}
	for _, r := range results {
		m[r.Scenario+"/"+r.Step] = r.Outcome
	}
	return m
}

func TestRunAgainstStub(t *testing.T) {
	srv := stub()
	defer srv.Close()
	cases := scenarioFiles(t)

	r := newTestRunner(t, srv.URL, cases, t.TempDir(), false, nil)
	scenarios, err := r.LoadScenarios()
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(r.Run(scenarios))

	want := map[string]Outcome{
		"images-basic/create":            Pass,
		"images-basic/get-until-created": Pass, // capture + template + until all in play
		"images-basic/get-missing":       Pass, // status and error-type expectation
		"vms-basic/run":                  Unimplemented,
		"vms-basic/never-reached":        Skipped,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %s, want %s", k, got[k], w)
		}
	}
}

func TestTagFilter(t *testing.T) {
	srv := stub()
	defer srv.Close()
	cases := scenarioFiles(t)

	r := newTestRunner(t, srv.URL, cases, t.TempDir(), false, []string{"subset:floci"})
	scenarios, err := r.LoadScenarios()
	if err != nil {
		t.Fatal(err)
	}
	results := r.Run(scenarios)
	for _, res := range results {
		if res.Scenario != "images-basic" {
			t.Errorf("tag filter leaked scenario %s", res.Scenario)
		}
	}
	if len(results) == 0 {
		t.Fatal("tag filter selected nothing")
	}
}

func TestRecordWritesRedactedFixture(t *testing.T) {
	srv := stub()
	defer srv.Close()
	cases := t.TempDir()
	fixtures := t.TempDir()
	writeScenario(t, cases, "images.json", Scenario{
		ID: "images-basic",
		Steps: []Step{{
			Name: "create", Operation: "CreateMicrovmImage",
			Method: "POST", Path: "/2025-09-09/microvm-images",
			Body:   json.RawMessage(`{"name":"img1"}`),
			Expect: Expect{Status: 201},
		}},
	})

	r := newTestRunner(t, srv.URL, cases, fixtures, true, nil)
	scenarios, err := r.LoadScenarios()
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(r.Run(scenarios))
	if got["images-basic/create"] != Pass {
		t.Fatalf("record run: %v", got)
	}

	raw, err := os.ReadFile(filepath.Join(fixtures, "images-basic", "create.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["createdTime"] != "TIMESTAMP" {
		t.Errorf("timestamp not redacted: %v", doc["createdTime"])
	}
}

func TestNormalize(t *testing.T) {
	in := []byte(`{"arn":"arn:aws:lambda:eu-west-1:123456789012:microvm:abc","authToken":"s3cret","when":"2026-07-29T10:00:00.123Z"}`)
	var doc map[string]any
	if err := json.Unmarshal(Normalize(in), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["arn"] != "arn:aws:lambda:REGION:ACCOUNT:microvm:abc" {
		t.Errorf("arn: %v", doc["arn"])
	}
	if doc["authToken"] != "REDACTED" {
		t.Errorf("token: %v", doc["authToken"])
	}
	if doc["when"] != "TIMESTAMP" {
		t.Errorf("timestamp: %v", doc["when"])
	}
}

func TestCoverageReport(t *testing.T) {
	inv := Inventory{Operations: []InventoryOp{
		{Operation: "CreateMicrovmImage"}, {Operation: "GetMicrovm"},
	}}
	rep := BuildReport(inv, []StepResult{
		{Scenario: "s", Step: "a", Operation: "CreateMicrovmImage", Outcome: Pass},
	})
	if len(rep.Coverage.Exercised) != 1 || rep.Coverage.Exercised[0] != "CreateMicrovmImage" {
		t.Errorf("exercised: %v", rep.Coverage.Exercised)
	}
	if len(rep.Coverage.Unexercised) != 1 || rep.Coverage.Unexercised[0] != "GetMicrovm" {
		t.Errorf("unexercised: %v", rep.Coverage.Unexercised)
	}
}
