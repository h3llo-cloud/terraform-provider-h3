package iam

import (
	"context"
	"fmt"
	"strings"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.ResourceWithConfigure      = &KeyResource{}
	_ resource.ResourceWithImportState    = &KeyResource{}
	_ resource.ResourceWithModifyPlan     = &KeyResource{}
	_ resource.ResourceWithValidateConfig = &KeyResource{}
)

func NewKeyResource() resource.Resource { return &KeyResource{} }

type KeyResource struct{ client *client.Client }

type KeyModel struct {
	ID               types.String `tfsdk:"id"`
	ProjectID        types.String `tfsdk:"project_id"`
	ServiceAccountID types.String `tfsdk:"service_account_id"`
	Name             types.String `tfsdk:"name"`
	Description      types.String `tfsdk:"description"`
	Scopes           types.Set    `tfsdk:"scopes"`
	ExpiresAt        types.String `tfsdk:"expires_at"`
	SecretKey        types.String `tfsdk:"secret_key"`
	Enabled          types.Bool   `tfsdk:"enabled"`
	Kind             types.String `tfsdk:"kind"`
	CreatedAt        types.String `tfsdk:"created_at"`
	LastUsedAt       types.String `tfsdk:"last_used_at"`
}

func (r *KeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_account_key"
}

func (r *KeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Статический API-ключ сервисного аккаунта (HMAC: `key_id` + `secret_key`). " +
			"Секрет API отдаёт один раз, при создании; провайдер хранит его в состоянии как чувствительное значение. " +
			"После импорта секрета нет. Ключ не меняется на месте: любое изменение аргументов выпускает новый ключ, " +
			"старый удаляется. Для плановой ротации используйте `replace_triggered_by` или `terraform apply -replace`. " +
			AccessNote,
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "ID ключа (`key_id`, UUID)"},
			"project_id": projectIDAttribute("ключ"),
			"service_account_id": schema.StringAttribute{Required: true, PlanModifiers: replace,
				MarkdownDescription: "ID сервисного аккаунта (`h3_service_account.<имя>.id`)"},
			"name": schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Имя ключа"},
			"description": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""),
				PlanModifiers: replace, MarkdownDescription: "Описание"},
			"scopes": schema.SetAttribute{ElementType: types.StringType, Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown(), setplanmodifier.RequiresReplace()},
				MarkdownDescription: "Что разрешено ключу: `<сервис>:read|write|delete|*`, например `vms:read`, `s3:write`, " +
					"`dns:*`. Итоговые права — пересечение scope и роли аккаунта. Ключ без scope пока действует со всеми правами роли, " +
					"но лучше задавать scope явно: платформа переходит на обязательные scope."},
			"expires_at": schema.StringAttribute{Optional: true, Computed: true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "Когда ключ перестанет работать (RFC 3339). По умолчанию через 90 дней, не дальше 366 дней."},
			"secret_key": schema.StringAttribute{Computed: true, Sensitive: true, PlanModifiers: keep,
				MarkdownDescription: "Секрет ключа. Есть только у ключа, созданного этим Terraform; после импорта пусто."},
			"enabled": schema.BoolAttribute{Computed: true,
				MarkdownDescription: "Работает ли ключ. Выключение аккаунта выключает все его ключи"},
			"kind":         schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "`static` или `session`"},
			"created_at":   schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "Когда выпущен (RFC 3339)"},
			"last_used_at": schema.StringAttribute{Computed: true, MarkdownDescription: "Когда ключ последний раз использовали (RFC 3339)"},
		},
	}
}

func (r *KeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *KeyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg KeyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if v := common.KnownString(cfg.ExpiresAt); v != "" {
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("expires_at"), "Неверная дата",
				fmt.Sprintf("%q: нужен формат RFC 3339, например 2027-01-31T00:00:00Z", v))
		}
	}
	if !cfg.Scopes.IsNull() && !cfg.Scopes.IsUnknown() {
		var scopes []string
		resp.Diagnostics.Append(cfg.Scopes.ElementsAs(ctx, &scopes, false)...)
		for _, s := range scopes {
			if !validScope(s) {
				resp.Diagnostics.AddAttributeError(path.Root("scopes"), "Неверный scope",
					fmt.Sprintf("%q: нужен \"*\" или \"<сервис>:read|write|delete|*\"", s))
			}
		}
	}
}

// validScope checks the grammar only; the list of services is h3api-key's.
func validScope(s string) bool {
	if s == "*" {
		return true
	}
	svc, level, ok := strings.Cut(s, ":")
	if !ok || svc == "" {
		return false
	}
	switch level {
	case "read", "write", "delete", "*":
		return true
	}
	return false
}

