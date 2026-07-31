package connectors

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/intentius/m80/internal/api"
)

func Register(srv *api.Server, svc *Service) {
	h := &handlers{svc: svc}
	srv.Register("CreateNetworkConnector", h.create)
	srv.Register("GetNetworkConnector", h.get)
	srv.Register("ListNetworkConnectors", h.list)
	srv.Register("UpdateNetworkConnector", h.update)
	srv.Register("DeleteNetworkConnector", h.delete)
}

type handlers struct{ svc *Service }

type configRequest struct {
	VpcEgressConfiguration *struct {
		SubnetIds                      []string `json:"SubnetIds"`
		SecurityGroupIds               []string `json:"SecurityGroupIds"`
		NetworkProtocol                *string  `json:"NetworkProtocol"`
		AssociatedComputeResourceTypes []string `json:"AssociatedComputeResourceTypes"`
	} `json:"VpcEgressConfiguration"`
}

type connectorRequest struct {
	Name          *string        `json:"Name"`
	Configuration *configRequest `json:"Configuration"`
	OperatorRole  *string        `json:"OperatorRole"`
	ClientToken   *string        `json:"ClientToken"`
}

func decode(r *http.Request, into any) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) == 0 {
		return err
	}
	return json.Unmarshal(raw, into)
}

// Bad requests come back two different ways, and which one is recorded, not
// inferred.
//
// Constraint violations — list lengths, enum membership, anything the model
// itself pins — are answered by a validation layer in front of the service,
// in AWS's standard "N validation error detected" wording, with the member
// path in camelCase even though the wire members are PascalCase. That layer
// answers ValidationException, which the Lambda Core model does not list as
// an error of any connector operation at all: another model-versus-service
// divergence, and one a client written from the model would never catch.
//
// The four members that are modeled optional and enforced anyway are service
// logic rather than model constraints, and come back as prose.

// validationError is the model-constraint form.
func validationError(w http.ResponseWriter, value, path, constraint string) {
	api.WriteError(w, http.StatusBadRequest, "ValidationException", map[string]any{
		"message": "1 validation error detected: Value '" + value + "' at '" + path +
			"' failed to satisfy constraint: " + constraint,
	})
}

// listValue renders a list the way the recorded validation message does:
// square brackets, comma and space, no quoting of the members.
func listValue(items []string) string { return "[" + strings.Join(items, ", ") + "]" }

func enumConstraint(allowed ...string) string {
	return "Member must satisfy enum value set: [" + strings.Join(allowed, ", ") + "]"
}

// invalidParameter is the service-logic form: prose, lowercase message.
func invalidParameter(w http.ResponseWriter, message string) {
	api.WriteError(w, http.StatusBadRequest, "InvalidParameterValueException",
		map[string]any{"message": message})
}

// notFound answers with a capital Message, matching Lambda Core's PascalCase
// member style rather than the lowercase message Lambda Microvms uses, and
// echoes the ARN the service built from whatever identifier arrived rather
// than the identifier itself. Both recorded.
func notFound(w http.ResponseWriter, region, identifier string) {
	arn := identifier
	if !strings.HasPrefix(arn, "arn:") {
		arn = "arn:aws:lambda:" + region + ":" + AccountID + ":network-connector:" + identifier
	}
	api.WriteError(w, http.StatusNotFound, "ResourceNotFoundException", map[string]any{
		"Message": "Network connector not found for: " + arn,
	})
}

const vpcPath = "configuration.vpcEgressConfiguration."

// validate runs in two passes, and the order between them is recorded rather
// than chosen.
//
// The too-many-subnets probe sent a body with seventeen subnets and no
// ClientToken, no OperatorRole, no NetworkProtocol and no
// AssociatedComputeResourceTypes — four of the enforced members missing — and
// the service still answered about subnetIds. So the model's constraint layer
// runs first and in full, and only once a request satisfies the model does the
// service's own required-member logic get a look. A client fixing errors one
// at a time therefore sees every constraint violation before it sees the first
// prose message.
func validate(w http.ResponseWriter, req connectorRequest, requireName bool) (VpcEgress, string, bool) {
	var zero VpcEgress
	if !modelConstraints(w, req, requireName) {
		return zero, "", false
	}
	if !requiredMembers(w, req, requireName) {
		return zero, "", false
	}
	cfg := req.Configuration.VpcEgressConfiguration
	return VpcEgress{
		SubnetIds:                      cfg.SubnetIds,
		SecurityGroupIds:               nonNil(cfg.SecurityGroupIds),
		NetworkProtocol:                *cfg.NetworkProtocol,
		AssociatedComputeResourceTypes: cfg.AssociatedComputeResourceTypes,
	}, *req.OperatorRole, true
}

