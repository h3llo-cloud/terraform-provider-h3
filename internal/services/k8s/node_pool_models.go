package k8s

import (
	"regexp"
	"strconv"

	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Node pools of h3k8s (/api/k8s/v3/clusters/{id}/node-pools, h3k8s MR !39).

// DefaultNodePoolName is the pool that h3_k8s_cluster.workers manages.
const DefaultNodePoolName = "default"

// Pool statuses of the h3k8s API.
const (
	PoolReady    = "READY"
	PoolDeleting = "DELETING"
)

// poolNameRe is the h3k8s rule for pool names.
var poolNameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,18}[a-z0-9])?$`)

// Taint effects allowed by h3k8s.
var taintEffects = []string{"NoSchedule", "PreferNoSchedule", "NoExecute"}

// NodePoolAutoscalingDTO is the autoscaling block of a pool.
type NodePoolAutoscalingDTO struct {
	Enabled                       bool    `json:"enabled"`
	MinSize                       *int64  `json:"minSize,omitempty"`
	MaxSize                       flexInt `json:"maxSize,omitempty"`
	ScaleDownUnneededTime         flexInt `json:"scaleDownUnneededTime,omitempty"`
	ScaleDownUtilizationThreshold string  `json:"scaleDownUtilizationThreshold,omitempty"`
	MaxNodeProvisionTime          flexInt `json:"maxNodeProvisionTime,omitempty"`
}

// TaintDTO is a node taint of a pool.
type TaintDTO struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"`
}

// KubeletDTO: kubelet reservations of the pool nodes. An empty object in a
// PATCH removes all reservations.
type KubeletDTO struct {
	PlatformDefaults bool              `json:"platformDefaults,omitempty"`
	SystemReserved   map[string]string `json:"systemReserved,omitempty"`
	KubeReserved     map[string]string `json:"kubeReserved,omitempty"`
	EvictionHard     map[string]string `json:"evictionHard,omitempty"`
}

// NodePool is the h3k8s NodePoolResponse.
type NodePool struct {
	Name              string                  `json:"name"`
	CPU               flexInt                 `json:"cpu"`
	MemoryGi          flexInt                 `json:"memoryGi"`
	DiskGi            flexInt                 `json:"diskGi"`
	DiskProfile       string                  `json:"diskProfile"`
	Replicas          flexInt                 `json:"replicas"`
	Autoscaling       *NodePoolAutoscalingDTO `json:"autoscaling"`
	Labels            map[string]string       `json:"labels"`
	Taints            []TaintDTO              `json:"taints"`
	MaxPods           flexInt                 `json:"maxPods"`
	Kubelet           *KubeletDTO             `json:"kubelet"`
	KubernetesVersion string                  `json:"kubernetesVersion"`
	BilledReplicas    flexInt                 `json:"billedReplicas"`
	CreatedAt         string                  `json:"createdAt"`
	// Runtime state, only from the /node-pools endpoints.
	Status          string `json:"status"`
	DesiredReplicas *int64 `json:"desiredReplicas"`
	CurrentReplicas *int64 `json:"currentReplicas"`
	ReadyReplicas   *int64 `json:"readyReplicas"`
}

// NodePoolsResponse is GET /clusters/{id}/node-pools.
type NodePoolsResponse struct {
	NodePools []NodePool `json:"nodePools"`
}

// CreateNodePoolRequest is POST /clusters/{id}/node-pools.
type CreateNodePoolRequest struct {
	Name        string                  `json:"name"`
	CPU         int64                   `json:"cpu"`
	MemoryGi    int64                   `json:"memoryGi"`
	DiskGi      int64                   `json:"diskGi,omitempty"`
	DiskProfile string                  `json:"diskProfile,omitempty"`
	Replicas    *int64                  `json:"replicas,omitempty"`
	Autoscaling *NodePoolAutoscalingDTO `json:"autoscaling,omitempty"`
	Labels      map[string]string       `json:"labels,omitempty"`
	Taints      []TaintDTO              `json:"taints,omitempty"`
	MaxPods     int64                   `json:"maxPods,omitempty"`
	Kubelet     *KubeletDTO             `json:"kubelet,omitempty"`
}

