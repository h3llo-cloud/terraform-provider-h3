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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &EIPResource{}
	_ resource.ResourceWithConfigure   = &EIPResource{}
	_ resource.ResourceWithImportState = &EIPResource{}
	_ resource.ResourceWithMoveState   = &EIPResource{}
)

var eipTimeout = 3 * time.Minute

func NewEIPResource() resource.Resource {
	return &EIPResource{}
}

// NewOVNEIPResource is the deprecated h3_ovn_eip alias (same schema and behaviour).
func NewOVNEIPResource() resource.Resource {
	return &EIPResource{alias: true}
}

type EIPResource struct {
	client *client.Client
	alias  bool // registered under the deprecated h3_ovn_eip name
}

type EIPResourceModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Description       types.String `tfsdk:"description"`
	ProjectID         types.String `tfsdk:"project_id"`
	NetworkID         types.String `tfsdk:"network_id"`
	GatewayName       types.String `tfsdk:"gateway_name"`
	IPAddress         types.String `tfsdk:"ip_address"`
	VMID              types.String `tfsdk:"vm_id"`
	Status            types.String `tfsdk:"status"`
	Bound             types.Bool   `tfsdk:"bound"`
	BoundResourceType types.String `tfsdk:"bound_resource_type"`
	BoundResourceID   types.String `tfsdk:"bound_resource_id"`
}

func applyBinding(m *EIPResourceModel, eip EIP) {
	if eip.Bound == nil {
		m.Bound = types.BoolNull()
		if eip.ResourceID != "" {
			m.Bound = types.BoolValue(true)
		}
	} else {
		m.Bound = types.BoolValue(*eip.Bound)
	}
	t, id := eip.binding()
	m.BoundResourceType = common.OptString(t)
	m.BoundResourceID = common.OptString(id)
}

// applyAPI maps the API object. network_id is not returned and keeps its
// prior value. vm_id is managed only when configured: an attachment made via
// h3_vm.eip_id is reported in bound_resource_id, not as drift of vm_id.
func (m *EIPResourceModel) applyAPI(eip *EIP) {
	m.ID = types.StringValue(eip.ID)
	m.Name = types.StringValue(eip.Name)
	m.Description = common.OptString(eip.Description)
	if eip.ProjectID != "" {
		m.ProjectID = types.StringValue(eip.ProjectID)
	}
	if m.NetworkID.IsUnknown() {
		m.NetworkID = types.StringNull()
	}
	m.GatewayName = types.StringValue(eip.GatewayName)
	m.IPAddress = types.StringValue(eip.IPAddress)
	m.Status = types.StringValue(eip.Status)
	applyBinding(m, *eip)
	if !m.VMID.IsNull() {
		_, boundID := eip.binding()
		m.VMID = common.OptString(first(boundID, eip.VMID))
	}
}

func (r *EIPResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = common.RenameEIP.TypeName(req.ProviderTypeName, r.alias)
}

func (r *EIPResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an Elastic IP (public IPv4 address) of the project. `vm_id` attaches it to a VM and is changed in place; " +
			"the address stays while the EIP exists, so a VM can be recreated without changing its public IP. Import by EIP ID.",
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
			"description": schema.StringAttribute{
				MarkdownDescription: "Comment shown in the console next to the address (one line, up to 255 characters, " +
					"no control characters). Changed in place; removing it clears the comment.",
				Optional:   true,
				Validators: []validator.String{common.EIPDescriptionValidator()},
			},
			"project_id": common.ProjectIDAttribute("EIP"),
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
				MarkdownDescription: "Gateway name in the platform",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"ip_address": schema.StringAttribute{
				MarkdownDescription: "Allocated public IP address",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"vm_id": schema.StringAttribute{
				MarkdownDescription: "Attached VM ID (use for attach/detach). Leave unset when the EIP is selected by `h3_vm.eip_id`: " +
					"an attachment made outside this attribute is then not treated as drift (see `bound_resource_id`).",
				Optional: true,
			},
			"bound": schema.BoolAttribute{
				MarkdownDescription: "True when the EIP is attached or reserved by a resource being created; only unbound EIPs can be selected via `h3_vm.eip_id`. Null on backends that do not report it.",
				Computed:            true,
			},
			"bound_resource_type": schema.StringAttribute{
				MarkdownDescription: "Type of the resource the EIP is bound to (`vm`, `db`, `lb`), when bound.",
				Computed:            true,
			},
			"bound_resource_id": schema.StringAttribute{
				MarkdownDescription: "ID of the resource the EIP is bound to, when bound.",
				Computed:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "EIP status (DETACHED, ATTACHED, PENDING, ERROR)",
				Computed:            true,
			},
		},
	}
	common.RenameEIP.Deprecate(&resp.Schema, r.alias)
}