// modelConstraints checks only what the model itself pins, and only on
// members that are actually present. An absent member has no constraint to
// violate; that is the next pass's business.
func modelConstraints(w http.ResponseWriter, req connectorRequest, requireName bool) bool {
	if requireName && req.Name != nil && len(*req.Name) > MaxName {
		validationError(w, *req.Name, "name",
			"Member must have length less than or equal to "+strconv.Itoa(MaxName))
		return false
	}
	if req.ClientToken != nil && len(*req.ClientToken) > MaxClientToken {
		validationError(w, *req.ClientToken, "clientToken",
			"Member must have length less than or equal to "+strconv.Itoa(MaxClientToken))
		return false
	}
	if req.Configuration == nil || req.Configuration.VpcEgressConfiguration == nil {
		return true // nothing modeled to check; requiredMembers reports it
	}
	cfg := req.Configuration.VpcEgressConfiguration

	if n := len(cfg.SubnetIds); n > MaxSubnets {
		validationError(w, listValue(cfg.SubnetIds), vpcPath+"subnetIds",
			"Member must have length less than or equal to "+strconv.Itoa(MaxSubnets))
		return false
	} else if n < MinSubnets {
		validationError(w, listValue(cfg.SubnetIds), vpcPath+"subnetIds",
			"Member must have length greater than or equal to "+strconv.Itoa(MinSubnets))
		return false
	}
	if len(cfg.SecurityGroupIds) > MaxSecurityGroups {
		validationError(w, listValue(cfg.SecurityGroupIds), vpcPath+"securityGroupIds",
			"Member must have length less than or equal to "+strconv.Itoa(MaxSecurityGroups))
		return false
	}
	if cfg.NetworkProtocol != nil && *cfg.NetworkProtocol != "" &&
		*cfg.NetworkProtocol != ProtocolIPv4 && *cfg.NetworkProtocol != ProtocolDualStack {
		validationError(w, *cfg.NetworkProtocol, vpcPath+"networkProtocol",
			enumConstraint(ProtocolIPv4, ProtocolDualStack))
		return false
	}
	if len(cfg.AssociatedComputeResourceTypes) > 1 {
		validationError(w, listValue(cfg.AssociatedComputeResourceTypes),
			vpcPath+"associatedComputeResourceTypes",
			"Member must have length less than or equal to 1")
		return false
	}
	if len(cfg.AssociatedComputeResourceTypes) == 1 &&
		cfg.AssociatedComputeResourceTypes[0] != ComputeResourceMicroVM {
		validationError(w, cfg.AssociatedComputeResourceTypes[0],
			vpcPath+"associatedComputeResourceTypes",
			enumConstraint(ComputeResourceMicroVM))
		return false
	}
	return true
}

