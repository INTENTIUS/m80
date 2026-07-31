// Package api is m80's HTTP surface: the route table, the router that serves
// it, and the health endpoint.
//
// Every one of the 29 operations is routed from the start, each answering 501
// until its issue lands. That is deliberate. The conformance runner already
// treats 501 as `unimplemented` rather than a failure, so the suite runs
// green-ish against the scaffold from day one and each implemented operation
// flips one line of the report. An unrouted path would instead 404, which the
// runner reads as a wrong answer and cannot distinguish from a bug.
package api

import "net/http"

// Route is one service operation's wire address. The table below is m80's own
// routing truth; a test asserts it matches conformance/inventory.json exactly,
// so the two cannot drift without CI saying so.
type Route struct {
	Operation string
	Method    string
	// Pattern is the Go 1.22+ ServeMux path pattern. Wildcards use the
	// inventory's own parameter names so the two read alike.
	Pattern string
}

// Routes covers both services. Lambda Microvms is the /2025-09-09 family;
// Lambda Core's network connectors are /2026-04-04. Both sign as lambda, so
// one router serves them.
var Routes = []Route{
	// Images
	{"CreateMicrovmImage", "POST", "/2025-09-09/microvm-images"},
	{"ListMicrovmImages", "GET", "/2025-09-09/microvm-images"},
	{"GetMicrovmImage", "GET", "/2025-09-09/microvm-images/{imageIdentifier}"},
	{"UpdateMicrovmImage", "PUT", "/2025-09-09/microvm-images/{imageIdentifier}"},
	{"DeleteMicrovmImage", "DELETE", "/2025-09-09/microvm-images/{imageIdentifier}"},

	// Image versions and builds
	{"ListMicrovmImageVersions", "GET", "/2025-09-09/microvm-images/{imageIdentifier}/versions"},
	{"GetMicrovmImageVersion", "GET", "/2025-09-09/microvm-images/{imageIdentifier}/versions/{imageVersion}"},
	{"UpdateMicrovmImageVersion", "PATCH", "/2025-09-09/microvm-images/{imageIdentifier}/versions/{imageVersion}"},
	{"DeleteMicrovmImageVersion", "DELETE", "/2025-09-09/microvm-images/{imageIdentifier}/versions/{imageVersion}"},
	{"ListMicrovmImageBuilds", "GET", "/2025-09-09/microvm-images/{imageIdentifier}/versions/{imageVersion}/builds"},
	{"GetMicrovmImageBuild", "GET", "/2025-09-09/microvm-images/{imageIdentifier}/versions/{imageVersion}/builds/{buildId}"},

	// Managed base images
	{"ListManagedMicrovmImages", "GET", "/2025-09-09/managed-microvm-images"},
	{"ListManagedMicrovmImageVersions", "GET", "/2025-09-09/managed-microvm-images/{imageIdentifier}/versions"},

	// VMs
	{"RunMicrovm", "POST", "/2025-09-09/microvms"},
	{"ListMicrovms", "GET", "/2025-09-09/microvms"},
	{"GetMicrovm", "GET", "/2025-09-09/microvms/{microvmIdentifier}"},
	{"SuspendMicrovm", "POST", "/2025-09-09/microvms/{microvmIdentifier}/suspend"},
	{"ResumeMicrovm", "POST", "/2025-09-09/microvms/{microvmIdentifier}/resume"},
	// Terminate is a DELETE on the VM itself, not a /terminate sub-resource.
	// Suspend and resume are sub-resources; terminate is not. Easy to get
	// wrong from memory, which is what the inventory cross-check is for.
	{"TerminateMicrovm", "DELETE", "/2025-09-09/microvms/{microvmIdentifier}"},

	// Tokens
	{"CreateMicrovmAuthToken", "POST", "/2025-09-09/microvms/{microvmIdentifier}/auth-token"},
	{"CreateMicrovmShellAuthToken", "POST", "/2025-09-09/microvms/{microvmIdentifier}/shell-auth-token"},

	// Tags, on the 2017-03-31 Lambda family path
	{"ListTags", "GET", "/2017-03-31/tags/{Resource}"},
	{"TagResource", "POST", "/2017-03-31/tags/{Resource}"},
	{"UntagResource", "DELETE", "/2017-03-31/tags/{Resource}"},

	// Network connectors (Lambda Core)
	{"CreateNetworkConnector", "POST", "/2026-04-04/network-connectors"},
	{"ListNetworkConnectors", "GET", "/2026-04-04/network-connectors"},
	{"GetNetworkConnector", "GET", "/2026-04-04/network-connectors/{Identifier}"},
	{"UpdateNetworkConnector", "PUT", "/2026-04-04/network-connectors/{Identifier}"},
	{"DeleteNetworkConnector", "DELETE", "/2026-04-04/network-connectors/{Identifier}"},
}

// Handler serves one operation. Operations register themselves as their
// issues land; anything unregistered answers 501.
type Handler func(http.ResponseWriter, *http.Request)
