package tokens

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// HeaderName is the header a client presents on the VM endpoint. The model
// calls the response member a map of auth token keys to values precisely so a
// scheme can return several; the recorded scheme returns this one.
const HeaderName = "X-aws-proxy-auth"

// PathPrefix is the second way to reach a VM's endpoint. The first is the
// hostname the control plane hands out, which is the real thing a client
// uses; this exists because reaching it against a local m80 means overriding
// DNS or forging a Host header, and a test harness should not have to.
const PathPrefix = "/_m80/vm/"

// defaultPort is the port a request is judged against when the Host header
// carries none. The endpoint the service hands out is HTTPS.
const defaultPort = 443

// Endpoint serves the per-VM stub.
//
// Almost nothing here is recorded, and it could not have been: the
// conformance runner signs and addresses control-plane requests, so it has no
// way to call a host that is not the control plane. Rather than scatter
// guesses through the code, every one of them is in the table below and
// nowhere else.
//
//	Situation                              m80 answers   Basis
//	-----------------------------------------------------------------------
//	unknown endpoint host                   404          no VM to serve
//	no or malformed token header            401          guess
//	token unknown, expired, or another      403          guess
//	  VM's
//	port outside the token's allowedPorts   403          guess
//	VM TERMINATED                           410          guess
//	VM PENDING                              503          guess
//	VM SUSPENDED, autoResumeEnabled         resume, 200  inferred: a suspended
//	                                                     VM issues tokens so a
//	                                                     client can wake it by
//	                                                     calling it
//	VM SUSPENDED, no autoResumeEnabled      503          guess
//	VM RUNNING, token good                  200 + body   stub
//
// 401 versus 403 is the one worth arguing about: a missing credential and a
// rejected one are different failures to a client retrying with a fresh
// token, so they are kept apart even though a single 403 would have been the
// safer guess. Recording any of this needs runner support for a
// non-control-plane host, which is not built.
type Endpoint struct {
	svc *Service
	vms VMSource

	mu   sync.RWMutex
	body []byte
}

// NewEndpoint returns the stub. body is the payload a successful request
// gets, and may be nil for the built-in default.
func NewEndpoint(svc *Service, source VMSource, body []byte) *Endpoint {
	return &Endpoint{svc: svc, vms: source, body: body}
}

// SetBody replaces the stub payload. The endpoint is a stand-in for whatever
// the user's own image would serve, so what it returns has to be theirs.
func (e *Endpoint) SetBody(body []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.body = body
}

// Intercept is the hook api.Server consults before its route table. It
// reports whether it served the request.
func (e *Endpoint) Intercept(w http.ResponseWriter, r *http.Request) bool {
	region, id, port, ok := e.route(r)
	if !ok {
		return false
	}
	e.serve(w, r, region, id, port)
	return true
}

// route decides whether a request is for a VM endpoint at all, by hostname or
// by path prefix, and returns the VM and the port the client asked for.
func (e *Endpoint) route(r *http.Request) (region, id string, port int, ok bool) {
	if strings.HasPrefix(r.URL.Path, PathPrefix) {
		rest := strings.TrimPrefix(r.URL.Path, PathPrefix)
		vmID := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			vmID = rest[:i]
		}
		if vmID == "" {
			return "", "", 0, false
		}
		reg, found := e.vms.LookupID(vmID)
		if !found {
			// Claim the request anyway: the caller clearly meant an endpoint,
			// and falling through to the control-plane mux would answer 404
			// for the wrong reason.
			return "", vmID, hostPort(r.Host), true
		}
		return reg, vmID, hostPort(r.Host), true
	}
	reg, vmID, found := e.vms.LookupEndpoint(r.Host)
	if !found {
		return "", "", 0, false
	}
	return reg, vmID, hostPort(r.Host), true
}

// hostPort reads the port a client addressed, falling back to 443 — the
// endpoint the control plane hands out is HTTPS, and a client that reached
// m80 on some other local port is still conceptually talking to it.
func hostPort(host string) int {
	_, p, err := net.SplitHostPort(host)
	if err != nil {
		return defaultPort
	}
	n, err := strconv.Atoi(p)
	if err != nil || !validPort(n) {
		return defaultPort
	}
	return n
}

func (e *Endpoint) serve(w http.ResponseWriter, r *http.Request, region, id string, port int) {
	state, autoResume, found := e.vms.Status(region, id)
	if !found {
		endpointError(w, http.StatusNotFound, "NotFound", "No MicroVM endpoint for this host")
		return
	}

	presented := r.Header.Get(HeaderName)
	if strings.TrimSpace(presented) == "" {
		endpointError(w, http.StatusUnauthorized, "Unauthorized", "Missing "+HeaderName)
		return
	}
	t, valid := e.svc.Validate(presented)
	if !valid {
		endpointError(w, http.StatusForbidden, "Forbidden", "Token is not valid for this MicroVM")
		return
	}
	if t.VMID != id || t.Region != region {
		endpointError(w, http.StatusForbidden, "Forbidden", "Token is not valid for this MicroVM")
		return
	}
	if t.Shell {
		endpointError(w, http.StatusForbidden, "Forbidden", "Shell tokens do not authorize the HTTP endpoint")
		return
	}
	if !t.Allows(port) {
		endpointError(w, http.StatusForbidden, "Forbidden",
			"Token does not grant port "+strconv.Itoa(port))
		return
	}

	switch state {
	case stateTerminated:
		endpointError(w, http.StatusGone, "Gone", "MicroVM has been terminated")
		return
	case stateSuspended:
		if !autoResume {
			endpointError(w, http.StatusServiceUnavailable, "Unavailable",
				"MicroVM is suspended and its idle policy does not enable auto-resume")
			return
		}
		// The auto-resume path. Waking before recording traffic means the
		// marker the client reads is the one it just caused, not a stale one.
		e.vms.Wake(region, id)
	case "RUNNING":
	default:
		endpointError(w, http.StatusServiceUnavailable, "Unavailable",
			"MicroVM is not running (state "+state+")")
		return
	}

	marker, _ := e.vms.RecordTraffic(region, id)

	e.mu.RLock()
	body := e.body
	e.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	// The marker rides a header as well as the body so a caller that replaced
	// the stub body with its own payload can still read it.
	w.Header().Set("X-M80-State-Marker", strconv.FormatUint(marker, 10))
	w.WriteHeader(http.StatusOK)
	if body != nil {
		_, _ = w.Write(body)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"microvmId": id,
		"region":    region,
		"state":     "RUNNING",
		// stateMarker is the point of the default body: a counter that
		// survives suspend and resume, so a client can prove the VM it is
		// talking to kept its state rather than being rebuilt underneath it.
		"stateMarker": marker,
		"emulator":    "m80",
	})
}

// endpointError answers in m80's own shape, not a modeled one. These are the
// VM's endpoint rather than the control plane, so no service error type
// applies and inventing one would be worse than being obviously local.
func endpointError(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":   kind,
		"message": message,
		"source":  "m80-vm-endpoint",
	})
}
