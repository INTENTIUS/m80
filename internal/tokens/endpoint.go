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
// Every answer here was a guess until the vm-endpoint scenario recorded them
// against real AWS (#42), which needed the runner to be able to address a
// host that is not the control plane. Four of the nine guesses were wrong,
// and the table now reads:
//
//	Situation                              answers      recorded as
//	-----------------------------------------------------------------------
//	no token header                         403          "Request missing
//	                                                     authentication"
//	token unparseable or unknown            403          the same body: an
//	                                                     undecryptable token
//	                                                     reads as no token
//	token for another VM                    403          "Token authentication
//	                                                     failed"
//	unknown endpoint host                   403          the same as another
//	                                                     VM's token — the host
//	                                                     names no VM, so no
//	                                                     token can match it
//	port outside the token's allowedPorts   200          not enforced here at
//	                                                     all; a token granting
//	                                                     only 8080 still serves
//	                                                     443
//	VM SUSPENDED, autoResumeEnabled         200          the VM is RUNNING
//	                                                     afterwards; calling
//	                                                     the endpoint does wake
//	                                                     it
//	VM SUSPENDED, no autoResumeEnabled      502, empty
//	VM TERMINATED                           502, empty
//	VM RUNNING, token good                  200          the image's own app
//
// Bodies are plain text, not the modeled error shape, which fits: this is a
// proxy in front of the VM rather than the control plane.
//
// Two situations stay guesses because nothing recorded reaches them. A
// PENDING VM answers as an unavailable one, 502 with an empty body, on the
// grounds that every unavailable case AWS was observed on answers that way.
// A shell token presented to the HTTP endpoint is refused as a token that
// does not match, since a recording needs SHELL_INGRESS on the image.
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
		if e.vms.IsEndpointHost(r.Host) {
			// Shaped like an endpoint but naming no VM. Claim it: serve
			// answers as it does for a token that matches nothing, which is
			// what was recorded.
			return "", "", hostPort(r.Host), true
		}
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
	presented := r.Header.Get(HeaderName)
	if strings.TrimSpace(presented) == "" {
		endpointError(w, http.StatusForbidden, "Request missing authentication")
		return
	}

	// An unknown host reaches here with no VM, and answers exactly as a token
	// for the wrong VM does: the host names nothing, so nothing can match it.
	state, autoResume, found := e.vms.Status(region, id)

	t, valid := e.svc.Validate(presented)
	switch {
	case t == nil:
		// Never issued, so AWS could not have decrypted it either, and an
		// undecryptable token is indistinguishable from an absent one.
		endpointError(w, http.StatusForbidden, "Request missing authentication")
		return
	case !valid, !found, t.VMID != id, t.Region != region, t.Shell:
		endpointError(w, http.StatusForbidden, "Token authentication failed")
		return
	}

	// allowedPorts is deliberately not checked. A token granting only 8080
	// was recorded serving 443, so the grant does not gate the endpoint the
	// control plane hands out, and enforcing it here would fail requests real
	// AWS answers.
	_ = port

	switch state {
	case stateSuspended:
		if !autoResume {
			unavailable(w)
			return
		}
		// Waking before recording traffic means the marker the client reads
		// is the one it just caused, not a stale one.
		e.vms.Wake(region, id)
	case "RUNNING":
	default:
		// TERMINATED, PENDING, and the transient states. Recorded for
		// TERMINATED; the rest share it because every unavailable VM AWS was
		// observed on answered the same way.
		unavailable(w)
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

// unavailable is what a VM that cannot serve answers: 502 with an empty body,
// recorded for both a terminated VM and a suspended one whose idle policy
// does not enable auto-resume.
func unavailable(w http.ResponseWriter) {
	w.WriteHeader(http.StatusBadGateway)
}

// endpointError answers in plain text, because that is what was recorded.
// No modeled error type applies: this is a proxy in front of the VM, not the
// control plane, and it does not speak the service's error shape.
func endpointError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message))
}
