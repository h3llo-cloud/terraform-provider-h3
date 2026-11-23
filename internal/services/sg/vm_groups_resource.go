package sg

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"h3terraform/internal/client"
)

var (
	_ resource.Resource                = &VMGroupsResource{}
	_ resource.ResourceWithConfigure   = &VMGroupsResource{}
	_ resource.ResourceWithImportState = &VMGroupsResource{}
)

func NewVMGroupsResource() resource.Resource { return &VMGroupsResource{} }

type VMGroupsResource struct{ client *client.Client }

type VMGroupsResourceModel struct {
	ID               types.String `tfsdk:"id"`
	VMID             types.String `tfsdk:"vm_id"`
	SecurityGroupIDs types.Set    `tfsdk:"security_group_ids"`
	ForeignGroups    types.List   `tfsdk:"foreign_groups"`
	DefaultOnDestroy types.Bool   `tfsdk:"default_on_destroy"`
}

func (r *VMGroupsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_security_groups"
}

func (r *VMGroupsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The security groups of a VM (exclusive: the listed groups replace whatever the VM had). " +
			"Changes apply live, without a VM restart. An empty list detaches every group (the VM is marked detached and the " +
			"platform does not put it back on `default`). On destroy the VM goes back to its project's `default` group " +
			"(allow all) unless `default_on_destroy = false`. Do not combine with `h3_vm_security_group_attachment` on the same VM: " +
			"the two would undo each other's changes.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Same as `vm_id`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"vm_id": schema.StringAttribute{
				MarkdownDescription: "VM ID.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"security_group_ids": schema.SetAttribute{
				MarkdownDescription: "0 to 5 security group IDs of the VM's project; `[]` detaches all groups.",
				ElementType:         types.StringType,
				Required:            true,
			},
			"default_on_destroy": schema.BoolAttribute{
				MarkdownDescription: "On destroy, put the VM back on the project `default` group (default `true`). " +
					"`false` leaves the groups as they are.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"foreign_groups": schema.ListAttribute{
				MarkdownDescription: "Platform groups on the VM port that are not managed here (e.g. the managed Kubernetes project group).",
				ElementType:         types.StringType,
				Computed:            true,
			},
		},
	}
}

func (r *VMGroupsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c, err := configure(req.ProviderData)
	if err != nil {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", err.Error())
		return
	}
	r.client = c
}

func (r *VMGroupsResource) put(ctx context.Context, m *VMGroupsResourceModel) error {
	var ids []string
	if d := m.SecurityGroupIDs.ElementsAs(ctx, &ids, false); d.HasError() {
		return fmt.Errorf("security_group_ids: %v", d)
	}
	if len(ids) > maxVMGroups {
		return fmt.Errorf("security_group_ids must contain 0 to %d groups, got %d", maxVMGroups, len(ids))
	}
	if ids == nil {
		ids = []string{} // [] detaches; null is rejected by the API
	}
	var out VMGroups
	if err := r.client.Do(ctx, "PUT", vmGroupsPath(m.VMID.ValueString()), nil, SetVMGroupsRequest{SecurityGroupIDs: ids}, &out); err != nil {
		return err
	}
	return applyVMGroups(ctx, m, &out)
}

func applyVMGroups(ctx context.Context, m *VMGroupsResourceModel, g *VMGroups) error {
	ids := g.SecurityGroupIDs
	if ids == nil {
		ids = []string{}
	}
	set, d := types.SetValueFrom(ctx, types.StringType, ids)
	if d.HasError() {
		return fmt.Errorf("%v", d)
	}
	foreign := g.ForeignGroups
	if foreign == nil {
		foreign = []string{}
	}
	list, d := types.ListValueFrom(ctx, types.StringType, foreign)
	if d.HasError() {
		return fmt.Errorf("%v", d)
	}
	m.ID = m.VMID
	if m.DefaultOnDestroy.IsNull() || m.DefaultOnDestroy.IsUnknown() {
		m.DefaultOnDestroy = types.BoolValue(true)
	}
	m.SecurityGroupIDs = set
	m.ForeignGroups = list
	return nil
}

func (r *VMGroupsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan VMGroupsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Error setting VM security groups", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *VMGroupsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VMGroupsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out VMGroups
	if err := r.client.Do(ctx, "GET", vmGroupsPath(state.VMID.ValueString()), nil, nil, &out); err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading VM security groups", err.Error())
		return
	}
	if err := applyVMGroups(ctx, &state, &out); err != nil {
		resp.Diagnostics.AddError("Error reading VM security groups", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *VMGroupsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan VMGroupsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Error setting VM security groups", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// defaultGroupFor finds the `default` group of the VM's project.
func defaultGroupFor(ctx context.Context, c *client.Client, vm *VMGroups, stateIDs []string) (string, error) {
	projectID := ""
	for _, g := range vm.SecurityGroups {
		if g.ProjectID != "" {
			projectID = g.ProjectID
			break
		}
	}
	if projectID == "" {
		for _, id := range stateIDs {
			if g, err := getGroup(ctx, c, id); err == nil && g.ProjectID != "" {
				projectID = g.ProjectID
				break
			}
		}
	}
	if projectID == "" {
		// A detached VM has no groups: ask the VM API, then the provider default.
		var v struct {
			ProjectID string `json:"project_id"`
		}
		if err := c.Do(ctx, "GET", "/api/vms/v1/"+vm.ResourceID, nil, nil, &v); err == nil {
			projectID = v.ProjectID
		}
	}
	if projectID == "" {
		projectID = c.ProjectID()
	}
	if projectID == "" {
		return "", fmt.Errorf("cannot determine the project of VM %s: set the provider project_id", vm.ResourceID)
	}
	groups, err := listGroups(ctx, c, projectID)
	if err != nil {
		return "", err
	}
	for _, g := range groups {
		if g.IsDefault {
			return g.ID, nil
		}
	}
	return "", fmt.Errorf("project %s has no default security group", projectID)
}

func (r *VMGroupsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VMGroupsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !state.DefaultOnDestroy.IsNull() && !state.DefaultOnDestroy.ValueBool() {
		return
	}
	var cur VMGroups
	if err := r.client.Do(ctx, "GET", vmGroupsPath(state.VMID.ValueString()), nil, nil, &cur); err != nil {
		if isNotFound(err) {
			return // VM is gone
		}
		resp.Diagnostics.AddError("Error reading VM security groups", err.Error())
		return
	}
	var stateIDs []string
	resp.Diagnostics.Append(state.SecurityGroupIDs.ElementsAs(ctx, &stateIDs, false)...)
	def, err := defaultGroupFor(ctx, r.client, &cur, stateIDs)
	if err != nil {
		resp.Diagnostics.AddError("Error resetting VM security groups", err.Error())
		return
	}
	err = r.client.Do(ctx, "PUT", vmGroupsPath(state.VMID.ValueString()), nil, SetVMGroupsRequest{SecurityGroupIDs: []string{def}}, nil)
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Error resetting VM security groups to default", err.Error())
	}
}

func (r *VMGroupsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vm_id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("default_on_destroy"), true)...)
}
