package iam

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.ResourceWithConfigure      = &ServiceAccountResource{}
	_ resource.ResourceWithImportState    = &ServiceAccountResource{}
	_ resource.ResourceWithModifyPlan     = &ServiceAccountResource{}
	_ resource.ResourceWithValidateConfig = &ServiceAccountResource{}
)

var saNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,61}[a-z0-9]$`)

func NewServiceAccountResource() resource.Resource { return &ServiceAccountResource{} }

type ServiceAccountResource struct{ client *client.Client }

type ServiceAccountModel struct {
	ID          types.String `tfsdk:"id"`
	ProjectID   types.String `tfsdk:"project_id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Role        types.String `tfsdk:"role"`
	Disabled    types.Bool   `tfsdk:"disabled"`
	PrincipalID types.String `tfsdk:"principal_id"`
	CreatedBy   types.String `tfsdk:"created_by"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

func (r *ServiceAccountResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_account"
}

func (r *ServiceAccountResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Сервисный аккаунт проекта: учётная запись для CI, скриптов и приложений. " +
			"Аккаунт входит в проект с ролью `role` и работает по ключам `h3_service_account_key`. " +
			"Удаление аккаунта удаляет и все его ключи. " + AccessNote,
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true, MarkdownDescription: "ID аккаунта, вида `sa-…`", PlanModifiers: keep},
			"project_id": projectIDAttribute("аккаунт"),
			"name": schema.StringAttribute{Required: true,
				MarkdownDescription: "Имя: 3–63 символа, строчные латинские буквы, цифры и дефис; начинается с буквы. " +
					"Имя уникально в проекте. Смена имени пересоздаёт аккаунт, а с ним и ключи.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"description": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""),
				MarkdownDescription: "Описание, до 256 символов"},
			"role": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(DefaultRole),
				MarkdownDescription: "Роль в проекте: `PROJ-ADM` (администратор), `PROJ-MEMB` (участник, по умолчанию) " +
					"или `PROJ-VIEWER` (только чтение). Меняется без пересоздания."},
			"disabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "`true` выключает аккаунт: все его ключи перестают работать, аккаунт выходит из проекта. " +
					"При обратном включении аккаунт возвращается в проект, но ключи остаются выключенными: выпустите новые."},
			"principal_id": schema.StringAttribute{Computed: true, PlanModifiers: keep,
				MarkdownDescription: "UUID аккаунта как участника проекта; его видно в журнале и в списке участников"},
			"created_by": schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "Кто создал аккаунт (UUID пользователя)"},
			"created_at": schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "Когда создан (RFC 3339)"},
			"updated_at": schema.StringAttribute{Computed: true, MarkdownDescription: "Когда изменён (RFC 3339)"},
		},
	}
}

func (r *ServiceAccountResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *ServiceAccountResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg ServiceAccountModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if n := common.KnownString(cfg.Name); n != "" && !saNameRe.MatchString(n) {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Недопустимое имя",
			fmt.Sprintf("%q: нужно 3–63 символа из a-z, 0-9 и дефиса, первая буква, последний символ не дефис", n))
	}
	if role := common.KnownString(cfg.Role); role != "" && !slices.Contains(Roles, role) {
		resp.Diagnostics.AddAttributeError(path.Root("role"), "Недопустимая роль",
			fmt.Sprintf("%q: допустимы %s", role, strings.Join(Roles, ", ")))
	}
	if d := common.KnownString(cfg.Description); len([]rune(d)) > 256 {
		resp.Diagnostics.AddAttributeError(path.Root("description"), "Слишком длинное описание", "не больше 256 символов")
	}
}

func (r *ServiceAccountResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
}

func (m *ServiceAccountModel) fromAPI(sa *ServiceAccount) {
	m.ID = types.StringValue(sa.ID)
	if sa.ProjectID != "" {
		m.ProjectID = types.StringValue(sa.ProjectID)
	}
	m.Name = types.StringValue(sa.Name)
	m.Description = types.StringValue(sa.Description)
	m.Role = types.StringValue(sa.Role)
	m.Disabled = types.BoolValue(sa.Disabled)
	m.PrincipalID = types.StringValue(sa.PrincipalID)
	m.CreatedBy = types.StringValue(sa.CreatedBy)
	m.CreatedAt = types.StringValue(sa.CreatedAt.UTC().Format(time.RFC3339))
	m.UpdatedAt = types.StringValue(sa.UpdatedAt.UTC().Format(time.RFC3339))
}

func (r *ServiceAccountResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ServiceAccountModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := plan.ProjectID.ValueString()
	var sa ServiceAccount
	err := r.client.Do(ctx, "POST", saBase(project), nil, createSARequest{
		Name: plan.Name.ValueString(), Description: plan.Description.ValueString(), Role: plan.Role.ValueString(),
	}, &sa)
	if err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError("Ошибка создания сервисного аккаунта", errAPIAbsent(project).Error())
			return
		}
		resp.Diagnostics.AddError("Ошибка создания сервисного аккаунта", explain(err).Error())
		return
	}
	plan.fromAPI(&sa)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A new account is enabled; disabled = true in the config is one more PATCH.
	if plan.Disabled.ValueBool() != sa.Disabled {
		want := plan.Disabled.ValueBool()
		if err := r.client.Do(ctx, "PATCH", saPath(project, sa.ID), nil, updateSARequest{Disabled: &want}, &sa); err != nil {
			resp.Diagnostics.AddError("Аккаунт создан, но не выключен", explain(err).Error())
			return
		}
		plan.fromAPI(&sa)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *ServiceAccountResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ServiceAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := state.ProjectID.ValueString()
	var sa ServiceAccount
	if err := r.client.Do(ctx, "GET", saPath(project, state.ID.ValueString()), nil, nil, &sa); err != nil {
		if client.IsNotFound(err) {
			if apiAbsent(ctx, r.client, project) {
				resp.Diagnostics.AddError("Ошибка чтения сервисного аккаунта", errAPIAbsent(project).Error())
				return
			}
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Ошибка чтения сервисного аккаунта", explain(err).Error())
		return
	}
	state.fromAPI(&sa)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ServiceAccountResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ServiceAccountModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var body updateSARequest
	if d := plan.Description.ValueString(); d != state.Description.ValueString() {
		body.Description = &d
	}
	if role := plan.Role.ValueString(); role != state.Role.ValueString() {
		body.Role = &role
	}
	if dis := plan.Disabled.ValueBool(); dis != state.Disabled.ValueBool() {
		body.Disabled = &dis
	}
	project := state.ProjectID.ValueString()
	var sa ServiceAccount
	if body == (updateSARequest{}) {
		if err := r.client.Do(ctx, "GET", saPath(project, state.ID.ValueString()), nil, nil, &sa); err != nil {
			resp.Diagnostics.AddError("Ошибка чтения сервисного аккаунта", explain(err).Error())
			return
		}
	} else if err := r.client.Do(ctx, "PATCH", saPath(project, state.ID.ValueString()), nil, body, &sa); err != nil {
		resp.Diagnostics.AddError("Ошибка изменения сервисного аккаунта", explain(err).Error())
		return
	}
	plan.fromAPI(&sa)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ServiceAccountResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ServiceAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := state.ProjectID.ValueString()
	err := r.client.Do(ctx, "DELETE", saPath(project, state.ID.ValueString()), nil, nil, nil)
	if err == nil {
		return
	}
	if client.IsNotFound(err) {
		if apiAbsent(ctx, r.client, project) {
			resp.Diagnostics.AddError("Ошибка удаления сервисного аккаунта", errAPIAbsent(project).Error())
		}
		return
	}
	resp.Diagnostics.AddError("Ошибка удаления сервисного аккаунта", explain(err).Error())
}

// ImportState: terraform import h3_service_account.ci <project_id>/<sa_id>
func (r *ServiceAccountResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, id, err := common.ImportProjectScoped(req.ID, "sa-…")
	if err != nil {
		resp.Diagnostics.AddError("Неверный ID для импорта", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
