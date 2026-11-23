package ssh

import (
	"context"
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
	_ resource.Resource                = &SSHKeyResource{}
	_ resource.ResourceWithConfigure   = &SSHKeyResource{}
	_ resource.ResourceWithImportState = &SSHKeyResource{}
)

const basePath = "/api/ssh/v1/keys"

func NewSSHKeyResource() resource.Resource {
	return &SSHKeyResource{}
}

type SSHKeyResource struct {
	client *client.Client
}

type SSHKeyResourceModel struct {
	ID        types.String `tfsdk:"id"`
	UserID    types.String `tfsdk:"user_id"`
	Name      types.String `tfsdk:"name"`
	PublicKey types.String `tfsdk:"public_key"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

func (r *SSHKeyResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh_key"
}

func (r *SSHKeyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an SSH public key of the user, to be installed into VMs with `h3_vm.ssh_key_id`. " +
			"The API has no update: changing any argument creates a new key (VMs keep the key they were created with).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "SSH key ID",
				PlanModifiers:       keep,
			},
			"user_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the user who owns the key; must be the owner of the API key",
				PlanModifiers:       replace,
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Key name. Changing it creates a new key.",
				PlanModifiers:       replace,
			},
			"public_key": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Public key in OpenSSH format (`ssh-ed25519 AAAA... comment`). Changing it creates a new key.",
				PlanModifiers:       replace,
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Creation timestamp",
				PlanModifiers:       keep,
			},
			"updated_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Last update timestamp",
				PlanModifiers:       keep,
			},
		},
	}
}

func (r *SSHKeyResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *SSHKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SSHKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var key SSHKey
	err := r.client.Do(ctx, "POST", basePath, nil, CreateSSHKeyRequest{
		UserID:    plan.UserID.ValueString(),
		Name:      plan.Name.ValueString(),
		PublicKey: plan.PublicKey.ValueString(),
	}, &key)
	if err != nil {
		if h, ok := client.AsHTTPError(err); ok && h.StatusCode == 403 {
			resp.Diagnostics.AddError("Error creating SSH key",
				"user_id must be the ID of the user who owns the API key: "+err.Error())
			return
		}
		resp.Diagnostics.AddError("Error creating SSH key", err.Error())
		return
	}
	plan.applyAPI(&key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SSHKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SSHKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var key SSHKey
	if err := r.client.Do(ctx, "GET", basePath+"/"+state.ID.ValueString(), nil, nil, &key); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading SSH key", err.Error())
		return
	}
	state.applyAPI(&key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called with a change: the public API has no PATCH and every
// argument forces a new key.
func (r *SSHKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan SSHKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SSHKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SSHKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Do(ctx, "DELETE", basePath+"/"+state.ID.ValueString(), nil, nil, nil); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting SSH key", err.Error())
	}
}

func (r *SSHKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// applyAPI keeps the configured public key when the API returns it trimmed.
func (m *SSHKeyResourceModel) applyAPI(k *SSHKey) {
	m.ID = types.StringValue(k.ID)
	m.UserID = types.StringValue(k.UserID)
	m.Name = types.StringValue(k.Name)
	if m.PublicKey.IsNull() || m.PublicKey.IsUnknown() || strings.TrimSpace(m.PublicKey.ValueString()) != strings.TrimSpace(k.PublicKey) {
		m.PublicKey = types.StringValue(k.PublicKey)
	}
	m.CreatedAt = types.StringValue(k.CreatedAt)
	m.UpdatedAt = types.StringValue(k.UpdatedAt)
}
