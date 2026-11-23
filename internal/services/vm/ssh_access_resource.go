package vm

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

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

// SSH access grant modes of h3vm.
const (
	SSHModeAll      = "all"
	SSHModeSelected = "selected"
)

var (
	_ resource.Resource                   = &SSHAccessResource{}
	_ resource.ResourceWithConfigure      = &SSHAccessResource{}
	_ resource.ResourceWithImportState    = &SSHAccessResource{}
	_ resource.ResourceWithValidateConfig = &SSHAccessResource{}
)

// NewSSHAccessResource is h3_vm_ssh_access: SSH access of one project member to one VM.
func NewSSHAccessResource() resource.Resource { return &SSHAccessResource{} }

type SSHAccessResource struct{ client *client.Client }

type SSHAccessResourceModel struct {
	ID             types.String `tfsdk:"id"`
	VMID           types.String `tfsdk:"vm_id"`
	UserID         types.String `tfsdk:"user_id"`
	Mode           types.String `tfsdk:"mode"`
	KeyIDs         types.Set    `tfsdk:"key_ids"`
	Active         types.Bool   `tfsdk:"active"`
	InactiveReason types.String `tfsdk:"inactive_reason"`
	GrantedBy      types.String `tfsdk:"granted_by"`
	GrantedAt      types.String `tfsdk:"granted_at"`
}

func sshAccessPath(vmID string) string {
	return "/api/vms/v1/" + url.PathEscape(vmID) + "/ssh-access"
}

func sshGrantPath(vmID, userID string) string {
	return sshAccessPath(vmID) + "/grants/" + url.PathEscape(userID)
}

func (r *SSHAccessResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_ssh_access"
}

func (r *SSHAccessResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "SSH access of one project member to a VM: the member's h3ssh keys may log in as `user`. " +
			"Changes apply live, without a VM restart, on VMs with `ssh_access_managed = true` (qemu-guest-agent in the " +
			"image); the keys reach the VM within about two minutes. Destroying the resource revokes the access, even " +
			"when it was the last key of the VM (console login with a password stays). Members with the read-only role " +
			"(PROJ-VIEWER) cannot be granted access.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`<vm_id>/<user_id>`.",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"vm_id": schema.StringAttribute{
				MarkdownDescription: "VM ID. Changing it creates a new grant.",
				Required:            true,
				PlanModifiers:       replace,
			},
			"user_id": schema.StringAttribute{
				MarkdownDescription: "ID of the project member who gets access. Changing it creates a new grant.",
				Required:            true,
				PlanModifiers:       replace,
			},
			"mode": schema.StringAttribute{
				MarkdownDescription: "`all`: every current and future SSH key of the member (keys the member adds or deletes " +
					"in h3ssh follow automatically). `selected`: only `key_ids`.",
				Required:   true,
				Validators: []validator.String{oneOfValidator{values: []string{SSHModeAll, SSHModeSelected}}},
			},
			"key_ids": schema.SetAttribute{
				MarkdownDescription: "h3ssh key IDs of the member; required with `mode = \"selected\"`, not allowed with `all`. " +
					"A key deleted in h3ssh leaves the VM and this set.",
				ElementType: types.StringType,
				Optional:    true,
			},
			"active": schema.BoolAttribute{
				MarkdownDescription: "`false` when the member's role no longer allows access (the keys are not on the VM).",
				Computed:            true,
			},
			"inactive_reason": schema.StringAttribute{
				MarkdownDescription: "Why the grant is inactive: `NOT_MEMBER` (left the project) or `ROLE_NOT_ALLOWED` " +
					"(read-only role). Empty when active.",
				Computed: true,
			},
			"granted_by": schema.StringAttribute{
				MarkdownDescription: "Who granted the access (user ID).",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"granted_at": schema.StringAttribute{
				MarkdownDescription: "When the access was granted (RFC 3339).",
				Computed:            true,
				PlanModifiers:       keep,
			},
		},
	}
}

func (r *SSHAccessResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *SSHAccessResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg SSHAccessResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Mode.IsUnknown() || cfg.KeyIDs.IsUnknown() {
		return
	}
	switch cfg.Mode.ValueString() {
	case SSHModeAll:
		if !cfg.KeyIDs.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("key_ids"), "key_ids not allowed with mode = \"all\"",
				"mode = \"all\" gives every key of the member; remove key_ids or use mode = \"selected\".")
		}
	case SSHModeSelected:
		if cfg.KeyIDs.IsNull() || len(cfg.KeyIDs.Elements()) == 0 {
			resp.Diagnostics.AddAttributeError(path.Root("key_ids"), "key_ids required with mode = \"selected\"",
				"List at least one h3ssh key of the member, or use mode = \"all\".")
		}
	}
}

