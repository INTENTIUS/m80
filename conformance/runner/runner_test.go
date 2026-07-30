package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// The states an until-poll walks through are the one thing only a live
// recording can answer, and the settled fixture alone throws them away.
func TestRecordCapturesObservedStates(t *testing.T) {
	states := []string{"PENDING", "PENDING", "RUNNING"}
	var i int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := states[min(i, len(states)-1)]
		i++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"state":"` + s + `"}`))
	}))
	defer srv.Close()

	cases, fixtures := t.TempDir(), t.TempDir()
	writeScenario(t, cases, "vms.json", Scenario{
		ID: "vms", Steps: []Step{{
			Name: "get-until-running", Operation: "GetMicrovm",
			Method: "GET", Path: "/2025-09-09/microvms/vm1",
			Expect: Expect{Status: 200},
			Until:  &Until{Path: "state", Equals: "RUNNING", TimeoutSec: 5, IntervalSec: 0.01},
		}},
	})

	r := newTestRunner(t, srv.URL, cases, fixtures, true, nil)
	scenarios, err := r.LoadScenarios()
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(r.Run(scenarios)); got["vms/get-until-running"] != Pass {
		t.Fatalf("record run: %v", got)
	}

	raw, err := os.ReadFile(filepath.Join(fixtures, "vms", "get-until-running.meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta fixtureMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	// Deduplicated: two PENDING polls are one observation, and the
	// transition into RUNNING is what matters.
	want := []string{"PENDING", "RUNNING"}
	if len(meta.ObservedStates) != len(want) {
		t.Fatalf("observed %v, want %v", meta.ObservedStates, want)
	}
	for i, s := range want {
		if meta.ObservedStates[i] != s {
			t.Fatalf("observed %v, want %v", meta.ObservedStates, want)
		}
	}
}

// Case timeouts are sized for real AWS. Against a target that never reaches
// the awaited state, the unclamped value is dead wall-clock before the step
// can fail.
func TestMaxPollSecClampsUntilTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"state":"CREATING"}`)) // never reaches CREATED
	}))
	defer srv.Close()

	cases := t.TempDir()
	writeScenario(t, cases, "images.json", Scenario{
		ID: "images-basic", Steps: []Step{{
			Name: "get-until-created", Operation: "GetMicrovmImage",
			Method: "GET", Path: "/2025-09-09/microvm-images/img1",
			Expect: Expect{Status: 200},
			Until:  &Until{Path: "state", Equals: "CREATED", TimeoutSec: 600, IntervalSec: 0.01},
		}},
	})

	r := newTestRunner(t, srv.URL, cases, t.TempDir(), false, nil)
	r.cfg.MaxPollSec = 0.2
	scenarios, err := r.LoadScenarios()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got := outcomes(r.Run(scenarios))
	elapsed := time.Since(start)

	if got["images-basic/get-until-created"] != Fail {
		t.Fatalf("want fail once the clamp elapses, got %v", got)
	}
	if elapsed > 30*time.Second {
		t.Errorf("clamp ignored: step took %s against a 600s case timeout", elapsed)
	}
	// The clamp must not leak back into the scenario it was applied to.
	if scenarios[0].Steps[0].Until.TimeoutSec != 600 {
		t.Errorf("case timeout mutated to %v", scenarios[0].Steps[0].Until.TimeoutSec)
	}
}

