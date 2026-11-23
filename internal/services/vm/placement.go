package vm

import (
	"context"
	"fmt"
	"strings"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Placement groups (h3vm TRD 09): GET/POST /api/vms/v1/placement/groups,
// GET/PATCH/DELETE /api/vms/v1/placement/groups/{id}?project_id=.

const placementBase = "/api/vms/v1/placement/groups"

// PlacementGroup is the h3vm public placement group (PlacementGroupResponse).
type PlacementGroup struct {
	ID          string   `json:"id"`
	ProjectID   string   `json:"project_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Strategy    string   `json:"strategy"`
	VMIDs       []string `json:"vm_ids"`
	MaxVMs      int64    `json:"max_vms"`
	CreatedAt   string   `json:"created_at"`
}

type createPlacementGroupRequest struct {
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Strategy    string `json:"strategy,omitempty"`
}

type updatePlacementGroupRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

func getPlacementGroup(ctx context.Context, c *client.Client, projectID, id string) (*PlacementGroup, error) {
	var g PlacementGroup
	if err := c.Do(ctx, "GET", placementBase+"/"+id, map[string]string{"project_id": projectID}, nil, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

func listPlacementGroups(ctx context.Context, c *client.Client, projectID string) ([]PlacementGroup, error) {
	var out []PlacementGroup
	if err := c.Do(ctx, "GET", placementBase, map[string]string{"project_id": projectID}, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// importProjectID parses "<project_id>/<id>", or a bare "<id>" with the
// provider default project.
func importProjectID(c *client.Client, raw, example string) (string, string, error) {
	if !strings.Contains(raw, "/") && raw != "" && c != nil && c.ProjectID() != "" {
		return c.ProjectID(), raw, nil
	}
	return common.ImportProjectScoped(raw, example)
}

var (
	_ resource.Resource                = &PlacementGroupResource{}
	_ resource.ResourceWithConfigure   = &PlacementGroupResource{}
	_ resource.ResourceWithImportState = &PlacementGroupResource{}
	_ resource.ResourceWithModifyPlan  = &PlacementGroupResource{}
)

// NewPlacementGroupResource is h3_vm_placement_group.
func NewPlacementGroupResource() resource.Resource { return &PlacementGroupResource{} }

// PlacementGroupResource manages a spread placement group.
type PlacementGroupResource struct{ client *client.Client }

// PlacementGroupModel is the state of h3_vm_placement_group (and its data source).
type PlacementGroupModel struct {
	ID          types.String `tfsdk:"id"`
	ProjectID   types.String `tfsdk:"project_id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Strategy    types.String `tfsdk:"strategy"`
	VMIDs       types.List   `tfsdk:"vm_ids"`
	MaxVMs      types.Int64  `tfsdk:"max_vms"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func (m *PlacementGroupModel) apply(g *PlacementGroup) {
	m.ID = types.StringValue(g.ID)
	if g.ProjectID != "" {
		m.ProjectID = types.StringValue(g.ProjectID)
	}
	m.Name = types.StringValue(g.Name)
	m.Description = types.StringValue(g.Description)
	m.Strategy = types.StringValue(orDefault(g.Strategy, "spread"))
	m.VMIDs = stringList(g.VMIDs)
	m.MaxVMs = types.Int64Value(g.MaxVMs)
	m.CreatedAt = types.StringValue(g.CreatedAt)
}

func stringList(ids []string) types.List {
	vals := make([]string, 0, len(ids))
	vals = append(vals, ids...)
	l, _ := types.ListValueFrom(context.Background(), types.StringType, vals)
	return l
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func (r *PlacementGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_placement_group"
}

func (r *PlacementGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a VM placement group. Strategy `spread`: every VM of the group runs on its own host, " +
			"so the failure of one host stops at most one VM of the group (database replicas, etcd, application clusters). " +
			"A VM joins a group only when it is created (`h3_vm.placement_group_id`). The group holds at most `max_vms` VMs " +
			"(the platform limit, never more than the number of hosts minus one, so that any host can be drained). " +
			"`name` and `description` change in place; a group with VMs cannot be deleted (409 `PLACEMENT_GROUP_NOT_EMPTY`). " +
			"Placement groups are free.",
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{MarkdownDescription: "Placement group ID.", Computed: true, PlanModifiers: keep},
			"project_id": common.ProjectIDAttribute("placement group"),
			"name": schema.StringAttribute{
				MarkdownDescription: "Group name (1-63 characters). Changed in place.",
				Required:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Description. Changed in place.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(""),
			},
			"strategy": schema.StringAttribute{
				MarkdownDescription: "Placement strategy: `spread` (the default and the only one). Changing it replaces the group.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("spread"),
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"vm_ids": schema.ListAttribute{
				MarkdownDescription: "IDs of the VMs in the group.",
				ElementType:         types.StringType,
				Computed:            true,
			},
			"max_vms":    schema.Int64Attribute{MarkdownDescription: "Maximum number of VMs in the group.", Computed: true},
			"created_at": schema.StringAttribute{MarkdownDescription: "Creation time (RFC 3339).", Computed: true, PlanModifiers: keep},
		},
	}
}

func (r *PlacementGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *PlacementGroupResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
}

func (r *PlacementGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PlacementGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var g PlacementGroup
	err := r.client.Do(ctx, "POST", placementBase, nil, createPlacementGroupRequest{
		ProjectID:   plan.ProjectID.ValueString(),
		Name:        plan.Name.ValueString(),
		Description: plan.Description.ValueString(),
		Strategy:    plan.Strategy.ValueString(),
	}, &g)
	if err != nil {
		resp.Diagnostics.AddError("Error creating placement group", err.Error())
		return
	}
	plan.apply(&g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *PlacementGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PlacementGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := getPlacementGroup(ctx, r.client, state.ProjectID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) && !isFeatureDisabled(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading placement group", err.Error())
		return
	}
	state.apply(g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *PlacementGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state PlacementGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body updatePlacementGroupRequest
	if !plan.Name.Equal(state.Name) {
		v := plan.Name.ValueString()
		body.Name = &v
	}
	if !plan.Description.Equal(state.Description) {
		v := plan.Description.ValueString()
		body.Description = &v
	}
	q := map[string]string{"project_id": state.ProjectID.ValueString()}
	var g PlacementGroup
	var err error
	if body.Name != nil || body.Description != nil {
		err = r.client.Do(ctx, "PATCH", placementBase+"/"+state.ID.ValueString(), q, body, &g)
	} else {
		err = r.client.Do(ctx, "GET", placementBase+"/"+state.ID.ValueString(), q, nil, &g)
	}
	if err != nil {
		resp.Diagnostics.AddError("Error updating placement group", err.Error())
		return
	}
	plan.apply(&g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *PlacementGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state PlacementGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.Do(ctx, "DELETE", placementBase+"/"+state.ID.ValueString(), map[string]string{"project_id": state.ProjectID.ValueString()}, nil, nil)
	if err == nil || (client.IsNotFound(err) && !isFeatureDisabled(err)) {
		return
	}
	if e, ok := client.AsHTTPError(err); ok && e.StatusCode == 409 && strings.Contains(e.Body, "PLACEMENT_GROUP_NOT_EMPTY") {
		resp.Diagnostics.AddError("Placement group is not empty",
			"Placement group "+state.ID.ValueString()+" still has VMs: delete them (or their h3_vm resources) first. "+err.Error())
		return
	}
	resp.Diagnostics.AddError("Error deleting placement group", err.Error())
}

// ImportState: "<project_id>/<group_id>", or "<group_id>" with the provider default project.
func (r *PlacementGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, id, err := importProjectID(r.client, req.ID, "<group_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// isFeatureDisabled: the h3vm feature flag answers 404 with a *_DISABLED code;
// that is not "the object is gone".
func isFeatureDisabled(err error) bool {
	e, ok := client.AsHTTPError(err)
	return ok && strings.Contains(e.Body, "_DISABLED")
}

// ---- data source h3_vm_placement_group ----

var (
	_ datasource.DataSource              = &PlacementGroupDataSource{}
	_ datasource.DataSourceWithConfigure = &PlacementGroupDataSource{}
)

// NewPlacementGroupDataSource is data h3_vm_placement_group.
func NewPlacementGroupDataSource() datasource.DataSource { return &PlacementGroupDataSource{} }

// PlacementGroupDataSource looks up a placement group by id or name.
type PlacementGroupDataSource struct{ client *client.Client }

func (d *PlacementGroupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_placement_group"
}

func (d *PlacementGroupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	computed := func(desc string) dsschema.StringAttribute {
		return dsschema.StringAttribute{MarkdownDescription: desc, Computed: true}
	}
	resp.Schema = dsschema.Schema{
		MarkdownDescription: "Looks up a VM placement group by `id`, or by `name` in the project.",
		Attributes: map[string]dsschema.Attribute{
			"id":          dsschema.StringAttribute{MarkdownDescription: "Placement group ID. Set `id` or `name`.", Optional: true, Computed: true},
			"project_id":  dsschema.StringAttribute{MarkdownDescription: "Project ID; the provider default when omitted.", Optional: true, Computed: true},
			"name":        dsschema.StringAttribute{MarkdownDescription: "Group name. Set `id` or `name`.", Optional: true, Computed: true},
			"description": computed("Description."),
			"strategy":    computed("Placement strategy (`spread`)."),
			"vm_ids":      dsschema.ListAttribute{MarkdownDescription: "IDs of the VMs in the group.", ElementType: types.StringType, Computed: true},
			"max_vms":     dsschema.Int64Attribute{MarkdownDescription: "Maximum number of VMs in the group.", Computed: true},
			"created_at":  computed("Creation time (RFC 3339)."),
		},
	}
}

func (d *PlacementGroupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *PlacementGroupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m PlacementGroupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, ok := common.DataSourceProjectID(d.client, m.ProjectID, resp)
	if !ok {
		return
	}
	id, name := common.KnownString(m.ID), common.KnownString(m.Name)
	var g *PlacementGroup
	var err error
	switch {
	case id != "":
		g, err = getPlacementGroup(ctx, d.client, project, id)
	case name != "":
		var all []PlacementGroup
		all, err = listPlacementGroups(ctx, d.client, project)
		for i := range all {
			if all[i].Name == name {
				g = &all[i]
				break
			}
		}
		if err == nil && g == nil {
			err = fmt.Errorf("no placement group named %q in project %s", name, project)
		}
	default:
		err = fmt.Errorf("set id or name")
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading placement group", err.Error())
		return
	}
	m.ProjectID = types.StringValue(project)
	m.apply(g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
