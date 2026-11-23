package sg

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"h3terraform/internal/client"
)

// h3_vm_security_group_attachment: one group on one VM, non-exclusive.
//
// It uses the point operations of h3ovn (MR !14):
//
//	POST   /api/ovn/v1/vm/{id}/security-groups/{sgId}   add, keep the others (idempotent)
//	DELETE /api/ovn/v1/vm/{id}/security-groups/{sgId}   remove one (idempotent)
//
// which write conditionally on the VM revision and answer 409
// REVISION_CONFLICT after their own retries. Where the public API does not
// serve them yet (plain 404/405 from the router), the provider falls back to
// read-modify-write over PUT /vm/{id}/security-groups, serialized per VM
// inside this provider process. That fallback cannot see writers outside
// this apply (console, another state) between its GET and PUT.

var (
	_ resource.Resource                = &AttachmentResource{}
	_ resource.ResourceWithConfigure   = &AttachmentResource{}
	_ resource.ResourceWithImportState = &AttachmentResource{}
)

func NewAttachmentResource() resource.Resource { return &AttachmentResource{} }

type AttachmentResource struct{ client *client.Client }

type AttachmentModel struct {
	ID              types.String `tfsdk:"id"`
	VMID            types.String `tfsdk:"vm_id"`
	SecurityGroupID types.String `tfsdk:"security_group_id"`
}

// vmLocks serializes the read-modify-write fallback per VM.
var vmLocks sync.Map

func lockVM(id string) func() {
	m, _ := vmLocks.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// pointRetry: attempts of a point call answered with REVISION_CONFLICT.
var pointRetry = struct {
	attempts int
	wait     time.Duration
}{5, 2 * time.Second}

func (r *AttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_security_group_attachment"
}

func (r *AttachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Attaches one security group to a VM and leaves the VM's other groups alone (non-exclusive), so " +
			"different configurations or modules can each add their own group. Destroy detaches only this group; detaching the " +
			"last group leaves the VM detached (no groups). At most 5 groups per VM. Applied live, without a VM restart. " +
			"Do not combine with `h3_vm_security_groups` on the same VM. Uses the point attach/detach API of the network service; " +
			"where it is not deployed yet the provider falls back to read-modify-write of the VM's list (safe within one apply, " +
			"not against concurrent changes from the console). Import: `<vm_id>/<security_group_id>`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "`<vm_id>/<security_group_id>`",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"vm_id":             schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "VM ID"},
			"security_group_id": schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Security group ID of the VM's project"},
		},
	}
}

func (r *AttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c, err := configure(req.ProviderData)
	if err != nil {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", err.Error())
		return
	}
	r.client = c
}

func pointPath(vmID, sgID string) string { return vmGroupsPath(vmID) + "/" + sgID }

// routeMissing: the router has no such route (plain-text 404/405), as opposed
// to a JSON NOT_FOUND for a missing VM or group.
func routeMissing(err error) bool {
	he, ok := client.AsHTTPError(err)
	if !ok {
		return false
	}
	if he.StatusCode == 405 {
		return true
	}
	return he.StatusCode == 404 && he.Code() == "" && !strings.Contains(he.Body, "{")
}

// change adds (attach=true) or removes one group.
func (r *AttachmentResource) change(ctx context.Context, vmID, sgID string, attach bool) error {
	method := "DELETE"
	if attach {
		method = "POST"
	}
	var err error
	for i := 0; i < pointRetry.attempts; i++ {
		err = r.client.Do(ctx, method, pointPath(vmID, sgID), nil, nil, nil)
		if !isRevisionConflict(err) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pointRetry.wait):
		}
	}
	if err == nil || !routeMissing(err) {
		return err
	}
	// Fallback: read-modify-write of the whole list.
	unlock := lockVM(vmID)
	defer unlock()
	var cur VMGroups
	if err := r.client.Do(ctx, "GET", vmGroupsPath(vmID), nil, nil, &cur); err != nil {
		return err
	}
	next := make([]string, 0, len(cur.SecurityGroupIDs)+1)
	has := false
	for _, id := range cur.SecurityGroupIDs {
		if id == sgID {
			has = true
			if !attach {
				continue
			}
		}
		next = append(next, id)
	}
	if attach == has {
		return nil // already in the wanted state
	}
	if attach {
		if len(next) >= maxVMGroups {
			return fmt.Errorf("VM %s already has %d security groups (the maximum)", vmID, maxVMGroups)
		}
		next = append(next, sgID)
	}
	return r.client.Do(ctx, "PUT", vmGroupsPath(vmID), nil, SetVMGroupsRequest{SecurityGroupIDs: next}, nil)
}

func (r *AttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vm, sgID := plan.VMID.ValueString(), plan.SecurityGroupID.ValueString()
	if err := r.change(ctx, vm, sgID, true); err != nil {
		resp.Diagnostics.AddError("Error attaching security group", err.Error())
		return
	}
	plan.ID = types.StringValue(vm + "/" + sgID)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *AttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var cur VMGroups
	if err := r.client.Do(ctx, "GET", vmGroupsPath(state.VMID.ValueString()), nil, nil, &cur); err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading VM security groups", err.Error())
		return
	}
	for _, id := range cur.SecurityGroupIDs {
		if id == state.SecurityGroupID.ValueString() {
			state.ID = types.StringValue(state.VMID.ValueString() + "/" + id)
			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx) // detached outside Terraform: plan re-attaches
}

// Update is never called: both arguments force replacement.
func (r *AttachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan AttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *AttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state AttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.change(ctx, state.VMID.ValueString(), state.SecurityGroupID.ValueString(), false)
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Error detaching security group", err.Error())
	}
}

func (r *AttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError("Invalid import ID", "expected <vm_id>/<security_group_id>")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vm_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("security_group_id"), parts[1])...)
}
