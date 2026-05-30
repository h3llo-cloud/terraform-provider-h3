package ssh

import (
	"context"
	"fmt"

	"h3terraform/internal/client"

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

// NewSSHKeyResource создает новый ресурс SSH ключа
func NewSSHKeyResource() resource.Resource {
	return &SSHKeyResource{}
}

// SSHKeyResource - ресурс для управления SSH ключами
type SSHKeyResource struct {
	client *client.Client
}

// SSHKeyResourceModel - модель состояния ресурса
type SSHKeyResourceModel struct {
	ID        types.String `tfsdk:"id"`
	UserID    types.String `tfsdk:"user_id"`
	Name      types.String `tfsdk:"name"`
	PublicKey types.String `tfsdk:"public_key"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

// Metadata возвращает метаданные ресурса
func (r *SSHKeyResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh_key"
}

// Schema определяет схему ресурса
func (r *SSHKeyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages H3 Cloud SSH key",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "SSH key ID (UUID)",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"user_id": schema.StringAttribute{
				MarkdownDescription: "User ID (UUID)",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "SSH key name (1-255 chars)",
				Required:            true,
			},
			"public_key": schema.StringAttribute{
				MarkdownDescription: "SSH public key content",
				Required:            true,
				Sensitive:           true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "Creation timestamp",
				Computed:            true,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "Last update timestamp",
				Computed:            true,
			},
		},
	}
}

// Configure инициализирует ресурс с клиентом
func (r *SSHKeyResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create создает новый SSH ключ
func (r *SSHKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SSHKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Формируем запрос
	createReq := CreateSSHKeyRequest{
		UserID:    plan.UserID.ValueString(),
		Name:      plan.Name.ValueString(),
		PublicKey: plan.PublicKey.ValueString(),
	}

	// Вызываем API (с HMAC подписью автоматически!)
	var sshKey SSHKey
	err := r.client.Do(ctx, "POST", "/api/ssh/v1/keys", nil, createReq, &sshKey)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating SSH key",
			"Could not create SSH key: "+err.Error(),
		)
		return
	}

	// Обновляем state
	plan.ID = types.StringValue(sshKey.ID)
	plan.CreatedAt = types.StringValue(sshKey.CreatedAt)
	plan.UpdatedAt = types.StringValue(sshKey.UpdatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read читает текущее состояние SSH ключа
func (r *SSHKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SSHKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var sshKey SSHKey
	err := r.client.Do(ctx, "GET", "/api/ssh/v1/keys/"+state.ID.ValueString(), nil, nil, &sshKey)
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.IsNotFound() {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading SSH key", err.Error())
		return
	}

	// Обновляем state из API ответа
	state.Name = types.StringValue(sshKey.Name)
	state.PublicKey = types.StringValue(sshKey.PublicKey)
	state.CreatedAt = types.StringValue(sshKey.CreatedAt)
	state.UpdatedAt = types.StringValue(sshKey.UpdatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update обновляет SSH ключ (только имя может быть обновлено)
func (r *SSHKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan SSHKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state SSHKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Формируем запрос обновления
	updateReq := UpdateSSHKeyRequest{}

	// Проверяем, изменилось ли имя
	if !plan.Name.Equal(state.Name) {
		name := plan.Name.ValueString()
		updateReq.Name = &name
	}

	// Если ничего не изменилось, возвращаем ошибку
	if updateReq.Name == nil {
		resp.Diagnostics.AddError(
			"Update not supported for these changes",
			"Only name can be updated in-place. Public key changes require resource replacement.",
		)
		return
	}

	// Вызываем API для обновления
	path := "/api/ssh/v1/keys/" + state.ID.ValueString()
	err := r.client.Do(ctx, "PATCH", path, nil, updateReq, nil)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error updating SSH key",
			"Could not update SSH key: "+err.Error(),
		)
		return
	}

	// Читаем финальное состояние
	var sshKey SSHKey
	if err := r.client.Do(ctx, "GET", "/api/ssh/v1/keys/"+state.ID.ValueString(), nil, nil, &sshKey); err != nil {
		resp.Diagnostics.AddError(
			"Error reading SSH key after update",
			err.Error(),
		)
		return
	}

	// Обновляем state
	plan.ID = types.StringValue(sshKey.ID)
	plan.UserID = state.UserID
	plan.Name = types.StringValue(sshKey.Name)
	plan.PublicKey = types.StringValue(sshKey.PublicKey)
	plan.CreatedAt = types.StringValue(sshKey.CreatedAt)
	plan.UpdatedAt = types.StringValue(sshKey.UpdatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete удаляет SSH ключ
func (r *SSHKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SSHKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.Do(ctx, "DELETE", "/api/ssh/v1/keys/"+state.ID.ValueString(), nil, nil, nil)
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.IsNotFound() {
			// Уже удален - OK
			return
		}
		resp.Diagnostics.AddError("Error deleting SSH key", err.Error())
		return
	}
}

// ImportState импортирует существующий SSH ключ
func (r *SSHKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