// UpdateNodePoolRequest is PATCH /clusters/{id}/node-pools/{pool}; absent
// fields keep their value, labels/taints/kubelet replace the whole value.
type UpdateNodePoolRequest struct {
	Replicas    *int64                  `json:"replicas,omitempty"`
	CPU         *int64                  `json:"cpu,omitempty"`
	MemoryGi    *int64                  `json:"memoryGi,omitempty"`
	DiskGi      *int64                  `json:"diskGi,omitempty"`
	Autoscaling *NodePoolAutoscalingDTO `json:"autoscaling,omitempty"`
	Labels      *map[string]string      `json:"labels,omitempty"`
	Taints      *[]TaintDTO             `json:"taints,omitempty"`
	MaxPods     *int64                  `json:"maxPods,omitempty"`
	Kubelet     *KubeletDTO             `json:"kubelet,omitempty"`
}

// Empty reports whether the PATCH changes nothing.
func (u UpdateNodePoolRequest) Empty() bool {
	return u.Replicas == nil && u.CPU == nil && u.MemoryGi == nil && u.DiskGi == nil && u.Autoscaling == nil &&
		u.Labels == nil && u.Taints == nil && u.MaxPods == nil && u.Kubelet == nil
}

// Rolling reports whether the PATCH replaces the nodes of the pool.
func (u UpdateNodePoolRequest) Rolling() bool {
	return u.CPU != nil || u.MemoryGi != nil || u.DiskGi != nil || u.MaxPods != nil || u.Kubelet != nil
}

// ---- Terraform models ----

type NodePoolModel struct {
	ID                types.String              `tfsdk:"id"`
	ClusterID         types.String              `tfsdk:"cluster_id"`
	ProjectID         types.String              `tfsdk:"project_id"`
	Name              types.String              `tfsdk:"name"`
	CPU               types.Int64               `tfsdk:"cpu"`
	MemoryGi          types.Int64               `tfsdk:"memory_gi"`
	DiskGi            types.Int64               `tfsdk:"disk_gi"`
	DiskProfile       types.String              `tfsdk:"disk_profile"`
	Replicas          types.Int64               `tfsdk:"replicas"`
	Autoscaling       *NodePoolAutoscalingModel `tfsdk:"autoscaling"`
	Labels            types.Map                 `tfsdk:"labels"`
	Taints            []TaintModel              `tfsdk:"taint"`
	MaxPods           types.Int64               `tfsdk:"max_pods"`
	Kubelet           *KubeletModel             `tfsdk:"kubelet"`
	KubernetesVersion types.String              `tfsdk:"kubernetes_version"`
	Status            types.String              `tfsdk:"status"`
	CurrentReplicas   types.Int64               `tfsdk:"current_replicas"`
	BilledReplicas    types.Int64               `tfsdk:"billed_replicas"`
}

type NodePoolAutoscalingModel struct {
	Enabled                       types.Bool   `tfsdk:"enabled"`
	MinSize                       types.Int64  `tfsdk:"min_size"`
	MaxSize                       types.Int64  `tfsdk:"max_size"`
	ScaleDownUnneededTime         types.Int64  `tfsdk:"scale_down_unneeded_time"`
	ScaleDownUtilizationThreshold types.String `tfsdk:"scale_down_utilization_threshold"`
	MaxNodeProvisionTime          types.Int64  `tfsdk:"max_node_provision_time"`
}

type TaintModel struct {
	Key    types.String `tfsdk:"key"`
	Value  types.String `tfsdk:"value"`
	Effect types.String `tfsdk:"effect"`
}

type KubeletModel struct {
	PlatformDefaults types.Bool `tfsdk:"platform_defaults"`
	SystemReserved   types.Map  `tfsdk:"system_reserved"`
	KubeReserved     types.Map  `tfsdk:"kube_reserved"`
	EvictionHard     types.Map  `tfsdk:"eviction_hard"`
}

// poolAutoscaled reports whether the autoscaler owns the replica count.
func poolAutoscaled(a *NodePoolAutoscalingModel) bool { return a != nil && a.Enabled.ValueBool() }

// ---- model -> API ----

