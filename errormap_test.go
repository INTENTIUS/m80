package m80_test

// The error-mapping sweep (#15).
//
// The model gives the error shape list and says nothing about which operation
// returns which one when. The fixtures answer that for the paths a recording
// reached; this test pins the whole table in one place so a later change to
// any handler that quietly moves an operation from one error type to another
// fails here rather than in a consumer.
//
// It wires the same server cmd/m80 does, because the mapping is a property of
// the assembled emulator rather than of any one package.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/intentius/m80/internal/api"
	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/connectors"
	"github.com/intentius/m80/internal/images"
	"github.com/intentius/m80/internal/limits"
	"github.com/intentius/m80/internal/managedimages"
	"github.com/intentius/m80/internal/store"
	"github.com/intentius/m80/internal/tags"
	"github.com/intentius/m80/internal/tokens"
	"github.com/intentius/m80/internal/vms"
)

const (
	region = "us-east-1"
	hop    = time.Second
)

type server struct {
	srv *api.Server
	clk *clock.Test
	lim *limits.Service
}

func newServer(t *testing.T, cfg limits.Config) *server {
	t.Helper()
	clk := clock.NewTest(time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC))
	st := store.New()
	srv := api.NewServer(clk, st, "test")
	managedimages.Register(srv)

	lim := limits.NewService(clk, cfg)
	srv.Gate = lim.Gate

	imageSvc := images.NewService(clk, st, hop)
	imageSvc.SnapshotQuota = lim
	vmSvc := vms.NewService(clk, st, hop)
	connectorSvc := connectors.NewService(clk, st, hop)

	images.Register(srv, imageSvc, vmSvc)
	vms.Register(srv, vmSvc, imageSvc, lim)
	connectors.Register(srv, connectorSvc)
	tags.Register(srv, imageSvc, vmSvc, connectorSvc)
	tokenSvc := tokens.NewService(clk)
	tokens.Register(srv, tokenSvc, vmSvc)
	srv.Intercept = tokens.NewEndpoint(tokenSvc, vmSvc, nil).Intercept

	return &server{srv: srv, clk: clk, lim: lim}
}

