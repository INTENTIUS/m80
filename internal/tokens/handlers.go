package tokens

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/intentius/m80/internal/api"
)

// VM states this package cares about. Duplicated rather than imported so the
// dependency stays one-way: vms implements VMSource without knowing tokens
// exists.
const (
	stateSuspended  = "SUSPENDED"
	stateTerminated = "TERMINATED"
)

func Register(srv *api.Server, svc *Service, source VMSource) {
	h := &handlers{svc: svc, vms: source}
	srv.Register("CreateMicrovmAuthToken", h.authToken)
	srv.Register("CreateMicrovmShellAuthToken", h.shellAuthToken)
}

type handlers struct {
	svc *Service
	vms VMSource
}

type portSpec struct {
	Port  *int `json:"port"`
	Range *struct {
		StartPort *int `json:"startPort"`
		EndPort   *int `json:"endPort"`
	} `json:"range"`
	AllPorts *struct{} `json:"allPorts"`
}

type tokenRequest struct {
	ExpirationInMinutes *int       `json:"expirationInMinutes"`
	AllowedPorts        []portSpec `json:"allowedPorts"`
}

func decode(r *http.Request, into any) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) == 0 {
		return err
	}
	return json.Unmarshal(raw, into)
}

func validationError(w http.ResponseWriter, message string) {
	api.WriteError(w, http.StatusBadRequest, "ValidationException",
		map[string]any{"message": message})
}

// nullConstraint is the message shape the recording pinned for a missing
// required member, reused here so a client sees one wording for the rule.
func nullConstraint(w http.ResponseWriter, member string) {
	validationError(w, "1 validation error detected: Value null at '"+member+
		"' failed to satisfy constraint: Member must not be null")
}

// resolve finds the VM and applies the one recorded rule about terminal
// state. Issuing against a SUSPENDED VM is deliberately allowed: recorded, a
// suspended VM returns a full token rather than a conflict, which is the
// order a client that means to wake a VM by calling it has to work in.
func (h *handlers) resolve(w http.ResponseWriter, r *http.Request) (region, id string, ok bool) {
	region = api.RegionFromRequest(r)
	id = r.PathValue("microvmIdentifier")
	state, _, found := h.vms.Status(region, id)
	if !found {
		api.WriteError(w, http.StatusNotFound, "ResourceNotFoundException", map[string]any{
			"message":      "MicroVM not found: " + id,
			"resourceId":   nil,
			"resourceType": nil,
		})
		return "", "", false
	}
	if state == stateTerminated {
		// Not recorded for this operation specifically. It is the recorded
		// rule for every other mutation of a terminated VM, and a token
		// against a VM that no longer exists could only mislead.
		validationError(w, "The MicroVM "+id+" has been terminated and its state cannot be changed.")
		return "", "", false
	}
	return region, id, true
}

// expiry validates expirationInMinutes. It is required by the model, a
// PositiveInteger, and documented at a maximum of 60.
func expiry(w http.ResponseWriter, minutes *int) (time.Duration, bool) {
	if minutes == nil {
		nullConstraint(w, "expirationInMinutes")
		return 0, false
	}
	if *minutes < 1 {
		validationError(w, "1 validation error detected: Value "+strconv.Itoa(*minutes)+
			" at 'expirationInMinutes' failed to satisfy constraint: Member must have value greater than or equal to 1")
		return 0, false
	}
	if *minutes > MaxExpirationMinutes {
		validationError(w, "1 validation error detected: Value "+strconv.Itoa(*minutes)+
			" at 'expirationInMinutes' failed to satisfy constraint: Member must have value less than or equal to "+
			strconv.Itoa(MaxExpirationMinutes))
		return 0, false
	}
	return time.Duration(*minutes) * time.Minute, true
}

