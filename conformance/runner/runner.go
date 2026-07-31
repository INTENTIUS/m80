// Package runner executes conformance scenarios against a Lambda MicroVMs
// endpoint — real AWS (recording fixtures), m80, or a floci module. Cases and
// fixtures are plain JSON so the contract stays language-agnostic; only this
// runner is Go, for sigv4.
package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// defaultAccount is m80's own account id, twelve digits so the normalizer
// flattens it the same way it flattens a real one.
const defaultAccount = "000000000000"

type Scenario struct {
	ID string `json:"id"`
	// Params are template defaults so scenarios run against an emulator
	// unmodified; CLI -param values override them for recording runs.
	Params map[string]string `json:"params,omitempty"`
	Tags   []string          `json:"tags"`
	Steps  []Step            `json:"steps"`
}

type Step struct {
	Name      string            `json:"name"`
	Operation string            `json:"operation"`
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	Body      json.RawMessage   `json:"body,omitempty"`
	Expect    Expect            `json:"expect"`
	Capture   map[string]string `json:"capture,omitempty"`
	Until     *Until            `json:"until,omitempty"`

	// Optional marks a step whose operation is incidental to the scenario, so
	// a 501 from it does not halt the rest. Without it a side probe wedged
	// mid-scenario hides every step behind it: CreateMicrovmAuthToken sits
	// between suspend and resume in vm-suspend-resume purely because that is
	// where a live recording could observe it, and while it was unimplemented
	// it took ResumeMicrovm's coverage down with it.
	//
	// It exempts Unimplemented only. A step that genuinely fails still halts
	// the scenario however it is marked, because a wrong answer means the
	// state the later steps assume is no longer trustworthy. A step carrying
	// Capture should not be optional: the vars it would have set go missing
	// and the failure resurfaces later, further from its cause.
	Optional bool `json:"optional,omitempty"`
}

// Until turns a step into a poll: the request repeats until the dot-path in
// the response equals the value, or the timeout elapses. Transient emulator
// states settle in wall-clock milliseconds; real AWS builds take minutes, so
// timeouts are per-step data, not runner policy.
type Until struct {
	Path        string  `json:"path"`
	Equals      string  `json:"equals"`
	TimeoutSec  float64 `json:"timeoutSec"`
	IntervalSec float64 `json:"intervalSec"`
}

type Expect struct {
	Status         int             `json:"status,omitempty"`
	ErrorType      string          `json:"errorType,omitempty"`
	ErrorTypeOneOf []string        `json:"errorTypeOneOf,omitempty"`
	BodyMatch      json.RawMessage `json:"bodyMatch,omitempty"`
	ItemShape      *ItemShape      `json:"itemShape,omitempty"`
}

// ItemShape checks the members of every element of a list, without checking
// their values.
//
// Some collections accumulate across an account's whole history — terminated
// VMs are listed apparently forever — so a recorded list response is a
// photograph of one account at one moment and can never equal a fresh
// target's. Dropping those steps to a bare status check would lose the part
// that actually matters, which is whether the target returns the members a
// client reads. This checks exactly that part.
type ItemShape struct {
	// Path is the dot-path to the array, e.g. "items".
	Path string `json:"path"`
	// Members every element must carry.
	Required []string `json:"required"`
	// Exact rejects members outside Required as well as missing ones. Sparse
	// bodies and over-full ones are both real divergences, and an emulator
	// returning half the members of a summary is the commonest of all.
	Exact bool `json:"exact,omitempty"`
	// MinItems guards against a target that returns an empty list and
	// vacuously satisfies every member check.
	MinItems int `json:"minItems,omitempty"`
}

type Outcome string

const (
	Pass          Outcome = "pass"
	Fail          Outcome = "fail"
	Unimplemented Outcome = "unimplemented"
	Skipped       Outcome = "skipped"
	Errored       Outcome = "error"
)

