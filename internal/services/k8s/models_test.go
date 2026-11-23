package k8s

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"h3terraform/internal/client"
)

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v1.35.9", "1.35.9", 0, true},
		{"v1.34.1", "v1.35.9", -1, true},
		{"1.36.5", "v1.35.9", 1, true},
		{"v1.35.10", "v1.35.9", 1, true},
		{"latest", "v1.35.9", 0, false},
	} {
		got, ok := CompareVersions(c.a, c.b)
		if got != c.want || ok != c.ok {
			t.Errorf("CompareVersions(%q,%q) = %d,%v want %d,%v", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
	if NormalizeVersion("1.34.1") != "v1.34.1" || !SameVersion("v1.34.1", "1.34.1") {
		t.Fatal("normalize")
	}
}

func TestKubeconfigEndpoint(t *testing.T) {
	ca := base64.StdEncoding.EncodeToString([]byte("PEM"))
	kc := "apiVersion: v1\nclusters:\n- cluster:\n    certificate-authority-data: " + ca + "\n    server: \"https://x:6443\"\n  name: x\n"
	host, pem := KubeconfigEndpoint(kc)
	if host != "https://x:6443" || pem != "PEM" {
		t.Fatalf("got %q %q", host, pem)
	}
}

func TestClusterDecodesNumbersAndStrings(t *testing.T) {
	var c Cluster
	if err := json.Unmarshal([]byte(`{"workers":{"replicas":3,"cpu":"4","memoryGi":"8","diskGi":50},"controlPlane":{"replicas":3}}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.Workers.CPU != 4 || c.Workers.MemoryGi != 8 || c.Workers.DiskGi != 50 || c.ControlPlane.Replicas != 3 {
		t.Fatalf("%+v", c)
	}
}

func TestMaintenanceBodyAndEqual(t *testing.T) {
	m := &MaintenanceModel{
		Enabled: types.BoolValue(true), DayOfWeek: types.StringValue("Sunday"), StartHour: types.Int64Value(2),
		AutoUpgrade: types.StringValue("patch"),
		Exclusions:  []MaintenanceExclusionModel{{StartDate: types.StringValue("2026-12-20"), EndDate: types.StringValue("2027-01-10"), Reason: types.StringNull()}},
	}
	b, _ := json.Marshal(maintenanceBody(m))
	want := `{"enabled":true,"dayOfWeek":"Sunday","startHour":2,"autoUpgrade":"patch","exclusions":[{"startDate":"2026-12-20","endDate":"2027-01-10"}]}`
	if string(b) != want {
		t.Fatalf("body=%s\nwant=%s", b, want)
	}
	// no exclusions configured: [] is sent so the list is cleared
	m2 := *m
	m2.Exclusions = nil
	b, _ = json.Marshal(maintenanceBody(&m2))
	if !strings.Contains(string(b), `"exclusions":[]`) {
		t.Fatalf("clear not sent: %s", b)
	}
	if maintenanceEqual(m, &m2) || !maintenanceEqual(m, m) {
		t.Fatal("equal")
	}
	m3 := *m
	m3.AutoUpgrade = types.StringValue("minor")
	if maintenanceEqual(m, &m3) {
		t.Fatal("auto_upgrade change not detected")
	}
}

func TestNodeLocalDNSSettled(t *testing.T) {
	on, off := true, false
	nld := func(status string, n *NodeLocalDNSCacheDTO) *Cluster {
		return &Cluster{Status: status, NodeLocalDNSCache: n}
	}
	for i, c := range []struct {
		c       *Cluster
		want    *bool
		done    bool
		wantErr string
	}{
		{nld("READY", &NodeLocalDNSCacheDTO{Enabled: true, State: "ENABLING"}), &on, false, ""},
		{nld("UPDATING", &NodeLocalDNSCacheDTO{Enabled: true, State: "READY"}), &on, false, ""},
		{nld("READY", &NodeLocalDNSCacheDTO{Enabled: true, State: "READY"}), &on, true, ""},
		{nld("READY", &NodeLocalDNSCacheDTO{Enabled: false, State: "DISABLING"}), &off, false, ""},
		{nld("READY", &NodeLocalDNSCacheDTO{Enabled: false, State: "DISABLED"}), &off, true, ""},
		{nld("READY", &NodeLocalDNSCacheDTO{Enabled: true}), &on, true, ""},
		{nld("READY", &NodeLocalDNSCacheDTO{Enabled: true, State: "ERROR", Message: "boom"}), &on, false, "ERROR state: boom"},
		{nld("READY", &NodeLocalDNSCacheDTO{Enabled: true, State: "ERROR"}), nil, true, ""},
		{nld("READY", &NodeLocalDNSCacheDTO{Enabled: true, State: "ENABLING"}), nil, false, ""},
		{nld("READY", nil), &on, false, "does not support"},
		{nld("READY", nil), &off, true, ""},
		{nld("READY", nil), nil, true, ""},
	} {
		done, err := nodeLocalDNSSettled(c.c, c.want)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%d: err = %v, want %q", i, err, c.wantErr)
			}
			continue
		}
		if err != nil || done != c.done {
			t.Errorf("%d: got %v, %v want %v", i, done, err, c.done)
		}
	}
}

func TestAPIErrorReason(t *testing.T) {
	err := &client.HTTPError{StatusCode: 409, Method: "PATCH", URL: "/x",
		Body: `{"code":409,"reason":"NODE_LOCAL_DNS_CONFLICT","message":"own node-local-dns found"}`}
	if got := errorReason(err); got != ReasonNodeLocalDNSConflict {
		t.Fatalf("reason = %q", got)
	}
	msg := apiError(err, false)
	for _, want := range []string{"NODE_LOCAL_DNS_CONFLICT", "HTTP 409", "own node-local-dns found", "remove it"} {
		if !strings.Contains(msg, want) {
			t.Errorf("apiError = %q, missing %q", msg, want)
		}
	}
	if errorReason(&client.HTTPError{StatusCode: 409, Body: "busy"}) != "" {
		t.Fatal("plain body has no reason")
	}
}

func TestClusterNodeLocalDNSCacheAbsent(t *testing.T) {
	var c Cluster
	if err := json.Unmarshal([]byte(`{"id":"x","status":"READY"}`), &c); err != nil {
		t.Fatal(err)
	}
	var m ClusterModel
	m.applyAPI(&c)
	if m.NodeLocalDNSCache.ValueBool() || m.NodeLocalDNSCache.IsNull() {
		t.Fatalf("node_local_dns_cache = %v, want false", m.NodeLocalDNSCache)
	}
	b, _ := json.Marshal(CreateClusterRequest{})
	if strings.Contains(string(b), "nodeLocalDNSCache") {
		t.Fatalf("create body without the cache: %s", b)
	}
}

func TestValidateAllowedCIDRs(t *testing.T) {
	if err := ValidateAllowedCIDRs([]string{"203.0.113.0/24", "198.51.100.7/32"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"198.51.100.7", "203.0.113.9/24", "0.0.0.0/0", "2001:db8::/32", "x"} {
		if err := ValidateAllowedCIDRs([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestClusterDecodesAPIAccess(t *testing.T) {
	var c Cluster
	if err := json.Unmarshal([]byte(`{"apiAccess":{"allowedCidrs":["203.0.113.0/24"]}}`), &c); err != nil {
		t.Fatal(err)
	}
	v := allowedCIDRsValue(&c, types.SetNull(types.StringType))
	if v.IsNull() || len(v.Elements()) != 1 {
		t.Fatalf("%v", v)
	}
	var old Cluster // h3k8s without the feature
	if got := allowedCIDRsValue(&old, v); !got.Equal(v) {
		t.Fatalf("missing apiAccess must keep the previous value: %v", got)
	}
}
