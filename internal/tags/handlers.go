package tags

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/intentius/m80/internal/api"
)

func Register(srv *api.Server, resources ...Resource) {
	h := &handlers{reg: registry{resources: resources}}
	srv.Register("ListTags", h.list)
	srv.Register("TagResource", h.tag)
	srv.Register("UntagResource", h.untag)
}

type handlers struct{ reg registry }

func notFound(w http.ResponseWriter, arn string) {
	api.WriteError(w, http.StatusNotFound, "ResourceNotFoundException", map[string]any{
		"message":      "The resource you requested does not exist. Resource: " + arn,
		"resourceId":   nil,
		"resourceType": nil,
	})
}

func invalidParameter(w http.ResponseWriter, message string) {
	api.WriteError(w, http.StatusBadRequest, "InvalidParameterValueException",
		map[string]any{"message": message})
}

// resource reads the ARN out of the path. It is a single path segment: every
// taggable MicroVM-family ARN is colon-separated with no slash in it, so the
// route pattern's wildcard captures the whole thing.
func (h *handlers) resource(r *http.Request) string { return r.PathValue("Resource") }

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	region := api.RegionFromRequest(r)
	arn := h.resource(r)
	t, ok := h.reg.get(region, arn)
	if !ok {
		notFound(w, arn)
		return
	}
	// Recorded: 200 with a Tags map. An untagged resource answers with an
	// empty object rather than null.
	api.WriteJSON(w, http.StatusOK, map[string]any{"Tags": tagsOrEmpty(t)})
}

func (h *handlers) tag(w http.ResponseWriter, r *http.Request) {
	region := api.RegionFromRequest(r)
	arn := h.resource(r)

	var req struct {
		Tags map[string]string `json:"Tags"`
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) == 0 {
		invalidParameter(w, "Tags is a required field")
		return
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		invalidParameter(w, "Invalid request body")
		return
	}
	if req.Tags == nil {
		invalidParameter(w, "Tags is a required field")
		return
	}
	for k, v := range req.Tags {
		if len(k) < 1 || len(k) > MaxKeyLength {
			invalidParameter(w, "Tag key must be between 1 and "+
				strconv.Itoa(MaxKeyLength)+" characters: "+k)
			return
		}
		if len(v) > MaxValueLength {
			invalidParameter(w, "Tag value for key "+k+" must be at most "+
				strconv.Itoa(MaxValueLength)+" characters")
			return
		}
	}

	existing, ok := h.reg.get(region, arn)
	if !ok {
		notFound(w, arn)
		return
	}
	// Tagging merges rather than replaces: TagResource adds and overwrites the
	// keys it names and leaves the rest alone, which is what UntagResource
	// exists for.
	merged := copyTags(existing)
	for k, v := range req.Tags {
		merged[k] = v
	}
	h.reg.set(region, arn, merged)

	// Recorded: 204 with an empty body. The fixture for this step is a
	// zero-byte file, which is what no content looks like on disk.
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) untag(w http.ResponseWriter, r *http.Request) {
	region := api.RegionFromRequest(r)
	arn := h.resource(r)

	keys := tagKeys(r)
	if len(keys) == 0 {
		invalidParameter(w, "TagKeys is a required field")
		return
	}
	existing, ok := h.reg.get(region, arn)
	if !ok {
		notFound(w, arn)
		return
	}
	remaining := copyTags(existing)
	// Removing a key that is not there is not an error: untag is idempotent,
	// which is what a reconciler converging on a desired tag set needs.
	for _, k := range keys {
		delete(remaining, k)
	}
	h.reg.set(region, arn, remaining)

	w.WriteHeader(http.StatusNoContent)
}

func tagsOrEmpty(t map[string]string) map[string]string {
	if t == nil {
		return map[string]string{}
	}
	return t
}
