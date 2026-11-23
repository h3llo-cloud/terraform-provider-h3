package k8s

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &NodePoolResource{}
	_ resource.ResourceWithConfigure      = &NodePoolResource{}
	_ resource.ResourceWithImportState    = &NodePoolResource{}
	_ resource.ResourceWithModifyPlan     = &NodePoolResource{}
	_ resource.ResourceWithValidateConfig = &NodePoolResource{}
)

// Wait limits of node pool operations; variables so tests can shorten them.
var (
	poolCreateTimeout  = 30 * time.Minute
	poolUpdateTimeout  = 40 * time.Minute
	poolUpgradeTimeout = 60 * time.Minute
	poolDeleteTimeout  = 30 * time.Minute
)

func NewNodePoolResource() resource.Resource { return &NodePoolResource{} }

// NodePoolResource manages one worker pool (a MachineDeployment) of an h3k8s cluster.
type NodePoolResource struct {
	client *client.Client
}

func (r *NodePoolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_k8s_node_pool"
}

func (r *NodePoolResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keepInt := []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}
	optInt := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Optional: true, MarkdownDescription: desc}
	}
	strMap := func(desc string) schema.MapAttribute {
		return schema.MapAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an additional worker pool (node pool) of a managed Kubernetes cluster (`h3_k8s_cluster`): " +
			"a group of identical worker nodes with its own size, autoscaling, labels, taints and kubelet settings. " +
			"`replicas`, `autoscaling`, `labels` and `taint` change in place without touching the nodes; `cpu`, " +
			"`memory_gi`, `disk_gi`, `max_pods` and `kubelet` change **in place by replacing the nodes of the pool one by " +
			"one** (rolling); `name`, `cluster_id`, `project_id` and `disk_profile` replace the pool. The `workers` group of " +
			"`h3_k8s_cluster` is the pool `default` of the cluster: manage it there, not with this resource. Deleting the " +
			"pool drains its nodes. Import: `<cluster_id>/<name>` (provider default project) or " +
			"`<project_id>/<cluster_id>/<name>`." + AccessNote,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "`<cluster_id>/<name>`"},
			"cluster_id": schema.StringAttribute{Required: true, PlanModifiers: replace,
				MarkdownDescription: "ID of the `h3_k8s_cluster`. Changing it replaces the pool."},
			"project_id": common.ProjectIDAttribute("node pool"),
			"name": schema.StringAttribute{Required: true, PlanModifiers: replace,
				MarkdownDescription: "Pool name: 1-20 characters, lowercase letters, digits and `-`, starting with a letter. " +
					"Nodes get the label `h3k8s.io/node-pool=<name>`. Changing it replaces the pool."},
			"cpu": schema.Int64Attribute{Required: true,
				MarkdownDescription: "vCPU per node (2-128). Changed in place by a rolling replacement of the pool nodes."},
			"memory_gi": schema.Int64Attribute{Required: true,
				MarkdownDescription: "Memory per node, GiB (2-512; 1-16 GiB per vCPU). Changed in place (rolling)."},
			"disk_gi": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt,
				MarkdownDescription: "System disk per node, GiB (40-400); platform default when omitted. Changed in place (rolling)."},
			"disk_profile": schema.StringAttribute{Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
				MarkdownDescription: "`replicated` (default, network storage) or `local` (local disk of the host; offered only " +
					"where the platform enables it). Changing it replaces the pool."},
			"replicas": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt,
				MarkdownDescription: "Number of nodes (1-100), changed in place. Required without autoscaling. With " +
					"`autoscaling.enabled = true` leave it unset: the autoscaler owns the node count, `replicas` then " +
					"shows `autoscaling.min_size` and the actual count is `current_replicas` (no drift)."},
			"autoscaling": schema.SingleNestedAttribute{Optional: true,
				MarkdownDescription: "Cluster autoscaler bounds of this pool; changed in place. Removing the block or " +
					"`enabled = false` turns autoscaling off and the pool keeps `replicas` nodes.",
				Attributes: map[string]schema.Attribute{
					"enabled":  schema.BoolAttribute{Required: true, MarkdownDescription: "Enable autoscaling of the pool"},
					"min_size": optInt("Minimum nodes (0-100); required when enabled. `0` lets the pool scale to zero (another pool must keep at least one untainted node)."),
					"max_size": optInt("Maximum nodes (1-100, >= `min_size`); required when enabled."),
					"scale_down_unneeded_time": optInt("Minutes a node must be unneeded before it is removed (1-60); " +
						"the cluster value when omitted."),
					"scale_down_utilization_threshold": schema.StringAttribute{Optional: true,
						MarkdownDescription: "Utilization below which a node may be removed, `\"0.1\"`-`\"0.9\"`; `0.5` when omitted."},
					"max_node_provision_time": optInt("Minutes to wait for a new node before the autoscaler gives up (5-60); 15 when omitted."),
				},
			},
			"labels": strMap("Kubernetes labels of the pool nodes; replaced as a whole in place (existing nodes are " +
				"relabelled). Keys in the `kubernetes.io`, `k8s.io`, `h3k8s.io`, `x-k8s.io` and `h3llo.*` domains are reserved."),
			"max_pods": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt,
				MarkdownDescription: "Maximum pods per node (10-250); platform default (110) when omitted. Changed in place " +
					"(rolling). Removing it keeps the current value."},
			"kubelet": schema.SingleNestedAttribute{Optional: true,
				MarkdownDescription: "Kubelet resource reservations of the pool nodes; changed in place (rolling). Removing " +
					"the block removes the reservations.",
				Attributes: map[string]schema.Attribute{
					"platform_defaults": schema.BoolAttribute{Optional: true,
						MarkdownDescription: "Platform reservations: `kube-reserved` by node size, `system-reserved` " +
							"100m/200Mi, `eviction-hard` `memory.available<200Mi,nodefs.available<10%,imagefs.available<15%`. " +
							"The maps below override single keys."},
					"system_reserved": strMap("`--system-reserved`, e.g. `{ cpu = \"100m\", memory = \"200Mi\" }`"),
					"kube_reserved":   strMap("`--kube-reserved`, e.g. `{ cpu = \"100m\", memory = \"1Gi\" }`"),
					"eviction_hard":   strMap("`--eviction-hard`, e.g. `{ \"memory.available\" = \"200Mi\" }`"),
				},
			},
			"kubernetes_version": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Kubernetes version of the pool nodes. A new pool runs the control plane version, and " +
					"an upgrade of `h3_k8s_cluster` upgrades every pool, so usually leave it unset or set it to " +
					"`h3_k8s_cluster.<name>.kubernetes_version`. Setting a newer version upgrades the pool **in place** " +
					"(`POST .../upgrade`; at most the control plane version, one step at a time); a downgrade is refused at plan time."},
			"status": schema.StringAttribute{Computed: true,
				MarkdownDescription: "Pool status: `READY`, `PROVISIONING`, `SCALING`, `ROLLING_OUT`, `DELETING`, `UNKNOWN`"},
			"current_replicas": schema.Int64Attribute{Computed: true,
				MarkdownDescription: "Nodes the pool has now (set by the autoscaler when autoscaling is on)"},
			"billed_replicas": schema.Int64Attribute{Computed: true, MarkdownDescription: "Nodes billed now"},
		},
		Blocks: map[string]schema.Block{
			"taint": schema.ListNestedBlock{
				MarkdownDescription: "Taint of the pool nodes (repeatable); the list is replaced as a whole in place. At least " +
					"one pool of the cluster must stay without `NoSchedule`/`NoExecute` taints for system workloads.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"key":    schema.StringAttribute{Required: true, MarkdownDescription: "Taint key"},
					"value":  schema.StringAttribute{Optional: true, MarkdownDescription: "Taint value"},
					"effect": schema.StringAttribute{Required: true, MarkdownDescription: "`NoSchedule`, `PreferNoSchedule` or `NoExecute`"},
				}},
			},
		},
	}
}