type StepResult struct {
	Scenario  string  `json:"scenario"`
	Step      string  `json:"step"`
	Operation string  `json:"operation"`
	Outcome   Outcome `json:"outcome"`
	Detail    string  `json:"detail,omitempty"`
	Fixture   bool    `json:"fixtureBacked"`
}

type Config struct {
	Endpoint    string
	Region      string
	CasesDir    string
	FixturesDir string
	TagFilter   []string // scenario runs if it carries every listed tag
	Record      bool
	// MaxPollSec caps every until timeout. Case timeouts are sized for real
	// AWS, where a build legitimately takes 45 minutes; against an emulator
	// the same numbers mean a state the target does not model burns its full
	// timeout before the step fails. Zero keeps the case values, which is
	// what a recording run needs.
	MaxPollSec float64
	// Tier and Tiers select how strictly fixtures are compared. Zero values
	// mean full equality, which is what a recording run and m80's own runs
	// want.
	Tier        Tier
	Tiers       *Tiers
	Params      map[string]string
	Credentials aws.Credentials
	HTTPClient  *http.Client
}

type Runner struct {
	cfg Config
}

func New(cfg Config) *Runner {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	return &Runner{cfg: cfg}
}

// LoadScenarios reads every *.json under the cases directory, sorted by path
// so execution order is stable.
func (r *Runner) LoadScenarios() ([]Scenario, error) {
	var files []string
	err := filepath.WalkDir(r.cfg.CasesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".json") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []Scenario
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var s Scenario
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *Runner) selected(s Scenario) bool {
	for _, want := range r.cfg.TagFilter {
		found := false
		for _, t := range s.Tags {
			if t == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Run executes all selected scenarios and returns per-step results.
func (r *Runner) Run(scenarios []Scenario) []StepResult {
	var results []StepResult
	for _, s := range scenarios {
		if !r.selected(s) {
			continue
		}
		results = append(results, r.runScenario(s)...)
	}
	return results
}

func (r *Runner) runScenario(s Scenario) []StepResult {
	vars := map[string]string{}
	// region is built in, so a case can spell an ARN once and have it follow
	// whatever region the run signs for. Hardcoding a region in a param made
	// every case silently wrong against any other one — the emulator's
	// region-scoped catalog rejects a base image from elsewhere, correctly.
	vars["region"] = r.cfg.Region
	// account is built in for the same reason as region. A case that probes
	// a "missing" resource must name one in the caller's own account: an ARN
	// carrying the 123456789012 placeholder is a foreign account, and live
	// AWS answers that with 403 AccessDenied — correctly, since cross-account
	// access needs a resource-based policy no matter how much admin the
	// caller holds. The default is m80's own account so emulator runs need no
	// flag; recording runs pass -param account=<real>.
	vars["account"] = defaultAccount
	for k, v := range r.cfg.Params {
		if k == "account" || k == "region" {
			vars[k] = v
		}
	}
	for k, v := range s.Params {
		vars[k] = v
	}
	for k, v := range r.cfg.Params {
		vars[k] = v
	}
	resolveVars(vars)
	var results []StepResult
	failed := false
	for _, st := range s.Steps {
		res := StepResult{Scenario: s.ID, Step: st.Name, Operation: st.Operation}
		if failed {
			res.Outcome = Skipped
			res.Detail = "earlier step did not pass"
			results = append(results, res)
			continue
		}
		outcome, detail, fixture := r.runStep(s, st, vars)
		res.Outcome, res.Detail, res.Fixture = outcome, detail, fixture
		if outcome != Pass && !(st.Optional && outcome == Unimplemented) {
			failed = true
		}
		results = append(results, res)
	}
	return results
}

func (r *Runner) runStep(s Scenario, st Step, vars map[string]string) (Outcome, string, bool) {
	deadline := time.Now().Add(time.Duration(1) * time.Second)
	interval := time.Duration(0)
	if st.Until != nil {
		// Locals, not writes back through the pointer: st.Until is shared
		// with the loaded scenario.
		timeoutSec, intervalSec := st.Until.TimeoutSec, st.Until.IntervalSec
		if timeoutSec <= 0 {
			timeoutSec = 30
		}
		if intervalSec <= 0 {
			intervalSec = 1
		}
		if r.cfg.MaxPollSec > 0 && timeoutSec > r.cfg.MaxPollSec {
			timeoutSec = r.cfg.MaxPollSec
		}
		deadline = time.Now().Add(time.Duration(timeoutSec * float64(time.Second)))
		interval = time.Duration(intervalSec * float64(time.Second))
	}

	// Distinct values the poll passes through, in order. Only the settled
	// response is kept as a fixture, so without this the transition order —
	// the thing a live recording run is uniquely able to answer — is thrown
	// away every time.
	var observed []string

	for {
		status, body, errType, err := r.request(st, vars)
		if err != nil {
			return Errored, err.Error(), false
		}
		if status == http.StatusNotImplemented {
			return Unimplemented, "", false
		}

		if st.Until != nil {
			got, _ := dotPath(body, st.Until.Path)
			if len(observed) == 0 || observed[len(observed)-1] != got {
				observed = append(observed, got)
			}
			if got != st.Until.Equals {
				if time.Now().After(deadline) {
					return Fail, fmt.Sprintf("until %s=%q not reached, last %q", st.Until.Path, st.Until.Equals, got), false
				}
				time.Sleep(interval)
				continue
			}
		}

		outcome, detail, fixture := r.check(s, st, status, body, errType, observed)
		if outcome == Pass {
			r.captureVars(st, body, vars)
		}
		return outcome, detail, fixture
	}
}

func (r *Runner) request(st Step, vars map[string]string) (int, []byte, string, error) {
	path := substitute(st.Path, vars)
	var payload []byte
	if len(st.Body) > 0 {
		payload = []byte(substitute(string(st.Body), vars))
	}
	req, err := http.NewRequest(st.Method, strings.TrimRight(r.cfg.Endpoint, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := Sign(req, payload, r.cfg.Credentials, r.cfg.Region); err != nil {
		return 0, nil, "", fmt.Errorf("sign: %w", err)
	}
	resp, err := r.cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, "", err
	}
	return resp.StatusCode, body, errorType(resp.Header, body), nil
}

// errorType extracts the modeled error name from the X-Amzn-Errortype header
// or the rest-json __type body field, stripping namespace and metadata.
func errorType(h http.Header, body []byte) string {
	t := h.Get("X-Amzn-Errortype")
	if t == "" {
		var e struct {
			Type string `json:"__type"`
		}
		_ = json.Unmarshal(body, &e)
		t = e.Type
	}
	if i := strings.IndexAny(t, ":"); i >= 0 {
		t = t[:i]
	}
	if i := strings.LastIndex(t, "#"); i >= 0 {
		t = t[i+1:]
	}
	return t
}

func (r *Runner) check(s Scenario, st Step, status int, body []byte, errType string, observed []string) (Outcome, string, bool) {
	fixPath := filepath.Join(r.cfg.FixturesDir, s.ID, st.Name+".json")

	if r.cfg.Record {
		if st.Expect.Status != 0 && status != st.Expect.Status {
			return Fail, fmt.Sprintf("recording aborted: status %d, want %d: %s", status, st.Expect.Status, truncate(body)), false
		}
		if err := writeFixture(fixPath, body); err != nil {
			return Errored, err.Error(), false
		}
		// The body alone loses the wire facts error mapping needs — status
		// code and the modeled error type ride the headers. Sidecar them,
		// along with the states the poll walked through to get here.
		if err := writeMeta(fixPath, status, errType, observed); err != nil {
			return Errored, err.Error(), false
		}
		return Pass, "recorded", true
	}

	if st.Expect.Status != 0 && status != st.Expect.Status {
		return Fail, fmt.Sprintf("status %d, want %d: %s", status, st.Expect.Status, truncate(body)), false
	}
	if st.Expect.ErrorType != "" && errType != st.Expect.ErrorType {
		return Fail, fmt.Sprintf("error type %q, want %q", errType, st.Expect.ErrorType), false
	}
	if len(st.Expect.ErrorTypeOneOf) > 0 {
		ok := false
		for _, want := range st.Expect.ErrorTypeOneOf {
			if errType == want {
				ok = true
				break
			}
		}
		if !ok {
			return Fail, fmt.Sprintf("error type %q not in %v", errType, st.Expect.ErrorTypeOneOf), false
		}
	}
	if len(st.Expect.BodyMatch) > 0 {
		if detail, ok := subsetMatch(st.Expect.BodyMatch, body); !ok {
			return Fail, detail, false
		}
	}
	if st.Expect.ItemShape != nil {
		if detail, ok := matchItemShape(*st.Expect.ItemShape, body); !ok {
			return Fail, detail, false
		}
	}

	// A fixture, when present, is the strongest check: full normalized equality,
	// plus the recorded status and error type when a meta sidecar exists.
	if raw, err := os.ReadFile(fixPath); err == nil {
		if meta, err := readMeta(fixPath); err == nil {
			if meta.Status != 0 && status != meta.Status {
				return Fail, fmt.Sprintf("status %d, recorded %d", status, meta.Status), true
			}
			if meta.ErrorType != errType {
				return Fail, fmt.Sprintf("error type %q, recorded %q", errType, meta.ErrorType), true
			}
		}
		want := r.cfg.Tiers.ApplyTier(Normalize(raw), r.cfg.Tier)
		got := r.cfg.Tiers.ApplyTier(Normalize(body), r.cfg.Tier)
		if !jsonEqual(want, got) {
			return Fail, "response diverges from fixture " + fixPath, true
		}
		return Pass, "", true
	}
	return Pass, "", false
}

type fixtureMeta struct {
	Status    int    `json:"status"`
	ErrorType string `json:"errorType,omitempty"`
	// ObservedStates is recorded truth, not an assertion. An emulator that
	// settles instantly walks a shorter path than the live service and is
	// still conformant on every observable endpoint state; asserting this
	// would fail floci by design. It exists so implementers can read the
	// real transition order off a recording.
	ObservedStates []string `json:"observedStates,omitempty"`
}

func metaPath(fixPath string) string {
	return strings.TrimSuffix(fixPath, ".json") + ".meta.json"
}

func writeMeta(fixPath string, status int, errType string, observed []string) error {
	// A single observation is just the settled state the fixture already
	// shows — only a genuine transition is worth recording.
	if len(observed) < 2 {
		observed = nil
	}
	raw, err := json.MarshalIndent(fixtureMeta{Status: status, ErrorType: errType, ObservedStates: observed}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metaPath(fixPath), append(raw, '\n'), 0o644)
}

func readMeta(fixPath string) (fixtureMeta, error) {
	var meta fixtureMeta
	raw, err := os.ReadFile(metaPath(fixPath))
	if err != nil {
		return meta, err
	}
	err = json.Unmarshal(raw, &meta)
	return meta, err
}

func (r *Runner) captureVars(st Step, body []byte, vars map[string]string) {
	for name, path := range st.Capture {
		if v, ok := dotPath(body, path); ok {
			vars[name] = v
		}
	}
}

// matchItemShape checks that every element of a list carries the members a
// client reads, without checking their values.
func matchItemShape(shape ItemShape, body []byte) (string, bool) {
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return "item shape: response is not JSON", false
	}
	node := doc
	if shape.Path != "" {
		for _, seg := range strings.Split(shape.Path, ".") {
			m, ok := node.(map[string]any)
			if !ok {
				return "item shape: " + shape.Path + " is not reachable", false
			}
			node, ok = m[seg]
			if !ok {
				return "item shape: response has no " + shape.Path, false
			}
		}
	}
	items, ok := node.([]any)
	if !ok {
		return "item shape: " + shape.Path + " is not a list", false
	}
	if len(items) < shape.MinItems {
		return fmt.Sprintf("item shape: %d items, want at least %d", len(items), shape.MinItems), false
	}

	want := map[string]bool{}
	for _, k := range shape.Required {
		want[k] = true
	}
	for i, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return fmt.Sprintf("item shape: item %d is not an object", i), false
		}
		var missing []string
		for k := range want {
			if _, present := item[k]; !present {
				missing = append(missing, k)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			return fmt.Sprintf("item shape: item %d is missing %v", i, missing), false
		}
		if shape.Exact {
			var extra []string
			for k := range item {
				if !want[k] {
					extra = append(extra, k)
				}
			}
			sort.Strings(extra)
			if len(extra) > 0 {
				return fmt.Sprintf("item shape: item %d has unexpected %v", i, extra), false
			}
		}
	}
	return "", true
}

// resolveVars lets one param reference another, so a case can build an ARN
// out of ${region} instead of pinning one. Iterated rather than recursive:
// a couple of passes covers any nesting a case plausibly has, and a cycle
// stops instead of hanging.
func resolveVars(vars map[string]string) {
	for range 5 {
		changed := false
		for k, v := range vars {
			if !strings.Contains(v, "${") {
				continue
			}
			if next := substitute(v, vars); next != v {
				vars[k] = next
				changed = true
			}
		}
		if !changed {
			return
		}
	}
}

func substitute(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
	}
	return s
}

// dotPath walks a.b.c through a JSON document; array indices are numeric
// segments. Returns the value rendered as a string.
func dotPath(body []byte, path string) (string, bool) {
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", false
	}
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[seg]
			if !ok {
				return "", false
			}
			cur = v
		case []any:
			var idx int
			if _, err := fmt.Sscanf(seg, "%d", &idx); err != nil || idx < 0 || idx >= len(node) {
				return "", false
			}
			cur = node[idx]
		default:
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case float64:
		return strings.TrimSuffix(fmt.Sprintf("%v", v), ".0"), true
	case bool:
		return fmt.Sprintf("%v", v), true
	default:
		b, _ := json.Marshal(v)
		return string(b), true
	}
}

