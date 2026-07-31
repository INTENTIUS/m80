package connectors

import "time"

// AccountID is the account m80 pretends to be. It must equal
// images.AccountID; a test pins that, since two different accounts in one
// emulator's ARNs would be a bug no fixture would catch.
const AccountID = "000000000000"

// The four responses carry four different member sets, and that is the model
// rather than an accident of recording. Create and Delete return the base
// six. Get adds the read-only status members. Update adds the update status
// but not StateReason. Only the list summary carries Type.
//
// Absent means absent, not null: a freshly created connector that has never
// been updated has no LastUpdateStatus in its recorded response at all, so
// emitting one as null would diverge on every read.

// configView renders the configuration union. Only VpcEgressConfiguration
// exists in it today.
func configView(c VpcEgress) map[string]any {
	return map[string]any{
		"VpcEgressConfiguration": map[string]any{
			"SubnetIds":                      strs(c.SubnetIds),
			"SecurityGroupIds":               strs(c.SecurityGroupIds),
			"NetworkProtocol":                c.NetworkProtocol,
			"AssociatedComputeResourceTypes": strs(c.AssociatedComputeResourceTypes),
		},
	}
}

// base is CreateNetworkConnectorResponse and DeleteNetworkConnectorResponse:
// Arn, Name, Id, Configuration, OperatorRole, State.
func base(c Connector) map[string]any {
	return map[string]any{
		"Arn":           c.Arn,
		"Name":          c.Name,
		"Id":            c.ID,
		"Configuration": configView(c.Config),
		"OperatorRole":  c.OperatorRole,
		"State":         c.State,
	}
}

// detail is GetNetworkConnectorResponse. Version, StateReasonCode and the
// LastUpdateStatus trio appear only once they have values.
func detail(c Connector) map[string]any {
	out := base(c)
	putTime(out, "LastModified", c.LastModified)
	putStr(out, "StateReason", c.StateReason)
	putStr(out, "StateReasonCode", c.StateReasonCode)
	putStr(out, "LastUpdateStatus", c.LastUpdateStatus)
	putStr(out, "LastUpdateStatusReason", c.LastUpdateStatusReason)
	putStr(out, "LastUpdateStatusReasonCode", c.LastUpdateStatusReasonCode)
	return out
}

// updated is UpdateNetworkConnectorResponse: the base plus LastModified and
// the update status, but no StateReason — the model leaves it off this one.
func updated(c Connector) map[string]any {
	out := base(c)
	putTime(out, "LastModified", c.LastModified)
	putStr(out, "LastUpdateStatus", c.LastUpdateStatus)
	putStr(out, "LastUpdateStatusReason", c.LastUpdateStatusReason)
	return out
}

// summary is NetworkConnectorSummary, the only shape carrying Type.
func summary(c Connector) map[string]any {
	out := map[string]any{
		"Arn":   c.Arn,
		"Name":  c.Name,
		"Id":    c.ID,
		"Type":  TypeVpcEgress,
		"State": c.State,
	}
	putTime(out, "LastModified", c.LastModified)
	return out
}

func putStr(m map[string]any, key string, v *string) {
	if v != nil {
		m[key] = *v
	}
}

func putTime(m map[string]any, key string, v *time.Time) {
	if v != nil {
		m[key] = v.UTC().Format(time.RFC3339)
	}
}

// strs never returns nil, so an empty security group list serializes as []
// rather than null. Zero security groups is legal — the model's minimum is 0.
func strs(s []string) []any {
	out := make([]any, 0, len(s))
	for _, v := range s {
		out = append(out, v)
	}
	return out
}
