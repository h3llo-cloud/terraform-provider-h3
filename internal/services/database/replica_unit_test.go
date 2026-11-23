package database

import (
	"strings"
	"testing"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestAPIErrorDiagReplicaCodes(t *testing.T) {
	cases := []struct {
		status        int
		body          string
		summary, text string
	}{
		{409, `{"code":"SOURCE_HAS_REPLICAS","message":"has replicas","replicas":[{"id":"r-1","name":"ro-a"},{"id":"r-2","name":"ro-b"}]}`,
			"Error deleting database: SOURCE_HAS_REPLICAS", "Replicas: ro-a (r-1), ro-b (r-2)."},
		{409, `{"code":"REPLICA_READ_ONLY","message":"read only"}`,
			"Error deleting database: REPLICA_READ_ONLY", "managed on its source"},
		{400, `{"code":"REPLICA_SIZE_TOO_SMALL","message":"too small"}`,
			"Error deleting database: REPLICA_SIZE_TOO_SMALL", "at least the source's size"},
		{503, `{"code":"READ_REPLICAS_DISABLED","message":"off"}`,
			"Error deleting database: READ_REPLICAS_DISABLED", "not enabled on this platform"},
		{409, `{"code":"CHANGE_IN_PROGRESS","message":"busy"}`,
			"Error deleting database: CHANGE_IN_PROGRESS", "in progress"},
		{500, `{"code":"internal","message":"boom"}`, "Error deleting database", "boom"},
	}
	for _, tc := range cases {
		err := &client.HTTPError{StatusCode: tc.status, Method: "DELETE", URL: "/x", Body: tc.body}
		summary, detail := apiErrorDiag("deleting database", err)
		if summary != tc.summary || !strings.Contains(detail, tc.text) {
			t.Errorf("%s: got %q / %q", tc.body, summary, detail)
		}
	}
	for code := range replicaErrorText {
		if code == "" || strings.ToUpper(code) != code {
			t.Errorf("bad code %q", code)
		}
	}
}

func TestReplicaOfFromAPI(t *testing.T) {
	src := "11111111-0000-0000-0000-000000000001"
	cases := []struct {
		db    Database
		prior types.String
		want  types.String
	}{
		{Database{ReplicaOf: src}, types.StringNull(), types.StringValue(src)},        // replica, import
		{Database{PromotedFrom: src}, types.StringValue(src), types.StringValue(src)}, // promoted, config keeps it
		{Database{PromotedFrom: src}, types.StringNull(), types.StringNull()},         // promoted, removed / import
		{Database{PromotedFrom: src}, types.StringValue("other"), types.StringNull()}, // not the former source
		{Database{}, types.StringNull(), types.StringNull()},                          // regular database
	}
	for i, tc := range cases {
		if got := replicaOfFromAPI(&tc.db, tc.prior); !got.Equal(tc.want) {
			t.Errorf("case %d: got %v, want %v", i, got, tc.want)
		}
	}
}
