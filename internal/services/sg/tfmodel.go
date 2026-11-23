package sg

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"h3terraform/internal/client"
)

// RuleBlockModel is one inline `rule` block of h3_security_group.
type RuleBlockModel struct {
	Direction             types.String `tfsdk:"direction"`
	Action                types.String `tfsdk:"action"`
	Protocol              types.String `tfsdk:"protocol"`
	PortFrom              types.Int64  `tfsdk:"port_from"`
	PortTo                types.Int64  `tfsdk:"port_to"`
	RemoteCIDR            types.String `tfsdk:"remote_cidr"`
	RemoteSecurityGroupID types.String `tfsdk:"remote_security_group_id"`
	Priority              types.Int64  `tfsdk:"priority"`
	Description           types.String `tfsdk:"description"`
}

func (m RuleBlockModel) toRule() Rule {
	return Rule{
		Direction:             m.Direction.ValueString(),
		Action:                m.Action.ValueString(),
		Protocol:              m.Protocol.ValueString(),
		PortFrom:              m.PortFrom.ValueInt64(),
		PortTo:                m.PortTo.ValueInt64(),
		RemoteCIDR:            m.RemoteCIDR.ValueString(),
		RemoteSecurityGroupID: m.RemoteSecurityGroupID.ValueString(),
		Priority:              m.Priority.ValueInt64(),
		Description:           m.Description.ValueString(),
	}
}

func optStr(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func optInt(v int64) types.Int64 {
	if v == 0 {
		return types.Int64Null()
	}
	return types.Int64Value(v)
}

func ruleBlockFromAPI(r Rule) RuleBlockModel {
	return RuleBlockModel{
		Direction:             types.StringValue(r.Direction),
		Action:                optStr(r.Action),
		Protocol:              optStr(r.Protocol),
		PortFrom:              optInt(r.PortFrom),
		PortTo:                optInt(r.PortTo),
		RemoteCIDR:            optStr(r.RemoteCIDR),
		RemoteSecurityGroupID: optStr(r.RemoteSecurityGroupID),
		Priority:              optInt(r.Priority),
		Description:           optStr(r.Description),
	}
}

func blocksToRules(bs []RuleBlockModel) ([]Rule, error) {
	out := make([]Rule, 0, len(bs))
	for i, b := range bs {
		r := b.toRule()
		if err := validateRule(r); err != nil {
			return nil, fmt.Errorf("rule %d: %w", i, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// reconcileBlocks keeps the configured blocks when the API holds the same rule
// set (avoids diffs from server-side defaults/canonical CIDRs) and otherwise
// reports the API rules as drift.
func reconcileBlocks(state []RuleBlockModel, api []Rule) []RuleBlockModel {
	cur := make([]Rule, len(state))
	for i, b := range state {
		cur[i] = b.toRule()
	}
	if sameRuleSet(cur, api) {
		return state
	}
	out := make([]RuleBlockModel, len(api))
	for i, r := range api {
		out[i] = ruleBlockFromAPI(r)
	}
	return out
}

func configure(data any) (*client.Client, error) {
	if data == nil {
		return nil, nil
	}
	c, ok := data.(*client.Client)
	if !ok {
		return nil, fmt.Errorf("expected *client.Client, got: %T", data)
	}
	return c, nil
}
