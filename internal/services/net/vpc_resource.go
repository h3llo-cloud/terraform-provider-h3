package net

import (
	"context"
	"fmt"
	"time"

	"h3terraform/internal/client"

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
)

func NewVPCResource() resource.Resource {
	return &VPCResource{}
}

type VPCResource struct {
	client *client.Client
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
	resp.TypeName = req.ProviderTypeName + "_ovn_vpc"
}

func (r *VPCResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages H3 Cloud OVN VPC",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "VPC ID",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Project ID (UUID)",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "VPC name",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"namespaces": schema.ListAttribute{
				MarkdownDescription: "List of namespaces attached to VPC",
				ElementType:         types.StringType,
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
			},
			"static_routes": schema.ListNestedAttribute{
				MarkdownDescription: "Static routes for VPC",
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
							MarkdownDescription: "Routing policy",
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
				MarkdownDescription: "VPC status",
				Computed:            true,
			},
		},
	}
}

func (r *VPCResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}

	r.client = client
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

	if !plan.Namespaces.IsNull() {
		var namespaces []string
		resp.Diagnostics.Append(plan.Namespaces.ElementsAs(ctx, &namespaces, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		createReq.Namespaces = namespaces
	}

	if len(plan.StaticRoutes) > 0 {
		var staticRoutes []StaticRouteDTO
		for _, sr := range plan.StaticRoutes {
			staticRoutes = append(staticRoutes, StaticRouteDTO{
				CIDR:      sr.CIDR.ValueString(),
				NextHopIP: sr.NextHopIP.ValueString(),
				Policy:    sr.Policy.ValueString(),
			})
		}
		createReq.StaticRoutes = staticRoutes
	}

	var vpc VPC
	err := r.client.Do(ctx, "POST", "/api/ovn/v1/vpcs", nil, createReq, &vpc)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating VPC",
			"Could not create VPC: "+err.Error(),
		)
		return
	}

	if err := r.waitForVPCReady(ctx, vpc.ID, 5*time.Minute); err != nil {
		resp.Diagnostics.AddError(
			"Error waiting for VPC",
			"VPC created but not ready: "+err.Error(),
		)
		return
	}

	if err := r.client.Do(ctx, "GET", "/api/ovn/v1/vpcs/"+vpc.ID, nil, nil, &vpc); err != nil {
		resp.Diagnostics.AddError(
			"Error reading VPC after creation",
			err.Error(),
		)
		return
	}

	plan.ID = types.StringValue(vpc.ID)
	plan.Status = types.StringValue(vpc.Status)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VPCResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VPCResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var vpc VPC
	err := r.client.Do(ctx, "GET", "/api/ovn/v1/vpcs/"+state.ID.ValueString(), nil, nil, &vpc)
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.IsNotFound() {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading VPC", err.Error())
		return
	}

	state.Status = types.StringValue(vpc.Status)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *VPCResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update not supported",
		"VPC resources cannot be updated. All changes require resource replacement.",
	)
}

func (r *VPCResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VPCResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.Do(ctx, "DELETE", "/api/ovn/v1/vpcs/"+state.ID.ValueString(), nil, nil, nil)
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.IsNotFound() {
			return
		}
		resp.Diagnostics.AddError("Error deleting VPC", err.Error())
		return
	}
}

func (r *VPCResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *VPCResource) waitForVPCReady(ctx context.Context, vpcID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for VPC to become ACTIVE")
		case <-ticker.C:
			var vpc VPC
			if err := r.client.Do(ctx, "GET", "/api/ovn/v1/vpcs/"+vpcID, nil, nil, &vpc); err != nil {
				return err
			}

			if vpc.Status == "ACTIVE" {
				return nil
			}
			if vpc.Status == "ERROR" {
				return fmt.Errorf("VPC entered ERROR state")
			}
		}
	}
}