// subsetMatch checks that every leaf in want appears with an equal value in
// got, recursively. Arrays match by index prefix.
func subsetMatch(want json.RawMessage, got []byte) (string, bool) {
	var w, g any
	if err := json.Unmarshal(want, &w); err != nil {
		return "bad bodyMatch: " + err.Error(), false
	}
	if err := json.Unmarshal(got, &g); err != nil {
		return "response is not JSON: " + err.Error(), false
	}
	return matchNode("", w, g)
}

func matchNode(path string, want, got any) (string, bool) {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return path + ": want object", false
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok {
				return path + "." + k + ": missing", false
			}
			if detail, ok := matchNode(path+"."+k, wv, gv); !ok {
				return detail, false
			}
		}
		return "", true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) < len(w) {
			return path + ": want array with at least " + fmt.Sprint(len(w)) + " items", false
		}
		for i, wv := range w {
			if detail, ok := matchNode(fmt.Sprintf("%s[%d]", path, i), wv, g[i]); !ok {
				return detail, false
			}
		}
		return "", true
	default:
		if !reflect.DeepEqual(want, got) {
			return fmt.Sprintf("%s: got %v, want %v", path, got, want), false
		}
		return "", true
	}
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(x, y)
}

func writeFixture(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	norm := Normalize(body)
	var doc any
	if err := json.Unmarshal(norm, &doc); err == nil {
		if pretty, err := json.MarshalIndent(doc, "", "  "); err == nil {
			norm = append(pretty, '\n')
		}
	}
	return os.WriteFile(path, norm, 0o644)
}

func truncate(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
