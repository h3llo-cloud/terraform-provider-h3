package vm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestUpgradeVMStateV0DropsSSHKeyText(t *testing.T) {
	out, err := upgradeVMStateV0JSON([]byte(`{"id":"v","ssh_key":"ssh-ed25519 AAAA u@h","ssh_key_id":null,"cpu":2}`))
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	_ = json.Unmarshal(out, &st)
	if _, ok := st["ssh_key"]; ok {
		t.Fatalf("ssh_key kept: %s", out)
	}
	for _, k := range []string{"ssh_key_ids", "ssh_access_managed", "allow_restart_on_resize"} {
		if v, ok := st[k]; !ok || v != nil {
			t.Fatalf("%s must be present and null: %s", k, out)
		}
	}
	if st["id"] != "v" || st["cpu"] != float64(2) {
		t.Fatalf("other attributes changed: %s", out)
	}
	if _, err := upgradeVMStateV0JSON([]byte(`not json`)); err == nil {
		t.Fatal("broken state must fail")
	}
}

func TestVMUpgradeStateRegistered(t *testing.T) {
	r := &VMResource{}
	if _, ok := r.UpgradeState(context.Background())[0]; !ok {
		t.Fatal("no upgrader from schema version 0")
	}
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Schema.Version != 1 {
		t.Fatalf("schema version = %d, want 1", resp.Schema.Version)
	}
	if _, ok := resp.Schema.Attributes["ssh_key"]; ok {
		t.Fatal("ssh_key (key text) must be gone")
	}
}

func TestSSHKeyIDsAndCreateRequest(t *testing.T) {
	ctx := context.Background()
	p := basePlan()
	if ids, ok := p.sshKeyIDs(ctx); !ok || len(ids) != 1 || ids[0] != "k" {
		t.Fatalf("ssh_key_id fallback: %v %v", ids, ok)
	}
	p.SSHKeyIDs, _ = types.SetValueFrom(ctx, types.StringType, []string{"b", "a"})
	p.SSHKeyID = types.StringNull()
	p.SSHAccessManaged = types.BoolValue(false)
	req := buildCreateRequest(p)
	if strings.Join(req.SSHKeyIDs, ",") != "a,b" || req.SSHKeyID != "" {
		t.Fatalf("two keys go as ssh_key_ids only: %+v", req)
	}
	if req.SSHAccessManaged == nil || *req.SSHAccessManaged {
		t.Fatalf("ssh_access_managed not sent: %+v", req)
	}
	b, _ := json.Marshal(req)
	if strings.Contains(string(b), `"ssh_key"`) {
		t.Fatalf("key text must never be sent: %s", b)
	}
	p.SSHKeyIDs, _ = types.SetValueFrom(ctx, types.StringType, []string{"a"})
	p.SSHAccessManaged = types.BoolNull()
	if req = buildCreateRequest(p); req.SSHKeyID != "a" || req.SSHKeyIDs != nil || req.SSHAccessManaged != nil {
		t.Fatalf("one key goes as ssh_key_id (works with SSH access management off): %+v", req)
	}
	p.SSHKeyIDs = types.SetUnknown(types.StringType)
	p.SSHKeyID = types.StringNull()
	p.SSHAccessManaged = types.BoolUnknown()
	req = buildCreateRequest(p)
	if req.SSHKeyIDs != nil || req.SSHAccessManaged != nil {
		t.Fatalf("unknown values must not be sent: %+v", req)
	}
	if !sameIDs([]string{"a", "b"}, []string{"b", "a"}) || sameIDs([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("sameIDs")
	}
}

func TestSSHAccessPutReadsBackWhenResponseIsEmpty(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(body))
		if r.Method == "PUT" {
			w.WriteHeader(204)
			return
		}
		_, _ = w.Write([]byte(`{"managed":true,"grants":[{"user_id":"u1","mode":"selected","keys":[{"key_id":"k1"}],"granted_by":"admin","granted_at":"2026-10-16T10:00:00Z"}]}`))
	}))
	defer srv.Close()
	c, err := client.NewClient(client.Config{BaseURL: srv.URL, KeyID: "k", SecretKey: "s", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	keys, _ := types.SetValueFrom(ctx, types.StringType, []string{"k1"})
	m := SSHAccessResourceModel{VMID: types.StringValue("vm 1"), UserID: types.StringValue("u1"), Mode: types.StringValue("selected"), KeyIDs: keys}
	if err := (&SSHAccessResource{client: c}).put(ctx, &m); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != `PUT /api/vms/v1/vm 1/ssh-access/grants/u1 {"mode":"selected","key_ids":["k1"]}` {
		t.Fatalf("calls: %v", calls)
	}
	if m.ID.ValueString() != "vm 1/u1" || !m.Active.ValueBool() || m.GrantedBy.ValueString() != "admin" || len(m.KeyIDs.Elements()) != 1 {
		t.Fatalf("model: %+v", m)
	}
}

func TestSSHAccessAllModeSendsEmptyKeyIDs(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = w.Write([]byte(`{"managed":true,"grants":[{"user_id":"u1","mode":"all","active":false,"inactive_reason":"ROLE_NOT_ALLOWED"}]}`))
	}))
	defer srv.Close()
	c, _ := client.NewClient(client.Config{BaseURL: srv.URL, KeyID: "k", SecretKey: "s", Timeout: 5 * time.Second})
	m := SSHAccessResourceModel{VMID: types.StringValue("v"), UserID: types.StringValue("u1"), Mode: types.StringValue("all"), KeyIDs: types.SetNull(types.StringType)}
	if err := (&SSHAccessResource{client: c}).put(context.Background(), &m); err != nil {
		t.Fatal(err)
	}
	if body != `{"mode":"all"}` {
		t.Fatalf("body %s", body)
	}
	if !m.KeyIDs.IsNull() || m.Active.ValueBool() || m.InactiveReason.ValueString() != "ROLE_NOT_ALLOWED" {
		t.Fatalf("model: %+v", m)
	}
}

