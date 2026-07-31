// Package connectors implements network connectors, which belong to the
// Lambda Core service rather than Lambda Microvms: a different model, a
// different API version, and the /2026-04-04/ URI family. One m80 process
// serves both because both sign as lambda.
//
// Four members are modeled optional and enforced anyway. ClientToken,
// OperatorRole, NetworkProtocol and AssociatedComputeResourceTypes are all
// required in practice, each recorded live, and each is the sort of
// model-versus-service divergence the freshness watch (#19) exists to catch.
// A client written from the model alone gets four 400s in a row.
package connectors

import "time"

// NetworkConnectorState.
const (
	StatePending      = "PENDING"
	StateActive       = "ACTIVE"
	StateInactive     = "INACTIVE"
	StateFailed       = "FAILED"
	StateDeleting     = "DELETING"
	StateDeleteFailed = "DELETE_FAILED"
)

// NetworkConnectorLastUpdateStatus.
const (
	UpdateSuccessful = "Successful"
	UpdateFailed     = "Failed"
	UpdateInProgress = "InProgress"
)

// TypeVpcEgress is the only member of NetworkConnectorType. It appears on the
// list summary and nowhere else — recorded, and the model agrees: Type is on
// NetworkConnectorSummary but on none of the single-connector responses.
const TypeVpcEgress = "VPC_EGRESS"

// ComputeResourceMicroVM is the only member of ComputeResourceType. The model
// names it generically, which reads as connectors being shared plumbing for
// whatever Lambda compute kinds come next.
const ComputeResourceMicroVM = "MicroVm"

// ReasonCodes is NetworkConnectorStateReasonCode, shared verbatim with
// NetworkConnectorLastUpdateStatusReasonCode.
//
// Every one of these is a realistic failure m80 can inject without inventing
// anything, and KubeMicroVM's MicroVMNetwork reconciler has visible handling
// to exercise against them.
var ReasonCodes = []string{
	"DisallowedByVpcEncryptionControl",
	"Ec2RequestLimitExceeded",
	"InsufficientRolePermissions",
	"InternalError",
	"InvalidSecurityGroup",
	"InvalidSubnet",
	"SubnetOutOfIPAddresses",
}

// ValidReasonCode reports whether s is one of them.
func ValidReasonCode(s string) bool {
	for _, c := range ReasonCodes {
		if c == s {
			return true
		}
	}
	return false
}

// NetworkProtocol enum.
const (
	ProtocolIPv4      = "IPv4"
	ProtocolDualStack = "DualStack"
)

// Model constraints on the VPC egress configuration.
const (
	MinSubnets        = 1
	MaxSubnets        = 16
	MaxSecurityGroups = 5
	MaxClientToken    = 64
	MaxName           = 140
)

// VpcEgress is the only member of the NetworkConnectorConfiguration union.
type VpcEgress struct {
	SubnetIds                      []string
	SecurityGroupIds               []string
	NetworkProtocol                string
	AssociatedComputeResourceTypes []string
}

// Connector is one network connector.
//
// The optional-and-absent members are pointers rather than zero values
// because the recorded responses omit them entirely rather than sending null.
// A freshly created connector that has never been updated carries no
// LastUpdateStatus at all, and emitting one as null would be a divergence on
// every read.
type Connector struct {
	ID           string
	Region       string
	Account      string
	Name         string
	Arn          string
	OperatorRole string
	Config       VpcEgress
	State        string
	ClientToken  string

	// Tags is set through the tags API and never reaches the wire; no
	// recorded connector response carries a tags member.
	Tags map[string]string

	StateReason     *string
	StateReasonCode *string
	LastModified    *time.Time

	LastUpdateStatus           *string
	LastUpdateStatusReason     *string
	LastUpdateStatusReasonCode *string
}
