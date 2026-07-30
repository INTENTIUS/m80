// Command throttleprobe answers the one question the service models cannot:
// which operation throttles, and roughly when.
//
// The models give every throttle error shape and all six ThrottleReason
// values for free. What they cannot say is whether a burst of concurrent VM
// launches comes back as ThrottlingException, ServiceQuotaExceededException,
// or simply succeeds. KubeMicroVM's QuotaGuard is built around
// ConcurrentSnapshotCreateLimitExceeded, so RunMicrovm is the operation worth
// probing and concurrency is the axis that provokes it.
//
// This is a separate command rather than a conformance case on purpose. The
// probe is irreducibly imperative: it fans out concurrently, collects ids from
// responses it may not be able to assert on, and must terminate every VM it
// created even when the run fails. The declarative case format models none of
// that, and shoehorning it in would make the runner worse at its actual job.
//
// SCOPE, DELIBERATE: -n defaults to 6, every VM is terminated the moment the
// burst returns, and teardown runs even on panic or partial failure. This is
// sized to observe a limit, not to stress the service. Do not raise -n
// casually; account-level throttling affects everything else in the account.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/intentius/m80/conformance/runner"
)

type client struct {
	endpoint string
	region   string
	creds    aws.Credentials
	http     *http.Client
}

type reply struct {
	Status    int
	ErrorType string
	Body      []byte
}

func (c *client) do(method, path string, body any) (reply, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return reply{}, err
		}
	}
	req, err := http.NewRequest(method, c.endpoint+path, bytes.NewReader(payload))
	if err != nil {
		return reply{}, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := runner.Sign(req, payload, c.creds, c.region); err != nil {
		return reply{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return reply{}, err
	}
	et := resp.Header.Get("X-Amzn-Errortype")
	if i := strings.IndexAny(et, ":#"); i >= 0 {
		et = et[:i]
	}
	return reply{Status: resp.StatusCode, ErrorType: et, Body: raw}, nil
}

func field(raw []byte, key string) string {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	if v, ok := doc[key].(string); ok {
		return v
	}
	return ""
}

func main() {
	endpoint := flag.String("endpoint", "https://lambda.us-east-2.amazonaws.com", "service endpoint")
	region := flag.String("region", "us-east-2", "sigv4 region")
	n := flag.Int("n", 6, "concurrent RunMicrovm attempts; keep this small")
	imageArn := flag.String("image-arn", "", "existing built image to launch from; created and deleted here when empty")
	baseImage := flag.String("base-image-arn", "", "managed base image (required when creating)")
	buildRole := flag.String("build-role-arn", "", "build role (required when creating)")
	codeArtifact := flag.String("code-artifact-uri", "", "s3 code artifact (required when creating)")
	name := flag.String("name", "m80-conf-throttle-image", "image name when creating")
	outDir := flag.String("out", "conformance/fixtures/throttle", "fixture directory")
	buildTimeout := flag.Duration("build-timeout", 45*time.Minute, "how long to wait for the image build")
	flag.Parse()

	if *n > 12 {
		fmt.Fprintf(os.Stderr, "refusing -n %d: this probe is scoped to observe a limit, not to stress the service\n", *n)
		os.Exit(2)
	}

	c := &client{
		endpoint: strings.TrimSuffix(*endpoint, "/"),
		region:   *region,
		creds:    runner.CredentialsFromEnv(),
		http:     &http.Client{Timeout: 60 * time.Second},
	}

	created := false
	if *imageArn == "" {
		if *baseImage == "" || *buildRole == "" || *codeArtifact == "" {
			fmt.Fprintln(os.Stderr, "-image-arn omitted, so -base-image-arn, -build-role-arn and -code-artifact-uri are required")
			os.Exit(2)
		}
		arn, err := createImage(c, *name, *baseImage, *buildRole, *codeArtifact, *buildTimeout)
		if err != nil {
			fmt.Fprintln(os.Stderr, "image:", err)
			os.Exit(1)
		}
		*imageArn = arn
		created = true
	}

	// Teardown is deferred before the first VM exists so no early return, and
	// no panic, can strand a running VM.
	var launched []string
	defer func() {
		teardown(c, launched)
		if created {
			deleteImage(c, *imageArn)
		}
	}()

	fmt.Printf("bursting %d concurrent RunMicrovm against %s\n", *n, *imageArn)
	replies := burst(c, *imageArn, *n)

	for _, r := range replies {
		if id := field(r.Body, "microvmId"); id != "" {
			launched = append(launched, id)
		}
	}

	report(replies, launched, *outDir, *n)
}

func createImage(c *client, name, base, role, code string, timeout time.Duration) (string, error) {
	r, err := c.do("POST", "/2025-09-09/microvm-images", map[string]any{
		"name":         name,
		"baseImageArn": base,
		"buildRoleArn": role,
		"codeArtifact": map[string]any{"uri": code},
	})
	if err != nil {
		return "", err
	}
	if r.Status != 201 {
		return "", fmt.Errorf("create returned %d: %s", r.Status, r.Body)
	}
	arn := field(r.Body, "imageArn")
	version := field(r.Body, "imageVersion")
	fmt.Printf("building %s version %s\n", arn, version)

	deadline := time.Now().Add(timeout)
	for {
		vr, err := c.do("GET", "/2025-09-09/microvm-images/"+arn+"/versions/"+version, nil)
		if err != nil {
			return "", err
		}
		switch state := field(vr.Body, "state"); state {
		case "SUCCESSFUL":
			return arn, nil
		case "FAILED":
			return "", fmt.Errorf("build failed: %s", vr.Body)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("build did not finish within %s", timeout)
		}
		time.Sleep(10 * time.Second)
	}
}

// burst issues every RunMicrovm at once and waits for all of them. Responses
// come back in launch order so the report reads deterministically.
func burst(c *client, imageArn string, n int) []reply {
	replies := make([]reply, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release together, so the calls actually overlap
			r, err := c.do("POST", "/2025-09-09/microvms", map[string]any{
				"imageIdentifier": imageArn,
			})
			if err != nil {
				replies[i] = reply{Status: -1, Body: []byte(err.Error())}
				return
			}
			replies[i] = r
		}(i)
	}
	close(start)
	wg.Wait()
	return replies
}

