package runner

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

type Inventory struct {
	SigningName string        `json:"signingName"`
	Operations  []InventoryOp `json:"operations"`
}

type InventoryOp struct {
	Operation    string   `json:"operation"`
	Service      string   `json:"service"`
	Method       string   `json:"method"`
	URI          string   `json:"uri"`
	ResponseCode int      `json:"responseCode"`
	Errors       []string `json:"errors"`
}

func LoadInventory(path string) (Inventory, error) {
	var inv Inventory
	raw, err := os.ReadFile(path)
	if err != nil {
		return inv, err
	}
	err = json.Unmarshal(raw, &inv)
	return inv, err
}

type Report struct {
	Results  []StepResult    `json:"results"`
	Summary  map[Outcome]int `json:"summary"`
	Coverage Coverage        `json:"coverage"`
}

type Coverage struct {
	Exercised   []string `json:"exercised"`
	Unexercised []string `json:"unexercised"`
}

func BuildReport(inv Inventory, results []StepResult) Report {
	rep := Report{Results: results, Summary: map[Outcome]int{}}
	seen := map[string]bool{}
	for _, r := range results {
		rep.Summary[r.Outcome]++
		if r.Operation != "" && r.Outcome != Skipped {
			seen[r.Operation] = true
		}
	}
	for _, op := range inv.Operations {
		if seen[op.Operation] {
			rep.Coverage.Exercised = append(rep.Coverage.Exercised, op.Operation)
		} else {
			rep.Coverage.Unexercised = append(rep.Coverage.Unexercised, op.Operation)
		}
	}
	sort.Strings(rep.Coverage.Exercised)
	sort.Strings(rep.Coverage.Unexercised)
	return rep
}

func (rep Report) WriteText(w io.Writer) {
	for _, r := range rep.Results {
		line := fmt.Sprintf("%-14s %s / %s", r.Outcome, r.Scenario, r.Step)
		if r.Fixture {
			line += "  [fixture]"
		}
		if r.Detail != "" {
			line += "  — " + r.Detail
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "\npass %d, fail %d, unimplemented %d, skipped %d, error %d\n",
		rep.Summary[Pass], rep.Summary[Fail], rep.Summary[Unimplemented], rep.Summary[Skipped], rep.Summary[Errored])
	fmt.Fprintf(w, "coverage: %d/%d operations exercised\n",
		len(rep.Coverage.Exercised), len(rep.Coverage.Exercised)+len(rep.Coverage.Unexercised))
	if len(rep.Coverage.Unexercised) > 0 {
		fmt.Fprintf(w, "unexercised: %v\n", rep.Coverage.Unexercised)
	}
}

func (rep Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// Failed reports whether the run should exit nonzero: any fail or error.
// Unimplemented is a valid outcome for a target under construction.
func (rep Report) Failed() bool {
	return rep.Summary[Fail] > 0 || rep.Summary[Errored] > 0
}
