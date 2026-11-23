package sg

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"h3terraform/internal/client"
)

var (
	_ resource.Resource                = &RuleResource{}
	_ resource.ResourceWithConfigure   = &RuleResource{}
	_ resource.ResourceWithImportState = &RuleResource{}
)

func NewRuleResource() resource.Resource { return &RuleResource{} }

type RuleResource struct{ client *client.Client }

type RuleResourceModel struct {
	ID                    types.String `tfsdk:"id"`
	SecurityGroupID       types.String `tfsdk:"security_group_id"`
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

func (m RuleResourceModel) toRule() Rule {
	return Rule{
		ID:                    m.ID.ValueString(),
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

func (m *RuleResourceModel) setFromAPI(r Rule) {
	m.ID = types.StringValue(r.ID)
	m.Direction = types.StringValue(r.Direction)
	m.Action = types.StringValue(normalize(r).Action)
	m.Protocol = types.StringValue(normalize(r).Protocol)
	m.PortFrom = optInt(r.PortFrom)
	m.PortTo = optInt(r.PortTo)
	m.RemoteCIDR = optStr(r.RemoteCIDR)
	m.RemoteSecurityGroupID = optStr(r.RemoteSecurityGroupID)
	m.Priority = types.Int64Value(normalize(r).Priority)
	m.Description = optStr(r.Description)
}

func (r *RuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group_rule"
}

func (r *RuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	str := func(desc string, required bool) schema.StringAttribute {
		return schema.StringAttribute{
			MarkdownDescription: desc, Required: required, Optional: !required,
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		}
	}
	num := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{
			MarkdownDescription: desc, Optional: true,
			PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "One rule of a security group, managed separately from the group. " +
			"Do not use it on a group that has inline `rule` blocks in `h3_security_group`. Any change replaces the rule.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Rule ID.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"security_group_id": str("Security group ID.", true),
			"direction":         str("`ingress` or `egress`.", true),
			"action": schema.StringAttribute{
				MarkdownDescription: "`allow` (default) or `deny`.", Optional: true, Computed: true,
				Default:       stringdefault.StaticString("allow"),
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"protocol": schema.StringAttribute{
				MarkdownDescription: "`all` (default), `tcp`, `udp` or `icmp`.", Optional: true, Computed: true,
				Default:       stringdefault.StaticString("all"),
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"port_from":                num("First port (tcp/udp). Unset = all ports."),
			"port_to":                  num("Last port (tcp/udp). Unset = `port_from`."),
			"remote_cidr":              str("IPv4/IPv6 CIDR or address of the peer. Conflicts with `remote_security_group_id`.", false),
			"remote_security_group_id": str("Peer security group of the same project.", false),
			"priority": schema.Int64Attribute{
				MarkdownDescription: "1..170, lower is evaluated first. Default 100.", Optional: true, Computed: true,
				Default:       int64default.StaticInt64(defaultPriority),
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"description": str("Free text, up to 255 characters.", false),
		},
	}
}

func (r *RuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c, err := configure(req.ProviderData)
	if err != nil {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", err.Error())
		return
	}
	r.client = c
}

// findNewRule returns the id of the rule in after that is not in before and
// equals want. Parallel applies may add other rules at the same time, hence
// the match on content and not only on "the new id".
func findNewRule(before, after []Rule, want Rule) (Rule, bool) {
	old := map[string]bool{}
	for _, r := range before {
		old[r.ID] = true
	}
	for _, r := range after {
		if !old[r.ID] && sameRule(r, want) {
			return r, true
		}
	}
	return Rule{}, false
}

func (r *RuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RuleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	want := plan.toRule()
	if err := validateRule(want); err != nil {
		resp.Diagnostics.AddError("Invalid security group rule", err.Error())
		return
	}
	sgID := plan.SecurityGroupID.ValueString()
	before, err := getGroup(ctx, r.client, sgID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading security group", err.Error())
		return
	}
	var after Group
	if err := r.client.Do(ctx, "POST", rulesPath(sgID), nil, want, &after); err != nil {
		resp.Diagnostics.AddError("Error adding security group rule", err.Error())
		return
	}
	created, ok := findNewRule(before.Rules, after.Rules, want)
	if !ok {
		resp.Diagnostics.AddError("Error adding security group rule", "the API accepted the rule but it is not in the group's rule list")
		return
	}
	plan.ID = types.StringValue(created.ID)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *RuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RuleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := getGroup(ctx, r.client, state.SecurityGroupID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading security group", err.Error())
		return
	}
	for _, api := range g.Rules {
		if api.ID != state.ID.ValueString() {
			continue
		}
		if !sameRule(state.toRule(), api) {
			state.setFromAPI(api)
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	}
	resp.State.RemoveResource(ctx)
}

func (r *RuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Every attribute forces replacement; nothing to update in place.
	var plan RuleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *RuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RuleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.Do(ctx, "DELETE", rulesPath(state.SecurityGroupID.ValueString())+"/"+state.ID.ValueString(), nil, nil, nil)
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Error deleting security group rule", err.Error())
	}
}

func (r *RuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	sgID, ruleID, ok := strings.Cut(req.ID, "/")
	if !ok || sgID == "" || ruleID == "" {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("expected <security_group_id>/<rule_id>, got %q", req.ID))
		return
	}
	g, err := getGroup(ctx, r.client, sgID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading security group", err.Error())
		return
	}
	for _, api := range g.Rules {
		if api.ID == ruleID {
			var m RuleResourceModel
			m.SecurityGroupID = types.StringValue(sgID)
			m.setFromAPI(api)
			resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
			return
		}
	}
	resp.Diagnostics.AddAttributeError(path.Root("id"), "Rule not found", fmt.Sprintf("group %s has no rule %s", sgID, ruleID))
}