// A step that settles on its first poll has no transition to report, and a
// one-element list would just restate the fixture.
func TestRecordOmitsObservedStatesWhenNoTransition(t *testing.T) {
	srv := stub()
	defer srv.Close()
	cases, fixtures := t.TempDir(), t.TempDir()
	writeScenario(t, cases, "images.json", Scenario{
		ID: "images-basic", Steps: []Step{{
			Name: "get-until-created", Operation: "GetMicrovmImage",
			Method: "GET", Path: "/2025-09-09/microvm-images/img1",
			Expect: Expect{Status: 200},
			Until:  &Until{Path: "state", Equals: "CREATED", TimeoutSec: 5, IntervalSec: 0.01},
		}},
	})

	r := newTestRunner(t, srv.URL, cases, fixtures, true, nil)
	scenarios, err := r.LoadScenarios()
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(r.Run(scenarios)); got["images-basic/get-until-created"] != Pass {
		t.Fatalf("record run: %v", got)
	}

	raw, err := os.ReadFile(filepath.Join(fixtures, "images-basic", "get-until-created.meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta fixtureMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ObservedStates != nil {
		t.Errorf("observedStates should be omitted, got %v", meta.ObservedStates)
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

// A live recording and an emulator response must land on the same string for
// the values neither side can agree on by construction: generated resource
// ids, and the region baked into the per-VM endpoint hostname.
func TestNormalizeShortIDsAndRegion(t *testing.T) {
	for _, tc := range []struct {
		name, live, emulated string
	}{
		{"security group", `{"v":"sg-f0e6979f"}`, `{"v":"sg-00000000000000001"}`},
		{"subnet", `{"v":"subnet-15c2f37d"}`, `{"v":"subnet-00000000000000001"}`},
		{"connector raw id", `{"v":"nc-8f14e45fceea167a"}`, `{"v":"nc-99e2dfd679d24b399"}`},
		// Fixtures on disk were normalized when written, so a live connector
		// id already reads nc-UUID there.
		{"connector recorded as UUID", `{"v":"nc-UUID"}`, `{"v":"nc-99e2dfd679d24b399"}`},
		{"endpoint region", `{"v":"x.lambda-microvm.us-east-2.on.aws"}`, `{"v":"x.lambda-microvm.us-east-1.on.aws"}`},
		// Recorded in one hour, replayed in another: without this the step
		// passes or fails by wall clock.
		{"version state time bucket", `{"v":"SUCCESSFUL#26073006"}`, `{"v":"SUCCESSFUL#26073018"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := string(Normalize([]byte(tc.emulated))), string(Normalize([]byte(tc.live))); got != want {
				t.Errorf("emulated normalized to %s, live to %s", got, want)
			}
		})
	}
}

// Redaction must not swallow the values a conformance run exists to compare.
func TestNormalizeKeepsMeaningfulValues(t *testing.T) {
	in := []byte(`{"name":"m80-conf-image","state":"CREATING","version":"1.0","base":"al2023-1","uri":"s3://m80-conformance-123456789012-use2/code.zip","bucket":"SUCCESSFUL#26073006"}`)
	var doc map[string]any
	if err := json.Unmarshal(Normalize(in), &doc); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"name":    "m80-conf-image",
		"state":   "CREATING",
		"version": "1.0",
		"base":    "al2023-1",
		"uri":     "s3://m80-conformance-ACCOUNT-use2/code.zip",
		// The state half of a bucket survives; only its clock half goes.
		"bucket": "SUCCESSFUL#TIMESTAMP",
	} {
		if doc[k] != want {
			t.Errorf("%s: got %v, want %v", k, doc[k], want)
		}
	}
}

func TestScenarioParamDefaultsAndOverride(t *testing.T) {
	srv := stub()
	defer srv.Close()
	cases := t.TempDir()
	writeScenario(t, cases, "p.json", Scenario{
		ID:     "params",
		Params: map[string]string{"img": "ghost"},
		Steps: []Step{{
			Name: "get", Operation: "GetMicrovmImage",
			Method: "GET", Path: "/2025-09-09/microvm-images/${img}",
			Expect: Expect{Status: 200},
		}},
	})

	// Scenario default resolves to the 404 route.
	r := newTestRunner(t, srv.URL, cases, t.TempDir(), false, nil)
	scenarios, _ := r.LoadScenarios()
	if got := outcomes(r.Run(scenarios))["params/get"]; got != Fail {
		t.Errorf("default param: got %s, want fail (404 route)", got)
	}

	// CLI param overrides the scenario default and hits the 200 route.
	r = New(Config{
		Endpoint: srv.URL, CasesDir: cases, FixturesDir: t.TempDir(),
		Params:      map[string]string{"img": "img1"},
		Credentials: aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"},
	})
	scenarios, _ = r.LoadScenarios()
	if got := outcomes(r.Run(scenarios))["params/get"]; got != Pass {
		t.Errorf("override param: got %s, want pass", got)
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
