package net

import (
	"context"
	"fmt"
	"strings"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &VPCRouteResource{}
	_ resource.ResourceWithConfigure   = &VPCRouteResource{}
	_ resource.ResourceWithImportState = &VPCRouteResource{}
)

// NewVPCRouteResource is h3_vpc_route: one route of the VPC route table (h3ovn
// GET|POST /api/ovn/v1/vpcs/{id}/routes, DELETE …/routes/{routeId}).
func NewVPCRouteResource() resource.Resource { return &VPCRouteResource{} }

type VPCRouteResource struct{ client *client.Client }

type VPCRouteModel struct {
	ID          types.String `tfsdk:"id"`
	VPCID       types.String `tfsdk:"vpc_id"`
	Destination types.String `tfsdk:"destination"`
	TargetType  types.String `tfsdk:"target_type"`
	NextHopIP   types.String `tfsdk:"next_hop_ip"`
	VMID        types.String `tfsdk:"vm_id"`
	Description types.String `tfsdk:"description"`
	Status      types.String `tfsdk:"status"`
}

// VPCRoute is the h3ovn route (snake_case on the wire).
type VPCRoute struct {
	ID          string `json:"id"`
	Destination string `json:"destination"`
	TargetType  string `json:"target_type"`
	NextHopIP   string `json:"next_hop_ip,omitempty"`
	VMID        string `json:"vm_id,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
}

// VPCRouteTable is GET /vpcs/{id}/routes.
type VPCRouteTable struct {
	Routes []VPCRoute `json:"routes"`
}

func (r *VPCRouteResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc_route"
}

func (r *VPCRouteResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A route of the VPC route table: traffic to `destination` goes to a VM or an address inside the VPC " +
			"(VPN gateway, firewall, NAT appliance) or is dropped (`blackhole`). The default route `0.0.0.0/0` belongs to the " +
			"egress mode of the VPC and cannot be set here. The destination must not overlap the VPC networks or platform " +
			"ranges; at most 50 routes per VPC. Free of charge. Every argument change replaces the route.\n\n" +
			"The VM that receives the traffic must forward it (IP forwarding in the guest) and its security group must allow " +
			"the routed networks.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Route ID",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"vpc_id": schema.StringAttribute{
				MarkdownDescription: "VPC ID (equals the project ID)",
				Required:            true,
				PlanModifiers:       replace,
			},
			"destination": schema.StringAttribute{
				MarkdownDescription: "Destination IPv4 CIDR, e.g. `10.50.0.0/16` (an address means /32)",
				Required:            true,
				PlanModifiers:       replace,
			},
			"target_type": schema.StringAttribute{
				MarkdownDescription: "`vm` (a VM of the project in this VPC, `vm_id`), `ip` (a host address of a VPC network, `next_hop_ip`) or `blackhole`",
				Required:            true,
				PlanModifiers:       replace,
			},
			"next_hop_ip": schema.StringAttribute{
				MarkdownDescription: "Next hop address. Required for `ip`; for `vm` it is the address of the VM, filled by the platform.",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
			},
			"vm_id": schema.StringAttribute{
				MarkdownDescription: "VM ID for `target_type = \"vm\"`",
				Optional:            true,
				PlanModifiers:       replace,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Free text, up to 255 characters",
				Optional:            true,
				PlanModifiers:       replace,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "`active` (in the VPC router) or `pending`",
				Computed:            true,
			},
		},
	}
}

func (r *VPCRouteResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func routesPath(vpcID string) string { return "/api/ovn/v1/vpcs/" + vpcID + "/routes" }

func (r *VPCRouteResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan VPCRouteModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := VPCRoute{
		Destination: plan.Destination.ValueString(),
		TargetType:  plan.TargetType.ValueString(),
		NextHopIP:   common.KnownString(plan.NextHopIP),
		VMID:        common.KnownString(plan.VMID),
		Description: common.KnownString(plan.Description),
	}
	switch body.TargetType {
	case "ip":
		if body.NextHopIP == "" {
			resp.Diagnostics.AddAttributeError(path.Root("next_hop_ip"), "Missing next_hop_ip", "target_type \"ip\" needs next_hop_ip")
			return
		}
	case "vm":
		if body.VMID == "" {
			resp.Diagnostics.AddAttributeError(path.Root("vm_id"), "Missing vm_id", "target_type \"vm\" needs vm_id")
			return
		}
		body.NextHopIP = ""
	case "blackhole":
		if body.NextHopIP != "" || body.VMID != "" {
			resp.Diagnostics.AddError("Invalid blackhole route", "a blackhole route has no next_hop_ip or vm_id")
			return
		}
	default:
		resp.Diagnostics.AddAttributeError(path.Root("target_type"), "Invalid target_type", "target_type must be one of vm, ip, blackhole")
		return
	}
	var created VPCRoute
	if err := r.client.Do(ctx, "POST", routesPath(plan.VPCID.ValueString()), nil, body, &created); err != nil {
		resp.Diagnostics.AddError("Error creating VPC route", "Could not create the route: "+err.Error())
		return
	}
	plan.apply(&created)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VPCRouteResource) find(ctx context.Context, vpcID, id string) (*VPCRoute, error) {
	var t VPCRouteTable
	if err := r.client.Do(ctx, "GET", routesPath(vpcID), nil, nil, &t); err != nil {
		return nil, err
	}
	for i := range t.Routes {
		if t.Routes[i].ID == id {
			return &t.Routes[i], nil
		}
	}
	return nil, nil
}

func (r *VPCRouteResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VPCRouteModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rt, err := r.find(ctx, state.VPCID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading VPC route", err.Error())
		return
	}
	if rt == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.apply(rt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called with a change: every argument forces replacement.
func (r *VPCRouteResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan VPCRouteModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VPCRouteResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VPCRouteModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.Do(ctx, "DELETE", routesPath(state.VPCID.ValueString())+"/"+state.ID.ValueString(), nil, nil, nil)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting VPC route", err.Error())
	}
}

// ImportState takes "<vpc_id>/<route_id>".
func (r *VPCRouteResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	vpcID, id, ok := strings.Cut(req.ID, "/")
	if !ok || vpcID == "" || id == "" {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("expected <vpc_id>/<route_id>, got %q", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpc_id"), vpcID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func (m *VPCRouteModel) apply(rt *VPCRoute) {
	m.ID = types.StringValue(rt.ID)
	m.Destination = types.StringValue(rt.Destination)
	m.TargetType = types.StringValue(rt.TargetType)
	m.NextHopIP = optString(rt.NextHopIP)
	if rt.VMID != "" || !m.VMID.IsNull() {
		m.VMID = optString(rt.VMID)
	}
	if rt.Description != "" || !m.Description.IsNull() {
		m.Description = optString(rt.Description)
	}
	m.Status = types.StringValue(rt.Status)
	if rt.Status == "" {
		m.Status = types.StringValue("active")
	}
}

func optString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