func teardown(c *client, ids []string) {
	for _, id := range ids {
		var lastErr error
		for attempt := range 5 {
			// TerminateMicrovm is a DELETE on the VM, not a /terminate
			// sub-resource. Getting this wrong strands billable VMs.
			r, err := c.do("DELETE", "/2025-09-09/microvms/"+id, nil)
			if err == nil && r.Status < 400 {
				fmt.Printf("terminated %s\n", id)
				lastErr = nil
				break
			}
			if err != nil {
				lastErr = err
			} else {
				lastErr = fmt.Errorf("status %d: %s", r.Status, r.Body)
			}
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
		}
		if lastErr != nil {
			// Loud, and on stderr: a stranded VM bills until someone acts.
			fmt.Fprintf(os.Stderr, "TERMINATE FAILED for %s: %v — terminate it by hand\n", id, lastErr)
		}
	}
}

func deleteImage(c *client, arn string) {
	// Delete is refused while VMs are still draining, so retry past the
	// TERMINATING window rather than giving up on the first 400.
	for attempt := range 10 {
		r, err := c.do("DELETE", "/2025-09-09/microvm-images/"+arn, nil)
		if err == nil && r.Status < 400 {
			fmt.Printf("deleted %s\n", arn)
			return
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Second)
	}
	fmt.Fprintf(os.Stderr, "DELETE FAILED for %s — delete it by hand\n", arn)
}

func report(replies []reply, launched []string, outDir string, n int) {
	counts := map[string]int{}
	var throttled *reply
	for i := range replies {
		r := replies[i]
		key := fmt.Sprintf("%d %s", r.Status, r.ErrorType)
		counts[key]++
		if r.Status == 429 || strings.Contains(r.ErrorType, "Throttling") ||
			strings.Contains(r.ErrorType, "TooManyRequests") ||
			strings.Contains(r.ErrorType, "ServiceQuotaExceeded") {
			if throttled == nil {
				throttled = &replies[i]
			}
		}
	}

	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("\n%d attempts, %d VMs launched\n", n, len(launched))
	for _, k := range keys {
		fmt.Printf("  %2d x %s\n", counts[k], k)
	}

	if throttled == nil {
		fmt.Printf("\nno throttle observed at concurrency %d — the limit is above this, which is\n"+
			"itself the recorded answer. Raising -n materially is a maintainer decision.\n", n)
		return
	}

	fmt.Printf("\nthrottle observed: %d %s\n%s\n", throttled.Status, throttled.ErrorType, throttled.Body)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "fixture:", err)
		return
	}
	body := runner.Normalize(throttled.Body)
	var doc any
	if json.Unmarshal(body, &doc) == nil {
		if pretty, err := json.MarshalIndent(doc, "", "  "); err == nil {
			body = append(pretty, '\n')
		}
	}
	if err := os.WriteFile(filepath.Join(outDir, "run-burst.json"), body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "fixture:", err)
		return
	}
	meta, _ := json.MarshalIndent(map[string]any{
		"status":      throttled.Status,
		"errorType":   throttled.ErrorType,
		"concurrency": n,
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "run-burst.meta.json"), append(meta, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "fixture:", err)
	}
	fmt.Printf("recorded to %s\n", outDir)
}
