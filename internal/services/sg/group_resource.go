package sg

import (
	"context"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"h3terraform/internal/client"
)

var (
	_ resource.Resource                = &GroupResource{}
	_ resource.ResourceWithConfigure   = &GroupResource{}
	_ resource.ResourceWithImportState = &GroupResource{}
)

func NewGroupResource() resource.Resource { return &GroupResource{} }

type GroupResource struct{ client *client.Client }

type GroupResourceModel struct {
	ID                  types.String     `tfsdk:"id"`
	ProjectID           types.String     `tfsdk:"project_id"`
	Name                types.String     `tfsdk:"name"`
	Description         types.String     `tfsdk:"description"`
	RevokeDefaultEgress types.Bool       `tfsdk:"revoke_default_egress"`
	IsDefault           types.Bool       `tfsdk:"is_default"`
	Status              types.String     `tfsdk:"status"`
	AttachedCount       types.Int64      `tfsdk:"attached_count"`
	Rules               []RuleBlockModel `tfsdk:"rule"`
}

func (r *GroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group"
}

func ruleBlockSchema() schema.ListNestedBlock {
	return schema.ListNestedBlock{
		MarkdownDescription: "Inline rule. When at least one block is present the resource owns the **whole** rule set " +
			"(rules added elsewhere are removed). Without blocks the rules are not managed, so the group can be " +
			"combined with `h3_security_group_rule`. Do not mix both styles on one group.",
		NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
			"direction": schema.StringAttribute{MarkdownDescription: "`ingress` or `egress`.", Required: true},
			"action":    schema.StringAttribute{MarkdownDescription: "`allow` (default) or `deny`.", Optional: true},
			"protocol":  schema.StringAttribute{MarkdownDescription: "`all` (default), `tcp`, `udp` or `icmp`.", Optional: true},
			"port_from": schema.Int64Attribute{MarkdownDescription: "First port (tcp/udp). Unset = all ports.", Optional: true},
			"port_to":   schema.Int64Attribute{MarkdownDescription: "Last port (tcp/udp). Unset = `port_from`.", Optional: true},
			"remote_cidr": schema.StringAttribute{
				MarkdownDescription: "IPv4/IPv6 CIDR or address of the peer (`0.0.0.0/0`, `::/0` for any). Conflicts with `remote_security_group_id`.",
				Optional:            true,
			},
			"remote_security_group_id": schema.StringAttribute{
				MarkdownDescription: "Peer security group of the same project (may be this group).",
				Optional:            true,
			},
			"priority":    schema.Int64Attribute{MarkdownDescription: "1..170, lower is evaluated first. Default 100.", Optional: true},
			"description": schema.StringAttribute{MarkdownDescription: "Free text, up to 255 characters.", Optional: true},
		}},
	}
}

func (r *GroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Security group of a project: stateful firewall rules applied to the network port of a VM. " +
			"Every project has a group named `default` that allows all traffic; it cannot be created or deleted, " +
			"but it can be imported and its rules edited.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Security group ID.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"project_id": common.ProjectIDAttribute("security group"),
			"name": schema.StringAttribute{
				MarkdownDescription: "Name, unique in the project. `default` is reserved.",
				Required:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Description.",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"revoke_default_egress": schema.BoolAttribute{
				MarkdownDescription: "Only on create without `rule` blocks: create the group with no rules at all instead of " +
					"the platform default (allow all outbound, no inbound).",
				Optional:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"is_default":     schema.BoolAttribute{MarkdownDescription: "True for the project `default` group.", Computed: true},
			"status":         schema.StringAttribute{MarkdownDescription: "`ACTIVE`, `SYNCING` or `ERROR`.", Computed: true},
			"attached_count": schema.Int64Attribute{MarkdownDescription: "Number of resources the group is attached to.", Computed: true},
		},
		Blocks: map[string]schema.Block{"rule": ruleBlockSchema()},
	}
}

func (r *GroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c, err := configure(req.ProviderData)
	if err != nil {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", err.Error())
		return
	}
	r.client = c
}

func applyGroup(m *GroupResourceModel, g *Group) {
	m.ID = types.StringValue(g.ID)
	m.ProjectID = types.StringValue(g.ProjectID)
	m.Name = types.StringValue(g.Name)
	m.Description = types.StringValue(g.Description)
	m.IsDefault = types.BoolValue(g.IsDefault)
	m.Status = types.StringValue(g.Status)
	m.AttachedCount = types.Int64Value(g.AttachedCount)
	if len(m.Rules) > 0 {
		m.Rules = reconcileBlocks(m.Rules, g.Rules)
	}
}

func (r *GroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan GroupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	create := CreateGroupRequest{
		ProjectID:   plan.ProjectID.ValueString(),
		Name:        plan.Name.ValueString(),
		Description: plan.Description.ValueString(),
	}
	if len(plan.Rules) > 0 {
		rules, err := blocksToRules(plan.Rules)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("rule"), "Invalid rule", err.Error())
			return
		}
		create.Rules = &rules
	} else if plan.RevokeDefaultEgress.ValueBool() {
		empty := []Rule{}
		create.Rules = &empty
	}
	var g Group
	if err := r.client.Do(ctx, "POST", groupsPath, nil, create, &g); err != nil {
		resp.Diagnostics.AddError("Error creating security group", err.Error())
		return
	}
	applyGroup(&plan, &g)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *GroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state GroupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := getGroup(ctx, r.client, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading security group", err.Error())
		return
	}
	applyGroup(&state, g)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *GroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan GroupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var desired []Rule
	if len(plan.Rules) > 0 {
		var err error
		if desired, err = blocksToRules(plan.Rules); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("rule"), "Invalid rule", err.Error())
			return
		}
	}
	g, err := updateGroup(ctx, r.client, plan.ID.ValueString(), func(cur *Group) UpdateGroupRequest {
		rules := cur.Rules // unmanaged: send back unchanged, ids included
		if len(plan.Rules) > 0 {
			rules = keepIDs(desired, cur.Rules)
		}
		return UpdateGroupRequest{Name: plan.Name.ValueString(), Description: plan.Description.ValueString(), Rules: rules}
	})
	if err != nil {
		resp.Diagnostics.AddError("Error updating security group", err.Error())
		return
	}
	applyGroup(&plan, g)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *GroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state GroupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.IsDefault.ValueBool() {
		resp.Diagnostics.AddWarning("Default security group kept",
			"The project `default` security group cannot be deleted; it was only removed from the Terraform state.")
		return
	}
	err := r.client.Do(ctx, "DELETE", groupPath(state.ID.ValueString()), nil, nil, nil)
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Error deleting security group",
			err.Error()+"\nA group cannot be deleted while it is attached to a resource or referenced by a rule of another group.")
	}
}

func (r *GroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ModifyPlan fills project_id from the provider default.
func (r *GroupResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
}
