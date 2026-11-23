package common

import (
	"context"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Project support (0.3.0): the provider takes a default project
// (`project_id` / H3_PROJECT_ID). Every project-scoped resource has an
// Optional+Computed project_id: when it is not set, the provider default is
// planned on create; afterwards the value is kept from state, so changing the
// provider default never moves or replaces existing resources. Setting a
// different project_id explicitly replaces the resource (objects cannot move
// between projects).

// ProjectIDAttribute is the project_id attribute of project-scoped resources.
func ProjectIDAttribute(what string) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: "Project ID (UUID). Defaults to the provider `project_id` (`H3_PROJECT_ID`). " +
			"Changing it replaces the " + what + ".",
		Optional: true,
		Computed: true,
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.UseStateForUnknown(),
			stringplanmodifier.RequiresReplace(),
		},
	}
}

// DefaultProjectID fills project_id in the plan from the provider default.
// Call it first in ModifyPlan.
func DefaultProjectID(ctx context.Context, c *client.Client, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy
	}
	var planned, configured types.String
	resp.Diagnostics.Append(resp.Plan.GetAttribute(ctx, path.Root("project_id"), &planned)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("project_id"), &configured)...)
	if resp.Diagnostics.HasError() || !planned.IsUnknown() || configured.IsUnknown() {
		return // set, kept from state, or known only at apply
	}
	if c == nil {
		return // provider not configured yet (unknown provider arguments)
	}
	if def := c.ProjectID(); def != "" {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("project_id"), def)...)
		return
	}
	resp.Diagnostics.AddAttributeError(path.Root("project_id"), "Missing project_id",
		"Set project_id on the resource, or a default project with the provider argument `project_id` or the H3_PROJECT_ID environment variable.")
}

// DataSourceProjectID returns the configured project_id or the provider
// default; ok is false (with a diagnostic) when neither is set.
func DataSourceProjectID(c *client.Client, configured types.String, resp *datasource.ReadResponse) (string, bool) {
	if v := KnownString(configured); v != "" {
		return v, true
	}
	if c != nil && c.ProjectID() != "" {
		return c.ProjectID(), true
	}
	resp.Diagnostics.AddAttributeError(path.Root("project_id"), "Missing project_id",
		"Set project_id, or a default project with the provider argument `project_id` or H3_PROJECT_ID.")
	return "", false
}
