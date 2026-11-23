package net

import (
	"context"
	"fmt"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

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
	_ resource.ResourceWithMoveState   = &NetworkResource{}
)

var networkReadyTimeout = 5 * time.Minute

func NewNetworkResource() resource.Resource {
	return &NetworkResource{}
}

// NewOVNNetworkResource is the deprecated h3_ovn_network alias (same schema and behaviour).
func NewOVNNetworkResource() resource.Resource {
	return &NetworkResource{alias: true}
}

type NetworkResource struct {
	client *client.Client
	alias  bool // registered under the deprecated h3_ovn_network name
}

type NetworkResourceModel struct {
	ID              types.String `tfsdk:"id"`
	SubnetID        types.String `tfsdk:"subnet_id"`
	SubnetName      types.String `tfsdk:"subnet_name"`
	GatewayID       types.String `tfsdk:"gateway_id"`
	GatewayName     types.String `tfsdk:"gateway_name"`
	GatewayIP       types.String `tfsdk:"gateway_ip"`
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
	resp.TypeName = common.RenameNetwork.TypeName(req.ProviderTypeName, r.alias)
}

func (r *NetworkResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keepOrReplace := []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a subnet with its NAT gateway in the project VPC. Every argument change replaces the network " +
			"(the API has no update). Place a VM in it with `h3_vm.subnet_name = h3_network.<name>.subnet_name`. " +
			"`external_subnets` is not returned by the API (not checked for drift, empty after import).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Network (subnet) ID; same as `subnet_id`",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"subnet_id": schema.StringAttribute{
				MarkdownDescription: "Subnet ID",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"subnet_name": schema.StringAttribute{
				MarkdownDescription: "Subnet name in the platform; pass it to `h3_vm.subnet_name`",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"gateway_id": schema.StringAttribute{
				MarkdownDescription: "NAT gateway ID",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"gateway_name": schema.StringAttribute{
				MarkdownDescription: "NAT gateway name in the platform",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"gateway_ip": schema.StringAttribute{
				MarkdownDescription: "Gateway IP address in the subnet",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Network name",
				Required:            true,
				PlanModifiers:       replace,
			},
			"project_id": common.ProjectIDAttribute("network"),
			"vpc_id": schema.StringAttribute{
				MarkdownDescription: "VPC ID (`h3_vpc.<name>.id`); the project VPC when omitted",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       keepOrReplace,
			},
			"vpc_name": schema.StringAttribute{
				MarkdownDescription: "VPC name in the platform",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"cidr_block": schema.StringAttribute{
				MarkdownDescription: "CIDR block (e.g. `10.1.0.0/24`)",
				Required:            true,
				PlanModifiers:       replace,
			},
			"protocol": schema.StringAttribute{
				MarkdownDescription: "IP protocol: `IPv4` (default), `IPv6` or `Dual`",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       keepOrReplace,
			},
			"external_subnets": schema.ListAttribute{
				MarkdownDescription: "External subnets of the NAT gateway (optional)",
				ElementType:         types.StringType,
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
					listplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Network status (`CREATING`, `ACTIVE`)",
				Computed:            true,
				PlanModifiers:       keep,
			},
		},
	}
	common.RenameNetwork.Deprecate(&resp.Schema, r.alias)
}

func (r *NetworkResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
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
		VPCID:     common.KnownString(plan.VPCID),
		Protocol:  common.KnownString(plan.Protocol),
	}
	// external_subnets is Optional+Computed: unknown (not null) when not configured.
	if !plan.ExternalSubnets.IsNull() && !plan.ExternalSubnets.IsUnknown() {
		resp.Diagnostics.Append(plan.ExternalSubnets.ElementsAs(ctx, &createReq.ExternalSubnets, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	} else {
		plan.ExternalSubnets = types.ListNull(types.StringType)
	}

	var created Network
	if err := r.client.Do(ctx, "POST", "/api/ovn/v1/networks", nil, createReq, &created); err != nil {
		resp.Diagnostics.AddError("Error creating network", "Could not create network: "+err.Error())
		return
	}
	if created.SubnetID == "" {
		resp.Diagnostics.AddError("Error creating network", "the API response has no network id")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), created.SubnetID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("subnet_id"), created.SubnetID)...)

	got, err := r.waitReady(ctx, created.SubnetID)
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for network", "Network created but not ready: "+err.Error())
		return
	}
	plan.applyAPI(got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *NetworkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state NetworkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.SubnetID.ValueString()
	if id == "" {
		id = state.ID.ValueString()
	}
	n, err := r.get(ctx, id)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading network", err.Error())
		return
	}
	state.applyAPI(n)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called with a change: every argument forces replacement.
func (r *NetworkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan NetworkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *NetworkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state NetworkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Do(ctx, "DELETE", "/api/ovn/v1/networks/"+state.SubnetID.ValueString(), nil, nil, nil); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting network", err.Error())
	}
}

// ImportState by network (subnet) id.
func (r *NetworkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("subnet_id"), req.ID)...)
}

func (m *NetworkResourceModel) applyAPI(n *Network) {
	m.ID = types.StringValue(n.SubnetID)
	m.SubnetID = types.StringValue(n.SubnetID)
	if n.Name != "" {
		m.Name = types.StringValue(n.Name)
	}
	if n.ProjectID != "" {
		m.ProjectID = types.StringValue(n.ProjectID)
	}
	m.SubnetName = types.StringValue(n.SubnetName)
	m.GatewayID = types.StringValue(n.GatewayID)
	m.GatewayName = types.StringValue(n.GatewayName)
	m.GatewayIP = common.OptString(n.GatewayIP)
	m.VPCID = types.StringValue(n.VPCID)
	m.VPCName = types.StringValue(n.VPCName)
	if n.CIDRBlock != "" {
		m.CIDRBlock = types.StringValue(n.CIDRBlock)
	}
	if n.Protocol != "" {
		m.Protocol = types.StringValue(n.Protocol)
	} else if m.Protocol.IsUnknown() {
		m.Protocol = types.StringNull()
	}
	if m.ExternalSubnets.IsUnknown() {
		m.ExternalSubnets = types.ListNull(types.StringType)
	}
	m.Status = types.StringValue(n.Status)
}

func (r *NetworkResource) get(ctx context.Context, id string) (*Network, error) {
	var n Network
	if err := r.client.Do(ctx, "GET", "/api/ovn/v1/networks/"+id, nil, nil, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

func (r *NetworkResource) waitReady(ctx context.Context, id string) (*Network, error) {
	var last *Network
	err := common.WaitFor(ctx, networkReadyTimeout, "network "+id+" to become ACTIVE", func(ctx context.Context) (bool, error) {
		n, err := r.get(ctx, id)
		if err != nil {
			return false, err
		}
		last = n
		switch n.Status {
		case "ACTIVE":
			return true, nil
		case "ERROR", "FAILED":
			return false, fmt.Errorf("network entered %s state", n.Status)
		}
		return false, nil
	})
	return last, err
}

// MoveState accepts `moved {}` from the deprecated h3_ovn_network type.
func (r *NetworkResource) MoveState(ctx context.Context) []resource.StateMover {
	if r.alias {
		return nil
	}
	old := &NetworkResource{alias: true}
	var sr resource.SchemaResponse
	old.Schema(ctx, resource.SchemaRequest{}, &sr)
	return common.RenameNetwork.MoveFromOld(sr.Schema)
}

// ModifyPlan fills project_id from the provider default.
func (r *NetworkResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
}