func (r *NodePoolResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

// knowable is any framework value.
type knowable interface {
	IsNull() bool
	IsUnknown() bool
}

// known: the value is set and known at plan time.
func known(v knowable) bool { return !v.IsNull() && !v.IsUnknown() }

func (r *NodePoolResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var c NodePoolModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &c)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, msg := range validateNodePool(&c) {
		resp.Diagnostics.AddAttributeError(msg.path, msg.summary, msg.detail)
	}
	if known(c.Name) && c.Name.ValueString() == DefaultNodePoolName {
		resp.Diagnostics.AddAttributeWarning(path.Root("name"), "Pool \"default\" is managed by h3_k8s_cluster",
			"The workers group of h3_k8s_cluster is the pool \"default\". Managing it here as well makes both resources fight "+
				"over its size; use h3_k8s_cluster.workers/autoscaler for it.")
	}
}

type configError struct {
	path            path.Path
	summary, detail string
}

// validateNodePool checks the h3k8s pool rules on known values.
func validateNodePool(c *NodePoolModel) []configError {
	var errs []configError
	add := func(p path.Path, s, d string) { errs = append(errs, configError{p, s, d}) }
	rng := func(v types.Int64, p path.Path, name string, lo, hi int64) {
		if known(v) && (v.ValueInt64() < lo || v.ValueInt64() > hi) {
			add(p, "Invalid "+name, fmt.Sprintf("must be %d-%d", lo, hi))
		}
	}
	if known(c.Name) && !poolNameRe.MatchString(c.Name.ValueString()) {
		add(path.Root("name"), "Invalid name", "1-20 characters, lowercase letters, digits and '-', starting with a letter and not ending with '-'")
	}
	rng(c.CPU, path.Root("cpu"), "cpu", 2, 128)
	rng(c.MemoryGi, path.Root("memory_gi"), "memory_gi", 2, 512)
	if known(c.CPU) && known(c.MemoryGi) && (c.MemoryGi.ValueInt64() < c.CPU.ValueInt64() || c.MemoryGi.ValueInt64() > 16*c.CPU.ValueInt64()) {
		add(path.Root("memory_gi"), "Invalid memory_gi", "memory must be 1-16 GiB per vCPU")
	}
	rng(c.DiskGi, path.Root("disk_gi"), "disk_gi", 40, 400)
	rng(c.MaxPods, path.Root("max_pods"), "max_pods", 10, 250)
	if known(c.DiskProfile) && c.DiskProfile.ValueString() != "replicated" && c.DiskProfile.ValueString() != "local" {
		add(path.Root("disk_profile"), "Invalid disk_profile", "must be replicated or local")
	}
	a := c.Autoscaling
	switch {
	case a != nil && a.Enabled.IsUnknown():
	case poolAutoscaled(a):
		if !c.Replicas.IsNull() {
			add(path.Root("replicas"), "replicas with autoscaling",
				"With autoscaling.enabled = true the autoscaler owns the node count: set autoscaling.min_size and leave replicas unset.")
		}
		if a.MinSize.IsNull() {
			add(path.Root("autoscaling").AtName("min_size"), "Missing autoscaling.min_size", "min_size is required when autoscaling is enabled")
		}
		if a.MaxSize.IsNull() {
			add(path.Root("autoscaling").AtName("max_size"), "Missing autoscaling.max_size", "max_size is required when autoscaling is enabled")
		}
		rng(a.MinSize, path.Root("autoscaling").AtName("min_size"), "autoscaling.min_size", 0, 100)
		rng(a.MaxSize, path.Root("autoscaling").AtName("max_size"), "autoscaling.max_size", 1, 100)
		if known(a.MinSize) && known(a.MaxSize) && a.MaxSize.ValueInt64() < a.MinSize.ValueInt64() {
			add(path.Root("autoscaling").AtName("max_size"), "Invalid autoscaling.max_size", "max_size must be >= min_size")
		}
		rng(a.ScaleDownUnneededTime, path.Root("autoscaling").AtName("scale_down_unneeded_time"), "autoscaling.scale_down_unneeded_time", 1, 60)
		rng(a.MaxNodeProvisionTime, path.Root("autoscaling").AtName("max_node_provision_time"), "autoscaling.max_node_provision_time", 5, 60)
		if t := a.ScaleDownUtilizationThreshold; known(t) {
			if f, err := strconv.ParseFloat(t.ValueString(), 64); err != nil || f < 0.1 || f > 0.9 {
				add(path.Root("autoscaling").AtName("scale_down_utilization_threshold"), "Invalid scale_down_utilization_threshold", "must be a number 0.1-0.9, e.g. \"0.5\"")
			}
		}
	default:
		if c.Replicas.IsNull() {
			add(path.Root("replicas"), "Missing replicas", "replicas is required unless autoscaling is enabled")
		}
		rng(c.Replicas, path.Root("replicas"), "replicas", 1, 100)
	}
	for i, t := range c.Taints {
		if known(t.Effect) && !oneOf(t.Effect.ValueString(), taintEffects) {
			add(path.Root("taint").AtListIndex(i).AtName("effect"), "Invalid taint effect", "must be one of "+strings.Join(taintEffects, ", "))
		}
	}
	return errs
}

