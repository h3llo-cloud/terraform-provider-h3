package net

import (
	"context"
	"fmt"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &VPCResource{}
	_ resource.ResourceWithConfigure   = &VPCResource{}
	_ resource.ResourceWithImportState = &VPCResource{}
	_ resource.ResourceWithMoveState   = &VPCResource{}
)

var vpcReadyTimeout = 5 * time.Minute

func NewVPCResource() resource.Resource {
	return &VPCResource{}
}

// NewOVNVPCResource is the deprecated h3_ovn_vpc alias (same schema and behaviour).
func NewOVNVPCResource() resource.Resource {
	return &VPCResource{alias: true}
}

type VPCResource struct {
	client *client.Client
	alias  bool // registered under the deprecated h3_ovn_vpc name
}

type VPCResourceModel struct {
	ID           types.String       `tfsdk:"id"`
	ProjectID    types.String       `tfsdk:"project_id"`
	Name         types.String       `tfsdk:"name"`
	Namespaces   types.List         `tfsdk:"namespaces"`
	StaticRoutes []StaticRouteModel `tfsdk:"static_routes"`
	Status       types.String       `tfsdk:"status"`
}

type StaticRouteModel struct {
	CIDR      types.String `tfsdk:"cidr"`
	NextHopIP types.String `tfsdk:"next_hop_ip"`
	Policy    types.String `tfsdk:"policy"`
}

func (r *VPCResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = common.RenameVPC.TypeName(req.ProviderTypeName, r.alias)
}

func (r *VPCResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the VPC of a project. The platform has one VPC per project and its ID equals the project ID. " +
			"Every argument change replaces the VPC (the API has no update), so remove networks first. " +
			"Routes of the VPC are managed with `h3_vpc_route`; `static_routes` is deprecated and refused by the API.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "VPC ID (equals the project ID)",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"project_id": common.ProjectIDAttribute("VPC"),
			"name": schema.StringAttribute{
				MarkdownDescription: "VPC name",
				Required:            true,
				PlanModifiers:       replace,
			},
			"namespaces": schema.ListAttribute{
				MarkdownDescription: "Namespaces attached to the VPC. Optional: the project namespace is used when omitted.",
				ElementType:         types.StringType,
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
					listplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"static_routes": schema.ListNestedAttribute{
				MarkdownDescription: "Deprecated: the API refuses static routes at VPC creation since 04.11.2026. Use `h3_vpc_route`.",
				DeprecationMessage:  "static_routes are refused by the API at VPC creation; use the h3_vpc_route resource",
				Optional:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"cidr": schema.StringAttribute{
							MarkdownDescription: "Destination CIDR",
							Required:            true,
						},
						"next_hop_ip": schema.StringAttribute{
							MarkdownDescription: "Next hop IP address",
							Required:            true,
						},
						"policy": schema.StringAttribute{
							MarkdownDescription: "Routing policy (`policyDst` by default)",
							Optional:            true,
							Computed:            true,
						},
					},
				},
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "VPC status (`CREATING`, `ACTIVE`)",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
	common.RenameVPC.Deprecate(&resp.Schema, r.alias)
}

func (r *VPCResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *VPCResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan VPCResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := CreateVPCRequest{
		ProjectID: plan.ProjectID.ValueString(),
		Name:      plan.Name.ValueString(),
	}
	// namespaces is Optional+Computed: unknown (not null) when not configured.
	if !plan.Namespaces.IsNull() && !plan.Namespaces.IsUnknown() {
		resp.Diagnostics.Append(plan.Namespaces.ElementsAs(ctx, &createReq.Namespaces, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	for _, sr := range plan.StaticRoutes {
		createReq.StaticRoutes = append(createReq.StaticRoutes, StaticRouteDTO{
			CIDR:      sr.CIDR.ValueString(),
			NextHopIP: sr.NextHopIP.ValueString(),
			Policy:    common.KnownString(sr.Policy),
		})
	}

	var created VPC
	if err := r.client.Do(ctx, "POST", "/api/ovn/v1/vpcs", nil, createReq, &created); err != nil {
		resp.Diagnostics.AddError("Error creating VPC", "Could not create VPC: "+err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), created.ID)...)

	got, err := r.waitReady(ctx, created.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for VPC", "VPC created but not ready: "+err.Error())
		return
	}
	// Route policies defaulted by the API come only with the create response.
	for i := range plan.StaticRoutes {
		if !plan.StaticRoutes[i].Policy.IsUnknown() {
			continue
		}
		plan.StaticRoutes[i].Policy = types.StringNull()
		if i < len(created.StaticRoutes) && created.StaticRoutes[i].Policy != "" {
			plan.StaticRoutes[i].Policy = types.StringValue(created.StaticRoutes[i].Policy)
		}
	}
	resp.Diagnostics.Append(plan.applyAPI(ctx, got)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VPCResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VPCResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vpc, err := r.get(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading VPC", err.Error())
		return
	}
	resp.Diagnostics.Append(state.applyAPI(ctx, vpc)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called with a change: every argument forces replacement.
func (r *VPCResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan VPCResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VPCResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VPCResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Do(ctx, "DELETE", "/api/ovn/v1/vpcs/"+state.ID.ValueString(), nil, nil, nil); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting VPC", err.Error())
	}
}

func (r *VPCResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// applyAPI maps the API object. Static routes are not returned by GET and
// keep their prior value.
func (m *VPCResourceModel) applyAPI(ctx context.Context, v *VPC) (diags diag.Diagnostics) {
	m.ID = types.StringValue(v.ID)
	m.Name = types.StringValue(v.Name)
	if v.ProjectID != "" {
		m.ProjectID = types.StringValue(v.ProjectID)
	}
	if v.Namespaces != nil || m.Namespaces.IsUnknown() {
		ns, d := types.ListValueFrom(ctx, types.StringType, nonNil(v.Namespaces))
		diags.Append(d...)
		m.Namespaces = ns
	}
	m.Status = types.StringValue(v.Status)
	return diags
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (r *VPCResource) get(ctx context.Context, id string) (*VPC, error) {
	var v VPC
	if err := r.client.Do(ctx, "GET", "/api/ovn/v1/vpcs/"+id, nil, nil, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (r *VPCResource) waitReady(ctx context.Context, id string) (*VPC, error) {
	var last *VPC
	err := common.WaitFor(ctx, vpcReadyTimeout, "VPC "+id+" to become ACTIVE", func(ctx context.Context) (bool, error) {
		v, err := r.get(ctx, id)
		if err != nil {
			return false, err
		}
		last = v
		switch v.Status {
		case "ACTIVE":
			return true, nil
		case "ERROR", "FAILED":
			return false, fmt.Errorf("VPC entered %s state", v.Status)
		}
		return false, nil
	})
	return last, err
}

// MoveState accepts `moved {}` from the deprecated h3_ovn_vpc type.
func (r *VPCResource) MoveState(ctx context.Context) []resource.StateMover {
	if r.alias {
		return nil
	}
	old := &VPCResource{alias: true}
	var sr resource.SchemaResponse
	old.Schema(ctx, resource.SchemaRequest{}, &sr)
	return common.RenameVPC.MoveFromOld(sr.Schema)
}

// ModifyPlan fills project_id from the provider default.
func (r *VPCResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
}
