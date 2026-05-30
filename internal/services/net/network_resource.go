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
	_ resource.Resource                = &NetworkResource{}
	_ resource.ResourceWithConfigure   = &NetworkResource{}
	_ resource.ResourceWithImportState = &NetworkResource{}
)

func NewNetworkResource() resource.Resource {
	return &NetworkResource{}
}

type NetworkResource struct {
	client *client.Client
}

type NetworkResourceModel struct {
	SubnetID        types.String `tfsdk:"subnet_id"`
	SubnetName      types.String `tfsdk:"subnet_name"`
	GatewayID       types.String `tfsdk:"gateway_id"`
	GatewayName     types.String `tfsdk:"gateway_name"`
	Name            types.String `tfsdk:"name"`
	ProjectID       types.String `tfsdk:"project_id"`
	VPCID           types.String `tfsdk:"vpc_id"`
	VPCName         types.String `tfsdk:"vpc_name"`
	CIDRBlock       types.String `tfsdk:"cidr_block"`
	Protocol        types.String `tfsdk:"protocol"`
	ExternalSubnets types.List   `tfsdk:"external_subnets"`
	Status          types.String `tfsdk:"status"`
}

func (r *NetworkResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ovn_network"
}

func (r *NetworkResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages H3 Cloud OVN Network (Subnet + Gateway)",
		Attributes: map[string]schema.Attribute{
			"subnet_id": schema.StringAttribute{
				MarkdownDescription: "Subnet ID",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"subnet_name": schema.StringAttribute{
				MarkdownDescription: "Subnet Kubernetes name",
				Computed:            true,
			},
			"gateway_id": schema.StringAttribute{
				MarkdownDescription: "Gateway ID",
				Computed:            true,
			},
			"gateway_name": schema.StringAttribute{
				MarkdownDescription: "Gateway Kubernetes name",
				Computed:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Network name",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Project ID (UUID)",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_id": schema.StringAttribute{
				MarkdownDescription: "VPC ID (if empty, VPC will be auto-created)",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"vpc_name": schema.StringAttribute{
				MarkdownDescription: "VPC Kubernetes name",
				Computed:            true,
			},
			"cidr_block": schema.StringAttribute{
				MarkdownDescription: "CIDR block (e.g., 10.1.0.0/24)",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"protocol": schema.StringAttribute{
				MarkdownDescription: "IP protocol (IPv4, IPv6, Dual)",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"external_subnets": schema.ListAttribute{
				MarkdownDescription: "External subnets for NAT gateway",
				ElementType:         types.StringType,
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Network status",
				Computed:            true,
			},
		},
	}
}

func (r *NetworkResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *NetworkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan NetworkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := CreateNetworkRequest{
		Name:      plan.Name.ValueString(),
		ProjectID: plan.ProjectID.ValueString(),
		CIDRBlock: plan.CIDRBlock.ValueString(),
	}

	if !plan.VPCID.IsNull() {
		createReq.VPCID = plan.VPCID.ValueString()
	}

	if !plan.Protocol.IsNull() {
		createReq.Protocol = plan.Protocol.ValueString()
	}

	if !plan.ExternalSubnets.IsNull() {
		var externalSubnets []string
		resp.Diagnostics.Append(plan.ExternalSubnets.ElementsAs(ctx, &externalSubnets, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		createReq.ExternalSubnets = externalSubnets
	}

	var network Network
	err := r.client.Do(ctx, "POST", "/api/ovn/v1/networks", nil, createReq, &network)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating Network",
			"Could not create Network: "+err.Error(),
		)
		return
	}

	if err := r.waitForNetworkReady(ctx, network.SubnetID, 5*time.Minute); err != nil {
		resp.Diagnostics.AddError(
			"Error waiting for Network",
			"Network created but not ready: "+err.Error(),
		)
		return
	}

	if err := r.client.Do(ctx, "GET", "/api/ovn/v1/networks/"+network.SubnetID, nil, nil, &network); err != nil {
		resp.Diagnostics.AddError(
			"Error reading Network after creation",
			err.Error(),
		)
		return
	}

	plan.SubnetID = types.StringValue(network.SubnetID)
	plan.SubnetName = types.StringValue(network.SubnetName)
	plan.GatewayID = types.StringValue(network.GatewayID)
	plan.GatewayName = types.StringValue(network.GatewayName)
	plan.VPCID = types.StringValue(network.VPCID)
	plan.VPCName = types.StringValue(network.VPCName)
	plan.Status = types.StringValue(network.Status)

	if plan.Protocol.IsNull() {
		plan.Protocol = types.StringValue(network.Protocol)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *NetworkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state NetworkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var network Network
	err := r.client.Do(ctx, "GET", "/api/ovn/v1/networks/"+state.SubnetID.ValueString(), nil, nil, &network)
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.IsNotFound() {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading Network", err.Error())
		return
	}

	state.Status = types.StringValue(network.Status)
	state.GatewayID = types.StringValue(network.GatewayID)
	state.GatewayName = types.StringValue(network.GatewayName)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *NetworkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update not supported",
		"Network resources cannot be updated. All changes require resource replacement.",
	)
}

func (r *NetworkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state NetworkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.Do(ctx, "DELETE", "/api/ovn/v1/networks/"+state.SubnetID.ValueString(), nil, nil, nil)
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.IsNotFound() {
			return
		}
		resp.Diagnostics.AddError("Error deleting Network", err.Error())
		return
	}
}

func (r *NetworkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("subnet_id"), req, resp)
}

func (r *NetworkResource) waitForNetworkReady(ctx context.Context, subnetID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for Network to become ACTIVE")
		case <-ticker.C:
			var network Network
			if err := r.client.Do(ctx, "GET", "/api/ovn/v1/networks/"+subnetID, nil, nil, &network); err != nil {
				return err
			}

			if network.Status == "ACTIVE" {
				return nil
			}
			if network.Status == "ERROR" {
				return fmt.Errorf("Network entered ERROR state")
			}
		}
	}
}