func (s *server) do(method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	var r *http.Request
	if body != nil {
		raw, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=AKID/20260730/"+region+"/lambda/aws4_request, SignedHeaders=host, Signature=x")
	rec := httptest.NewRecorder()
	s.srv.ServeHTTP(rec, r)
	var doc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	return rec, doc
}

// buildImage creates an image and settles it to a runnable version.
func (s *server) buildImage(t *testing.T, name string) string {
	t.Helper()
	rec, doc := s.do("POST", "/2025-09-09/microvm-images", map[string]any{
		"name":         name,
		"baseImageArn": "arn:aws:lambda:" + region + ":aws:microvm-image:al2023-1",
		"buildRoleArn": "arn:aws:iam::000000000000:role/build",
		"codeArtifact": map[string]any{"uri": "s3://bucket/code.zip"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create image: status %d (%s)", rec.Code, rec.Body.String())
	}
	for i := 0; i < 6; i++ {
		s.clk.Advance(hop)
	}
	return doc["imageArn"].(string)
}

func (s *server) runVM(t *testing.T, imageArn string) string {
	t.Helper()
	rec, doc := s.do("POST", "/2025-09-09/microvms", map[string]any{"imageIdentifier": imageArn})
	if rec.Code != http.StatusOK {
		t.Fatalf("run: status %d (%s)", rec.Code, rec.Body.String())
	}
	s.clk.Advance(hop)
	return doc["microvmId"].(string)
}

// The mapping table. Every row is either recorded or derived from the model,
// and the "why" column says which.
func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name      string
		why       string
		setup     func(t *testing.T, s *server) (method, path string, body any)
		status    int
		errorType string
		message   string // substring
	}{
		{
			name: "missing VM",
			why:  "recorded",
			setup: func(t *testing.T, s *server) (string, string, any) {
				return "GET", "/2025-09-09/microvms/microvm-00000000-0000-0000-0000-000000000000", nil
			},
			status: 404, errorType: "ResourceNotFoundException", message: "MicroVM not found",
		},
		{
			name: "terminal-state mutation is not a conflict",
			why:  "recorded, and the surprise of the whole surface",
			setup: func(t *testing.T, s *server) (string, string, any) {
				arn := s.buildImage(t, "terminal-img")
				id := s.runVM(t, arn)
				s.do("DELETE", "/2025-09-09/microvms/"+id, nil)
				s.clk.Advance(hop)
				return "POST", "/2025-09-09/microvms/" + id + "/suspend", nil
			},
			status: 400, errorType: "ValidationException", message: "has been terminated",
		},
		{
			name: "run against an image with nothing built",
			why:  "recorded: the missing version is reported, not the missing image",
			setup: func(t *testing.T, s *server) (string, string, any) {
				return "POST", "/2025-09-09/microvms", map[string]any{
					"imageIdentifier": "arn:aws:lambda:" + region + ":000000000000:microvm-image:absent",
				}
			},
			status: 404, errorType: "ResourceNotFoundException", message: "No active version found",
		},
		{
			name: "missing required member",
			why:  "model: imageIdentifier is required",
			setup: func(t *testing.T, s *server) (string, string, any) {
				return "POST", "/2025-09-09/microvms", map[string]any{}
			},
			status: 400, errorType: "ValidationException", message: "imageIdentifier",
		},
		{
			name: "token against a terminated VM",
			why:  "the recorded terminal-state rule, applied to tokens",
			setup: func(t *testing.T, s *server) (string, string, any) {
				arn := s.buildImage(t, "token-img")
				id := s.runVM(t, arn)
				s.do("DELETE", "/2025-09-09/microvms/"+id, nil)
				s.clk.Advance(hop)
				return "POST", "/2025-09-09/microvms/" + id + "/auth-token", map[string]any{
					"expirationInMinutes": 60,
					"allowedPorts":        []any{map[string]any{"allPorts": map[string]any{}}},
				}
			},
			status: 400, errorType: "ValidationException", message: "has been terminated",
		},
		{
			name: "shell token without SHELL_INGRESS",
			why:  "recorded; the only observable behavior this operation has",
			setup: func(t *testing.T, s *server) (string, string, any) {
				arn := s.buildImage(t, "shell-img")
				id := s.runVM(t, arn)
				return "POST", "/2025-09-09/microvms/" + id + "/shell-auth-token",
					map[string]any{"expirationInMinutes": 60}
			},
			status: 400, errorType: "ValidationException", message: "SHELL_INGRESS",
		},
		{
			name: "connector constraint violation",
			why:  "recorded: a validation layer in front of the service",
			setup: func(t *testing.T, s *server) (string, string, any) {
				subnets := make([]any, 17)
				for i := range subnets {
					subnets[i] = "subnet-0000000000000000" + string(rune('a'+i))
				}
				return "POST", "/2026-04-04/network-connectors", map[string]any{
					"Name": "too-many",
					"Configuration": map[string]any{
						"VpcEgressConfiguration": map[string]any{"SubnetIds": subnets},
					},
				}
			},
			status: 400, errorType: "ValidationException", message: "1 validation error detected",
		},
		{
			name: "connector enforced-but-optional member",
			why:  "recorded: service logic rather than a model constraint",
			setup: func(t *testing.T, s *server) (string, string, any) {
				return "POST", "/2026-04-04/network-connectors", map[string]any{
					"Name": "no-token",
					"Configuration": map[string]any{
						"VpcEgressConfiguration": map[string]any{
							"SubnetIds":                      []any{"subnet-1"},
							"NetworkProtocol":                "IPv4",
							"AssociatedComputeResourceTypes": []any{"MicroVm"},
						},
					},
					"OperatorRole": "arn:aws:iam::000000000000:role/op",
				}
			},
			status: 400, errorType: "InvalidParameterValueException", message: "ClientToken is a required field",
		},
		{
			name: "missing connector",
			why:  "recorded: capital Message, and an ARN echoed back",
			setup: func(t *testing.T, s *server) (string, string, any) {
				return "GET", "/2026-04-04/network-connectors/nc-absent", nil
			},
			status: 404, errorType: "ResourceNotFoundException", message: "",
		},
		{
			name: "tags on a resource that does not exist",
			why:  "model: ResourceNotFoundException is on all three tag operations",
			setup: func(t *testing.T, s *server) (string, string, any) {
				return "GET", "/2017-03-31/tags/arn:aws:lambda:" + region +
					":000000000000:microvm-image:absent", nil
			},
			status: 404, errorType: "ResourceNotFoundException", message: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newServer(t, limits.Config{})
			method, path, body := c.setup(t, s)
			rec, doc := s.do(method, path, body)

			if rec.Code != c.status {
				t.Fatalf("status %d, want %d (%s) — %s", rec.Code, c.status, rec.Body.String(), c.why)
			}
			if got := rec.Header().Get("X-Amzn-Errortype"); got != c.errorType {
				t.Errorf("error type %q, want %q — %s", got, c.errorType, c.why)
			}
			if c.message != "" {
				// Lambda Core uses a capital Message; Lambda Microvms lowercase.
				msg, _ := doc["message"].(string)
				if msg == "" {
					msg, _ = doc["Message"].(string)
				}
				if !strings.Contains(msg, c.message) {
					t.Errorf("message %q, want it to mention %q", msg, c.message)
				}
			}
			// No error body may carry __type: the live service does not put one
			// there, and an emulator that adds it diverges on every error.
			if _, has := doc["__type"]; has {
				t.Error("error body carries __type; the type rides X-Amzn-Errortype alone")
			}
		})
	}
}