func (r *EIPResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *EIPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan EIPResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var created EIP
	err := r.client.Do(ctx, "POST", "/api/ovn/v1/eips", nil, CreateEIPRequest{
		Name:        plan.Name.ValueString(),
		Description: common.KnownString(plan.Description),
		ProjectID:   plan.ProjectID.ValueString(),
		NetworkID:   common.KnownString(plan.NetworkID),
	}, &created)
	if err != nil {
		resp.Diagnostics.AddError("Error creating EIP", "Could not create EIP: "+err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), created.ID)...)

	got, err := r.waitStatus(ctx, created.ID, "DETACHED", "ATTACHED")
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for EIP", "EIP created but not ready: "+err.Error())
		return
	}
	if vm := common.KnownString(plan.VMID); vm != "" {
		if got, err = r.attach(ctx, created.ID, vm); err != nil {
			resp.Diagnostics.AddError("Error attaching EIP", "EIP created but could not attach to VM: "+err.Error())
			return
		}
	}
	plan.applyAPI(got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *EIPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state EIPResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	eip, err := r.get(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading EIP", err.Error())
		return
	}
	state.applyAPI(eip)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update changes description (PATCH) and attaches/detaches (vm_id); every
// other argument forces replacement.
func (r *EIPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state EIPResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	cur, err := r.get(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Error reading EIP", err.Error())
		return
	}
	if !plan.Description.Equal(state.Description) {
		desc := common.KnownString(plan.Description)
		var upd EIP
		if err := r.client.Do(ctx, "PATCH", "/api/ovn/v1/eips/"+id, nil, UpdateEIPRequest{Description: &desc}, &upd); err != nil {
			resp.Diagnostics.AddError("Error updating EIP description", err.Error())
			return
		}
		cur.Description = upd.Description
	}
	_, boundTo := cur.binding()
	if boundTo == "" {
		boundTo = cur.VMID
	}
	want := common.KnownString(plan.VMID)

	// Act only when vm_id itself changed: a binding made by h3_vm.eip_id
	// (vm_id unset) is not owned by this resource.
	if !plan.VMID.Equal(state.VMID) && boundTo != want {
		if boundTo != "" && cur.Status != "DETACHED" {
			if cur, err = r.detach(ctx, id); err != nil {
				resp.Diagnostics.AddError("Error detaching EIP", err.Error())
				return
			}
		}
		if want != "" {
			if cur, err = r.attach(ctx, id, want); err != nil {
				resp.Diagnostics.AddError("Error attaching EIP", err.Error())
				return
			}
		}
	}
	plan.applyAPI(cur)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *EIPResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state EIPResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if common.KnownString(state.VMID) != "" {
		if _, err := r.detach(ctx, id); err != nil && !client.IsNotFound(err) {
			resp.Diagnostics.AddError("Error detaching EIP before deletion", err.Error())
			return
		}
	}
	if err := r.client.Do(ctx, "DELETE", "/api/ovn/v1/eips/"+id, nil, nil, nil); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting EIP", err.Error())
	}
}

func (r *EIPResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *EIPResource) get(ctx context.Context, id string) (*EIP, error) {
	var e EIP
	if err := r.client.Do(ctx, "GET", "/api/ovn/v1/eips/"+id, nil, nil, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *EIPResource) attach(ctx context.Context, id, vmID string) (*EIP, error) {
	if err := r.client.Do(ctx, "POST", "/api/ovn/v1/eips/attach", nil, AttachEIPRequest{EIPID: id, ResourceID: vmID, ResourceType: "vm"}, nil); err != nil {
		return nil, err
	}
	return r.waitStatus(ctx, id, "ATTACHED")
}

func (r *EIPResource) detach(ctx context.Context, id string) (*EIP, error) {
	if err := r.client.Do(ctx, "POST", "/api/ovn/v1/eips/detach", nil, DetachEIPRequest{EIPID: id}, nil); err != nil {
		return nil, err
	}
	return r.waitStatus(ctx, id, "DETACHED")
}

func (r *EIPResource) waitStatus(ctx context.Context, id string, want ...string) (*EIP, error) {
	var last *EIP
	err := common.WaitFor(ctx, eipTimeout, fmt.Sprintf("EIP %s to become %v", id, want), func(ctx context.Context) (bool, error) {
		e, err := r.get(ctx, id)
		if err != nil {
			return false, err
		}
		last = e
		if e.Status == "ERROR" {
			return false, fmt.Errorf("EIP entered ERROR state")
		}
		for _, w := range want {
			if e.Status == w {
				return true, nil
			}
		}
		return false, nil
	})
	return last, err
}

// MoveState accepts `moved {}` from the deprecated h3_ovn_eip type.
func (r *EIPResource) MoveState(ctx context.Context) []resource.StateMover {
	if r.alias {
		return nil
	}
	old := &EIPResource{alias: true}
	var sr resource.SchemaResponse
	old.Schema(ctx, resource.SchemaRequest{}, &sr)
	return common.RenameEIP.MoveFromOld(sr.Schema)
}

// ModifyPlan fills project_id from the provider default.
func (r *EIPResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
}
