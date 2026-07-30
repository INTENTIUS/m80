// Package managedimages serves the AWS-managed base image catalog.
//
// The catalog is read-only and identical in every account, so it is data
// rather than state: a snapshot recorded from the live service on 2026-07-30
// (#6), embedded here. Only the region varies, and it comes from the caller's
// own sigv4 credential scope, so a client that signs for eu-west-1 sees
// eu-west-1 ARNs and CreateMicrovmImage's baseImageArn resolves against the
// same region the client thinks it is in.
//
// This is also the reference implementation of an m80 resource package: it
// registers handlers on the server, owns its data, and exposes the one
// question other packages need to ask it (Has).
package managedimages

import (
	"net/http"
	"strings"

	"github.com/intentius/m80/internal/api"
)

// image is one catalog entry. Timestamps are epoch seconds because that is
// what the service emits — recorded, not chosen.
type image struct {
	Name      string
	CreatedAt float64
	UpdatedAt float64
	Versions  []version
}

type version struct {
	Version   string
	Status    string
	CreatedAt float64
	UpdatedAt float64
}

// catalog is the recorded snapshot. Versions are newest first, matching the
// order the live service returned them.
var catalog = []image{{
	Name:      "al2023-1",
	CreatedAt: 1781833144.754,
	UpdatedAt: 1784165932.388,
	Versions: []version{
		{Version: "1", Status: "AVAILABLE", CreatedAt: 1784165751.854, UpdatedAt: 1784165932.388},
		{Version: "0", Status: "AVAILABLE", CreatedAt: 1781833144.754, UpdatedAt: 1781833325.185},
	},
}}

// arn builds a managed image ARN. The account segment is the literal "aws",
// not a number: these images belong to the service, and the conformance
// normalizer preserves that spelling precisely because it distinguishes an
// AWS-owned resource from a caller-owned one.
func arn(region, name string) string {
	return "arn:aws:lambda:" + region + ":aws:microvm-image:" + name
}

// Has reports whether a base image ARN names something in the catalog for the
// region. CreateMicrovmImage validates against this (#8).
func Has(region, imageArn string) bool {
	for _, img := range catalog {
		if imageArn == arn(region, img.Name) {
			return true
		}
	}
	return false
}

// Names lists the catalog entries, for error messages that want to say what
// is on offer.
func Names() []string {
	out := make([]string, 0, len(catalog))
	for _, img := range catalog {
		out = append(out, img.Name)
	}
	return out
}

// Register wires both list operations onto the server.
func Register(srv *api.Server) {
	srv.Register("ListManagedMicrovmImages", func(w http.ResponseWriter, r *http.Request) {
		region := api.RegionFromRequest(r)
		items := make([]any, 0, len(catalog))
		for _, img := range catalog {
			items = append(items, map[string]any{
				"imageArn":  arn(region, img.Name),
				"createdAt": img.CreatedAt,
				"updatedAt": img.UpdatedAt,
			})
		}
		api.WriteJSON(w, http.StatusOK, map[string]any{
			"items": items,
			// Explicit null, not omitted: the recorded response carries the
			// member, and a client checking for it must find it.
			"nextToken": nil,
		})
	})

	srv.Register("ListManagedMicrovmImageVersions", func(w http.ResponseWriter, r *http.Request) {
		region := api.RegionFromRequest(r)
		identifier := r.PathValue("imageIdentifier")

		// The path carries a full ARN, but tolerate a bare name too: the ARN
		// is what the service documents and what the CLI sends, while a bare
		// name is the obvious thing for a human probing by hand.
		var found *image
		for i := range catalog {
			if identifier == arn(region, catalog[i].Name) || identifier == catalog[i].Name {
				found = &catalog[i]
				break
			}
		}
		if found == nil {
			api.WriteError(w, http.StatusNotFound, "ResourceNotFoundException",
				map[string]any{
					"message":      "Managed MicroVM image not found: " + identifier,
					"resourceId":   nil,
					"resourceType": nil,
				})
			return
		}

		items := make([]any, 0, len(found.Versions))
		for _, v := range found.Versions {
			items = append(items, map[string]any{
				"imageArn":     arn(region, found.Name),
				"imageVersion": v.Version,
				"status":       v.Status,
				"createdAt":    v.CreatedAt,
				"updatedAt":    v.UpdatedAt,
			})
		}
		api.WriteJSON(w, http.StatusOK, map[string]any{
			"items":     items,
			"nextToken": nil,
		})
	})
}

// TrimARN returns the resource name from a managed image ARN, or the input
// unchanged when it is not one. Kept here so ARN spelling stays in one place.
func TrimARN(imageArn string) string {
	if i := strings.LastIndex(imageArn, ":microvm-image:"); i >= 0 {
		return imageArn[i+len(":microvm-image:"):]
	}
	return imageArn
}