func (r *SSHAccessResource) put(ctx context.Context, m *SSHAccessResourceModel) error {
	body := SetSSHGrantRequest{Mode: m.Mode.ValueString()}
	if m.Mode.ValueString() == SSHModeSelected {
		if d := m.KeyIDs.ElementsAs(ctx, &body.KeyIDs, false); d.HasError() {
			return fmt.Errorf("key_ids: %v", d)
		}
		sort.Strings(body.KeyIDs)
	}
	// h3vm answers with the whole ssh-access view of the VM.
	var view SSHAccess
	if err := r.client.Do(ctx, "PUT", sshGrantPath(m.VMID.ValueString(), m.UserID.ValueString()), nil, body, &view); err != nil {
		return err
	}
	g := view.grant(m.UserID.ValueString())
	if g == nil {
		var err error
		if g, err = r.find(ctx, m.VMID.ValueString(), m.UserID.ValueString()); err != nil {
			return err
		}
	}
	if g == nil {
		return fmt.Errorf("grant of user %s on VM %s not found after PUT", m.UserID.ValueString(), m.VMID.ValueString())
	}
	return m.apply(ctx, g)
}

func (r *SSHAccessResource) find(ctx context.Context, vmID, userID string) (*SSHAccessGrant, error) {
	var acc SSHAccess
	if err := r.client.Do(ctx, "GET", sshAccessPath(vmID), nil, nil, &acc); err != nil {
		return nil, err
	}
	return acc.grant(userID), nil
}

func (m *SSHAccessResourceModel) apply(ctx context.Context, g *SSHAccessGrant) error {
	m.ID = types.StringValue(m.VMID.ValueString() + "/" + m.UserID.ValueString())
	m.Mode = types.StringValue(g.Mode)
	if g.Mode == SSHModeSelected {
		set, d := types.SetValueFrom(ctx, types.StringType, g.selectedKeyIDs())
		if d.HasError() {
			return fmt.Errorf("%v", d)
		}
		m.KeyIDs = set
	} else {
		m.KeyIDs = types.SetNull(types.StringType)
	}
	m.Active = types.BoolValue(g.Active == nil || *g.Active)
	m.InactiveReason = types.StringValue(g.InactiveReason)
	m.GrantedBy = types.StringValue(g.GrantedBy)
	m.GrantedAt = types.StringValue(g.GrantedAt)
	return nil
}

func (r *SSHAccessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SSHAccessResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Error granting SSH access", sshAccessErrorDetail(err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *SSHAccessResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SSHAccessResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.find(ctx, state.VMID.ValueString(), state.UserID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading SSH access", err.Error())
		return
	}
	if g == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	if err := state.apply(ctx, g); err != nil {
		resp.Diagnostics.AddError("Error reading SSH access", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *SSHAccessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan SSHAccessResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Error changing SSH access", sshAccessErrorDetail(err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete revokes the grant. allow_empty=true: destroying the resource is an
// explicit revoke even when the VM is left without SSH keys. 404
// (SSH_GRANT_NOT_FOUND, or the VM is gone) means it is already revoked.
func (r *SSHAccessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SSHAccessResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.Do(ctx, "DELETE", sshGrantPath(state.VMID.ValueString(), state.UserID.ValueString()),
		map[string]string{"allow_empty": "true"}, nil, nil)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error revoking SSH access", sshAccessErrorDetail(err))
	}
}

func (r *SSHAccessResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	vmID, userID, ok := strings.Cut(req.ID, "/")
	if !ok || vmID == "" || userID == "" || strings.Contains(userID, "/") {
		resp.Diagnostics.AddError("Invalid import ID", "Expected <vm_id>/<user_id>, got "+req.ID)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vm_id"), vmID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), userID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// sshAccessErrorDetail turns h3vm error codes into a hint.
func sshAccessErrorDetail(err error) string {
	msg := err.Error()
	hints := map[string]string{
		"SSH_GRANTEE_NOT_MEMBER":            "the user is not a member of the VM's project, or has the read-only role (PROJ-VIEWER).",
		"SSH_KEY_NOT_OWNED":                 "a key in key_ids does not belong to user_id.",
		"SSH_KEYS_LIMIT":                    "the VM would have more than 64 SSH keys.",
		"SSH_KEYS_LAST_KEY":                 "the change would leave the VM without SSH keys.",
		"SSH_ACCESS_NOT_MANAGED":            "SSH access management is off for this VM: set ssh_access_managed = true on h3_vm (a running VM restarts).",
		"SSH_ACCESS_DISABLED":               "SSH access management is not enabled on this platform yet.",
		"SSH_ACCESS_DEPENDENCY_UNAVAILABLE": "h3ssh or the project membership service is unavailable; nothing was changed, retry later.",
		"SSH_KEYS_UNSUPPORTED":              "the VM image does not support SSH access management (Windows).",
		"WORKLOAD_RBAC_DENIED":              "the platform cannot write to the VM's project namespace; contact support.",
	}
	for code, hint := range hints {
		if strings.Contains(msg, code) {
			return hint + "\n\n" + msg
		}
	}
	return msg
}

// oneOfValidator checks a string against a fixed list.
type oneOfValidator struct{ values []string }

func (v oneOfValidator) Description(context.Context) string {
	return "one of: " + strings.Join(v.values, ", ")
}

func (v oneOfValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v oneOfValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, s := range v.values {
		if req.ConfigValue.ValueString() == s {
			return
		}
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid value",
		fmt.Sprintf("%q is not %s", req.ConfigValue.ValueString(), v.Description(context.Background())))
}
