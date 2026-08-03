// Package inject exposes m80's failure-injection levers over HTTP.
//
// The levers themselves are older than this package and live where the state
// they corrupt lives: images.FailNextBuild and connectors.FailNext. Both were
// reachable only from Go, so a suite that imported m80 could drive them and a
// suite pointed at ghcr.io/intentius/m80 could not (#56). Failure paths are
// the ones a consumer most needs a test target for — a KubeMicroVM test that
// wants to watch its operator handle a failed image build had no way to cause
// one.
//
// Off unless asked for. Nothing under /_m80/ is signed, so anything that can
// reach the port can reach this; a lever that arms a failure is not something
// a container should carry by default. Same posture as -serve-sts: the flag
// is the consent.
package inject

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/intentius/m80/internal/api"
	"github.com/intentius/m80/internal/connectors"
	"github.com/intentius/m80/internal/images"
)

// Path is the one route this package serves.
const Path = "/_m80/inject"

// Targets, as they appear in a request body.
const (
	TargetBuild     = "build"
	TargetConnector = "connector"
)

// Service arms the levers on behalf of an HTTP caller.
type Service struct {
	Images     *images.Service
	Connectors *connectors.Service
}

type request struct {
	Target string `json:"target"`
	// Name of the resource the lever is keyed by.
	//
	// Not an ARN, which is what the issue's sketch reached for: both levers
	// arm *before* the resource exists, so at the moment of arming there is
	// no ARN to name it with. Keying by name is what makes "the next build of
	// this image fails" expressible at all.
	Name string `json:"name"`
	// Connector only. One of connectors.ReasonCodes.
	ReasonCode string `json:"reasonCode"`
}

type response struct {
	// Injected is always true on success, and is the field a consumer should
	// assert on. A state m80 reached on its own never carries it, so a test
	// cannot mistake an injected FAILED for a real one — which is the whole
	// reason to be careful here rather than just returning 200.
	Injected   bool   `json:"injected"`
	Target     string `json:"target"`
	Name       string `json:"name"`
	ReasonCode string `json:"reasonCode,omitempty"`
	// Armed says what will happen and when, because a lever is a promise
	// about a future request rather than a change to anything now.
	Armed string `json:"armed"`
}

// Register wires the route. Call it only when injection was asked for; the
// route is registered either way (see Disabled) so a consumer that forgot the
// flag is told so rather than getting a bare 404 off the end of the mux.
func Register(srv *api.Server, svc *Service) {
	srv.Handle("POST "+Path, svc.serve)
}

// RegisterDisabled wires the route to an explanation. Without this, forgetting
// -enable-injection produces a 404 indistinguishable from a typo in the path.
func RegisterDisabled(srv *api.Server) {
	srv.Handle("POST "+Path, func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound,
			"failure injection is disabled; start m80 with -enable-injection to turn it on")
	})
}

func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "body is not JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required — the levers are keyed by resource name")
		return
	}

	switch req.Target {
	case TargetBuild:
		if req.ReasonCode != "" {
			writeError(w, http.StatusBadRequest,
				"reasonCode is a connector field; a failed build carries a message, not a code")
			return
		}
		s.Images.FailNextBuild(req.Name)
		api.WriteJSON(w, http.StatusOK, response{
			Injected: true,
			Target:   TargetBuild,
			Name:     req.Name,
			Armed:    fmt.Sprintf("the next build of image %q settles FAILED", req.Name),
		})

	case TargetConnector:
		if !connectors.ValidReasonCode(req.ReasonCode) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"reasonCode %q is not one of the model's seven: %s",
				req.ReasonCode, strings.Join(sortedReasonCodes(), ", ")))
			return
		}
		s.Connectors.FailNext(req.Name, req.ReasonCode)
		api.WriteJSON(w, http.StatusOK, response{
			Injected:   true,
			Target:     TargetConnector,
			Name:       req.Name,
			ReasonCode: req.ReasonCode,
			Armed: fmt.Sprintf("the next connector named %q settles FAILED with %s",
				req.Name, req.ReasonCode),
		})

	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"target %q is not one of: %s, %s", req.Target, TargetBuild, TargetConnector))
	}
}

func sortedReasonCodes() []string {
	out := append([]string(nil), connectors.ReasonCodes...)
	sort.Strings(out)
	return out
}

// writeError keeps this surface's errors plainly non-service-shaped. Nothing
// under /_m80/ is the MicroVMs API, so an error here must not look like one a
// client should retry or map to a model exception.
func writeError(w http.ResponseWriter, status int, message string) {
	api.WriteJSON(w, status, map[string]any{"message": message})
}