func oneOf(v string, vs []string) bool {
	for _, x := range vs {
		if x == v {
			return true
		}
	}
	return false
}

// ModifyPlan: project default; with autoscaling replicas follows min_size;
// a version downgrade is refused.
func (r *NodePoolResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
	if req.Plan.Raw.IsNull() || resp.Diagnostics.HasError() {
		return
	}
	var plan NodePoolModel
	resp.Diagnostics.Append(resp.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if a := plan.Autoscaling; poolAutoscaled(a) {
		// The API reports replicas = min_size for an autoscaled pool.
		v := a.MinSize
		if v.IsNull() {
			v = types.Int64Unknown()
		}
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("replicas"), v)...)
	}
	if req.State.Raw.IsNull() {
		return
	}
	var state NodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !known(plan.KubernetesVersion) {
		return
	}
	if c, ok := CompareVersions(plan.KubernetesVersion.ValueString(), state.KubernetesVersion.ValueString()); ok && c < 0 {
		resp.Diagnostics.AddAttributeError(path.Root("kubernetes_version"), "Kubernetes downgrade is not supported",
			fmt.Sprintf("The pool runs %s; %s is older. Node pools can only be upgraded (up to the control plane version).",
				state.KubernetesVersion.ValueString(), plan.KubernetesVersion.ValueString()))
	}
}

