package common

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
)

// RequiresReplaceIfPriorSet is for create-only arguments the API does not
// return (ssh_key_id, source_snapshot_id, ...). A change replaces the resource,
// except when the prior state is null: after `terraform import` the value is
// unknown to the provider, and setting it in the configuration must not
// destroy the imported resource. That plan is an in-place no-op update that
// only records the value.
func RequiresReplaceIfPriorSet() planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(
		func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
			resp.RequiresReplace = !req.StateValue.IsNull()
		},
		"Changing this value replaces the resource (not after import, where the prior value is unknown).",
		"Changing this value replaces the resource (not after `terraform import`, where the prior value is unknown).",
	)
}

// RequiresReplaceIfQuantityChanged replaces the resource when the configured
// quantity changes amount; "25Gi" -> "25600Mi" is not a replacement.
func RequiresReplaceIfQuantityChanged() planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(
		func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
			if req.ConfigValue.IsNull() || req.StateValue.IsNull() || req.PlanValue.IsUnknown() {
				return
			}
			resp.RequiresReplace = !QuantityEqual(req.StateValue.ValueString(), req.PlanValue.ValueString())
		},
		"Changing the amount replaces the resource; a different spelling of the same amount does not.",
		"Changing the amount replaces the resource; a different spelling of the same amount (`25Gi`, `25600Mi`) does not.",
	)
}