func (r *KeyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
}

func rfc3339(t *time.Time) types.String {
	if t == nil {
		return types.StringNull()
	}
	return types.StringValue(t.UTC().Format(time.RFC3339))
}

// sameInstant keeps the configured spelling of a time the API returns in UTC.
func sameInstant(prior types.String, api *time.Time) types.String {
	if api == nil {
		return types.StringNull()
	}
	if p, err := time.Parse(time.RFC3339, prior.ValueString()); err == nil && p.Equal(*api) {
		return prior
	}
	return rfc3339(api)
}

func (m *KeyModel) fromAPI(ctx context.Context, k *Key) {
	m.ID = types.StringValue(k.KeyID)
	m.Name = types.StringValue(k.Name)
	m.Description = types.StringValue(k.Description)
	scopes := k.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	m.Scopes, _ = types.SetValueFrom(ctx, types.StringType, scopes)
	m.ExpiresAt = sameInstant(m.ExpiresAt, k.ExpiresAt)
	m.Enabled = types.BoolValue(k.Enabled)
	m.Kind = types.StringValue(k.Kind)
	m.CreatedAt = types.StringValue(k.CreatedAt.UTC().Format(time.RFC3339))
	m.LastUsedAt = rfc3339(k.LastUsedAt)
	if m.SecretKey.IsUnknown() {
		m.SecretKey = types.StringNull()
	}
}

func (r *KeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan KeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := createKeyRequest{Name: plan.Name.ValueString(), Description: plan.Description.ValueString()}
	if !plan.Scopes.IsNull() && !plan.Scopes.IsUnknown() {
		resp.Diagnostics.Append(plan.Scopes.ElementsAs(ctx, &body.Scopes, false)...)
	}
	if v := common.KnownString(plan.ExpiresAt); v != "" {
		body.ExpiresAt = &v
	}
	project, sa := plan.ProjectID.ValueString(), plan.ServiceAccountID.ValueString()
	var key Key
	if err := r.client.Do(ctx, "POST", keysPath(project, sa), nil, body, &key); err != nil {
		if client.IsNotFound(err) && apiAbsent(ctx, r.client, project) {
			resp.Diagnostics.AddError("Ошибка выпуска ключа", errAPIAbsent(project).Error())
			return
		}
		resp.Diagnostics.AddError("Ошибка выпуска ключа", explain(err).Error())
		return
	}
	if key.SecretKey == "" {
		resp.Diagnostics.AddError("Ошибка выпуска ключа", "API не вернул secret_key")
		return
	}
	plan.SecretKey = types.StringValue(key.SecretKey)
	plan.fromAPI(ctx, &key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// findKey returns the key, nil when it (or its service account) is gone.
func (r *KeyResource) findKey(ctx context.Context, project, sa, id string) (*Key, error) {
	keys, err := listKeys(ctx, r.client, project, sa)
	if err != nil {
		if client.IsNotFound(err) {
			if apiAbsent(ctx, r.client, project) {
				return nil, errAPIAbsent(project)
			}
			return nil, nil // the service account is deleted, and its keys with it
		}
		return nil, explain(err)
	}
	for i := range keys {
		if keys[i].KeyID == id {
			return &keys[i], nil
		}
	}
	return nil, nil
}

func (r *KeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state KeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key, err := r.findKey(ctx, state.ProjectID.ValueString(), state.ServiceAccountID.ValueString(), state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Ошибка чтения ключа", err.Error())
		return
	}
	if key == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fromAPI(ctx, key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called with a change: every argument replaces the key.
func (r *KeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan KeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *KeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state KeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := state.ProjectID.ValueString()
	p := keysPath(project, state.ServiceAccountID.ValueString()) + "/" + state.ID.ValueString()
	err := r.client.Do(ctx, "DELETE", p, nil, nil, nil)
	if err == nil {
		return
	}
	if client.IsNotFound(err) {
		if apiAbsent(ctx, r.client, project) {
			resp.Diagnostics.AddError("Ошибка удаления ключа", errAPIAbsent(project).Error())
		}
		return
	}
	resp.Diagnostics.AddError("Ошибка удаления ключа", explain(err).Error())
}

// ImportState: terraform import h3_service_account_key.ci <project_id>/<sa_id>/<key_id>
func (r *KeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError("Неверный ID для импорта",
			fmt.Sprintf("нужен \"<project_id>/<service_account_id>/<key_id>\", получено %q", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("service_account_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
}
