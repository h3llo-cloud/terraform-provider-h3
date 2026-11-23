package k8s

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func strMapValue(kv ...string) types.Map {
	m := map[string]attr.Value{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = types.StringValue(kv[i+1])
	}
	return types.MapValueMust(types.StringType, m)
}

func basePool() NodePoolModel {
	return NodePoolModel{
		ClusterID: types.StringValue("c1"), Name: types.StringValue("api"),
		CPU: types.Int64Value(4), MemoryGi: types.Int64Value(8), DiskGi: types.Int64Unknown(), DiskProfile: types.StringUnknown(),
		Replicas: types.Int64Value(2), Labels: types.MapNull(types.StringType), MaxPods: types.Int64Unknown(),
		KubernetesVersion: types.StringUnknown(),
	}
}

func TestCreatePoolBody(t *testing.T) {
	m := basePool()
	m.Labels = strMapValue("workload", "api")
	m.Taints = []TaintModel{{Key: types.StringValue("dedicated"), Value: types.StringNull(), Effect: types.StringValue("NoSchedule")}}
	b, _ := json.Marshal(createPoolBody(&m))
	want := `{"name":"api","cpu":4,"memoryGi":8,"replicas":2,"labels":{"workload":"api"},"taints":[{"key":"dedicated","effect":"NoSchedule"}]}`
	if string(b) != want {
		t.Fatalf("fixed pool:\n got %s\nwant %s", b, want)
	}

	// Autoscaling: no replicas, minSize is the lower bound (0 = scale from zero).
	m = basePool()
	m.Replicas = types.Int64Unknown()
	m.Autoscaling = &NodePoolAutoscalingModel{Enabled: types.BoolValue(true), MinSize: types.Int64Value(0), MaxSize: types.Int64Value(5),
		ScaleDownUnneededTime: types.Int64Null(), ScaleDownUtilizationThreshold: types.StringValue("0.6"), MaxNodeProvisionTime: types.Int64Null()}
	m.Kubelet = &KubeletModel{PlatformDefaults: types.BoolValue(true), SystemReserved: types.MapNull(types.StringType),
		KubeReserved: types.MapNull(types.StringType), EvictionHard: strMapValue("memory.available", "300Mi")}
	b, _ = json.Marshal(createPoolBody(&m))
	want = `{"name":"api","cpu":4,"memoryGi":8,"autoscaling":{"enabled":true,"minSize":0,"maxSize":5,"scaleDownUtilizationThreshold":"0.6"},` +
		`"kubelet":{"platformDefaults":true,"evictionHard":{"memory.available":"300Mi"}}}`
	if string(b) != want {
		t.Fatalf("autoscaled pool:\n got %s\nwant %s", b, want)
	}
}

func TestUpdatePoolBody(t *testing.T) {
	state := basePool()
	state.DiskGi, state.MaxPods, state.DiskProfile = types.Int64Value(50), types.Int64Null(), types.StringValue("replicated")
	state.Labels = strMapValue("a", "1")
	state.Taints = []TaintModel{{Key: types.StringValue("k"), Value: types.StringNull(), Effect: types.StringValue("NoSchedule")}}
	plan := state

	if u := updatePoolBody(&plan, &state); !u.Empty() {
		t.Fatalf("no change: %+v", u)
	}

	// Labels removed -> {}, taints removed -> [], no rollout.
	plan.Labels, plan.Taints = types.MapNull(types.StringType), nil
	u := updatePoolBody(&plan, &state)
	b, _ := json.Marshal(u)
	if string(b) != `{"labels":{},"taints":[]}` || u.Rolling() {
		t.Fatalf("labels/taints: %s rolling=%v", b, u.Rolling())
	}

	// Size change is rolling.
	plan = state
	plan.CPU, plan.MemoryGi = types.Int64Value(8), types.Int64Value(16)
	if u := updatePoolBody(&plan, &state); !u.Rolling() || *u.CPU != 8 || *u.MemoryGi != 16 || u.Replicas != nil {
		t.Fatalf("size: %+v", u)
	}

	// Autoscaled: replicas never sent; turning it off sends the configured size.
	as := &NodePoolAutoscalingModel{Enabled: types.BoolValue(true), MinSize: types.Int64Value(1), MaxSize: types.Int64Value(5),
		ScaleDownUnneededTime: types.Int64Null(), ScaleDownUtilizationThreshold: types.StringNull(), MaxNodeProvisionTime: types.Int64Null()}
	state.Autoscaling, state.Replicas = as, types.Int64Value(1)
	plan = state
	plan.Replicas = types.Int64Value(1)
	if u := updatePoolBody(&plan, &state); !u.Empty() {
		t.Fatalf("autoscaled, unchanged: %+v", u)
	}
	plan.Autoscaling, plan.Replicas = nil, types.Int64Value(3)
	u = updatePoolBody(&plan, &state)
	b, _ = json.Marshal(u)
	if string(b) != `{"replicas":3,"autoscaling":{"enabled":false}}` {
		t.Fatalf("autoscaling off: %s", b)
	}

	// Removing the kubelet block sends {} (no reservations).
	state.Autoscaling = nil
	state.Kubelet = &KubeletModel{PlatformDefaults: types.BoolValue(true), SystemReserved: types.MapNull(types.StringType),
		KubeReserved: types.MapNull(types.StringType), EvictionHard: types.MapNull(types.StringType)}
	plan = state
	plan.Kubelet = nil
	u = updatePoolBody(&plan, &state)
	b, _ = json.Marshal(u)
	if string(b) != `{"kubelet":{}}` || !u.Rolling() {
		t.Fatalf("kubelet removed: %s", b)
	}
}

