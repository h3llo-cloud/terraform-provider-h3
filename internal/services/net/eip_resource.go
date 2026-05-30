package net

import (
	"context"
	"fmt"
	"time"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &EIPResource{}
	_ resource.ResourceWithConfigure   = &EIPResource{}
	_ resource.ResourceWithImportState = &EIPResource{}
)

func NewEIPResource() resource.Resource {
	return &EIPResource{}
}

type EIPResource struct {
	client *client.Client
}

type EIPResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	ProjectID   types.String `tfsdk:"project_id"`
	NetworkID   types.String `tfsdk:"network_id"`
	GatewayName types.String `tfsdk:"gateway_name"`
	IPAddress   types.String `tfsdk:"ip_address"`
	VMID        types.String `tfsdk:"vm_id"`
	Status      types.String `tfsdk:"status"`
}

func (r *EIPResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ovn_eip"
}

func (r *EIPResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages H3 Cloud OVN Elastic IP",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "EIP ID",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "EIP name",
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
			"network_id": schema.StringAttribute{
				MarkdownDescription: "Network ID (subnet ID)",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"gateway_name": schema.StringAttribute{
				MarkdownDescription: "Gateway name in Kubernetes",
				Computed:            true,
			},
			"ip_address": schema.StringAttribute{
				MarkdownDescription: "Allocated IP address",
				Computed:            true,
			},
			"vm_id": schema.StringAttribute{
				MarkdownDescription: "Attached VM ID (use for attach/detach)",
				Optional:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "EIP status (DETACHED, ATTACHED, PENDING, ERROR)",
				Computed:            true,
			},
		},
	}
}

