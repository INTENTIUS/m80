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

type Scenario struct {
	ID    string   `json:"id"`
	Tags  []string `json:"tags"`
	Steps []Step   `json:"steps"`
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
	for k, v := range r.cfg.Params {
		vars[k] = v
	}
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
		if outcome != Pass {
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
		if st.Until.TimeoutSec <= 0 {
			st.Until.TimeoutSec = 30
		}
		if st.Until.IntervalSec <= 0 {
			st.Until.IntervalSec = 1
		}
		deadline = time.Now().Add(time.Duration(st.Until.TimeoutSec * float64(time.Second)))
		interval = time.Duration(st.Until.IntervalSec * float64(time.Second))
	}

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
			if got != st.Until.Equals {
				if time.Now().After(deadline) {
					return Fail, fmt.Sprintf("until %s=%q not reached, last %q", st.Until.Path, st.Until.Equals, got), false
				}
				time.Sleep(interval)
				continue
			}
		}

		outcome, detail, fixture := r.check(s, st, status, body, errType)
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

func (r *Runner) check(s Scenario, st Step, status int, body []byte, errType string) (Outcome, string, bool) {
	fixPath := filepath.Join(r.cfg.FixturesDir, s.ID, st.Name+".json")

	if r.cfg.Record {
		if st.Expect.Status != 0 && status != st.Expect.Status {
			return Fail, fmt.Sprintf("recording aborted: status %d, want %d: %s", status, st.Expect.Status, truncate(body)), false
		}
		if err := writeFixture(fixPath, body); err != nil {
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

	// A fixture, when present, is the strongest check: full normalized equality.
	if raw, err := os.ReadFile(fixPath); err == nil {
		want, got := Normalize(raw), Normalize(body)
		if !jsonEqual(want, got) {
			return Fail, "response diverges from fixture " + fixPath, true
		}
		return Pass, "", true
	}
	return Pass, "", false
}

func (r *Runner) captureVars(st Step, body []byte, vars map[string]string) {
	for name, path := range st.Capture {
		if v, ok := dotPath(body, path); ok {
			vars[name] = v
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