func TestPoolApplyAPI(t *testing.T) {
	var p NodePool
	body := `{"name":"api","cpu":"4","memoryGi":8,"diskGi":50,"diskProfile":"replicated","replicas":1,
		"autoscaling":{"enabled":true,"minSize":1,"maxSize":5,"scaleDownUtilizationThreshold":"0.5"},
		"kubernetesVersion":"v1.35.9","billedReplicas":4,"status":"READY","desiredReplicas":4,"currentReplicas":4,"readyReplicas":4}`
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	m := basePool()
	m.Labels = types.MapValueMust(types.StringType, map[string]attr.Value{}) // configured {}
	m.KubernetesVersion = types.StringValue("1.35.9")
	m.Autoscaling = &NodePoolAutoscalingModel{Enabled: types.BoolValue(true), MinSize: types.Int64Value(1), MaxSize: types.Int64Value(5),
		ScaleDownUnneededTime: types.Int64Null(), ScaleDownUtilizationThreshold: types.StringValue("0.50"), MaxNodeProvisionTime: types.Int64Null()}
	m.Kubelet = &KubeletModel{PlatformDefaults: types.BoolValue(false), SystemReserved: types.MapNull(types.StringType),
		KubeReserved: types.MapNull(types.StringType), EvictionHard: types.MapNull(types.StringType)}
	m.applyAPI(&p)

	if m.ID.ValueString() != "c1/api" || m.CPU.ValueInt64() != 4 || m.Replicas.ValueInt64() != 1 || m.CurrentReplicas.ValueInt64() != 4 ||
		m.BilledReplicas.ValueInt64() != 4 || m.Status.ValueString() != "READY" || !m.MaxPods.IsNull() {
		t.Fatalf("basic fields: %+v", m)
	}
	if m.Labels.IsNull() || len(m.Labels.Elements()) != 0 {
		t.Fatalf("configured empty labels must stay {}: %v", m.Labels)
	}
	if m.KubernetesVersion.ValueString() != "1.35.9" {
		t.Fatalf("version spelling: %v", m.KubernetesVersion)
	}
	if m.Autoscaling.ScaleDownUtilizationThreshold.ValueString() != "0.50" || m.Autoscaling.MinSize.ValueInt64() != 1 {
		t.Fatalf("autoscaling: %+v", m.Autoscaling)
	}
	if m.Kubelet == nil || !m.Kubelet.PlatformDefaults.Equal(types.BoolValue(false)) {
		t.Fatalf("configured empty kubelet block must stay: %+v", m.Kubelet)
	}

	// Autoscaling turned off on the platform: the configured options stay, enabled=false is drift.
	p.Autoscaling = &NodePoolAutoscalingDTO{Enabled: false}
	m.applyAPI(&p)
	if m.Autoscaling == nil || m.Autoscaling.Enabled.ValueBool() || m.Autoscaling.MaxSize.ValueInt64() != 5 {
		t.Fatalf("disabled autoscaling: %+v", m.Autoscaling)
	}
	// Labels set on the platform, kubelet removed while configured: both drift.
	p.Labels = map[string]string{"x": "y"}
	m.Kubelet = &KubeletModel{PlatformDefaults: types.BoolValue(true), SystemReserved: types.MapNull(types.StringType),
		KubeReserved: types.MapNull(types.StringType), EvictionHard: types.MapNull(types.StringType)}
	m.applyAPI(&p)
	if m.Labels.Elements()["x"].(types.String).ValueString() != "y" || m.Kubelet != nil {
		t.Fatalf("drift: labels %v kubelet %+v", m.Labels, m.Kubelet)
	}
}

