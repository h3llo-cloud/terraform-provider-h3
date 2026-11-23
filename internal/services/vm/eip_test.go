package vm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const testEIP = "6f1c2b8e-3d4a-4e5f-9a0b-1c2d3e4f5a6b"

func TestValidateEIPConfig(t *testing.T) {
	cases := []struct {
		name    string
		eip     types.String
		white   types.Bool
		wantErr bool
	}{
		{"no eip", types.StringNull(), types.BoolNull(), false},
		{"eip with white_ip", types.StringValue(testEIP), types.BoolValue(true), false},
		{"eip without white_ip", types.StringValue(testEIP), types.BoolNull(), true},
		{"eip with white_ip false", types.StringValue(testEIP), types.BoolValue(false), true},
		{"eip unknown", types.StringUnknown(), types.BoolValue(false), false},
		{"white_ip unknown", types.StringValue(testEIP), types.BoolUnknown(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validateEIPConfig(tc.eip, tc.white)
			if d.HasError() != tc.wantErr {
				t.Fatalf("HasError=%v want %v: %v", d.HasError(), tc.wantErr, d)
			}
		})
	}
}

func TestUUIDValidator(t *testing.T) {
	for v, ok := range map[string]bool{testEIP: true, "not-a-uuid": false, "": false, testEIP + "0": false} {
		resp := &validator.StringResponse{}
		common.UUIDValidator().ValidateString(context.Background(),
			validator.StringRequest{Path: path.Root("eip_id"), ConfigValue: types.StringValue(v)}, resp)
		if resp.Diagnostics.HasError() == ok {
			t.Errorf("%q: HasError=%v", v, resp.Diagnostics.HasError())
		}
	}
}

func TestSchemaHasEIPAttributes(t *testing.T) {
	resp := &resource.SchemaResponse{}
	(&VMResource{}).Schema(context.Background(), resource.SchemaRequest{}, resp)
	eip, ok := resp.Schema.Attributes["eip_id"]
	if !ok || !eip.IsOptional() || !eip.IsComputed() {
		t.Fatalf("eip_id must be optional+computed: %+v", eip)
	}
	rel, ok := resp.Schema.Attributes["release_eip_on_destroy"]
	if !ok || !rel.IsOptional() {
		t.Fatalf("release_eip_on_destroy missing")
	}
	if d := resp.Schema.ValidateImplementation(context.Background()); d.HasError() {
		t.Fatalf("schema invalid: %v", d)
	}
}

func basePlan() VMResourceModel {
	return VMResourceModel{
		ProjectID: types.StringValue("p"), Name: types.StringValue("vm1"), CPU: types.Int64Value(2),
		Memory: types.StringValue("4Gi"), SSHKeyID: types.StringValue("k"), SSHKeyIDs: types.SetNull(types.StringType), SSHAccessManaged: types.BoolNull(),
		DiskSize: types.StringUnknown(), Image: types.StringValue("ubuntu:24.04"), SubnetName: types.StringNull(),
		SourceSnapshotID: types.StringNull(), SourceBackupID: types.StringNull(),
		WhiteIP: types.BoolValue(true), EIPID: types.StringUnknown(),
	}
}

func TestBuildCreateRequestEIP(t *testing.T) {
	p := basePlan()
	b, _ := json.Marshal(buildCreateRequest(p))
	if strings.Contains(string(b), "eip_id") || !strings.Contains(string(b), `"white_ip":true`) {
		t.Fatalf("no eip_id expected when unset: %s", b)
	}

	p.EIPID = types.StringValue(testEIP)
	b, _ = json.Marshal(buildCreateRequest(p))
	if !strings.Contains(string(b), `"eip_id":"`+testEIP+`"`) {
		t.Fatalf("eip_id not sent: %s", b)
	}

	p.WhiteIP = types.BoolValue(false)
	if r := buildCreateRequest(p); r.EIPID != "" {
		t.Fatalf("eip_id must not be sent without white_ip")
	}
}

func TestDeleteQuery(t *testing.T) {
	if q := deleteQuery(false); q["release_eip"] != "" || q["preserve_eip"] != "true" || q["preserve_disk"] != "false" {
		t.Fatalf("default delete query: %v", q)
	}
	if q := deleteQuery(true); q["release_eip"] != "true" || q["preserve_eip"] != "" {
		t.Fatalf("release delete query: %v", q)
	}
}

func TestEIPIDFromResponse(t *testing.T) {
	prior := types.StringValue(testEIP)
	if got := eipIDFromResponse(nil, prior); !got.Equal(prior) {
		t.Fatalf("absent field must keep prior, got %v", got)
	}
	if got := eipIDFromResponse(nil, types.StringUnknown()); !got.IsNull() {
		t.Fatalf("absent field with unknown prior must be null, got %v", got)
	}
	empty := ""
	if got := eipIDFromResponse(&empty, prior); !got.IsNull() {
		t.Fatalf("empty must be null, got %v", got)
	}
	other := "other"
	if got := eipIDFromResponse(&other, prior); got.ValueString() != "other" {
		t.Fatalf("got %v", got)
	}
	var vm VM
	_ = json.Unmarshal([]byte(`{"id":"v","eip_id":"`+testEIP+`"}`), &vm)
	if vm.EIPID == nil || *vm.EIPID != testEIP {
		t.Fatalf("eip_id not decoded: %+v", vm)
	}
}

func TestSwapEIPDetachThenAttach(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	status := map[string]string{"old": "ATTACHED", testEIP: "DETACHED"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(body))
		switch r.URL.Path {
		case ovnEIPDetachPath:
			status["old"] = "DETACHED"
		case ovnEIPAttachPath:
			status[testEIP] = "ATTACHED"
		default:
			id := strings.TrimPrefix(r.URL.Path, ovnEIPPath)
			_ = json.NewEncoder(w).Encode(ovnEIP{ID: id, Status: status[id]})
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, err := client.NewClient(client.Config{BaseURL: srv.URL, KeyID: "k", SecretKey: "s", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	r := &VMResource{client: c}
	if s, d, ok := r.swapEIP(context.Background(), "vm-1", "old", testEIP, 10*time.Second); !ok {
		t.Fatalf("%s: %s", s, d)
	}
	if len(calls) != 4 ||
		calls[0] != `POST /api/ovn/v1/eips/detach {"eip_id":"old"}` ||
		calls[2] != `POST /api/ovn/v1/eips/attach {"eip_id":"`+testEIP+`","resource_id":"vm-1","resource_type":"vm"}` {
		t.Fatalf("calls: %q", calls)
	}
}

func TestSwapEIPAlreadyBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"EIP_ALREADY_BOUND","message":"taken"}}`))
	}))
	defer srv.Close()
	c, _ := client.NewClient(client.Config{BaseURL: srv.URL, KeyID: "k", SecretKey: "s", Timeout: 5 * time.Second})
	r := &VMResource{client: c}
	s, _, ok := r.swapEIP(context.Background(), "vm-1", "", testEIP, time.Second)
	if ok || s != "Elastic IP already in use" {
		t.Fatalf("ok=%v summary=%q", ok, s)
	}
}