func TestSSHAccessErrorDetail(t *testing.T) {
	got := sshAccessErrorDetail(errors.New(`API error 422: {"code":"SSH_KEY_NOT_OWNED"}`))
	if !strings.HasPrefix(got, "a key in key_ids does not belong") {
		t.Fatalf("got %q", got)
	}
	if got := sshAccessErrorDetail(errors.New("boom")); got != "boom" {
		t.Fatalf("got %q", got)
	}
}

func TestLearnSSHCallerAndCallerGrant(t *testing.T) {
	acc := &SSHAccess{Grants: []SSHAccessGrant{
		{UserID: "creator", Mode: "selected", KeyIDs: []string{"k1"}, Source: "creator"},
		{UserID: "member", Mode: "selected", KeyIDs: []string{"k1"}, Source: "member"},
		{UserID: "all", Mode: "all", Source: "self"},
	}}
	if got := learnSSHCaller(acc, []string{"k1"}); got != "creator" {
		t.Fatalf("learned %q", got)
	}
	if got := learnSSHCaller(acc, []string{"k2"}); got != "" {
		t.Fatalf("no match must give empty, got %q", got)
	}
	acc.Grants = append(acc.Grants, SSHAccessGrant{UserID: "self2", Mode: "selected", KeyIDs: []string{"k1"}, Source: "self"})
	if got := learnSSHCaller(acc, []string{"k1"}); got != "" {
		t.Fatalf("ambiguous must give empty, got %q", got)
	}
	if g, ok := callerGrant(acc, ""); !ok || g.UserID != "creator" {
		t.Fatalf("fallback to the creator grant: %v %v", g, ok)
	}
	if g, ok := callerGrant(acc, "gone"); !ok || g != nil {
		t.Fatalf("known caller without grant: %v %v", g, ok)
	}
}

func TestIsSSHAccessUnavailable(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&client.HTTPError{StatusCode: 404, Body: `404 page not found`}, true},
		{&client.HTTPError{StatusCode: 404, Body: `{"error":"x","code":"SSH_GRANT_NOT_FOUND"}`}, false},
		{&client.HTTPError{StatusCode: 503, Body: `{"code":"SSH_ACCESS_DISABLED"}`}, true},
		{&client.HTTPError{StatusCode: 503, Body: `{"code":"SSH_ACCESS_DEPENDENCY_UNAVAILABLE"}`}, false},
		{errors.New("boom"), false},
	} {
		if got := isSSHAccessUnavailable(c.err); got != c.want {
			t.Errorf("%v: %v", c.err, got)
		}
	}
}