func poolBase(cluster string) string { return apiBase + "/clusters/" + cluster + "/node-pools" }

func (r *NodePoolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, config NodePoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, cluster, name := plan.ProjectID.ValueString(), plan.ClusterID.ValueString(), plan.Name.ValueString()
	q := map[string]string{"projectId": project}
	body := createPoolBody(&plan)
	err := r.mutate(ctx, project, cluster, "", func() error {
		return r.client.Do(ctx, "POST", poolBase(cluster), q, body, nil)
	})
	if err != nil {
		detail := poolAPIError(err, true)
		if h, ok := client.AsHTTPError(err); ok && h.IsConflict() && strings.Contains(h.Body, "already exists") {
			detail += fmt.Sprintf("\n\nImport the existing pool with: terraform import <address> %s/%s/%s", project, cluster, name)
		}
		resp.Diagnostics.AddError("Error creating Kubernetes node pool", detail)
		return
	}
	// Track the pool right away: if waiting fails it is tainted, not orphaned.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), cluster+"/"+name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), cluster)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)

	got, err := r.waitReady(ctx, project, cluster, name, poolCreateTimeout, "to become READY", nil)
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for Kubernetes node pool", "Node pool created but not ready: "+err.Error())
		return
	}
	// A new pool runs the control plane version; a configured newer one is an upgrade.
	if v := config.KubernetesVersion; known(v) && !SameVersion(v.ValueString(), got.KubernetesVersion) {
		from := got.KubernetesVersion
		if got, err = r.upgrade(ctx, project, cluster, name, v.ValueString()); err != nil {
			resp.Diagnostics.AddError("Error upgrading Kubernetes node pool",
				"Node pool created with the control plane version "+from+"; the upgrade to "+v.ValueString()+" failed: "+
					poolAPIError(err, false)+"\n\nA new pool always runs the control plane version: leave kubernetes_version "+
					"unset or set it to the cluster version.")
			return
		}
	}
	plan.applyAPI(got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *NodePoolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state NodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := getNodePool(ctx, r.client, state.ProjectID.ValueString(), state.ClusterID.ValueString(), state.Name.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading Kubernetes node pool", poolAPIError(err, false))
		return
	}
	state.applyAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *NodePoolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state, config NodePoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, cluster, name := state.ProjectID.ValueString(), state.ClusterID.ValueString(), state.Name.ValueString()
	fail := func(what string, err error) {
		resp.Diagnostics.AddError("Error updating Kubernetes node pool", what+": "+poolAPIError(err, false))
	}

	// 1. Upgrade first: rolling changes are refused while the pool rolls out.
	if v := config.KubernetesVersion; known(v) && !SameVersion(v.ValueString(), state.KubernetesVersion.ValueString()) {
		if _, err := r.upgrade(ctx, project, cluster, name, v.ValueString()); err != nil {
			fail("upgrade to "+NormalizeVersion(v.ValueString()), err)
			return
		}
	}
	// 2. Everything else in one PATCH.
	if u := updatePoolBody(&plan, &state); !u.Empty() {
		err := r.mutate(ctx, project, cluster, name, func() error {
			return r.client.Do(ctx, "PATCH", poolBase(cluster)+"/"+name, map[string]string{"projectId": project}, u, nil)
		})
		if err != nil {
			fail("PATCH", err)
			return
		}
		if _, err := r.waitReady(ctx, project, cluster, name, poolUpdateTimeout, "to apply the change", func(p *NodePool) bool {
			return poolMatches(p, u)
		}); err != nil {
			fail("waiting for the pool", err)
			return
		}
	}
	got, err := getNodePool(ctx, r.client, project, cluster, name)
	if err != nil {
		resp.Diagnostics.AddError("Error reading Kubernetes node pool", poolAPIError(err, false))
		return
	}
	planned := plan.KubernetesVersion
	plan.applyAPI(got)
	// Not configured: a cluster upgrade in the same apply may have moved the
	// pool already; the planned value is kept and the next refresh records it.
	if config.KubernetesVersion.IsNull() && known(planned) {
		plan.KubernetesVersion = planned
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *NodePoolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state NodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, cluster, name := state.ProjectID.ValueString(), state.ClusterID.ValueString(), state.Name.ValueString()
	err := r.mutate(ctx, project, cluster, name, func() error {
		return r.client.Do(ctx, "DELETE", poolBase(cluster)+"/"+name, map[string]string{"projectId": project}, nil, nil)
	})
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Error deleting Kubernetes node pool", poolAPIError(err, false))
		return
	}
	err = common.WaitFor(ctx, poolDeleteTimeout, "node pool "+name+" to be deleted", func(ctx context.Context) (bool, error) {
		_, err := getNodePool(ctx, r.client, project, cluster, name)
		if client.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for Kubernetes node pool deletion", err.Error())
	}
}

// ImportState: "<cluster_id>/<name>" (provider default project) or
// "<project_id>/<cluster_id>/<name>".
func (r *NodePoolResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, cluster, name, err := parsePoolImportID(req.ID, func() string {
		if r.client == nil {
			return ""
		}
		return r.client.ProjectID()
	})
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), cluster)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), cluster+"/"+name)...)
}

