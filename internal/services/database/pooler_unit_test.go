package database

import (
	"reflect"
	"strings"
	"testing"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestValidatePoolerParameter(t *testing.T) {
	ok := map[string]string{
		"default_pool_size": "50", "min_pool_size": "0", "client_tls_sslmode": "prefer", "server_lifetime": "86400",
	}
	for k, v := range ok {
		if err := validatePoolerParameter(k, v); err != nil {
			t.Errorf("%s=%s: %v", k, v, err)
		}
	}
	bad := []struct{ k, v, want string }{
		{"pool_mode", "session", "not allowed"},
		{"client_tls_protocols", "secure", "not allowed"},
		{"default_pool_size", "", "empty value"},
		{"default_pool_size", "-1", "non-negative integer"},
		{"default_pool_size", "007", "non-negative integer"},
		{"default_pool_size", " 5", "non-negative integer"},
		{"max_client_conn", "99999999999", "too large"},
		{"client_tls_sslmode", "Require", "not one of"},
	}
	for _, tc := range bad {
		err := validatePoolerParameter(tc.k, tc.v)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s=%q: got %v, want %q", tc.k, tc.v, err, tc.want)
		}
	}
}

func TestValidatePoolerIDNamePort(t *testing.T) {
	for _, id := range []string{"main", "ro", "rw-session", "a", "1", "abcdefghij0123456789"} {
		if err := validatePoolerID(id); err != nil {
			t.Errorf("%q: %v", id, err)
		}
	}
	for _, id := range []string{"", "Main", "a_b", "-a", "a-", "abcdefghij01234567890", "a/b"} {
		if validatePoolerID(id) == nil {
			t.Errorf("%q accepted", id)
		}
	}
	if err := validateDisplayName(strings.Repeat("я", 64)); err != nil {
		t.Errorf("64 runes: %v", err)
	}
	if validateDisplayName(strings.Repeat("a", 65)) == nil || validateDisplayName("a\nb") == nil {
		t.Error("long or multi-line display_name accepted")
	}
	for _, tc := range []struct {
		port int64
		zero bool
		ok   bool
	}{{1024, false, true}, {5432, false, true}, {65535, false, true}, {0, true, true}, {0, false, false}, {1023, true, false}, {65536, false, false}} {
		if err := validateListenerPort("p", tc.port, tc.zero); (err == nil) != tc.ok {
			t.Errorf("port %d zero=%v: %v", tc.port, tc.zero, err)
		}
	}
}