func stringMap(m types.Map) map[string]string {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	out := make(map[string]string, len(m.Elements()))
	for k, v := range m.Elements() {
		if s, ok := v.(types.String); ok {
			out[k] = s.ValueString()
		}
	}
	return out
}

func taintsBody(ts []TaintModel) []TaintDTO {
	out := make([]TaintDTO, 0, len(ts))
	for _, t := range ts {
		out = append(out, TaintDTO{Key: t.Key.ValueString(), Value: common.KnownString(t.Value), Effect: t.Effect.ValueString()})
	}
	return out
}

// poolAutoscalingBody is the autoscaling block of a request; a disabled or
// absent block is {"enabled": false}.
func poolAutoscalingBody(a *NodePoolAutoscalingModel) *NodePoolAutoscalingDTO {
	if !poolAutoscaled(a) {
		return &NodePoolAutoscalingDTO{Enabled: false}
	}
	out := &NodePoolAutoscalingDTO{
		Enabled:                       true,
		MaxSize:                       flexInt(knownInt(a.MaxSize)),
		ScaleDownUnneededTime:         flexInt(knownInt(a.ScaleDownUnneededTime)),
		ScaleDownUtilizationThreshold: common.KnownString(a.ScaleDownUtilizationThreshold),
		MaxNodeProvisionTime:          flexInt(knownInt(a.MaxNodeProvisionTime)),
	}
	if !a.MinSize.IsNull() && !a.MinSize.IsUnknown() {
		v := a.MinSize.ValueInt64()
		out.MinSize = &v
	}
	return out
}

// kubeletBody is the kubelet block of a request; nil model -> {} (no reservations).
func kubeletBody(k *KubeletModel) *KubeletDTO {
	if k == nil {
		return &KubeletDTO{}
	}
	return &KubeletDTO{
		PlatformDefaults: k.PlatformDefaults.ValueBool(),
		SystemReserved:   stringMap(k.SystemReserved),
		KubeReserved:     stringMap(k.KubeReserved),
		EvictionHard:     stringMap(k.EvictionHard),
	}
}

// kubeletEmpty: a block without any setting means "no reservations", as no block.
func kubeletEmpty(k *KubeletModel) bool {
	return k == nil || (!k.PlatformDefaults.ValueBool() && len(stringMap(k.SystemReserved)) == 0 &&
		len(stringMap(k.KubeReserved)) == 0 && len(stringMap(k.EvictionHard)) == 0)
}

// createPoolBody builds the create request from the plan.
func createPoolBody(m *NodePoolModel) CreateNodePoolRequest {
	body := CreateNodePoolRequest{
		Name:        m.Name.ValueString(),
		CPU:         m.CPU.ValueInt64(),
		MemoryGi:    m.MemoryGi.ValueInt64(),
		DiskGi:      knownInt(m.DiskGi),
		DiskProfile: common.KnownString(m.DiskProfile),
		Labels:      stringMap(m.Labels),
		MaxPods:     knownInt(m.MaxPods),
	}
	if len(m.Taints) > 0 {
		body.Taints = taintsBody(m.Taints)
	}
	if poolAutoscaled(m.Autoscaling) {
		body.Autoscaling = poolAutoscalingBody(m.Autoscaling)
	} else {
		v := knownInt(m.Replicas)
		body.Replicas = &v
	}
	if !kubeletEmpty(m.Kubelet) {
		body.Kubelet = kubeletBody(m.Kubelet)
	}
	return body
}