// requiredMembers is the service's own logic. Four of these five are modeled
// optional and enforced anyway, each recorded live, and each is the sort of
// model-versus-service divergence the freshness watch exists to catch.
func requiredMembers(w http.ResponseWriter, req connectorRequest, requireName bool) bool {
	if requireName && (req.Name == nil || *req.Name == "") {
		invalidParameter(w, "Name is a required field")
		return false
	}
	// The model marks ClientToken optional and even tags it idempotencyToken,
	// which normally means the SDK generates one for you.
	if req.ClientToken == nil || *req.ClientToken == "" {
		invalidParameter(w, "ClientToken is a required field")
		return false
	}
	if req.Configuration == nil || req.Configuration.VpcEgressConfiguration == nil {
		invalidParameter(w, "Configuration must specify VpcEgressConfiguration")
		return false
	}
	cfg := req.Configuration.VpcEgressConfiguration
	if cfg.NetworkProtocol == nil || *cfg.NetworkProtocol == "" {
		invalidParameter(w, "NetworkProtocol cannot be null or empty")
		return false
	}
	if len(cfg.AssociatedComputeResourceTypes) == 0 {
		invalidParameter(w, "AssociatedComputeResourceTypes cannot be null or empty")
		return false
	}
	// The message names NetworkConnectorOperatorRole rather than the member.
	if req.OperatorRole == nil || *req.OperatorRole == "" {
		invalidParameter(w, "NetworkConnectorOperatorRole is required for "+TypeVpcEgress+" connector type")
		return false
	}
	return true
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	region := api.RegionFromRequest(r)
	var req connectorRequest
	if err := decode(r, &req); err != nil {
		invalidParameter(w, "Invalid request body")
		return
	}
	cfg, role, ok := validate(w, req, true)
	if !ok {
		return
	}

	// ClientToken is an idempotency token: the model says a retry with the
	// same one returns the existing connector rather than a duplicate.
	if existing, found := h.svc.ByName(region, *req.Name); found {
		if existing.ClientToken == *req.ClientToken {
			api.WriteJSON(w, http.StatusAccepted, base(h.svc.Snapshot(existing)))
			return
		}
		api.WriteError(w, http.StatusConflict, "ResourceConflictException", map[string]any{
			"message": "Network connector " + *req.Name + " already exists",
		})
		return
	}

	conn := h.svc.Create(region, AccountID, *req.Name, role, *req.ClientToken, cfg)
	api.WriteJSON(w, http.StatusAccepted, base(h.svc.Snapshot(conn)))
}

func (h *handlers) lookup(w http.ResponseWriter, r *http.Request) (*Connector, bool) {
	region := api.RegionFromRequest(r)
	id := r.PathValue("Identifier")
	conn, ok := h.svc.Get(region, id)
	if !ok {
		notFound(w, region, id)
		return nil, false
	}
	return conn, true
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	conn, ok := h.lookup(w, r)
	if !ok {
		return
	}
	api.WriteJSON(w, http.StatusOK, detail(h.svc.Snapshot(conn)))
}

// list has no NextMarker. The model carries one and the recorded response
// omits it entirely rather than sending null, so m80 omits it too; a client
// looping until the marker is null would otherwise loop forever on a member
// that never appears.
func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	region := api.RegionFromRequest(r)
	items := make([]any, 0)
	for _, c := range h.svc.Snapshots(region) {
		items = append(items, summary(c))
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"NetworkConnectors": items})
}

func (h *handlers) update(w http.ResponseWriter, r *http.Request) {
	conn, ok := h.lookup(w, r)
	if !ok {
		return
	}
	var req connectorRequest
	if err := decode(r, &req); err != nil {
		invalidParameter(w, "Invalid request body")
		return
	}
	// Update does not take a Name; the model marks only Identifier required.
	cfg, role, ok := validate(w, req, false)
	if !ok {
		return
	}
	if snap := h.svc.Snapshot(conn); snap.State == StateDeleting {
		api.WriteError(w, http.StatusConflict, "ResourceConflictException", map[string]any{
			"message": "Network connector " + snap.ID + " is being deleted and cannot be updated",
		})
		return
	}
	h.svc.Update(conn, &cfg, role)
	api.WriteJSON(w, http.StatusAccepted, updated(h.svc.Snapshot(conn)))
}

func (h *handlers) delete(w http.ResponseWriter, r *http.Request) {
	conn, ok := h.lookup(w, r)
	if !ok {
		return
	}
	snap := h.svc.Snapshot(conn)
	if h.svc.InUse(snap.Region, snap.ID) {
		api.WriteError(w, http.StatusConflict, "ResourceConflictException", map[string]any{
			"message": "Cannot delete network connector while it is in use",
		})
		return
	}
	h.svc.Delete(conn)
	api.WriteJSON(w, http.StatusAccepted, base(h.svc.Snapshot(conn)))
}