func parsePoolImportID(raw string, defaultProject func() string) (project, cluster, name string, err error) {
	parts := strings.Split(raw, "/")
	for _, p := range parts {
		if p == "" {
			parts = nil
			break
		}
	}
	switch len(parts) {
	case 3:
		return parts[0], parts[1], parts[2], nil
	case 2:
		if project = defaultProject(); project == "" {
			return "", "", "", fmt.Errorf("import id %q has no project: use \"<project_id>/<cluster_id>/<name>\" or set the provider project_id", raw)
		}
		return project, parts[0], parts[1], nil
	}
	return "", "", "", fmt.Errorf("expected import id \"<cluster_id>/<name>\" or \"<project_id>/<cluster_id>/<name>\", got %q", raw)
}

// ---- helpers ----

func getNodePool(ctx context.Context, c *client.Client, project, cluster, name string) (*NodePool, error) {
	var out NodePool
	if err := c.Do(ctx, "GET", poolBase(cluster)+"/"+name, map[string]string{"projectId": project}, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func listNodePools(ctx context.Context, c *client.Client, project, cluster string) ([]NodePool, error) {
	var out NodePoolsResponse
	if err := c.Do(ctx, "GET", poolBase(cluster), map[string]string{"projectId": project}, nil, &out); err != nil {
		return nil, err
	}
	return out.NodePools, nil
}

// poolAPIError is apiError plus the hint that a 404 on the collection can
// also mean a platform without node pools.
func poolAPIError(err error, collection bool) string {
	msg := apiError(err, collection)
	if h, ok := client.AsHTTPError(err); ok && h.StatusCode == 404 && collection {
		msg += "\n\nA 404 here can also mean that the cluster does not exist or that the platform does not offer " +
			"node pools yet (h3k8s without /node-pools)."
	}
	return msg
}

// poolSettled: the pool is READY with all desired nodes ready.
func poolSettled(p *NodePool) bool {
	if p.Status != PoolReady {
		return false
	}
	if p.DesiredReplicas != nil && p.ReadyReplicas != nil && *p.ReadyReplicas < *p.DesiredReplicas {
		return false
	}
	return true
}

// poolMatches: the spec of p shows the PATCH u (the API echoes the spec at once).
func poolMatches(p *NodePool, u UpdateNodePoolRequest) bool {
	eq := func(want *int64, got flexInt) bool { return want == nil || int64(got) == *want }
	return eq(u.CPU, p.CPU) && eq(u.MemoryGi, p.MemoryGi) && eq(u.DiskGi, p.DiskGi) &&
		(u.Replicas == nil || p.Autoscaling != nil && p.Autoscaling.Enabled || int64(p.Replicas) == *u.Replicas)
}

// waitReady waits until the pool is READY (all desired nodes ready) and cond
// holds; the cluster must exist and not be in an error state.
func (r *NodePoolResource) waitReady(ctx context.Context, project, cluster, name string, timeout time.Duration, what string, cond func(*NodePool) bool) (*NodePool, error) {
	var last *NodePool
	err := common.WaitFor(ctx, timeout, "node pool "+name+" "+what, func(ctx context.Context) (bool, error) {
		p, err := getNodePool(ctx, r.client, project, cluster, name)
		if err != nil {
			return false, err
		}
		last = p
		if strings.Contains(p.Status, "ERROR") {
			return false, fmt.Errorf("node pool entered %s state", p.Status)
		}
		return poolSettled(p) && (cond == nil || cond(p)), nil
	})
	return last, err
}

// waitClusterReady waits for the cluster to be READY (pool operations need it).
func (r *NodePoolResource) waitClusterReady(ctx context.Context, project, cluster string) error {
	return common.WaitFor(ctx, poolUpdateTimeout, "cluster "+cluster+" to become READY", func(ctx context.Context) (bool, error) {
		c, err := getCluster(ctx, r.client, project, cluster)
		if err != nil {
			return false, err
		}
		if isErrorStatus(c.Status) {
			return false, fmt.Errorf("cluster entered %s state", c.Status)
		}
		return c.Status == "READY", nil
	})
}

// mutate runs a change of the pool. A 409 (cluster not READY, pool busy with a
// rollout or scaling) is retried after the cluster and the pool (when name is
// set) are ready again; "already exists" is final.
func (r *NodePoolResource) mutate(ctx context.Context, project, cluster, name string, do func() error) error {
	for attempt := 0; ; attempt++ {
		err := do()
		h, ok := client.AsHTTPError(err)
		if !ok || !h.IsConflict() || strings.Contains(h.Body, "already exists") || attempt >= 5 {
			return err
		}
		if err := r.waitClusterReady(ctx, project, cluster); err != nil {
			return err
		}
		if name != "" {
			if _, err := r.waitReady(ctx, project, cluster, name, poolUpdateTimeout, "to become READY before the change", nil); err != nil {
				return err
			}
		}
	}
}

// upgrade moves the pool to version and waits for the rollout.
func (r *NodePoolResource) upgrade(ctx context.Context, project, cluster, name, version string) (*NodePool, error) {
	target := NormalizeVersion(version)
	cur, err := getNodePool(ctx, r.client, project, cluster, name)
	if err != nil {
		return nil, err
	}
	if SameVersion(cur.KubernetesVersion, target) {
		// Already there (the cluster upgrade in the same apply moved it).
		return r.waitReady(ctx, project, cluster, name, poolUpgradeTimeout, "upgrade to "+target, nil)
	}
	err = r.mutate(ctx, project, cluster, name, func() error {
		return r.client.Do(ctx, "POST", poolBase(cluster)+"/"+name+"/upgrade", map[string]string{"projectId": project},
			map[string]string{"kubernetesVersion": target}, nil)
	})
	if err != nil {
		return cur, err
	}
	return r.waitReady(ctx, project, cluster, name, poolUpgradeTimeout, "upgrade to "+target, func(p *NodePool) bool {
		return SameVersion(p.KubernetesVersion, target)
	})
}
