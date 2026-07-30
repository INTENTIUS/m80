package images

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/intentius/m80/internal/api"
	"github.com/intentius/m80/internal/managedimages"
)

// VMChecker reports whether any VM is running off an image. Images refuses to
// delete over one, recorded as
// 400 "Cannot delete microvm image with running microvms." VMs land in #10,
// so this is an interface: images does not import them, and until #10 wires a
// real one the nil checker means no VMs exist.
type VMChecker interface {
	HasRunningVMs(region, imageArn string) bool
}

type noVMs struct{}

func (noVMs) HasRunningVMs(string, string) bool { return false }

// Register wires every image, version, and build operation.
func Register(srv *api.Server, svc *Service, vms VMChecker) {
	if vms == nil {
		vms = noVMs{}
	}
	h := &handlers{svc: svc, vms: vms}

	srv.Register("CreateMicrovmImage", h.create)
	srv.Register("GetMicrovmImage", h.get)
	srv.Register("ListMicrovmImages", h.list)
	srv.Register("UpdateMicrovmImage", h.update)
	srv.Register("DeleteMicrovmImage", h.delete)

	srv.Register("GetMicrovmImageVersion", h.getVersion)
	srv.Register("ListMicrovmImageVersions", h.listVersions)
	srv.Register("UpdateMicrovmImageVersion", h.updateVersion)
	srv.Register("DeleteMicrovmImageVersion", h.deleteVersion)

	srv.Register("ListMicrovmImageBuilds", h.listBuilds)
	srv.Register("GetMicrovmImageBuild", h.getBuild)
}

type handlers struct {
	svc *Service
	vms VMChecker
}

type createRequest struct {
	Name         *string `json:"name"`
	BaseImageArn *string `json:"baseImageArn"`
	BuildRoleArn *string `json:"buildRoleArn"`
	Description  *string `json:"description"`
	CodeArtifact *struct {
		URI string `json:"uri"`
	} `json:"codeArtifact"`
	Resources []struct {
		MinimumMemoryInMiB *int `json:"minimumMemoryInMiB"`
	} `json:"resources"`
}

func decode(r *http.Request, into any) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, into)
}

// validationError reproduces the service's message format, which counts the
// failures and names each member's path. Clients log these verbatim, so the
// wording is part of the contract.
func validationError(w http.ResponseWriter, failures []string) {
	noun := "validation errors"
	if len(failures) == 1 {
		noun = "validation error"
	}
	api.WriteError(w, http.StatusBadRequest, "ValidationException", map[string]any{
		"message": fmt.Sprintf("%d %s detected: %s", len(failures), noun, strings.Join(failures, "; ")),
	})
}

func notFound(w http.ResponseWriter, message string) {
	api.WriteError(w, http.StatusNotFound, "ResourceNotFoundException", map[string]any{
		"message":      message,
		"resourceId":   nil,
		"resourceType": nil,
	})
}

