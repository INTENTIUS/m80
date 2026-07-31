package vms

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/intentius/m80/internal/api"
	"github.com/intentius/m80/internal/limits"
)

// ImageResolver is what vms needs from images: does this image exist, and
// which version would a VM run. An interface so the two packages do not
// depend on each other's internals.
type ImageResolver interface {
	// ResolveRunnable returns the image ARN and the version a VM would run,
	// or false when the image is unknown or has nothing runnable.
	ResolveRunnable(region, identifier string) (arn string, version string, ok bool)
	// MemoryMiB reports what a VM off this image allocates, for the account
	// memory ceiling.
	MemoryMiB(region, identifier string) int
}

// Quota is the account ceiling RunMicrovm is checked against. The recording
// found the binding limit to be allocated memory rather than VM count: six
// concurrent calls on a fresh account left two VMs running and rejected four.
type Quota interface {
	AllowMemory(currentMiB, wantMiB int) bool
	AllowMicrovm(current int) bool
}

func Register(srv *api.Server, svc *Service, images ImageResolver, quota Quota) {
	h := &handlers{svc: svc, images: images, quota: quota}
	srv.Register("RunMicrovm", h.run)
	srv.Register("GetMicrovm", h.get)
	srv.Register("ListMicrovms", h.list)
	srv.Register("SuspendMicrovm", h.suspend)
	srv.Register("ResumeMicrovm", h.resume)
	srv.Register("TerminateMicrovm", h.terminate)
}

type handlers struct {
	svc    *Service
	images ImageResolver
	quota  Quota
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
		// Recorded: RunMicrovm reports the missing *version*, not the missing
		// image, even when the image itself does not exist. It reads as the
		// service resolving an image to a runnable version and failing at the
		// second step regardless of which one was really absent.
		api.WriteError(w, http.StatusNotFound, "ResourceNotFoundException", map[string]any{
			"message":      "No active version found for MicroVM image " + *req.ImageIdentifier,
			"resourceId":   nil,
			"resourceType": nil,
		})
		return
	}

	// The account ceiling, checked after the image resolves so a run against
	// a missing image still reports the missing image.
	if h.quota != nil {
		want := h.images.MemoryMiB(region, *req.ImageIdentifier)
		allocated, live := h.svc.Allocated(region)
		if !h.quota.AllowMemory(allocated, want) || !h.quota.AllowMicrovm(live) {
			limits.WriteQuotaExceeded(w, "")
			return
		}
	}

	vm := h.svc.Run(region, arn, version, h.images.MemoryMiB(region, *req.ImageIdentifier), idle)
	api.WriteJSON(w, http.StatusOK, detail(h.svc.Snapshot(vm)))
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
	api.WriteJSON(w, http.StatusOK, detail(h.svc.Snapshot(vm)))
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	region := api.RegionFromRequest(r)
	items := make([]any, 0)
	for _, vm := range h.svc.Snapshots(region) {
		items = append(items, listItem(vm))
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "nextToken": nil})
}

// mutable resolves the VM and rejects the one case the recording pinned down:
// any state change on a terminated VM is a plain 400 ValidationException, not
// either modeled conflict type. Suspend, resume and terminate share it.
func (h *handlers) mutable(w http.ResponseWriter, r *http.Request) (*VM, bool) {
	vm, ok := h.lookup(w, r)
	if !ok {
		return nil, false
	}
	if snap := h.svc.Snapshot(vm); snap.Terminal() {
		api.WriteError(w, http.StatusBadRequest, "ValidationException", map[string]any{
			"message": "The MicroVM " + vm.ID + " has been terminated and its state cannot be changed.",
		})
		return nil, false
	}
	return vm, true
}

// Recorded: 200 with an empty object, not the VM. All three mutations answer
// the same way.
func writeAccepted(w http.ResponseWriter) {
	api.WriteJSON(w, http.StatusOK, map[string]any{})
}

func (h *handlers) suspend(w http.ResponseWriter, r *http.Request) {
	vm, ok := h.mutable(w, r)
	if !ok {
		return
	}
	h.svc.Suspend(vm)
	writeAccepted(w)
}

func (h *handlers) resume(w http.ResponseWriter, r *http.Request) {
	vm, ok := h.mutable(w, r)
	if !ok {
		return
	}
	h.svc.Resume(vm)
	writeAccepted(w)
}

func (h *handlers) terminate(w http.ResponseWriter, r *http.Request) {
	vm, ok := h.mutable(w, r)
	if !ok {
		return
	}
	h.svc.Terminate(vm)
	writeAccepted(w)
}

// detail is the full VM shape, returned identically by Run and Get. It takes
// a snapshot rather than the live VM so a transition cannot change state
// halfway through building the body.
//
// The state marker is deliberately absent: it is m80's own instrumentation,
// read through the per-VM endpoint stub (#12), and putting it on a modeled
// response would be an invented member on the wire.
func detail(vm VM) map[string]any {
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
func listItem(vm VM) map[string]any {
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