func TestParametersPatch(t *testing.T) {
	got := parametersPatch(
		map[string]string{"default_pool_size": "50", "max_client_conn": "3000", "server_lifetime": "600"},
		map[string]string{"max_client_conn": "4000", "server_lifetime": "600", "query_timeout": "30"},
	)
	want := map[string]string{"default_pool_size": "", "max_client_conn": "4000", "query_timeout": "30"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if p := parametersPatch(map[string]string{"a": "1"}, map[string]string{"a": "1"}); p != nil {
		t.Errorf("no change: %v", p)
	}
	if p := parametersPatch(nil, nil); p != nil {
		t.Errorf("nil: %v", p)
	}
}

func TestDecodePoolerList(t *testing.T) {
	l, err := decodePoolerList([]byte(`{"poolers":[{"id":"x"},{"id":"y"}],"budget":{"maxConnections":1000,"reserve":10,
		"primary":{"used":380,"max":1000},"replica":{"used":910,"max":1000}},
		"limits":{"maxPoolers":4,"maxInstances":16,"portMin":1024,"portMax":65535,"defaultPorts":{"rw":6432,"ro":6433,"r":6434}}}`))
	if err != nil || len(l.Poolers) != 2 || l.Budget.Replica.Used != 910 || l.Limits.DefaultPorts["ro"] != 6433 {
		t.Fatalf("object: %+v %v", l, err)
	}
	if l, err := decodePoolerList([]byte(`[{"id":"main"}]`)); err != nil || len(l.Poolers) != 1 || l.Budget != nil {
		t.Errorf("array: %+v %v", l, err)
	}
	if l, err := decodePoolerList(nil); err != nil || len(l.Poolers) != 0 {
		t.Errorf("empty: %+v %v", l, err)
	}
	if !budgetValue(nil).IsNull() || limitsValue(l.Limits).IsNull() != (l.Limits == nil) {
		t.Error("null budget/limits")
	}
}

func TestPoolerErrorDiag(t *testing.T) {
	cases := []struct {
		status        int
		body          string
		summary, text string
	}{
		{409, `{"code":"POOLER_IN_USE","message":"public 5432 leads to main"}`, "Error deleting pooler: POOLER_IN_USE", "public_port = 5432"},
		{409, `{"code":"POOLER_IN_USE","message":"public 5432 leads to main"}`, "Error deleting pooler: POOLER_IN_USE", "h3db: public 5432 leads to main"},
		{409, `{"code":"PORT_IN_USE","message":"6432"}`, "Error deleting pooler: PORT_IN_USE", "another port"},
		{400, `{"code":"PORT_OUT_OF_RANGE","message":"80"}`, "Error deleting pooler: PORT_OUT_OF_RANGE", "1024-65535"},
		{409, `{"code":"POOLER_LIMIT_REACHED","message":"4"}`, "Error deleting pooler: POOLER_LIMIT_REACHED", "maximum number"},
		{400, `{"code":"ROLE_NEEDS_REPLICA","message":"one instance"}`, "Error deleting pooler: ROLE_NEEDS_REPLICA", "two instances"},
		{400, `{"code":"PUBLIC_NEEDS_EIP","message":"no eip"}`, "Error deleting pooler: PUBLIC_NEEDS_EIP", "white_ip = true"},
		{409, `{"code":"VPC_ADDRESS_UNAVAILABLE","message":"no vpcIp"}`, "Error deleting pooler: VPC_ADDRESS_UNAVAILABLE", `vpc_mode = "address"`},
		{404, `{"code":"POOLER_MULTI_DISABLED","message":"off"}`, "Error deleting pooler: POOLER_MULTI_DISABLED", "not enabled"},
		{400, `{"code":"INVALID_LIMITS","message":"over","budget":1000,"used":910,"requested":200}`,
			"Error deleting pooler: INVALID_LIMITS", "Budget 1000, used 910, requested 200."},
		{400, `{"code":"INVALID_LIMITS","message":"over"}`, "Error deleting pooler: INVALID_LIMITS", "max_connections"},
		{403, `{"code":"EIP_DENIED","message":"quota"}`, "Elastic IP not allowed", "cannot lease"},
		{409, `{"code":"SOURCE_HAS_REPLICAS","message":"x"}`, "Error deleting pooler: SOURCE_HAS_REPLICAS", "Delete them"},
		{500, `{"code":"internal","message":"boom"}`, "Error deleting pooler", "boom"},
	}
	for _, tc := range cases {
		err := &client.HTTPError{StatusCode: tc.status, Method: "DELETE", URL: "/x", Body: tc.body}
		summary, detail := poolerErrorDiag("deleting pooler", err)
		if summary != tc.summary || !strings.Contains(detail, tc.text) {
			t.Errorf("%s: got %q / %q", tc.body, summary, detail)
		}
	}
	for code := range poolerErrorText {
		if code == "" || strings.ToUpper(code) != code {
			t.Errorf("bad code %q", code)
		}
	}
}

func TestIsPoolerGone(t *testing.T) {
	he := func(code int, body string) error { return &client.HTTPError{StatusCode: code, Body: body} }
	if !isPoolerGone(he(404, `{"code":"POOLER_NOT_FOUND"}`)) || !isPoolerGone(he(404, `{"error":"Database not found"}`)) {
		t.Error("404 not gone")
	}
	if isPoolerGone(he(404, `{"code":"POOLER_MULTI_DISABLED"}`)) || isPoolerGone(he(500, ``)) {
		t.Error("flag off or 500 treated as gone")
	}
}

func TestPoolerSettled(t *testing.T) {
	assigned := &PoolerVPC{Mode: "port", Status: "assigned"}
	pending := &PoolerVPC{Mode: "port", Status: "pending"}
	cases := []struct {
		p      Pooler
		paused bool
		want   bool
	}{
		{Pooler{Phase: "READY", Instances: 2, ReadyInstances: 2, VPC: assigned}, false, true},
		{Pooler{Phase: "READY", Instances: 2, ReadyInstances: 2, VPC: pending}, false, false},
		{Pooler{Phase: "READY", Instances: 2, ReadyInstances: 2, VPC: &PoolerVPC{Mode: "port", Status: "unavailable"}}, false, true},
		{Pooler{Phase: "READY", Instances: 3, ReadyInstances: 2}, false, false},
		{Pooler{Phase: "PROGRESSING", Instances: 2, ReadyInstances: 2}, false, false},
		{Pooler{Phase: "PAUSED", Instances: 2}, true, true},
		{Pooler{Phase: "PAUSED", Instances: 2}, false, false},
		{Pooler{Phase: "READY", Instances: 2, ReadyInstances: 2}, true, false},
		{Pooler{Phase: "STOPPED", VPC: pending}, false, true},
	}
	for _, tc := range cases {
		if got := poolerSettled(&tc.p, tc.paused); got != tc.want {
			t.Errorf("%+v paused=%v: got %v", tc.p, tc.paused, got)
		}
	}
}

func TestApplyPooler(t *testing.T) {
	m := PoolerResourceModel{DatabaseID: types.StringValue("db-1"), Parameters: types.MapNull(types.StringType),
		PublicVia: types.StringUnknown()}
	m.applyPooler(&Pooler{
		ID: "rw-session", DisplayName: "main (transaction)", Role: "rw", Mode: "transaction", Instances: 2,
		ReadyInstances: 1, Phase: "PROGRESSING", K8sName: "db-1-pooler-rw-session", Adopted: true, Default: true,
		Parameters: map[string]string{"default_pool_size": "20"},
		VPC:        &PoolerVPC{Mode: "port", IP: "fd00::5", Port: 6432, Status: "assigned"},
		Public: []PoolerPublic{
			{IP: "45.93.22.2", Port: 5432, Via: "external"},
			{IP: "45.93.22.149", Port: 6432, Via: "port-sources", EIPID: "e-1"},
		},
	})
	if m.ID.ValueString() != "db-1/rw-session" || !m.Parameters.IsNull() || !m.Adopted.ValueBool() ||
		m.EffectiveParameters.Elements()["default_pool_size"].String() != `"20"` {
		t.Errorf("model: %+v", m)
	}
	if m.VPCEndpoint.ValueString() != "[fd00::5]:6432" || m.VPCPort.ValueInt64() != 6432 || m.VPCStatus.ValueString() != "assigned" {
		t.Errorf("vpc: %v %v %v", m.VPCEndpoint, m.VPCPort, m.VPCStatus)
	}
	// public_port manages the listener h3db set up, not the adopted external one.
	if m.PublicPort.ValueInt64() != 6432 || m.PublicVia.ValueString() != "port-sources" ||
		len(m.PublicEndpoints.Elements()) != 2 || len(m.PublicAddresses.Elements()) != 2 {
		t.Errorf("public: %v %v %v", m.PublicPort, m.PublicVia, m.PublicEndpoints)
	}

	// User parameters set elsewhere show as a diff against a null map; no
	// VPC field = none; a configured public_via stays without a listener.
	m.PublicVia = types.StringValue("service")
	m.applyPooler(&Pooler{ID: "main", UserParameters: map[string]string{"max_client_conn": "100"}})
	if m.Parameters.IsNull() || len(m.Parameters.Elements()) != 1 || m.VPCMode.ValueString() != "none" ||
		!m.VPCPort.IsNull() || !m.VPCEndpoint.IsNull() || m.PublicPort.ValueInt64() != 0 || m.PublicVia.ValueString() != "service" {
		t.Errorf("second: %+v", m)
	}
	// A configured empty map stays empty (not null); mode none drops the port.
	m.Parameters = types.MapValueMust(types.StringType, nil)
	m.applyPooler(&Pooler{ID: "main", VPC: &PoolerVPC{Mode: "none", Port: 6432}})
	if m.Parameters.IsNull() || len(m.Parameters.Elements()) != 0 || !m.VPCPort.IsNull() {
		t.Errorf("third: %v %v", m.Parameters, m.VPCPort)
	}
}
