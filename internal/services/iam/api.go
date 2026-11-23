// Package iam: service accounts of a project, their keys and the project IAM
// audit (h3iam /api/iam/v1, spec api/openapi/v1/service-accounts.yaml).
package iam

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
)

// projectIDAttribute is common.ProjectIDAttribute with a Russian description.
func projectIDAttribute(what string) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: "ID проекта (UUID). Если не задан, берётся `project_id` провайдера (`H3_PROJECT_ID`). " +
			"Смена проекта пересоздаёт " + what + ".",
		Optional: true,
		Computed: true,
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.UseStateForUnknown(),
			stringplanmodifier.RequiresReplace(),
		},
	}
}

// ServiceAccount is the h3iam ServiceAccount object.
type ServiceAccount struct {
	ID          string    `json:"id"`
	PrincipalID string    `json:"principal_id"`
	ProjectID   string    `json:"project_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Role        string    `json:"role"`
	Disabled    bool      `json:"disabled"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type createSARequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Role        string `json:"role,omitempty"`
}

type updateSARequest struct {
	Description *string `json:"description,omitempty"`
	Role        *string `json:"role,omitempty"`
	Disabled    *bool   `json:"disabled,omitempty"`
}

// Key is a key of a service account; h3iam never returns the secret after creation.
type Key struct {
	KeyID       string     `json:"key_id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Scopes      []string   `json:"scopes"`
	Enabled     bool       `json:"enabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	Kind        string     `json:"kind"`
	SecretKey   string     `json:"secret_key,omitempty"`
}

type createKeyRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
	ExpiresAt   *string  `json:"expires_at,omitempty"`
}

// AuditEvent is an audit.v1 event of h3iam.
type AuditEvent struct {
	EventID     string         `json:"event_id"`
	OccurredAt  time.Time      `json:"occurred_at"`
	Source      string         `json:"source"`
	WorkspaceID string         `json:"workspace_id"`
	ProjectID   string         `json:"project_id"`
	Resource    *AuditResource `json:"resource"`
	Actor       AuditActor     `json:"actor"`
	Action      string         `json:"action"`
	After       map[string]any `json:"after"`
	Result      string         `json:"result"`
	IP          string         `json:"ip"`
	RequestID   string         `json:"request_id"`
	UserAgent   string         `json:"user_agent"`
}

type AuditActor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type AuditResource struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type auditPage struct {
	Events        []AuditEvent `json:"events"`
	RetentionDays int64        `json:"retention_days"`
}

// Roles of a service account in its project.
var Roles = []string{"PROJ-ADM", "PROJ-MEMB", "PROJ-VIEWER"}

// DefaultRole is the role h3iam gives when none is set.
const DefaultRole = "PROJ-MEMB"

func saBase(project string) string {
	return "/api/iam/v1/projects/" + url.PathEscape(project) + "/service-accounts"
}

func saPath(project, id string) string { return saBase(project) + "/" + url.PathEscape(id) }

func keysPath(project, sa string) string { return saPath(project, sa) + "/keys" }

func auditPath(project string) string {
	return "/api/iam/v1/projects/" + url.PathEscape(project) + "/audit"
}

// AccessNote is appended to every description: the API is rolled out by project.
const AccessNote = "API сервисных аккаунтов включается по проектам: пока проект не подключён, API отвечает 404, " +
	"и провайдер сообщает об этом ошибкой, а не удаляет ресурсы из состояния."

func listSAs(ctx context.Context, c *client.Client, project string) ([]ServiceAccount, error) {
	var out struct {
		ServiceAccounts []ServiceAccount `json:"service_accounts"`
	}
	if err := c.Do(ctx, "GET", saBase(project), nil, nil, &out); err != nil {
		return nil, explain(err)
	}
	return out.ServiceAccounts, nil
}

func listKeys(ctx context.Context, c *client.Client, project, sa string) ([]Key, error) {
	var out struct {
		Keys []Key `json:"keys"`
	}
	if err := c.Do(ctx, "GET", keysPath(project, sa), nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

// apiAbsent reports whether the IAM API itself answers 404 for the project
// (rollout not reached it, or h3iam without service accounts): then a 404 on
// one object says nothing about that object.
func apiAbsent(ctx context.Context, c *client.Client, project string) bool {
	err := c.Do(ctx, "GET", saBase(project), nil, nil, nil)
	return client.IsNotFound(err)
}

// errAPIAbsent is the error for a project the IAM API is not enabled for.
func errAPIAbsent(project string) error {
	return fmt.Errorf("API сервисных аккаунтов недоступно для проекта %s (ответ 404 на список). "+
		"Ресурсы в состоянии не тронуты; повторите, когда API включат для проекта", project)
}

// explain turns API errors into a hint where the reason is known.
func explain(err error) error {
	h, ok := client.AsHTTPError(err)
	if !ok {
		return err
	}
	switch h.StatusCode {
	case 401:
		return fmt.Errorf("%w\nКлюч API не принят. Проверьте key_id и secret_key", err)
	case 403:
		return fmt.Errorf("%w\nНет прав на сервисные аккаунты проекта. Нужны права на них в проекте "+
			"и scope ключа iam:read (чтение), iam:write (изменения) или iam:delete (удаление); scope «*» не подходит. "+
			"Ключ сервисного аккаунта управлять IAM не может", err)
	}
	return err
}
