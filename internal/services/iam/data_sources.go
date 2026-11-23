package iam

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSourceWithConfigure = &ServiceAccountsDataSource{}
	_ datasource.DataSourceWithConfigure = &AuditEventsDataSource{}
)

// ---- h3_service_accounts ----

func NewServiceAccountsDataSource() datasource.DataSource { return &ServiceAccountsDataSource{} }

type ServiceAccountsDataSource struct{ client *client.Client }

type saData struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Role        types.String `tfsdk:"role"`
	Disabled    types.Bool   `tfsdk:"disabled"`
	PrincipalID types.String `tfsdk:"principal_id"`
	CreatedBy   types.String `tfsdk:"created_by"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

type ServiceAccountsModel struct {
	ProjectID       types.String `tfsdk:"project_id"`
	Name            types.String `tfsdk:"name"`
	ServiceAccounts []saData     `tfsdk:"service_accounts"`
}

func (d *ServiceAccountsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_accounts"
}

func (d *ServiceAccountsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Список сервисных аккаунтов проекта. " + AccessNote,
		Attributes: map[string]schema.Attribute{
			"project_id": schema.StringAttribute{Optional: true, Computed: true,
				MarkdownDescription: "ID проекта (UUID); если не задан, берётся `project_id` провайдера"},
			"name": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Вернуть только аккаунт с этим именем (пустой список, если такого нет)"},
			"service_accounts": schema.ListNestedAttribute{Computed: true, MarkdownDescription: "Аккаунты, по имени",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":           str("ID аккаунта (`sa-…`)"),
					"name":         str("Имя"),
					"description":  str("Описание"),
					"role":         str("Роль в проекте: `PROJ-ADM`, `PROJ-MEMB` или `PROJ-VIEWER`"),
					"disabled":     schema.BoolAttribute{Computed: true, MarkdownDescription: "Выключен ли аккаунт"},
					"principal_id": str("UUID аккаунта как участника проекта"),
					"created_by":   str("Кто создал (UUID пользователя)"),
					"created_at":   str("Когда создан (RFC 3339)"),
					"updated_at":   str("Когда изменён (RFC 3339)"),
				}}},
		},
	}
}

func (d *ServiceAccountsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *ServiceAccountsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg ServiceAccountsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, ok := common.DataSourceProjectID(d.client, cfg.ProjectID, resp)
	if !ok {
		return
	}
	list, err := listSAs(ctx, d.client, project)
	if err != nil {
		if client.IsNotFound(err) {
			err = errAPIAbsent(project)
		}
		resp.Diagnostics.AddError("Ошибка чтения сервисных аккаунтов", err.Error())
		return
	}
	name := common.KnownString(cfg.Name)
	out := ServiceAccountsModel{ProjectID: types.StringValue(project), Name: cfg.Name, ServiceAccounts: []saData{}}
	for _, sa := range sortedByName(list) {
		if name != "" && sa.Name != name {
			continue
		}
		out.ServiceAccounts = append(out.ServiceAccounts, saData{
			ID: types.StringValue(sa.ID), Name: types.StringValue(sa.Name), Description: types.StringValue(sa.Description),
			Role: types.StringValue(sa.Role), Disabled: types.BoolValue(sa.Disabled), PrincipalID: types.StringValue(sa.PrincipalID),
			CreatedBy: types.StringValue(sa.CreatedBy), CreatedAt: types.StringValue(sa.CreatedAt.UTC().Format(time.RFC3339)),
			UpdatedAt: types.StringValue(sa.UpdatedAt.UTC().Format(time.RFC3339)),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}

func sortedByName(l []ServiceAccount) []ServiceAccount {
	out := slices.Clone(l)
	slices.SortFunc(out, func(a, b ServiceAccount) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// ---- h3_iam_audit_events ----

func NewAuditEventsDataSource() datasource.DataSource { return &AuditEventsDataSource{} }

type AuditEventsDataSource struct{ client *client.Client }

type auditEventData struct {
	EventID      types.String `tfsdk:"event_id"`
	OccurredAt   types.String `tfsdk:"occurred_at"`
	Action       types.String `tfsdk:"action"`
	Result       types.String `tfsdk:"result"`
	ActorType    types.String `tfsdk:"actor_type"`
	ActorID      types.String `tfsdk:"actor_id"`
	ResourceType types.String `tfsdk:"resource_type"`
	ResourceID   types.String `tfsdk:"resource_id"`
	Details      types.String `tfsdk:"details"`
	IP           types.String `tfsdk:"ip"`
	UserAgent    types.String `tfsdk:"user_agent"`
	RequestID    types.String `tfsdk:"request_id"`
}

type AuditEventsModel struct {
	ProjectID     types.String     `tfsdk:"project_id"`
	From          types.String     `tfsdk:"from"`
	To            types.String     `tfsdk:"to"`
	Limit         types.Int64      `tfsdk:"limit"`
	RetentionDays types.Int64      `tfsdk:"retention_days"`
	Events        []auditEventData `tfsdk:"events"`
}

func (d *AuditEventsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_audit_events"
}

func (d *AuditEventsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Журнал IAM проекта: кто и когда создал, изменил, выключил или удалил сервисный аккаунт, " +
			"выпустил или отозвал ключ. Хранится 90 дней, новые события первыми. " + AccessNote,
		Attributes: map[string]schema.Attribute{
			"project_id": schema.StringAttribute{Optional: true, Computed: true,
				MarkdownDescription: "ID проекта (UUID); если не задан, берётся `project_id` провайдера"},
			"from": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Начало периода (RFC 3339). По умолчанию и не раньше чем 90 дней назад"},
			"to": schema.StringAttribute{Optional: true, MarkdownDescription: "Конец периода (RFC 3339). По умолчанию сейчас"},
			"limit": schema.Int64Attribute{Optional: true,
				MarkdownDescription: "Сколько последних событий вернуть: 1–500, по умолчанию 100"},
			"retention_days": schema.Int64Attribute{Computed: true, MarkdownDescription: "Сколько дней хранится журнал"},
			"events": schema.ListNestedAttribute{Computed: true, MarkdownDescription: "События, новые первыми",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"event_id":    str("ID события"),
					"occurred_at": str("Когда произошло (RFC 3339)"),
					"action": str("Что сделано: `service_account.create`, `service_account.update`, `service_account.disable`, " +
						"`service_account.enable`, `service_account.delete`, `service_account.key_create`, `service_account.key_delete`"),
					"result":        str("Результат, `RESULT_OK`"),
					"actor_type":    str("Кто сделал: тип (`TYPE_USER`)"),
					"actor_id":      str("Кто сделал: UUID"),
					"resource_type": str("Над чем: тип (`service_account`)"),
					"resource_id":   str("Над чем: ID"),
					"details":       str("Подробности изменения, JSON-объект строкой (`jsondecode()`)"),
					"ip":            str("IP-адрес, с которого пришёл запрос"),
					"user_agent":    str("User-Agent запроса"),
					"request_id":    str("ID запроса"),
				}}},
		},
	}
}

func (d *AuditEventsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *AuditEventsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg AuditEventsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, ok := common.DataSourceProjectID(d.client, cfg.ProjectID, resp)
	if !ok {
		return
	}
	q := map[string]string{}
	for k, v := range map[string]types.String{"from": cfg.From, "to": cfg.To} {
		if s := common.KnownString(v); s != "" {
			if _, err := time.Parse(time.RFC3339, s); err != nil {
				resp.Diagnostics.AddError("Неверная дата", fmt.Sprintf("%s = %q: нужен формат RFC 3339", k, s))
				return
			}
			q[k] = s
		}
	}
	if n := common.KnownInt64(cfg.Limit); n != 0 {
		if n < 1 || n > 500 {
			resp.Diagnostics.AddError("Неверный limit", "limit: от 1 до 500")
			return
		}
		q["limit"] = strconv.FormatInt(n, 10)
	}
	var page auditPage
	if err := d.client.Do(ctx, "GET", auditPath(project), q, nil, &page); err != nil {
		if client.IsNotFound(err) {
			err = errAPIAbsent(project)
		} else {
			err = explain(err)
		}
		resp.Diagnostics.AddError("Ошибка чтения журнала IAM", err.Error())
		return
	}
	out := AuditEventsModel{ProjectID: types.StringValue(project), From: cfg.From, To: cfg.To, Limit: cfg.Limit,
		RetentionDays: types.Int64Value(page.RetentionDays), Events: []auditEventData{}}
	for _, e := range page.Events {
		ev := auditEventData{
			EventID: types.StringValue(e.EventID), OccurredAt: types.StringValue(e.OccurredAt.UTC().Format(time.RFC3339)),
			Action: types.StringValue(e.Action), Result: types.StringValue(e.Result),
			ActorType: types.StringValue(e.Actor.Type), ActorID: types.StringValue(e.Actor.ID),
			ResourceType: types.StringValue(""), ResourceID: types.StringValue(""), Details: types.StringValue("{}"),
			IP: types.StringValue(e.IP), UserAgent: types.StringValue(e.UserAgent), RequestID: types.StringValue(e.RequestID),
		}
		if e.Resource != nil {
			ev.ResourceType, ev.ResourceID = types.StringValue(e.Resource.Type), types.StringValue(e.Resource.ID)
		}
		if len(e.After) > 0 {
			if b, err := json.Marshal(e.After); err == nil {
				ev.Details = types.StringValue(string(b))
			}
		}
		out.Events = append(out.Events, ev)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}
