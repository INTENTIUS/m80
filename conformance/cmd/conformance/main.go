// Command conformance runs the m80 conformance suite against an endpoint:
// real AWS (with --record to write fixtures), an m80 instance, or a floci
// module (--tags subset:floci).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/intentius/m80/conformance/runner"
)

func main() {
	endpoint := flag.String("endpoint", "", "target endpoint URL (required)")
	region := flag.String("region", "us-east-1", "sigv4 region")
	cases := flag.String("cases", "conformance/cases", "scenario directory")
	fixtures := flag.String("fixtures", "conformance/fixtures", "fixture directory")
	tags := flag.String("tags", "", "comma-separated tags a scenario must all carry")
	record := flag.Bool("record", false, "record fixtures from responses instead of asserting")
	maxPoll := flag.Float64("poll-timeout", 0, "cap every until timeout, in seconds (0 keeps case values; use ~15 against an emulator)")
	tier := flag.String("tier", "all", "comparison strictness: all, or load-bearing to ignore members nothing branches on")
	keepGoing := flag.Bool("keep-going", false, "continue a scenario past a step whose body diverged, to report every divergence in one run rather than the first")
	tiersPath := flag.String("tiers", "conformance/tiers.json", "member tier classification")
	vmRewrite := flag.String("vm-endpoint-rewrite", "", "send VM-endpoint steps here, keeping the Host header (like curl --resolve); needed against a local target, empty for a recording run")
	asJSON := flag.Bool("json", false, "JSON report on stdout")
	inventoryPath := flag.String("inventory", "conformance/inventory.json", "operation inventory")
	var params paramFlags
	flag.Var(&params, "param", "template parameter key=value (repeatable)")
	flag.Parse()

	if *endpoint == "" {
		fmt.Fprintln(os.Stderr, "-endpoint is required")
		os.Exit(2)
	}

	var tagFilter []string
	if *tags != "" {
		tagFilter = strings.Split(*tags, ",")
	}

	inv, err := runner.LoadInventory(*inventoryPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "inventory:", err)
		os.Exit(2)
	}

	if *tier != string(runner.TierAll) && *tier != string(runner.TierLoadBearing) {
		fmt.Fprintf(os.Stderr, "unknown -tier %q: want all or load-bearing\n", *tier)
		os.Exit(2)
	}
	tiers, err := runner.LoadTiers(*tiersPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tiers:", err)
		os.Exit(2)
	}

	r := runner.New(runner.Config{
		Endpoint:          *endpoint,
		Region:            *region,
		CasesDir:          *cases,
		FixturesDir:       *fixtures,
		TagFilter:         tagFilter,
		Record:            *record,
		MaxPollSec:        *maxPoll,
		Tier:              runner.Tier(*tier),
		KeepGoing:         *keepGoing,
		VMEndpointRewrite: *vmRewrite,
		Tiers:             tiers,
		Params:            params.m,
		Credentials:       runner.CredentialsFromEnv(),
	})

	scenarios, err := r.LoadScenarios()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cases:", err)
		os.Exit(2)
	}

	rep := runner.BuildReport(inv, r.Run(scenarios))
	if *asJSON {
		if err := rep.WriteJSON(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	} else {
		rep.WriteText(os.Stdout)
	}
	if rep.Failed() {
		os.Exit(1)
	}
}

type paramFlags struct{ m map[string]string }

func (p *paramFlags) String() string { return "" }

func (p *paramFlags) Set(v string) error {
	if p.m == nil {
		p.m = map[string]string{}
	}
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("want key=value, got %q", v)
	}
	p.m[k] = val
	return nil
}