func TestParsePoolImportID(t *testing.T) {
	def := func() string { return "p0" }
	none := func() string { return "" }
	for _, c := range []struct {
		raw, project, cluster, name, err string
		def                              func() string
	}{
		{raw: "p1/c1/api", project: "p1", cluster: "c1", name: "api", def: none},
		{raw: "c1/api", project: "p0", cluster: "c1", name: "api", def: def},
		{raw: "c1/api", err: "has no project", def: none},
		{raw: "c1", err: "expected import id", def: def},
		{raw: "p1//api", err: "expected import id", def: def},
	} {
		p, cl, n, err := parsePoolImportID(c.raw, c.def)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: err = %v, want %q", c.raw, err, c.err)
			}
			continue
		}
		if err != nil || p != c.project || cl != c.cluster || n != c.name {
			t.Errorf("%q: got %q %q %q %v", c.raw, p, cl, n, err)
		}
	}
}

func TestValidateNodePool(t *testing.T) {
	ok := basePool()
	if errs := validateNodePool(&ok); len(errs) != 0 {
		t.Fatalf("valid pool: %+v", errs)
	}
	bad := basePool()
	bad.Name = types.StringValue("pool-")
	bad.MemoryGi = types.Int64Value(128) // > 16 per vCPU
	bad.Autoscaling = &NodePoolAutoscalingModel{Enabled: types.BoolValue(true), MinSize: types.Int64Value(4), MaxSize: types.Int64Value(2),
		ScaleDownUnneededTime: types.Int64Null(), ScaleDownUtilizationThreshold: types.StringValue("0.95"), MaxNodeProvisionTime: types.Int64Value(2)}
	var got []string
	for _, e := range validateNodePool(&bad) {
		got = append(got, e.summary)
	}
	for _, want := range []string{"Invalid name", "Invalid memory_gi", "replicas with autoscaling", "Invalid autoscaling.max_size",
		"Invalid scale_down_utilization_threshold", "Invalid autoscaling.max_node_provision_time"} {
		if !strings.Contains(strings.Join(got, "|"), want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
}

func TestPoolSettledAndMatches(t *testing.T) {
	one, two := int64(1), int64(2)
	p := &NodePool{Status: "READY", DesiredReplicas: &two, ReadyReplicas: &one, CPU: 8}
	if poolSettled(p) {
		t.Fatal("1/2 ready is not settled")
	}
	p.ReadyReplicas = &two
	if !poolSettled(p) {
		t.Fatal("2/2 ready")
	}
	cpu := int64(4)
	if poolMatches(p, UpdateNodePoolRequest{CPU: &cpu}) {
		t.Fatal("cpu 8 != 4")
	}
}

func TestClusterNodePoolsAndExpander(t *testing.T) {
	var c Cluster
	if err := json.Unmarshal([]byte(`{"id":"x","status":"READY","workers":{"replicas":2,"cpu":2,"memoryGi":4,"diskGi":50},
		"nodePools":[{"name":"default","cpu":2,"memoryGi":4,"diskGi":50,"replicas":2,"autoscaling":{"enabled":false}}],
		"autoscaler":{"enabled":false}}`), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.NodePools) != 1 || c.NodePools[0].Name != DefaultNodePoolName {
		t.Fatalf("nodePools: %+v", c.NodePools)
	}
	// expander/balanceSimilarNodeGroups are not returned: the configured ones stay.
	var m ClusterModel
	m.Autoscaler = &AutoscalerModel{Enabled: types.BoolValue(false), Expander: types.StringValue("least-waste"), BalanceSimilarNodeGroups: types.BoolValue(true)}
	m.applyAPI(&c)
	if m.Autoscaler == nil || m.Autoscaler.Expander.ValueString() != "least-waste" || !m.Autoscaler.BalanceSimilarNodeGroups.ValueBool() {
		t.Fatalf("autoscaler: %+v", m.Autoscaler)
	}
	b, _ := json.Marshal(autoscalerBody(m.Autoscaler))
	if string(b) != `{"enabled":false,"expander":"least-waste","balanceSimilarNodeGroups":true}` {
		t.Fatalf("autoscaler body: %s", b)
	}
	b, _ = json.Marshal(autoscalerBody(&AutoscalerModel{Enabled: types.BoolValue(true), MaxSize: types.Int64Value(5),
		ScaleDownDelayAfterAdd: types.Int64Null(), ScaleDownUnneededTime: types.Int64Null(), Expander: types.StringNull(), BalanceSimilarNodeGroups: types.BoolNull()}))
	if strings.Contains(string(b), "expander") || strings.Contains(string(b), "balance") {
		t.Fatalf("flags must not be sent when unset: %s", b)
	}
}