// The two 409 shapes are easy to confuse, and nothing recorded uses either on
// the MicroVM family. Connectors do: ResourceConflictException, whose body is
// Type and message rather than ConflictException's resourceId and
// resourceType.
func TestConnectorNameCollisionIsResourceConflict(t *testing.T) {
	s := newServer(t, limits.Config{})
	body := func(token string) map[string]any {
		return map[string]any{
			"Name": "dup",
			"Configuration": map[string]any{
				"VpcEgressConfiguration": map[string]any{
					"SubnetIds":                      []any{"subnet-1"},
					"NetworkProtocol":                "IPv4",
					"AssociatedComputeResourceTypes": []any{"MicroVm"},
				},
			},
			"ClientToken":  token,
			"OperatorRole": "arn:aws:iam::000000000000:role/op",
		}
	}
	if rec, _ := s.do("POST", "/2026-04-04/network-connectors", body("one")); rec.Code != http.StatusAccepted {
		t.Fatalf("create: status %d", rec.Code)
	}
	rec, _ := s.do("POST", "/2026-04-04/network-connectors", body("two"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	if got := rec.Header().Get("X-Amzn-Errortype"); got != "ResourceConflictException" {
		t.Errorf("error type %q, want ResourceConflictException", got)
	}
}

// The recorded burst, end to end: six RunMicrovm calls against a fresh
// account admit two and reject four with the 402.
func TestAccountMemoryCeilingRejectsTheRecordedBurst(t *testing.T) {
	s := newServer(t, limits.Config{MaxAccountMemoryMiB: limits.DefaultAccountMemoryMiB})
	arn := s.buildImage(t, "burst-img")

	admitted, rejected := 0, 0
	for i := 0; i < 6; i++ {
		rec, doc := s.do("POST", "/2025-09-09/microvms", map[string]any{"imageIdentifier": arn})
		switch rec.Code {
		case http.StatusOK:
			admitted++
		case http.StatusPaymentRequired:
			rejected++
			if got := rec.Header().Get("X-Amzn-Errortype"); got != "ServiceQuotaExceededException" {
				t.Errorf("error type %q", got)
			}
			if doc["message"] != limits.QuotaMessage {
				t.Errorf("message %q, want the recorded one", doc["message"])
			}
		default:
			t.Fatalf("call %d: status %d (%s)", i, rec.Code, rec.Body.String())
		}
	}
	if admitted != 2 || rejected != 4 {
		t.Errorf("%d admitted / %d rejected, want 2 / 4 — the recorded burst", admitted, rejected)
	}
}

// Terminating a VM gives its memory back, so the ceiling is a live allocation
// rather than a lifetime count.
func TestTerminatedVMsReleaseTheirMemory(t *testing.T) {
	s := newServer(t, limits.Config{MaxAccountMemoryMiB: limits.DefaultAccountMemoryMiB})
	arn := s.buildImage(t, "release-img")

	first := s.runVM(t, arn)
	s.runVM(t, arn)
	if rec, _ := s.do("POST", "/2025-09-09/microvms", map[string]any{"imageIdentifier": arn}); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("third VM: status %d, want 402", rec.Code)
	}

	s.do("DELETE", "/2025-09-09/microvms/"+first, nil)
	s.clk.Advance(hop)
	if rec, _ := s.do("POST", "/2025-09-09/microvms", map[string]any{"imageIdentifier": arn}); rec.Code != http.StatusOK {
		t.Errorf("status %d after terminating one, want the memory released", rec.Code)
	}
}

// The concurrent snapshot-create cap, which is what
// ConcurrentSnapshotCreateLimitExceeded names.
func TestConcurrentSnapshotCreateCap(t *testing.T) {
	s := newServer(t, limits.Config{MaxConcurrentSnapshotCreates: 1})
	create := func(name string) *httptest.ResponseRecorder {
		rec, _ := s.do("POST", "/2025-09-09/microvm-images", map[string]any{
			"name":         name,
			"baseImageArn": "arn:aws:lambda:" + region + ":aws:microvm-image:al2023-1",
			"buildRoleArn": "arn:aws:iam::000000000000:role/build",
			"codeArtifact": map[string]any{"uri": "s3://bucket/code.zip"},
		})
		return rec
	}
	if rec := create("snap-one"); rec.Code != http.StatusCreated {
		t.Fatalf("first: status %d", rec.Code)
	}
	rec := create("snap-two")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second concurrent build: status %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("X-Amzn-Errortype"); got != "ThrottlingException" {
		t.Errorf("error type %q", got)
	}

	// Once the first build drains, another is admitted.
	for i := 0; i < 6; i++ {
		s.clk.Advance(hop)
	}
	if rec := create("snap-three"); rec.Code != http.StatusCreated {
		t.Errorf("status %d after the first build finished, want 201", rec.Code)
	}
}