// updatePoolBody builds the PATCH that turns state into plan. Replicas are
// sent only for a fixed-size pool, when changed or when autoscaling is turned
// off (the configured size then applies).
func updatePoolBody(plan, state *NodePoolModel) UpdateNodePoolRequest {
	var u UpdateNodePoolRequest
	planAS, stateAS := poolAutoscaled(plan.Autoscaling), poolAutoscaled(state.Autoscaling)
	if !planAS && (stateAS || !plan.Replicas.Equal(state.Replicas)) {
		v := plan.Replicas.ValueInt64()
		u.Replicas = &v
	}
	if !poolAutoscalingEqual(plan.Autoscaling, state.Autoscaling) {
		u.Autoscaling = poolAutoscalingBody(plan.Autoscaling)
	}
	setInt := func(dst **int64, p, s types.Int64) {
		if !p.IsNull() && !p.IsUnknown() && !p.Equal(s) {
			v := p.ValueInt64()
			*dst = &v
		}
	}
	setInt(&u.CPU, plan.CPU, state.CPU)
	setInt(&u.MemoryGi, plan.MemoryGi, state.MemoryGi)
	setInt(&u.DiskGi, plan.DiskGi, state.DiskGi)
	setInt(&u.MaxPods, plan.MaxPods, state.MaxPods)
	if !mapsEqual(plan.Labels, state.Labels) {
		l := stringMap(plan.Labels)
		if l == nil {
			l = map[string]string{}
		}
		u.Labels = &l
	}
	if !taintsEqual(plan.Taints, state.Taints) {
		t := taintsBody(plan.Taints)
		u.Taints = &t
	}
	if !kubeletEqual(plan.Kubelet, state.Kubelet) {
		u.Kubelet = kubeletBody(plan.Kubelet)
	}
	return u
}

// ---- comparisons (unknown plan values count as unchanged) ----