func (r *EIPResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *EIPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan EIPResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := CreateEIPRequest{
		Name:      plan.Name.ValueString(),
		ProjectID: plan.ProjectID.ValueString(),
	}

	if !plan.NetworkID.IsNull() {
		createReq.NetworkID = plan.NetworkID.ValueString()
	}

	var eip EIP
	err := r.client.Do(ctx, "POST", "/api/ovn/v1/eips", nil, createReq, &eip)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating EIP",
			"Could not create EIP: "+err.Error(),
		)
		return
	}

	if err := r.waitForEIPReady(ctx, eip.ID, 3*time.Minute); err != nil {
		resp.Diagnostics.AddError(
			"Error waiting for EIP",
			"EIP created but not ready: "+err.Error(),
		)
		return
	}

	if err := r.client.Do(ctx, "GET", "/api/ovn/v1/eips/"+eip.ID, nil, nil, &eip); err != nil {
		resp.Diagnostics.AddError(
			"Error reading EIP after creation",
			err.Error(),
		)
		return
	}

	plan.ID = types.StringValue(eip.ID)
	plan.GatewayName = types.StringValue(eip.GatewayName)
	plan.IPAddress = types.StringValue(eip.IPAddress)
	plan.Status = types.StringValue(eip.Status)

	if !plan.VMID.IsNull() && plan.VMID.ValueString() != "" {
		attachReq := AttachEIPRequest{
			EIPID:        eip.ID,
			ResourceID:   plan.VMID.ValueString(),
			ResourceType: "vm",
		}

		err := r.client.Do(ctx, "POST", "/api/ovn/v1/eips/attach", nil, attachReq, nil)
		if err != nil {
			resp.Diagnostics.AddError(
				"Error attaching EIP",
				"EIP created but could not attach to VM: "+err.Error(),
			)
			return
		}

		if err := r.waitForEIPAttached(ctx, eip.ID, 3*time.Minute); err != nil {
			resp.Diagnostics.AddError(
				"Error waiting for EIP attachment",
				err.Error(),
			)
			return
		}

		if err := r.client.Do(ctx, "GET", "/api/ovn/v1/eips/"+eip.ID, nil, nil, &eip); err != nil {
			resp.Diagnostics.AddError(
				"Error reading EIP after attachment",
				err.Error(),
			)
			return
		}

		plan.Status = types.StringValue(eip.Status)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *EIPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state EIPResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var eip EIP
	err := r.client.Do(ctx, "GET", "/api/ovn/v1/eips/"+state.ID.ValueString(), nil, nil, &eip)
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.IsNotFound() {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading EIP", err.Error())
		return
	}

	state.Status = types.StringValue(eip.Status)
	state.IPAddress = types.StringValue(eip.IPAddress)
	state.GatewayName = types.StringValue(eip.GatewayName)

	if eip.VMID != "" {
		state.VMID = types.StringValue(eip.VMID)
	} else {
		state.VMID = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *EIPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan EIPResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state EIPResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	planVMID := plan.VMID.ValueString()
	stateVMID := state.VMID.ValueString()

	if planVMID != stateVMID {
		if stateVMID != "" {
			detachReq := DetachEIPRequest{
				EIPID: state.ID.ValueString(),
			}

			err := r.client.Do(ctx, "POST", "/api/ovn/v1/eips/detach", nil, detachReq, nil)
			if err != nil {
				resp.Diagnostics.AddError(
					"Error detaching EIP",
					err.Error(),
				)
				return
			}

			if err := r.waitForEIPDetached(ctx, state.ID.ValueString(), 3*time.Minute); err != nil {
				resp.Diagnostics.AddError(
					"Error waiting for EIP detachment",
					err.Error(),
				)
				return
			}
		}

		if planVMID != "" {
			attachReq := AttachEIPRequest{
				EIPID:        state.ID.ValueString(),
				ResourceID:   planVMID,
				ResourceType: "vm",
			}

			err := r.client.Do(ctx, "POST", "/api/ovn/v1/eips/attach", nil, attachReq, nil)
			if err != nil {
				resp.Diagnostics.AddError(
					"Error attaching EIP",
					err.Error(),
				)
				return
			}

			if err := r.waitForEIPAttached(ctx, state.ID.ValueString(), 3*time.Minute); err != nil {
				resp.Diagnostics.AddError(
					"Error waiting for EIP attachment",
					err.Error(),
				)
				return
			}
		}
	}

	var eip EIP
	if err := r.client.Do(ctx, "GET", "/api/ovn/v1/eips/"+state.ID.ValueString(), nil, nil, &eip); err != nil {
		resp.Diagnostics.AddError(
			"Error reading EIP after update",
			err.Error(),
		)
		return
	}

	plan.ID = state.ID
	plan.Name = state.Name
	plan.ProjectID = state.ProjectID
	plan.NetworkID = state.NetworkID
	plan.GatewayName = types.StringValue(eip.GatewayName)
	plan.IPAddress = types.StringValue(eip.IPAddress)
	plan.Status = types.StringValue(eip.Status)

	if eip.VMID != "" {
		plan.VMID = types.StringValue(eip.VMID)
	} else {
		plan.VMID = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *EIPResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state EIPResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !state.VMID.IsNull() && state.VMID.ValueString() != "" {
		detachReq := DetachEIPRequest{
			EIPID: state.ID.ValueString(),
		}

		err := r.client.Do(ctx, "POST", "/api/ovn/v1/eips/detach", nil, detachReq, nil)
		if err != nil {
			if httpErr, ok := err.(*client.HTTPError); !ok || !httpErr.IsNotFound() {
				resp.Diagnostics.AddError(
					"Error detaching EIP before deletion",
					err.Error(),
				)
				return
			}
		} else {
			if err := r.waitForEIPDetached(ctx, state.ID.ValueString(), 3*time.Minute); err != nil {
				resp.Diagnostics.AddError(
					"Error waiting for EIP detachment before deletion",
					err.Error(),
				)
				return
			}
		}
	}

	err := r.client.Do(ctx, "DELETE", "/api/ovn/v1/eips/"+state.ID.ValueString(), nil, nil, nil)
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.IsNotFound() {
			return
		}
		resp.Diagnostics.AddError("Error deleting EIP", err.Error())
		return
	}
}

func (r *EIPResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *EIPResource) waitForEIPReady(ctx context.Context, eipID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for EIP to become ready")
		case <-ticker.C:
			var eip EIP
			if err := r.client.Do(ctx, "GET", "/api/ovn/v1/eips/"+eipID, nil, nil, &eip); err != nil {
				return err
			}

			if eip.Status == "DETACHED" || eip.Status == "ATTACHED" {
				return nil
			}
			if eip.Status == "ERROR" {
				return fmt.Errorf("EIP entered ERROR state")
			}
		}
	}
}

func (r *EIPResource) waitForEIPAttached(ctx context.Context, eipID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for EIP to become ATTACHED")
		case <-ticker.C:
			var eip EIP
			if err := r.client.Do(ctx, "GET", "/api/ovn/v1/eips/"+eipID, nil, nil, &eip); err != nil {
				return err
			}

			if eip.Status == "ATTACHED" {
				return nil
			}
			if eip.Status == "ERROR" {
				return fmt.Errorf("EIP entered ERROR state")
			}
		}
	}
}

func (r *EIPResource) waitForEIPDetached(ctx context.Context, eipID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for EIP to become DETACHED")
		case <-ticker.C:
			var eip EIP
			if err := r.client.Do(ctx, "GET", "/api/ovn/v1/eips/"+eipID, nil, nil, &eip); err != nil {
				return err
			}

			if eip.Status == "DETACHED" {
				return nil
			}
			if eip.Status == "ERROR" {
				return fmt.Errorf("EIP entered ERROR state")
			}
		}
	}
}
