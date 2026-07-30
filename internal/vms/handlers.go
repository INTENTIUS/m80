package vms

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/intentius/m80/internal/api"
)

// ImageResolver is what vms needs from images: does this image exist, and
// which version would a VM run. An interface so the two packages do not
// depend on each other's internals.
type ImageResolver interface {
	// ResolveRunnable returns the image ARN and the version a VM would run,
	// or false when the image is unknown or has nothing runnable.
	ResolveRunnable(region, identifier string) (arn string, version string, ok bool)
}

func Register(srv *api.Server, svc *Service, images ImageResolver) {
	h := &handlers{svc: svc, images: images}
	srv.Register("RunMicrovm", h.run)
	srv.Register("GetMicrovm", h.get)
	srv.Register("ListMicrovms", h.list)
	srv.Register("TerminateMicrovm", h.terminate)
}

type handlers struct {
	svc    *Service
	images ImageResolver
}

func decode(r *http.Request, into any) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) == 0 {
		return err
	}
	return json.Unmarshal(raw, into)
}

func (h *handlers) run(w http.ResponseWriter, r *http.Request) {
	region := api.RegionFromRequest(r)
	var req struct {
		ImageIdentifier *string `json:"imageIdentifier"`
		IdlePolicy      *struct {
			AutoResumeEnabled        *bool `json:"autoResumeEnabled"`
			MaxIdleDurationSeconds   *int  `json:"maxIdleDurationSeconds"`
			SuspendedDurationSeconds *int  `json:"suspendedDurationSeconds"`
		} `json:"idlePolicy"`
	}
	if err := decode(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "ValidationException",
			map[string]any{"message": "Invalid request body"})
		return
	}
	if req.ImageIdentifier == nil {
		api.WriteError(w, http.StatusBadRequest, "ValidationException", map[string]any{
			"message": "1 validation error detected: Value null at 'imageIdentifier' failed to satisfy constraint: Member must not be null",
		})
		return
	}

	var idle *IdlePolicy
	if req.IdlePolicy != nil {
		// autoResumeEnabled is required whenever idlePolicy is present at
		// all — recorded, and the model marks no member of it required.
		if req.IdlePolicy.AutoResumeEnabled == nil {
			api.WriteError(w, http.StatusBadRequest, "ValidationException", map[string]any{
				"message": "1 validation error detected: Value null at 'idlePolicy.autoResumeEnabled' failed to satisfy constraint: Member must not be null",
			})
			return
		}
		idle = &IdlePolicy{
			AutoResumeEnabled:        *req.IdlePolicy.AutoResumeEnabled,
			MaxIdleDurationSeconds:   req.IdlePolicy.MaxIdleDurationSeconds,
			SuspendedDurationSeconds: req.IdlePolicy.SuspendedDurationSeconds,
		}
	}

	arn, version, ok := h.images.ResolveRunnable(region, *req.ImageIdentifier)
	if !ok {
		api.WriteError(w, http.StatusNotFound, "ResourceNotFoundException", map[string]any{
			"message":      "MicroVMImage not found for MicroVMImageID: " + *req.ImageIdentifier,
			"resourceId":   nil,
			"resourceType": nil,
		})
		return
	}

	vm := h.svc.Run(region, arn, version, idle)
	api.WriteJSON(w, http.StatusOK, detail(vm))
}

func (h *handlers) lookup(w http.ResponseWriter, r *http.Request) (*VM, bool) {
	region := api.RegionFromRequest(r)
	id := r.PathValue("microvmIdentifier")
	vm, ok := h.svc.Get(region, id)
	if !ok {
		api.WriteError(w, http.StatusNotFound, "ResourceNotFoundException", map[string]any{
			"message":      "MicroVM not found: " + id,
			"resourceId":   nil,
			"resourceType": nil,
		})
		return nil, false
	}
	return vm, true
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	vm, ok := h.lookup(w, r)
	if !ok {
		return
	}
	api.WriteJSON(w, http.StatusOK, detail(vm))
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	region := api.RegionFromRequest(r)
	items := make([]any, 0)
	for _, vm := range h.svc.List(region) {
		items = append(items, listItem(vm))
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "nextToken": nil})
}

func (h *handlers) terminate(w http.ResponseWriter, r *http.Request) {
	vm, ok := h.lookup(w, r)
	if !ok {
		return
	}
	if vm.Terminal() {
		// The recorded shape for mutating a terminated VM: a plain 400
		// ValidationException, not either modeled conflict type.
		api.WriteError(w, http.StatusBadRequest, "ValidationException", map[string]any{
			"message": "The MicroVM " + vm.ID + " has been terminated and its state cannot be changed.",
		})
		return
	}
	h.svc.Terminate(vm)
	// Recorded: 200 with an empty object, not the VM.
	api.WriteJSON(w, http.StatusOK, map[string]any{})
}

// detail is the full VM shape, returned identically by Run and Get.
func detail(vm *VM) map[string]any {
	body := map[string]any{
		"egressNetworkConnectors":  []any{managedConnector(vm.Region, "INTERNET_EGRESS")},
		"endpoint":                 vm.Endpoint,
		"executionRoleArn":         nil,
		"idlePolicy":               idleOrNil(vm.IdlePolicy),
		"imageArn":                 vm.ImageArn,
		"imageVersion":             vm.ImageVersion,
		"ingressNetworkConnectors": []any{managedConnector(vm.Region, "HTTP_INGRESS")},
		"maximumDurationInSeconds": MaximumDurationSeconds,
		"microvmId":                vm.ID,
		"startedAt":                epoch(vm.StartedAt),
		"state":                    vm.State,
		"stateReason":              strOrNil(vm.StateReason),
		"terminatedAt":             nil,
		"terminationReasonCode":    nil,
	}
	if vm.TerminatedAt != nil {
		body["terminatedAt"] = epoch(*vm.TerminatedAt)
	}
	return body
}

// listItem is a summary: five members, not the full detail.
func listItem(vm *VM) map[string]any {
	return map[string]any{
		"imageArn":     vm.ImageArn,
		"imageVersion": vm.ImageVersion,
		"microvmId":    vm.ID,
		"startedAt":    epoch(vm.StartedAt),
		"state":        vm.State,
	}
}

func idleOrNil(p *IdlePolicy) any {
	if p == nil {
		return nil
	}
	out := map[string]any{"autoResumeEnabled": p.AutoResumeEnabled}
	if p.MaxIdleDurationSeconds != nil {
		out["maxIdleDurationSeconds"] = *p.MaxIdleDurationSeconds
	}
	if p.SuspendedDurationSeconds != nil {
		out["suspendedDurationSeconds"] = *p.SuspendedDurationSeconds
	}
	return out
}

func managedConnector(region, kind string) string {
	return "arn:aws:lambda:" + region + ":aws:network-connector:aws-network-connector:" + kind
}

func strOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func epoch(t interface{ UnixNano() int64 }) float64 {
	return float64(t.UnixNano()) / 1e9
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("vms: no entropy: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return strings.Join([]string{h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]}, "-")
}
