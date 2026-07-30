// Command modelwatch re-derives the operation inventory from the upstream
// service models and reports any drift against the committed inventory.json.
//
// The inventory was extracted by hand on 2026-07-29 and is the list every
// coverage number in the conformance report is measured against. If AWS adds
// an operation, changes a URI, or widens an error list, that number silently
// starts lying. This command is the check; CI runs it on a schedule.
//
// Two source formats exist. KubeMicroVM vendors the classic service-2 JSON
// for both services, which is the authority here because it is the only
// public model carrying Lambda Core's network connectors. aws-sdk-go-v2
// publishes a Smithy model for the MicroVMs service alone, used as a
// cross-check so a divergence between AWS's own two representations is
// visible rather than assumed away.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

type inventoryOp struct {
	Operation    string   `json:"operation"`
	Service      string   `json:"service"`
	Method       string   `json:"method"`
	URI          string   `json:"uri"`
	ResponseCode int      `json:"responseCode"`
	Errors       []string `json:"errors"`
}

type inventory struct {
	Generated   string        `json:"generated"`
	SigningName string        `json:"signingName"`
	Operations  []inventoryOp `json:"operations"`
}

// serviceModel is the classic AWS service-2.json shape.
type serviceModel struct {
	Metadata struct {
		ServiceID   string `json:"serviceId"`
		SigningName string `json:"signingName"`
	} `json:"metadata"`
	Operations map[string]struct {
		Name string `json:"name"`
		HTTP struct {
			Method       string `json:"method"`
			RequestURI   string `json:"requestUri"`
			ResponseCode int    `json:"responseCode"`
		} `json:"http"`
		Errors []struct {
			Shape string `json:"shape"`
		} `json:"errors"`
	} `json:"operations"`
}

// smithyModel is the aws-sdk-go-v2 aws-models shape, used for cross-check only.
type smithyModel struct {
	Shapes map[string]struct {
		Type   string `json:"type"`
		Traits struct {
			HTTP struct {
				Method string `json:"method"`
				URI    string `json:"uri"`
				Code   int    `json:"code"`
			} `json:"smithy.api#http"`
		} `json:"traits"`
	} `json:"shapes"`
}

func fetch(src string) ([]byte, error) {
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		return os.ReadFile(src)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(src)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", src, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func opsFromServiceModel(raw []byte) (map[string]inventoryOp, string, error) {
	var m serviceModel
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, "", err
	}
	out := map[string]inventoryOp{}
	for name, op := range m.Operations {
		errs := make([]string, 0, len(op.Errors))
		for _, e := range op.Errors {
			errs = append(errs, e.Shape)
		}
		sort.Strings(errs)
		out[name] = inventoryOp{
			Operation:    name,
			Service:      m.Metadata.ServiceID,
			Method:       op.HTTP.Method,
			URI:          op.HTTP.RequestURI,
			ResponseCode: op.HTTP.ResponseCode,
			Errors:       errs,
		}
	}
	return out, m.Metadata.SigningName, nil
}