func badRequest(w http.ResponseWriter, message string) {
	api.WriteError(w, http.StatusBadRequest, "ValidationException", map[string]any{
		"message": message,
	})
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	region := api.RegionFromRequest(r)
	var req createRequest
	if err := decode(r, &req); err != nil {
		badRequest(w, "Invalid request body")
		return
	}

	// Required-member failures are reported together and in the recorded
	// order: baseImageArn, codeArtifact, buildRoleArn. A client fixing them
	// one round-trip at a time is a worse experience than the real service.
	var failures []string
	if req.BaseImageArn == nil {
		failures = append(failures, "Value null at 'baseImageArn' failed to satisfy constraint: Member must not be null")
	}
	if req.CodeArtifact == nil {
		failures = append(failures, "Value null at 'codeArtifact' failed to satisfy constraint: Member must not be null")
	}
	if req.BuildRoleArn == nil {
		failures = append(failures, "Value null at 'buildRoleArn' failed to satisfy constraint: Member must not be null")
	}
	if req.Name != nil && !namePattern.MatchString(*req.Name) {
		failures = append(failures, fmt.Sprintf(
			"Value '%s' at 'name' failed to satisfy constraint: Member must satisfy regular expression pattern: %s",
			*req.Name, namePatternSource))
	}
	if len(failures) > 0 {
		validationError(w, failures)
		return
	}
	if req.Name == nil {
		validationError(w, []string{"Value null at 'name' failed to satisfy constraint: Member must not be null"})
		return
	}

	memory := DefaultMemoryMiB
	if len(req.Resources) > 0 && req.Resources[0].MinimumMemoryInMiB != nil {
		memory = *req.Resources[0].MinimumMemoryInMiB
		if !validTier(memory) {
			badRequest(w, fmt.Sprintf(
				"Value '%d' at 'resources.minimumMemoryInMiB' failed to satisfy constraint: Member must be one of %v",
				memory, MemoryTiers))
			return
		}
	}

	if !managedimages.Has(region, *req.BaseImageArn) {
		badRequest(w, "Invalid base image ARN: "+*req.BaseImageArn)
		return
	}

	// The name stays reserved through the asynchronous delete window, so a
	// create during it is refused rather than resurrecting the image.
	if _, exists := h.svc.Get(region, *req.Name); exists {
		badRequest(w, "MicroVM image already exists: "+*req.Name)
		return
	}

	img := h.svc.Create(region, *req.Name, Spec{
		BaseImageArn: *req.BaseImageArn,
		BuildRoleArn: *req.BuildRoleArn,
		CodeURI:      req.CodeArtifact.URI,
		Description:  req.Description,
		MemoryMiB:    memory,
	})
	api.WriteJSON(w, http.StatusCreated, createDetail(img))
}

func validTier(m int) bool {
	for _, t := range MemoryTiers {
		if t == m {
			return true
		}
	}
	return false
}

func (h *handlers) lookup(w http.ResponseWriter, r *http.Request) (*Image, string, bool) {
	region := api.RegionFromRequest(r)
	identifier := r.PathValue("imageIdentifier")
	img, ok := h.svc.Get(region, identifier)
	if !ok {
		notFound(w, "MicroVMImage not found for MicroVMImageID: "+identifier)
		return nil, region, false
	}
	return img, region, true
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	img, _, ok := h.lookup(w, r)
	if !ok {
		return
	}
	api.WriteJSON(w, http.StatusOK, summary(img))
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	region := api.RegionFromRequest(r)
	items := make([]any, 0)
	for _, img := range h.svc.List(region) {
		items = append(items, listItem(img))
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "nextToken": nil})
}

func (h *handlers) update(w http.ResponseWriter, r *http.Request) {
	img, region, ok := h.lookup(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := decode(r, &req); err != nil {
		badRequest(w, "Invalid request body")
		return
	}

	// PUT is a full replace, recorded: omitting a required member is a
	// per-member validation error, not a merge with what is stored.
	var failures []string
	if req.BaseImageArn == nil {
		failures = append(failures, "Value null at 'baseImageArn' failed to satisfy constraint: Member must not be null")
	}
	if req.CodeArtifact == nil {
		failures = append(failures, "Value null at 'codeArtifact' failed to satisfy constraint: Member must not be null")
	}
	if req.BuildRoleArn == nil {
		failures = append(failures, "Value null at 'buildRoleArn' failed to satisfy constraint: Member must not be null")
	}
	if len(failures) > 0 {
		validationError(w, failures)
		return
	}
	if !managedimages.Has(region, *req.BaseImageArn) {
		badRequest(w, "Invalid base image ARN: "+*req.BaseImageArn)
		return
	}

	memory := DefaultMemoryMiB
	if len(req.Resources) > 0 && req.Resources[0].MinimumMemoryInMiB != nil {
		memory = *req.Resources[0].MinimumMemoryInMiB
		if !validTier(memory) {
			badRequest(w, fmt.Sprintf(
				"Value '%d' at 'resources.minimumMemoryInMiB' failed to satisfy constraint: Member must be one of %v",
				memory, MemoryTiers))
			return
		}
	}

	h.svc.Update(img, Spec{
		BaseImageArn: *req.BaseImageArn,
		BuildRoleArn: *req.BuildRoleArn,
		CodeURI:      req.CodeArtifact.URI,
		Description:  req.Description,
		MemoryMiB:    memory,
	})
	api.WriteJSON(w, http.StatusOK, detail(img))
}

func (h *handlers) delete(w http.ResponseWriter, r *http.Request) {
	img, region, ok := h.lookup(w, r)
	if !ok {
		return
	}
	if h.vms.HasRunningVMs(region, imageARN(region, img.Name)) {
		badRequest(w, "Cannot delete microvm image with running microvms.")
		return
	}
	if img.Building() {
		badRequest(w, "Cannot delete MicroVM image in its current state: "+img.State)
		return
	}
	h.svc.Delete(img)
	api.WriteJSON(w, http.StatusOK, map[string]any{
		"imageIdentifier": imageARN(region, img.Name),
		"state":           StateDeleting,
	})
}

func (h *handlers) lookupVersion(w http.ResponseWriter, r *http.Request) (*Image, *Version, string, bool) {
	img, region, ok := h.lookup(w, r)
	if !ok {
		return nil, nil, region, false
	}
	version := r.PathValue("imageVersion")
	v, ok := img.Version(version)
	if !ok {
		notFound(w, fmt.Sprintf("MicroVMImage not found for MicroVMImage: %s, Version: %s",
			imageARN(region, img.Name), version))
		return nil, nil, region, false
	}
	return img, v, region, true
}

func (h *handlers) getVersion(w http.ResponseWriter, r *http.Request) {
	img, v, _, ok := h.lookupVersion(w, r)
	if !ok {
		return
	}
	api.WriteJSON(w, http.StatusOK, versionDetail(img, v))
}

func (h *handlers) listVersions(w http.ResponseWriter, r *http.Request) {
	img, _, ok := h.lookup(w, r)
	if !ok {
		return
	}
	items := make([]any, 0, len(img.Versions))
	for _, v := range img.Versions {
		items = append(items, versionDetail(img, v))
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "nextToken": nil})
}

