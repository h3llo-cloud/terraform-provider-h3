package net

import (
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestApplyBinding(t *testing.T) {
	cases := []struct {
		name           string
		body           string
		bound          types.Bool
		resType, resID string
	}{
		{"old backend", `{"id":"e","status":"DETACHED"}`, types.BoolNull(), "", ""},
		{"free", `{"id":"e","bound":false}`, types.BoolValue(false), "", ""},
		{"camel", `{"id":"e","bound":true,"boundTo":{"resourceType":"vm","resourceId":"v1"}}`, types.BoolValue(true), "vm", "v1"},
		{"current h3ovn", `{"id":"e","resourceType":"vm","resourceId":"v1","status":"ATTACHED"}`, types.BoolValue(true), "vm", "v1"},
		{"snake", `{"id":"e","bound":true,"bound_to":{"resource_type":"db","resource_id":"d1"}}`, types.BoolValue(true), "db", "d1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var eip EIP
			if err := json.Unmarshal([]byte(tc.body), &eip); err != nil {
				t.Fatal(err)
			}
			var m EIPResourceModel
			applyBinding(&m, eip)
			if !m.Bound.Equal(tc.bound) || m.BoundResourceType.ValueString() != tc.resType || m.BoundResourceID.ValueString() != tc.resID {
				t.Fatalf("got bound=%v type=%v id=%v", m.Bound, m.BoundResourceType, m.BoundResourceID)
			}
			if tc.resID == "" && !m.BoundResourceID.IsNull() {
				t.Fatalf("unbound id must be null")
			}
		})
	}
}