// opsFromSmithy returns method/uri/code only — the cross-check cares about
// wire routing, and Smithy error targets are namespaced differently enough
// that comparing them would report noise as drift.
func opsFromSmithy(raw []byte) (map[string]inventoryOp, error) {
	var m smithyModel
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	out := map[string]inventoryOp{}
	for id, sh := range m.Shapes {
		if sh.Type != "operation" {
			continue
		}
		name := id
		if i := strings.LastIndex(id, "#"); i >= 0 {
			name = id[i+1:]
		}
		out[name] = inventoryOp{
			Operation:    name,
			Method:       sh.Traits.HTTP.Method,
			URI:          sh.Traits.HTTP.URI,
			ResponseCode: sh.Traits.HTTP.Code,
		}
	}
	return out, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func main() {
	microvmsURL := flag.String("microvms-model",
		"https://raw.githubusercontent.com/codriverlabs/KubeMicroVM/main/operator-aws-client/src/main/resources/codegen-resources/service-2.json",
		"Lambda Microvms service-2 model (URL or path)")
	coreURL := flag.String("core-model",
		"https://raw.githubusercontent.com/codriverlabs/KubeMicroVM/main/operator-aws-client-core/src/main/resources/codegen-resources/service-2.json",
		"Lambda Core service-2 model (URL or path)")
	sdkURL := flag.String("sdk-model",
		"https://raw.githubusercontent.com/aws/aws-sdk-go-v2/main/codegen/sdk-codegen/aws-models/lambda-microvms.json",
		"aws-sdk-go-v2 Smithy model for the cross-check; empty to skip")
	invPath := flag.String("inventory", "conformance/inventory.json", "committed inventory to check")
	flag.Parse()

	raw, err := os.ReadFile(*invPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "inventory:", err)
		os.Exit(2)
	}
	var inv inventory
	if err := json.Unmarshal(raw, &inv); err != nil {
		fmt.Fprintln(os.Stderr, "inventory:", err)
		os.Exit(2)
	}
	have := map[string]inventoryOp{}
	for _, o := range inv.Operations {
		sort.Strings(o.Errors)
		have[o.Operation] = o
	}

	upstream := map[string]inventoryOp{}
	for _, src := range []string{*microvmsURL, *coreURL} {
		body, err := fetch(src)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fetch:", err)
			os.Exit(2)
		}
		ops, signing, err := opsFromServiceModel(body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse %s: %v\n", src, err)
			os.Exit(2)
		}
		if signing != inv.SigningName {
			fmt.Printf("DRIFT signingName: inventory %q, model %q\n", inv.SigningName, signing)
		}
		for k, v := range ops {
			upstream[k] = v
		}
	}

	var drift []string
	for name, up := range upstream {
		cur, ok := have[name]
		if !ok {
			drift = append(drift, fmt.Sprintf("ADDED   %s (%s %s) — not in inventory, so no conformance case covers it",
				name, up.Method, up.URI))
			continue
		}
		if cur.Method != up.Method || cur.URI != up.URI {
			drift = append(drift, fmt.Sprintf("ROUTE   %s: inventory %s %s, model %s %s",
				name, cur.Method, cur.URI, up.Method, up.URI))
		}
		if cur.ResponseCode != up.ResponseCode {
			drift = append(drift, fmt.Sprintf("STATUS  %s: inventory %d, model %d",
				name, cur.ResponseCode, up.ResponseCode))
		}
		if !sameStrings(cur.Errors, up.Errors) {
			drift = append(drift, fmt.Sprintf("ERRORS  %s: inventory %v, model %v",
				name, cur.Errors, up.Errors))
		}
	}
	for name := range have {
		if _, ok := upstream[name]; !ok {
			drift = append(drift, fmt.Sprintf("REMOVED %s — in inventory, absent upstream", name))
		}
	}

	if *sdkURL != "" {
		body, err := fetch(*sdkURL)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sdk cross-check skipped:", err)
		} else if sdkOps, err := opsFromSmithy(body); err != nil {
			fmt.Fprintln(os.Stderr, "sdk cross-check skipped:", err)
		} else {
			for name, sdk := range sdkOps {
				up, ok := upstream[name]
				if !ok {
					drift = append(drift, fmt.Sprintf("SDK-ONLY %s (%s %s) — in aws-sdk-go-v2, absent from the vendored models",
						name, sdk.Method, sdk.URI))
					continue
				}
				if up.Method != sdk.Method || up.URI != sdk.URI {
					drift = append(drift, fmt.Sprintf("SDK-DIFF %s: vendored %s %s, aws-sdk-go-v2 %s %s",
						name, up.Method, up.URI, sdk.Method, sdk.URI))
				}
			}
		}
	}

	sort.Strings(drift)
	fmt.Printf("inventory: %d operations (generated %s)\nupstream:  %d operations\n\n",
		len(have), inv.Generated, len(upstream))
	if len(drift) == 0 {
		fmt.Println("no drift")
		return
	}
	for _, d := range drift {
		fmt.Println(d)
	}
	fmt.Printf("\n%d drift finding(s)\n", len(drift))
	os.Exit(1)
}