func mapsEqual(a, b types.Map) bool {
	if a.IsUnknown() {
		return true
	}
	ma, mb := stringMap(a), stringMap(b)
	if len(ma) != len(mb) {
		return false
	}
	for k, v := range ma {
		if w, ok := mb[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func taintsEqual(a, b []TaintModel) bool {
	ta, tb := taintsBody(a), taintsBody(b)
	if len(ta) != len(tb) {
		return false
	}
	for i := range ta {
		if ta[i] != tb[i] {
			return false
		}
	}
	return true
}

func kubeletEqual(a, b *KubeletModel) bool {
	if kubeletEmpty(a) || kubeletEmpty(b) {
		return kubeletEmpty(a) == kubeletEmpty(b)
	}
	return a.PlatformDefaults.ValueBool() == b.PlatformDefaults.ValueBool() && mapsEqual(a.SystemReserved, b.SystemReserved) &&
		mapsEqual(a.KubeReserved, b.KubeReserved) && mapsEqual(a.EvictionHard, b.EvictionHard)
}

func poolAutoscalingEqual(a, b *NodePoolAutoscalingModel) bool {
	if !poolAutoscaled(a) || !poolAutoscaled(b) {
		return poolAutoscaled(a) == poolAutoscaled(b)
	}
	same := func(x, y types.Int64) bool { return x.IsUnknown() || knownInt(x) == knownInt(y) }
	return same(a.MinSize, b.MinSize) && same(a.MaxSize, b.MaxSize) && same(a.ScaleDownUnneededTime, b.ScaleDownUnneededTime) &&
		same(a.MaxNodeProvisionTime, b.MaxNodeProvisionTime) &&
		(a.ScaleDownUtilizationThreshold.IsUnknown() || thresholdEqual(a.ScaleDownUtilizationThreshold.ValueString(), b.ScaleDownUtilizationThreshold.ValueString()))
}

// thresholdEqual compares utilization thresholds numerically ("0.5" = "0.50").
func thresholdEqual(a, b string) bool {
	if a == b {
		return true
	}
	fa, e1 := strconv.ParseFloat(a, 64)
	fb, e2 := strconv.ParseFloat(b, 64)
	return e1 == nil && e2 == nil && fa == fb
}

// ---- API -> model ----

// mapValue converts an API map; an empty API map keeps a configured empty
// map ({}), otherwise it is null.
func mapValue(prior types.Map, m map[string]string) types.Map {
	if len(m) == 0 {
		if !prior.IsNull() && !prior.IsUnknown() && len(prior.Elements()) == 0 {
			return prior
		}
		return types.MapNull(types.StringType)
	}
	vals := make(map[string]attr.Value, len(m))
	for k, v := range m {
		vals[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, vals)
}

func taintModels(ts []TaintDTO) []TaintModel {
	if len(ts) == 0 {
		return nil
	}
	out := make([]TaintModel, 0, len(ts))
	for _, t := range ts {
		out = append(out, TaintModel{Key: types.StringValue(t.Key), Value: common.OptString(t.Value), Effect: types.StringValue(t.Effect)})
	}
	return out
}

func optPtr(p *int64) types.Int64 {
	if p == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*p)
}

// applyAPI copies a pool into the model. Values the API does not echo for
// "nothing set" (empty maps, a kubelet block without settings, disabled
// autoscaling options) keep their configured form so that they are not drift.
func (m *NodePoolModel) applyAPI(p *NodePool) {
	m.Name = types.StringValue(p.Name)
	m.ID = types.StringValue(m.ClusterID.ValueString() + "/" + p.Name)
	m.CPU = types.Int64Value(int64(p.CPU))
	m.MemoryGi = types.Int64Value(int64(p.MemoryGi))
	m.DiskGi = types.Int64Value(int64(p.DiskGi))
	m.DiskProfile = common.OptString(p.DiskProfile)
	m.Replicas = types.Int64Value(int64(p.Replicas))
	m.MaxPods = optInt(int64(p.MaxPods))
	m.Labels = mapValue(m.Labels, p.Labels)
	m.Taints = taintModels(p.Taints)

	switch a := p.Autoscaling; {
	case a != nil && a.Enabled:
		as := &NodePoolAutoscalingModel{
			Enabled:                       types.BoolValue(true),
			MinSize:                       types.Int64Value(int64(p.Replicas)),
			MaxSize:                       optInt(int64(a.MaxSize)),
			ScaleDownUnneededTime:         optInt(int64(a.ScaleDownUnneededTime)),
			ScaleDownUtilizationThreshold: common.OptString(a.ScaleDownUtilizationThreshold),
			MaxNodeProvisionTime:          optInt(int64(a.MaxNodeProvisionTime)),
		}
		if a.MinSize != nil {
			as.MinSize = types.Int64Value(*a.MinSize)
		}
		// "0.50" configured, "0.5" echoed: keep the configured spelling.
		if prev := m.Autoscaling; prev != nil && thresholdEqual(prev.ScaleDownUtilizationThreshold.ValueString(), a.ScaleDownUtilizationThreshold) {
			as.ScaleDownUtilizationThreshold = prev.ScaleDownUtilizationThreshold
		}
		m.Autoscaling = as
	case m.Autoscaling != nil:
		// Disabled: the API echoes only enabled=false; keep the configured options.
		prev := *m.Autoscaling
		prev.Enabled = types.BoolValue(false)
		for _, v := range []*types.Int64{&prev.MinSize, &prev.MaxSize, &prev.ScaleDownUnneededTime, &prev.MaxNodeProvisionTime} {
			if v.IsUnknown() {
				*v = types.Int64Null()
			}
		}
		if prev.ScaleDownUtilizationThreshold.IsUnknown() {
			prev.ScaleDownUtilizationThreshold = types.StringNull()
		}
		m.Autoscaling = &prev
	}

	if k := p.Kubelet; k == nil || (!k.PlatformDefaults && len(k.SystemReserved) == 0 && len(k.KubeReserved) == 0 && len(k.EvictionHard) == 0) {
		if !kubeletEmpty(m.Kubelet) {
			m.Kubelet = nil
		} // a configured empty block stays as configured
	} else {
		var prev KubeletModel
		if m.Kubelet != nil {
			prev = *m.Kubelet
		}
		pd := types.BoolValue(k.PlatformDefaults)
		if !k.PlatformDefaults && prev.PlatformDefaults.IsNull() {
			pd = types.BoolNull()
		}
		m.Kubelet = &KubeletModel{
			PlatformDefaults: pd,
			SystemReserved:   mapValue(prev.SystemReserved, k.SystemReserved),
			KubeReserved:     mapValue(prev.KubeReserved, k.KubeReserved),
			EvictionHard:     mapValue(prev.EvictionHard, k.EvictionHard),
		}
	}

	// The configured spelling of the same version ("1.35.9" for "v1.35.9") is kept.
	if p.KubernetesVersion != "" && !(m.KubernetesVersion.ValueString() != "" && SameVersion(m.KubernetesVersion.ValueString(), p.KubernetesVersion)) {
		m.KubernetesVersion = types.StringValue(p.KubernetesVersion)
	}
	m.Status = common.OptString(p.Status)
	m.CurrentReplicas = optPtr(p.CurrentReplicas)
	m.BilledReplicas = types.Int64Value(int64(p.BilledReplicas))
}