// ports validates allowedPorts. The list is required with a minimum of one,
// and each member is a union — exactly one of port, range or allPorts.
func ports(w http.ResponseWriter, specs []portSpec) (allPorts bool, out []portRange, ok bool) {
	if specs == nil {
		nullConstraint(w, "allowedPorts")
		return false, nil, false
	}
	if len(specs) == 0 {
		validationError(w, "1 validation error detected: Value '[]' at 'allowedPorts' failed to satisfy constraint: Member must have length greater than or equal to 1")
		return false, nil, false
	}
	for i, s := range specs {
		at := "allowedPorts[" + strconv.Itoa(i) + "]"
		set := 0
		if s.Port != nil {
			set++
		}
		if s.Range != nil {
			set++
		}
		if s.AllPorts != nil {
			set++
		}
		if set != 1 {
			validationError(w, "1 validation error detected: Value at '"+at+
				"' failed to satisfy constraint: Exactly one of port, range or allPorts must be set")
			return false, nil, false
		}
		switch {
		case s.AllPorts != nil:
			allPorts = true
		case s.Port != nil:
			if !validPort(*s.Port) {
				validationError(w, portConstraint(at+".port", *s.Port))
				return false, nil, false
			}
			out = append(out, portRange{lo: *s.Port, hi: *s.Port})
		default:
			if s.Range.StartPort == nil {
				nullConstraint(w, at+".range.startPort")
				return false, nil, false
			}
			if s.Range.EndPort == nil {
				nullConstraint(w, at+".range.endPort")
				return false, nil, false
			}
			lo, hi := *s.Range.StartPort, *s.Range.EndPort
			if !validPort(lo) {
				validationError(w, portConstraint(at+".range.startPort", lo))
				return false, nil, false
			}
			if !validPort(hi) {
				validationError(w, portConstraint(at+".range.endPort", hi))
				return false, nil, false
			}
			if lo > hi {
				validationError(w, "1 validation error detected: Value at '"+at+
					".range' failed to satisfy constraint: startPort must be less than or equal to endPort")
				return false, nil, false
			}
			out = append(out, portRange{lo: lo, hi: hi})
		}
	}
	return allPorts, out, true
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }

func portConstraint(at string, v int) string {
	return "1 validation error detected: Value " + strconv.Itoa(v) + " at '" + at +
		"' failed to satisfy constraint: Member must be between 1 and 65535"
}

func (h *handlers) authToken(w http.ResponseWriter, r *http.Request) {
	region, id, ok := h.resolve(w, r)
	if !ok {
		return
	}
	var req tokenRequest
	if err := decode(r, &req); err != nil {
		validationError(w, "Invalid request body")
		return
	}
	expiresIn, ok := expiry(w, req.ExpirationInMinutes)
	if !ok {
		return
	}
	allPorts, ranges, ok := ports(w, req.AllowedPorts)
	if !ok {
		return
	}

	t := h.svc.Issue(region, id, expiresIn, allPorts, ranges, false)
	api.WriteJSON(w, http.StatusOK, map[string]any{
		"authToken": map[string]any{HeaderName: t.Value},
	})
}

// shellAuthToken can only ever fail, and that is the honest implementation.
//
// The recorded response is a 400 with this message. Satisfying it needs a
// SHELL_INGRESS network connector, and SHELL_INGRESS is absent from the
// service model entirely — not merely unrecorded, but unrepresentable, so
// there is no request a client could send that would make m80 succeed here.
// Answering 501 instead would be worse: the operation is implemented, its one
// observable behavior is this error, and a consumer that handles the error is
// correctly exercised by it.
func (h *handlers) shellAuthToken(w http.ResponseWriter, r *http.Request) {
	_, _, ok := h.resolve(w, r)
	if !ok {
		return
	}
	var req tokenRequest
	if err := decode(r, &req); err != nil {
		validationError(w, "Invalid request body")
		return
	}
	// expirationInMinutes is required on this operation too; the model gives
	// it no documented ceiling, so only the positive-integer floor applies.
	if req.ExpirationInMinutes == nil {
		nullConstraint(w, "expirationInMinutes")
		return
	}
	if *req.ExpirationInMinutes < 1 {
		validationError(w, "1 validation error detected: Value "+strconv.Itoa(*req.ExpirationInMinutes)+
			" at 'expirationInMinutes' failed to satisfy constraint: Member must have value greater than or equal to 1")
		return
	}
	validationError(w, "Shell access requires SHELL_INGRESS network connector to be configured on the MicroVM.")
}