func (h *handlers) updateVersion(w http.ResponseWriter, r *http.Request) {
	img, v, _, ok := h.lookupVersion(w, r)
	if !ok {
		return
	}
	// A PATCH while the image is mid-update is a 409, recorded, and it is a
	// ConflictException rather than the ValidationException the terminal-VM
	// case returns.
	if img.State == StateUpdating {
		api.WriteError(w, http.StatusConflict, "ConflictException", map[string]any{
			"message": "MicroVM Image is already in state: " + StateUpdating,
		})
		return
	}
	var req struct {
		Status *string `json:"status"`
	}
	if err := decode(r, &req); err != nil {
		badRequest(w, "Invalid request body")
		return
	}
	if req.Status != nil {
		if *req.Status != StatusActive && *req.Status != StatusInactive {
			badRequest(w, fmt.Sprintf(
				"Value '%s' at 'status' failed to satisfy constraint: Member must satisfy enum value set: [ACTIVE, INACTIVE]",
				*req.Status))
			return
		}
		v.Status = *req.Status
		v.UpdatedAt = h.svc.clock.Now()
	}
	api.WriteJSON(w, http.StatusOK, versionDetail(img, v))
}

func (h *handlers) deleteVersion(w http.ResponseWriter, r *http.Request) {
	img, v, region, ok := h.lookupVersion(w, r)
	if !ok {
		return
	}
	if v.State == BuildPending || v.State == BuildInProgress {
		badRequest(w, "Cannot delete MicroVM image in its current state: "+v.State)
		return
	}
	h.svc.DeleteVersion(img, v)
	api.WriteJSON(w, http.StatusOK, map[string]any{
		"imageIdentifier": imageARN(region, img.Name),
		"imageVersion":    v.Version,
		"state":           StateDeleting,
	})
}

func (h *handlers) listBuilds(w http.ResponseWriter, r *http.Request) {
	img, v, _, ok := h.lookupVersion(w, r)
	if !ok {
		return
	}
	items := make([]any, 0, len(v.Builds))
	for _, b := range v.Builds {
		items = append(items, buildListItem(img, b))
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "nextToken": nil})
}

func (h *handlers) getBuild(w http.ResponseWriter, r *http.Request) {
	img, v, _, ok := h.lookupVersion(w, r)
	if !ok {
		return
	}
	buildID := r.PathValue("buildId")
	for _, b := range v.Builds {
		if b.BuildID == buildID {
			api.WriteJSON(w, http.StatusOK, buildDetail(img, b))
			return
		}
	}
	notFound(w, "MicroVM image build not found: "+buildID)
}
