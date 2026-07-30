package api

import (
	"net/http"
	"strings"
)

// DefaultRegion is used when a request carries no usable credential scope.
// Unsigned requests are not a real client's behavior, but they are exactly
// what a human poking at the emulator with curl produces, and answering them
// with a plausible region beats answering with an error.
const DefaultRegion = "us-east-1"

// RegionFromRequest reads the region out of the sigv4 credential scope.
//
// The emulator has no configured region of its own on purpose. Real clients
// declare their region in every signature, so taking it from the request means
// one m80 instance serves every region correctly and region isolation is
// testable without running several instances. An SDK pointed at m80 with an
// endpoint override still signs normally, so this works untouched.
//
// The header looks like:
//
//	Authorization: AWS4-HMAC-SHA256 Credential=AKID/20260730/us-east-2/lambda/aws4_request, SignedHeaders=..., Signature=...
//
// The scope is the third element of the credential path.
func RegionFromRequest(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return DefaultRegion
	}
	i := strings.Index(auth, "Credential=")
	if i < 0 {
		return DefaultRegion
	}
	scope := auth[i+len("Credential="):]
	if j := strings.IndexAny(scope, ", "); j >= 0 {
		scope = scope[:j]
	}
	parts := strings.Split(scope, "/")
	// access-key / date / region / service / aws4_request
	if len(parts) < 5 || parts[2] == "" {
		return DefaultRegion
	}
	return parts[2]
}
