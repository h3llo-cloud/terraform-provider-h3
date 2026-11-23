package k8s

import (
	"context"
	"fmt"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &ClusterResource{}
	_ resource.ResourceWithConfigure      = &ClusterResource{}
	_ resource.ResourceWithImportState    = &ClusterResource{}
	_ resource.ResourceWithModifyPlan     = &ClusterResource{}
	_ resource.ResourceWithValidateConfig = &ClusterResource{}
)

// Wait limits; variables so tests can shorten them.
var (
	createTimeout  = 40 * time.Minute
	upgradeTimeout = 60 * time.Minute
	updateTimeout  = 30 * time.Minute
	deleteTimeout  = 30 * time.Minute
)

func NewClusterResource() resource.Resource { return &ClusterResource{} }

// ClusterResource manages an h3k8s cluster with its single worker group.
type ClusterResource struct {
	client *client.Client
}

type ClusterModel struct {
	ID                   types.String      `tfsdk:"id"`
	ProjectID            types.String      `tfsdk:"project_id"`
	Name                 types.String      `tfsdk:"name"`
	KubernetesVersion    types.String      `tfsdk:"kubernetes_version"`
	ClusterType          types.String      `tfsdk:"cluster_type"`
	ControlPlaneReplicas types.Int64       `tfsdk:"control_plane_replicas"`
	PodCIDR              types.String      `tfsdk:"pod_cidr"`
	ServiceCIDR          types.String      `tfsdk:"service_cidr"`
	Workers              *WorkersModel     `tfsdk:"workers"`
	Autoscaler           *AutoscalerModel  `tfsdk:"autoscaler"`
	MaintenanceWindow    *MaintenanceModel `tfsdk:"maintenance_window"`
	NodeLocalDNSCache    types.Bool        `tfsdk:"node_local_dns_cache"`
	Status               types.String      `tfsdk:"status"`
	Namespace            types.String      `tfsdk:"namespace"`
	CreatedAt            types.String      `tfsdk:"created_at"`
	Kubeconfig           types.String      `tfsdk:"kubeconfig"`
	Endpoint             types.String      `tfsdk:"endpoint"`
	ClusterCACertificate types.String      `tfsdk:"cluster_ca_certificate"`
	APIAllowedCIDRs      types.Set         `tfsdk:"api_allowed_cidrs"`
	NetworkID            types.String      `tfsdk:"network_id"`
	Network              types.Object      `tfsdk:"network"`
}

// clusterNetworkAttrTypes is the computed `network` object: mode and network_id of the worker network.
var clusterNetworkAttrTypes = map[string]attr.Type{"mode": types.StringType, "network_id": types.StringType}

// clusterNetworkObject maps ClusterResponse.network; null when h3k8s does not report it.
func clusterNetworkObject(n *ClusterNetworkDTO) types.Object {
	if n == nil || n.Mode == "" {
		return types.ObjectNull(clusterNetworkAttrTypes)
	}
	return types.ObjectValueMust(clusterNetworkAttrTypes, map[string]attr.Value{
		"mode":       types.StringValue(n.Mode),
		"network_id": common.OptString(n.NetworkID),
	})
}

type WorkersModel struct {
	Replicas types.Int64 `tfsdk:"replicas"`
	CPU      types.Int64 `tfsdk:"cpu"`
	MemoryGi types.Int64 `tfsdk:"memory_gi"`
	DiskGi   types.Int64 `tfsdk:"disk_gi"`
}

type AutoscalerModel struct {
	Enabled                  types.Bool   `tfsdk:"enabled"`
	MaxSize                  types.Int64  `tfsdk:"max_size"`
	ScaleDownDelayAfterAdd   types.Int64  `tfsdk:"scale_down_delay_after_add"`
	ScaleDownUnneededTime    types.Int64  `tfsdk:"scale_down_unneeded_time"`
	Expander                 types.String `tfsdk:"expander"`
	BalanceSimilarNodeGroups types.Bool   `tfsdk:"balance_similar_node_groups"`
}

type MaintenanceModel struct {
	Enabled     types.Bool                  `tfsdk:"enabled"`
	DayOfWeek   types.String                `tfsdk:"day_of_week"`
	StartHour   types.Int64                 `tfsdk:"start_hour"`
	AutoUpgrade types.String                `tfsdk:"auto_upgrade"`
	Exclusions  []MaintenanceExclusionModel `tfsdk:"exclusions"`
}

type MaintenanceExclusionModel struct {
	StartDate types.String `tfsdk:"start_date"`
	EndDate   types.String `tfsdk:"end_date"`
	Reason    types.String `tfsdk:"reason"`
}

func (r *ClusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_k8s_cluster"
}

func (r *ClusterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keepInt := []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a managed Kubernetes cluster (h3k8s) with its worker group `workers` (the node pool " +
			"`default`; add more pools with `h3_k8s_node_pool`). " +
			"`kubernetes_version` is upgraded **in place** (one step: a newer patch or the next minor; no downgrade), " +
			"`control_plane_replicas`, `workers.replicas`, `workers.cpu`/`workers.memory_gi`, `autoscaler`, " +
			"`maintenance_window`, `node_local_dns_cache` and `api_allowed_cidrs` change in place; `name`, `project_id`, `cluster_type`, `pod_cidr`, `service_cidr` and " +
			"`workers.disk_gi` replace the cluster. `kubeconfig` is sensitive and stored in the state. Import: `<project_id>/<cluster_id>`." + AccessNote,
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "Cluster ID"},
			"project_id": common.ProjectIDAttribute("cluster"),
			"name": schema.StringAttribute{Required: true, PlanModifiers: replace,
				MarkdownDescription: "Cluster name (Kubernetes DNS label). Changing it replaces the cluster."},
			"kubernetes_version": schema.StringAttribute{Required: true,
				MarkdownDescription: "Kubernetes version, e.g. `v1.35.9` (`1.35.9` is the same version). See the " +
					"`h3_k8s_versions` data source for the versions of the platform. A change **upgrades the cluster in place** " +
					"(control plane first, then workers), it never recreates it. Allowed: a newer patch of the current minor or " +
					"the next minor; a downgrade is refused at plan time."},
			"cluster_type": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("DEV"),
				PlanModifiers:       replace,
				MarkdownDescription: "`DEV` (default) or `PROD`; limits the control plane and worker replicas. Changing it replaces the cluster."},
			"control_plane_replicas": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(1),
				MarkdownDescription: "Control plane replicas: 1 (default), 3 or 5. Changed in place."},
			"pod_cidr": schema.StringAttribute{Optional: true, Computed: true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()},
				MarkdownDescription: "Pod CIDR; platform default when omitted. Changing it replaces the cluster."},
			"service_cidr": schema.StringAttribute{Optional: true, Computed: true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()},
				MarkdownDescription: "Service CIDR; platform default when omitted. Changing it replaces the cluster."},
			"network_id": schema.StringAttribute{Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()},
				MarkdownDescription: "ID of an h3ovn network (`h3_network`) of the project VPC: the worker nodes run in that subnet, " +
					"reach the resources of the VPC (databases, VMs) by private addresses and go to the internet through the " +
					"gateway of the network. Omitted = the platform default: the project network `default` where workers in " +
					"the VPC are enabled for the project (it is created with internet access when missing), otherwise the " +
					"shared platform network. A Service of type LoadBalancer with the annotation `h3.io/lb-internal: \"true\"` " +
					"gets an address of this network instead of a public IP. Changing it replaces the cluster."},
			"network": schema.SingleNestedAttribute{Computed: true,
				PlanModifiers:       []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Worker network reported by the platform; null on platforms without workers in the tenant VPC.",
				Attributes: map[string]schema.Attribute{
					"mode": schema.StringAttribute{Computed: true,
						MarkdownDescription: "`vpc` (a network of the project VPC) or `shared` (shared platform network)"},
					"network_id": schema.StringAttribute{Computed: true,
						MarkdownDescription: "h3ovn network ID when `mode` is `vpc`"},
				},
			},
			"workers": schema.SingleNestedAttribute{
				Required: true,
				MarkdownDescription: "The worker group: the node pool `default` of the cluster (one MachineDeployment). " +
					"Further pools with their own size, labels, taints and autoscaling are `h3_k8s_node_pool` resources; " +
					"`workers` and `autoscaler` always describe the pool `default`.",
				Attributes: map[string]schema.Attribute{
					"replicas": schema.Int64Attribute{Required: true,
						MarkdownDescription: "Number of worker nodes, changed in place. With `autoscaler.enabled = true` the " +
							"autoscaler owns the actual count: changes it makes are not shown as drift, and this value is sent " +
							"only when you change it."},
					"cpu": schema.Int64Attribute{Required: true,
						MarkdownDescription: "vCPU per worker (2-128). Changed in place (rolling replacement of the nodes)."},
					"memory_gi": schema.Int64Attribute{Required: true,
						MarkdownDescription: "Memory per worker, GiB (2-512). Changed in place together with `cpu`."},
					"disk_gi": schema.Int64Attribute{Required: true,
						PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
						MarkdownDescription: "System disk per worker, GiB. Changing it replaces the cluster (the API has no update)."},
				},
			},
			"autoscaler": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Cluster autoscaler of the worker group (pool `default`) and the cluster-wide autoscaler " +
					"settings; changed in place. Removing the block disables autoscaling of `workers`. The autoscaling of " +
					"other pools is set on `h3_k8s_node_pool`.",
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{Required: true, MarkdownDescription: "Enable the autoscaler"},
					"max_size": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt,
						MarkdownDescription: "Maximum workers (2-100); required when enabled"},
					"scale_down_delay_after_add": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt,
						MarkdownDescription: "Minutes after a scale-up before scale-down is considered (1-60)"},
					"scale_down_unneeded_time": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt,
						MarkdownDescription: "Minutes a node must be unneeded before it is removed (1-60)"},
					"expander": schema.StringAttribute{Optional: true,
						MarkdownDescription: "Which pool the autoscaler grows when several fit (cluster-wide): `random` " +
							"(platform default), `least-waste` or `most-pods`. Takes effect on clusters with node pools " +
							"(`h3_k8s_node_pool`); can also be set with `enabled = false` when only other pools autoscale. " +
							"The API does not return it, so changes made outside Terraform are not detected."},
					"balance_similar_node_groups": schema.BoolAttribute{Optional: true,
						MarkdownDescription: "Keep pools with the same node size and labels at the same size (cluster-wide). " +
							"Same notes as `expander`."},
				},
			},
			"maintenance_window": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Weekly maintenance window for platform-initiated work; changed in place. Removing the block disables it.",
				Attributes: map[string]schema.Attribute{
					"enabled":     schema.BoolAttribute{Required: true, MarkdownDescription: "Enable the window"},
					"day_of_week": schema.StringAttribute{Optional: true, MarkdownDescription: "`Monday` .. `Sunday`"},
					"start_hour":  schema.Int64Attribute{Optional: true, MarkdownDescription: "Start hour, UTC (0-23)"},
					"auto_upgrade": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("minor"),
						MarkdownDescription: "What the window upgrades automatically: `minor` (default; the latest patch of the current minor, else the next minor, one step per window), `patch` (only patch versions of the current minor; a new minor only by an explicit `kubernetes_version` change), `none`"},
					"exclusions": schema.ListNestedAttribute{
						Optional:            true,
						MarkdownDescription: "Periods without automatic upgrades (up to 10, each at most 90 days, not in the past). Removing the attribute clears them.",
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"start_date": schema.StringAttribute{Required: true, MarkdownDescription: "First day, `YYYY-MM-DD` (UTC)"},
							"end_date":   schema.StringAttribute{Required: true, MarkdownDescription: "Last day, `YYYY-MM-DD` (UTC, inclusive)"},
							"reason":     schema.StringAttribute{Optional: true, MarkdownDescription: "Free text, up to 200 characters"},
						}},
					},
				},
			},
			"node_local_dns_cache": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Enables NodeLocal DNSCache: a caching DNS server on every node. DNS queries of pods to " +
					"kube-dns are redirected to the cache on the same node with a CiliumLocalRedirectPolicy; pods with " +
					"`hostNetwork` and the nodes themselves bypass the cache. Changed **in place** (default `false`); the " +
					"provider waits until the cache is `READY` (or `DISABLED` when turned off). The first enablement on an " +
					"older cluster does a rolling restart of the Cilium agents. The platform refuses it with " +
					"`NODE_LOCAL_DNS_UNSUPPORTED` when the Kubernetes/Cilium version is too old and with " +
					"`NODE_LOCAL_DNS_CONFLICT` when the cluster already runs its own node-local-dns or a local redirect " +
					"policy for kube-dns."},
			"api_allowed_cidrs": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType,
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Source IPv4 networks allowed to connect to the Kubernetes API, as network addresses with a " +
					"prefix (`203.0.113.0/24`, `198.51.100.7/32`; at most 50). `[]` opens the API to everyone; when omitted the " +
					"current list is kept. The cluster's own nodes and the platform are always allowed. Changed in place, " +
					"without a restart: new connections are checked within about a second, established ones are kept."},
			"status":     schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "Cluster status (`CREATING`, `READY`, `UPGRADING`, `UPDATING`, `DELETING`, `ERROR`, ...)"},
			"namespace":  schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "Platform namespace of the cluster"},
			"created_at": schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "Creation time"},
			"kubeconfig": schema.StringAttribute{Computed: true, Sensitive: true, PlanModifiers: keep,
				MarkdownDescription: "Admin kubeconfig (YAML). Sensitive; it is stored in the Terraform state, so protect the state."},
			"endpoint": schema.StringAttribute{Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Kubernetes API server URL from the kubeconfig"},
			"cluster_ca_certificate": schema.StringAttribute{Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Cluster CA certificate (PEM) from the kubeconfig"},
		},
	}
}

func (r *ClusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *ClusterResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var c ClusterModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &c)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if v := c.ControlPlaneReplicas; !v.IsNull() && !v.IsUnknown() {
		if n := v.ValueInt64(); n != 1 && n != 3 && n != 5 {
			resp.Diagnostics.AddAttributeError(path.Root("control_plane_replicas"), "Invalid control_plane_replicas", "must be 1, 3 or 5")
		}
	}
	if t := c.ClusterType; !t.IsNull() && !t.IsUnknown() && t.ValueString() != "DEV" && t.ValueString() != "PROD" {
		resp.Diagnostics.AddAttributeError(path.Root("cluster_type"), "Invalid cluster_type", "must be DEV or PROD")
	}
	if v := c.KubernetesVersion; !v.IsNull() && !v.IsUnknown() {
		if _, ok := parseVersion(v.ValueString()); !ok {
			resp.Diagnostics.AddAttributeError(path.Root("kubernetes_version"), "Invalid kubernetes_version", "expected a version like v1.35.9")
		}
	}
	if w := c.Workers; w != nil {
		if v := w.CPU; !v.IsNull() && !v.IsUnknown() && (v.ValueInt64() < 2 || v.ValueInt64() > 128) {
			resp.Diagnostics.AddAttributeError(path.Root("workers").AtName("cpu"), "Invalid workers.cpu", "must be 2-128")
		}
		if v := w.MemoryGi; !v.IsNull() && !v.IsUnknown() && (v.ValueInt64() < 2 || v.ValueInt64() > 512) {
			resp.Diagnostics.AddAttributeError(path.Root("workers").AtName("memory_gi"), "Invalid workers.memory_gi", "must be 2-512")
		}
	}
	if a := c.Autoscaler; a != nil && a.Enabled.ValueBool() && a.MaxSize.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("autoscaler").AtName("max_size"), "Missing autoscaler.max_size", "max_size is required when the autoscaler is enabled")
	}
	if v := c.APIAllowedCIDRs; !v.IsNull() && !v.IsUnknown() {
		var cidrs []string
		resp.Diagnostics.Append(v.ElementsAs(ctx, &cidrs, false)...)
		if err := ValidateAllowedCIDRs(cidrs); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("api_allowed_cidrs"), "Invalid api_allowed_cidrs", err.Error())
		}
	}
	if a := c.Autoscaler; a != nil && !a.Expander.IsNull() && !a.Expander.IsUnknown() && !oneOf(a.Expander.ValueString(), autoscalerExpanders) {
		resp.Diagnostics.AddAttributeError(path.Root("autoscaler").AtName("expander"), "Invalid autoscaler.expander", "must be random, least-waste or most-pods")
	}
	if m := c.MaintenanceWindow; m != nil && !m.StartHour.IsNull() && !m.StartHour.IsUnknown() && (m.StartHour.ValueInt64() < 0 || m.StartHour.ValueInt64() > 23) {
		resp.Diagnostics.AddAttributeError(path.Root("maintenance_window").AtName("start_hour"), "Invalid start_hour", "must be 0-23")
	}
}

// ModifyPlan refuses a downgrade at plan time; an upgrade is an in-place update.
func (r *ClusterResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state ClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || plan.KubernetesVersion.IsUnknown() {
		return
	}
	if c, ok := CompareVersions(plan.KubernetesVersion.ValueString(), state.KubernetesVersion.ValueString()); ok && c < 0 {
		resp.Diagnostics.AddAttributeError(path.Root("kubernetes_version"), "Kubernetes downgrade is not supported",
			fmt.Sprintf("The cluster runs %s; %s is older. Kubernetes clusters can only be upgraded (a newer patch or the next minor).",
				state.KubernetesVersion.ValueString(), plan.KubernetesVersion.ValueString()))
	}
}

func (r *ClusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := CreateClusterRequest{
		ProjectID:            plan.ProjectID.ValueString(),
		Name:                 plan.Name.ValueString(),
		ControlPlaneReplicas: plan.ControlPlaneReplicas.ValueInt64(),
		ClusterType:          plan.ClusterType.ValueString(),
		KubernetesVersion:    NormalizeVersion(plan.KubernetesVersion.ValueString()),
		PodCIDR:              common.KnownString(plan.PodCIDR),
		ServiceCIDR:          common.KnownString(plan.ServiceCIDR),
		NetworkID:            common.KnownString(plan.NetworkID),
		Workers: WorkersBody{
			Replicas: plan.Workers.Replicas.ValueInt64(),
			CPU:      plan.Workers.CPU.ValueInt64(),
			MemoryGi: plan.Workers.MemoryGi.ValueInt64(),
			DiskGi:   plan.Workers.DiskGi.ValueInt64(),
		},
		Autoscaler:        autoscalerBody(plan.Autoscaler),
		MaintenanceWindow: maintenanceBody(plan.MaintenanceWindow),
	}
	// Sent only when enabled, so that backends without the feature still work.
	if plan.NodeLocalDNSCache.ValueBool() {
		body.NodeLocalDNSCache = &NodeLocalDNSCacheDTO{Enabled: true}
	}
	if cidrs, ok := knownCIDRs(ctx, plan.APIAllowedCIDRs); ok && len(cidrs) > 0 {
		body.APIAccess = &APIAccessDTO{AllowedCIDRs: cidrs}
	}
	var created Cluster
	if err := r.client.Do(ctx, "POST", apiBase+"/clusters", nil, body, &created); err != nil {
		resp.Diagnostics.AddError("Error creating Kubernetes cluster", apiError(err, true))
		return
	}
	if created.ID == "" {
		resp.Diagnostics.AddError("Error creating Kubernetes cluster", "the API response has no cluster id")
		return
	}
	// Track the cluster right away: if waiting fails it is tainted, not orphaned.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), created.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), plan.ProjectID)...)

	got, err := r.waitFor(ctx, plan.ProjectID.ValueString(), created.ID, createTimeout, "to become READY", nil)
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for Kubernetes cluster", "Cluster created but not ready: "+err.Error())
		return
	}
	// The create body has no cluster-wide autoscaler flags: set them right after.
	if a := plan.Autoscaler; autoscalerHasClusterFlags(a) {
		q := map[string]string{"projectId": plan.ProjectID.ValueString()}
		if err := r.mutate(ctx, plan.ProjectID.ValueString(), created.ID, "PATCH", apiBase+"/clusters/"+created.ID+"/autoscaler", q, autoscalerBody(a)); err != nil {
			resp.Diagnostics.AddError("Error configuring the cluster autoscaler", "Cluster created, autoscaler expander/balancing not set: "+apiError(err, false))
			return
		}
		if got, err = r.waitFor(ctx, plan.ProjectID.ValueString(), created.ID, updateTimeout, "autoscaler update", nil); err != nil {
			resp.Diagnostics.AddError("Error waiting for Kubernetes cluster", err.Error())
			return
		}
	}
	if plan.NodeLocalDNSCache.ValueBool() {
		want := true
		if got, err = r.waitNodeLocalDNS(ctx, plan.ProjectID.ValueString(), created.ID, &want); err != nil {
			resp.Diagnostics.AddError("Error waiting for NodeLocal DNSCache", "Cluster created, NodeLocal DNSCache not ready: "+err.Error())
			return
		}
	}
	plan.applyAPI(got)
	r.fetchKubeconfig(ctx, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ClusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	c, err := r.get(ctx, state.ProjectID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading Kubernetes cluster", apiError(err, false))
		return
	}
	state.applyAPI(c)
	if n := c.NodeLocalDNSCache; n != nil && n.State == NodeLocalDNSError {
		resp.Diagnostics.AddWarning("NodeLocal DNSCache is in ERROR state",
			"The last NodeLocal DNSCache change of the cluster failed: "+n.Message+
				"\n\nSet node_local_dns_cache = false and apply to turn it off, then enable it again to retry.")
	}
	if c.Status == "READY" {
		r.fetchKubeconfig(ctx, &state, &resp.Diagnostics)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ClusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, id := state.ProjectID.ValueString(), state.ID.ValueString()
	q := map[string]string{"projectId": project}
	base := apiBase + "/clusters/" + id
	fail := func(what string, err error) {
		resp.Diagnostics.AddError("Error updating Kubernetes cluster", what+": "+apiError(err, false))
	}

	// 1. Upgrade first: workers changes are refused during a rollout.
	if !SameVersion(plan.KubernetesVersion.ValueString(), state.KubernetesVersion.ValueString()) {
		target := NormalizeVersion(plan.KubernetesVersion.ValueString())
		if err := r.mutate(ctx, project, id, "POST", base+"/upgrade", q, map[string]string{"kubernetesVersion": target}); err != nil {
			fail("upgrade to "+target, err)
			return
		}
		if _, err := r.waitFor(ctx, project, id, upgradeTimeout, "to be upgraded to "+target, func(c *Cluster) bool {
			return SameVersion(c.KubernetesVersion, target)
		}); err != nil {
			fail("waiting for the upgrade", err)
			return
		}
	}
	// 2. Control plane.
	if !plan.ControlPlaneReplicas.Equal(state.ControlPlaneReplicas) {
		n := plan.ControlPlaneReplicas.ValueInt64()
		if err := r.mutate(ctx, project, id, "PATCH", base+"/control-plane", q, map[string]int64{"replicas": n}); err != nil {
			fail("control plane replicas", err)
			return
		}
		if _, err := r.waitFor(ctx, project, id, updateTimeout, "control plane scaling", func(c *Cluster) bool {
			return int64(c.ControlPlane.Replicas) == n
		}); err != nil {
			fail("waiting for the control plane", err)
			return
		}
	}
	// 3. Autoscaler and workers. When the autoscaler is turned off it goes
	// first, so that the configured replica count is applied and kept.
	autoscalerOff := autoscalerOn(state.Autoscaler) && !autoscalerOn(plan.Autoscaler)
	updateAutoscaler := func() bool {
		if autoscalerEqual(plan.Autoscaler, state.Autoscaler) {
			return true
		}
		body := autoscalerBody(plan.Autoscaler)
		if body == nil {
			body = &AutoscalerDTO{Enabled: false}
		}
		if err := r.mutate(ctx, project, id, "PATCH", base+"/autoscaler", q, body); err != nil {
			fail("autoscaler", err)
			return false
		}
		if _, err := r.waitFor(ctx, project, id, updateTimeout, "autoscaler update", nil); err != nil {
			fail("waiting for the autoscaler", err)
			return false
		}
		return true
	}
	if autoscalerOff && !updateAutoscaler() {
		return
	}
	// Workers: replicas only when changed (the autoscaler may own it) or when
	// the autoscaler was just turned off; cpu and memory together.
	var wp PatchWorkersRequest
	if !plan.Workers.Replicas.Equal(state.Workers.Replicas) || autoscalerOff {
		v := plan.Workers.Replicas.ValueInt64()
		wp.Replicas = &v
	}
	if !plan.Workers.CPU.Equal(state.Workers.CPU) || !plan.Workers.MemoryGi.Equal(state.Workers.MemoryGi) {
		cpu, mem := plan.Workers.CPU.ValueInt64(), plan.Workers.MemoryGi.ValueInt64()
		wp.CPU, wp.MemoryGi = &cpu, &mem
	}
	if wp.Replicas != nil || wp.CPU != nil {
		if err := r.mutate(ctx, project, id, "PATCH", base+"/workers", q, wp); err != nil {
			fail("workers", err)
			return
		}
		if _, err := r.waitFor(ctx, project, id, updateTimeout, "workers update", func(c *Cluster) bool {
			return (wp.Replicas == nil || int64(c.Workers.Replicas) == *wp.Replicas) &&
				(wp.CPU == nil || (int64(c.Workers.CPU) == *wp.CPU && int64(c.Workers.MemoryGi) == *wp.MemoryGi))
		}); err != nil {
			fail("waiting for workers", err)
			return
		}
	}
	if !autoscalerOff && !updateAutoscaler() {
		return
	}
	// 5. Maintenance window.
	if !maintenanceEqual(plan.MaintenanceWindow, state.MaintenanceWindow) {
		body := maintenanceBody(plan.MaintenanceWindow)
		if body == nil {
			body = &MaintenanceDTO{Enabled: false}
		}
		if err := r.mutate(ctx, project, id, "PATCH", base+"/maintenance-window", q, body); err != nil {
			fail("maintenance window", err)
			return
		}
	}
	// 6. NodeLocal DNSCache (async: wait for READY / DISABLED).
	if want := plan.NodeLocalDNSCache.ValueBool(); !plan.NodeLocalDNSCache.IsUnknown() && want != state.NodeLocalDNSCache.ValueBool() {
		// A rollout of a previous change answers 409 NODE_LOCAL_DNS_BUSY: let it settle first.
		cur, err := r.waitNodeLocalDNS(ctx, project, id, nil)
		if err != nil {
			fail("waiting for NodeLocal DNSCache", err)
			return
		}
		if cur.NodeLocalDNSCache == nil && want {
			resp.Diagnostics.AddError("Error updating Kubernetes cluster", "node_local_dns_cache: the API response has no "+
				"nodeLocalDNSCache: the platform does not support NodeLocal DNSCache yet")
			return
		}
		if err := r.mutate(ctx, project, id, "PATCH", base+"/node-local-dns-cache", q, NodeLocalDNSCacheDTO{Enabled: want}); err != nil {
			fail("node_local_dns_cache", err)
			return
		}
		if _, err := r.waitNodeLocalDNS(ctx, project, id, &want); err != nil {
			fail("waiting for NodeLocal DNSCache", err)
			return
		}
	}

	// 7. API allowlist (no rollout).
	if cidrs, ok := knownCIDRs(ctx, plan.APIAllowedCIDRs); ok && !plan.APIAllowedCIDRs.Equal(state.APIAllowedCIDRs) {
		if err := r.mutate(ctx, project, id, "PUT", base+"/api-access", q, APIAccessDTO{AllowedCIDRs: cidrs}); err != nil {
			fail("API allowlist", err)
			return
		}
	}

	got, err := r.get(ctx, project, id)
	if err != nil {
		resp.Diagnostics.AddError("Error reading Kubernetes cluster", apiError(err, false))
		return
	}
	plan.applyAPI(got)
	r.fetchKubeconfig(ctx, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ClusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, id := state.ProjectID.ValueString(), state.ID.ValueString()
	q := map[string]string{"projectId": project}
	if err := r.client.Do(ctx, "DELETE", apiBase+"/clusters/"+id, q, nil, nil); err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Error deleting Kubernetes cluster", apiError(err, false))
		return
	}
	err := common.WaitFor(ctx, deleteTimeout, "cluster "+id+" to be deleted", func(ctx context.Context) (bool, error) {
		_, err := r.get(ctx, project, id)
		if client.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for Kubernetes cluster deletion", err.Error())
	}
}

// ImportState: "<project_id>/<cluster_id>" (the API needs the project).
func (r *ClusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, id, err := common.ImportProjectScoped(req.ID, "<cluster_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// ---- helpers ----

func (r *ClusterResource) get(ctx context.Context, project, id string) (*Cluster, error) {
	return getCluster(ctx, r.client, project, id)
}

func getCluster(ctx context.Context, c *client.Client, project, id string) (*Cluster, error) {
	var out Cluster
	if err := c.Do(ctx, "GET", apiBase+"/clusters/"+id, map[string]string{"projectId": project}, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func getKubeconfig(ctx context.Context, c *client.Client, project, id string) (string, error) {
	var kc KubeconfigResponse
	if err := c.Do(ctx, "GET", apiBase+"/clusters/"+id+"/kubeconfig", map[string]string{"projectId": project}, nil, &kc); err != nil {
		return "", err
	}
	return kc.Kubeconfig, nil
}

// fetchKubeconfig refreshes kubeconfig, endpoint and CA. A failure keeps the
// previous values and warns: the cluster itself is fine.
func (r *ClusterResource) fetchKubeconfig(ctx context.Context, m *ClusterModel, diags interface {
	AddWarning(string, string)
}) {
	kc, err := getKubeconfig(ctx, r.client, m.ProjectID.ValueString(), m.ID.ValueString())
	if err != nil || kc == "" {
		if err != nil {
			diags.AddWarning("Could not read kubeconfig", apiError(err, false))
		}
		for _, v := range []*types.String{&m.Kubeconfig, &m.Endpoint, &m.ClusterCACertificate} {
			if v.IsUnknown() {
				*v = types.StringNull()
			}
		}
		return
	}
	host, ca := KubeconfigEndpoint(kc)
	m.Kubeconfig = types.StringValue(kc)
	m.Endpoint = common.OptString(host)
	m.ClusterCACertificate = common.OptString(ca)
}

// mutate sends a change; a 409 (cluster busy: not READY, rollout running) is
// retried after the cluster is READY again.
func (r *ClusterResource) mutate(ctx context.Context, project, id, method, p string, q map[string]string, body any) error {
	for attempt := 0; ; attempt++ {
		if _, err := r.waitFor(ctx, project, id, updateTimeout, "to become READY before the change", nil); err != nil {
			return err
		}
		err := r.client.Do(ctx, method, p, q, body, nil)
		// NODE_LOCAL_DNS_CONFLICT is a 409 about the cluster contents, not a busy cluster.
		if h, ok := client.AsHTTPError(err); ok && h.IsConflict() && errorReason(err) != ReasonNodeLocalDNSConflict && attempt < 3 {
			continue
		}
		return err
	}
}

// waitFor waits until the cluster is READY and cond (when set) holds. A
// failure status ends the wait with an error.
func (r *ClusterResource) waitFor(ctx context.Context, project, id string, timeout time.Duration, what string, cond func(*Cluster) bool) (*Cluster, error) {
	var last *Cluster
	err := common.WaitFor(ctx, timeout, "cluster "+id+" "+what, func(ctx context.Context) (bool, error) {
		c, err := r.get(ctx, project, id)
		if err != nil {
			return false, err
		}
		last = c
		if isErrorStatus(c.Status) {
			return false, fmt.Errorf("cluster entered %s state", c.Status)
		}
		return c.Status == "READY" && (cond == nil || cond(c)), nil
	})
	return last, err
}

// waitNodeLocalDNS waits until the cluster is READY and its NodeLocal DNSCache
// has settled: READY for want=true, DISABLED for want=false, READY, DISABLED
// or ERROR for want=nil. With a wanted state ERROR ends the wait with the
// platform message.
func (r *ClusterResource) waitNodeLocalDNS(ctx context.Context, project, id string, want *bool) (*Cluster, error) {
	var last *Cluster
	err := common.WaitFor(ctx, updateTimeout, "cluster "+id+" NodeLocal DNSCache to settle", func(ctx context.Context) (bool, error) {
		c, err := r.get(ctx, project, id)
		if err != nil {
			return false, err
		}
		last = c
		if isErrorStatus(c.Status) {
			return false, fmt.Errorf("cluster entered %s state", c.Status)
		}
		return nodeLocalDNSSettled(c, want)
	})
	return last, err
}

// nodeLocalDNSSettled is the condition of waitNodeLocalDNS. A backend without
// the feature (no nodeLocalDNSCache in the response) is an error when the cache
// is wanted on and "settled" otherwise.
func nodeLocalDNSSettled(c *Cluster, want *bool) (bool, error) {
	n := c.NodeLocalDNSCache
	if n == nil {
		if want != nil && *want {
			return false, fmt.Errorf("the API response has no nodeLocalDNSCache: the platform does not support NodeLocal DNSCache yet")
		}
		return c.Status == "READY", nil
	}
	if n.State == NodeLocalDNSError && want == nil {
		// A failed rollout is settled: the next change may retry or turn it off.
		return c.Status == "READY", nil
	}
	if n.State == NodeLocalDNSError {
		msg := n.Message
		if msg == "" {
			msg = "no details from the platform"
		}
		return false, fmt.Errorf("NodeLocal DNSCache entered ERROR state: %s", msg)
	}
	if c.Status != "READY" {
		return false, nil
	}
	if want == nil {
		return n.State == "" || n.State == NodeLocalDNSReady || n.State == NodeLocalDNSDisabled, nil
	}
	target := NodeLocalDNSDisabled
	if *want {
		target = NodeLocalDNSReady
	}
	// A backend that reports no state is settled once enabled matches.
	return n.Enabled == *want && (n.State == "" || n.State == target), nil
}

func (m *ClusterModel) applyAPI(c *Cluster) {
	m.ID = types.StringValue(c.ID)
	if c.ProjectID != "" {
		m.ProjectID = types.StringValue(c.ProjectID)
	}
	if c.Name != "" {
		m.Name = types.StringValue(c.Name)
	}
	if c.KubernetesVersion != "" && !(m.KubernetesVersion.ValueString() != "" && SameVersion(m.KubernetesVersion.ValueString(), c.KubernetesVersion)) {
		m.KubernetesVersion = types.StringValue(c.KubernetesVersion)
	}
	if c.ClusterType != "" {
		m.ClusterType = types.StringValue(c.ClusterType)
	}
	if c.ControlPlane.Replicas > 0 {
		m.ControlPlaneReplicas = types.Int64Value(int64(c.ControlPlane.Replicas))
	}
	m.PodCIDR = common.OptString(c.PodCIDR)
	m.ServiceCIDR = common.OptString(c.ServiceCIDR)
	m.Network = clusterNetworkObject(c.Network)
	switch {
	case c.Network != nil && c.Network.Mode == "vpc" && c.Network.NetworkID != "":
		m.NetworkID = types.StringValue(c.Network.NetworkID)
	case m.NetworkID.IsUnknown():
		m.NetworkID = types.StringNull()
	}

	autoscaled := c.Autoscaler != nil && c.Autoscaler.Enabled
	prev := m.Workers
	m.Workers = &WorkersModel{
		Replicas: types.Int64Value(int64(c.Workers.Replicas)),
		CPU:      types.Int64Value(int64(c.Workers.CPU)),
		MemoryGi: types.Int64Value(int64(c.Workers.MemoryGi)),
		DiskGi:   types.Int64Value(int64(c.Workers.DiskGi)),
	}
	// The autoscaler changes the replica count on its own: keep the configured value.
	if autoscaled && prev != nil && !prev.Replicas.IsNull() && !prev.Replicas.IsUnknown() {
		m.Workers.Replicas = prev.Replicas
	}

	// Optional blocks: a disabled autoscaler/window that is not configured stays null.
	if c.Autoscaler == nil || (!c.Autoscaler.Enabled && m.Autoscaler == nil) {
		m.Autoscaler = nil
	} else {
		a := &AutoscalerModel{
			Enabled:                  types.BoolValue(c.Autoscaler.Enabled),
			MaxSize:                  optInt(int64(c.Autoscaler.MaxSize)),
			ScaleDownDelayAfterAdd:   optInt(int64(c.Autoscaler.ScaleDownDelayAfterAdd)),
			ScaleDownUnneededTime:    optInt(int64(c.Autoscaler.ScaleDownUnneededTime)),
			Expander:                 common.OptString(c.Autoscaler.Expander),
			BalanceSimilarNodeGroups: types.BoolNull(),
		}
		if b := c.Autoscaler.BalanceSimilarNodeGroups; b != nil {
			a.BalanceSimilarNodeGroups = types.BoolValue(*b)
		}
		// The cluster-wide flags are not returned by h3k8s: keep the configured ones.
		if prev := m.Autoscaler; prev != nil {
			if a.Expander.IsNull() && !prev.Expander.IsUnknown() {
				a.Expander = prev.Expander
			}
			if a.BalanceSimilarNodeGroups.IsNull() && !prev.BalanceSimilarNodeGroups.IsUnknown() {
				a.BalanceSimilarNodeGroups = prev.BalanceSimilarNodeGroups
			}
		}
		m.Autoscaler = a
	}
	if c.MaintenanceWindow == nil || (!c.MaintenanceWindow.Enabled && m.MaintenanceWindow == nil) {
		m.MaintenanceWindow = nil
	} else {
		mw := &MaintenanceModel{
			Enabled:   types.BoolValue(c.MaintenanceWindow.Enabled),
			DayOfWeek: common.OptString(c.MaintenanceWindow.DayOfWeek),
			StartHour: types.Int64Value(int64(c.MaintenanceWindow.StartHour)),
		}
		mw.AutoUpgrade = types.StringValue("minor")
		if c.MaintenanceWindow.AutoUpgrade != "" {
			mw.AutoUpgrade = types.StringValue(c.MaintenanceWindow.AutoUpgrade)
		}
		if c.MaintenanceWindow.Exclusions != nil {
			for _, e := range *c.MaintenanceWindow.Exclusions {
				mw.Exclusions = append(mw.Exclusions, MaintenanceExclusionModel{
					StartDate: types.StringValue(e.StartDate), EndDate: types.StringValue(e.EndDate), Reason: common.OptString(e.Reason)})
			}
		}
		// A configured window without start_hour stays null rather than 0.
		if m.MaintenanceWindow != nil && m.MaintenanceWindow.StartHour.IsNull() && c.MaintenanceWindow.StartHour == 0 {
			mw.StartHour = types.Int64Null()
		}
		m.MaintenanceWindow = mw
	}
	// Absent on backends without the feature: off.
	m.NodeLocalDNSCache = types.BoolValue(c.NodeLocalDNSCache != nil && c.NodeLocalDNSCache.Enabled)
	m.Status = types.StringValue(c.Status)
	m.Namespace = common.OptString(c.Namespace)
	m.CreatedAt = common.OptString(c.CreatedAt)
	m.APIAllowedCIDRs = allowedCIDRsValue(c, m.APIAllowedCIDRs)
}

func optInt(v int64) types.Int64 {
	if v == 0 {
		return types.Int64Null()
	}
	return types.Int64Value(v)
}

func autoscalerBody(a *AutoscalerModel) *AutoscalerDTO {
	if a == nil {
		return nil
	}
	out := &AutoscalerDTO{
		Enabled:                a.Enabled.ValueBool(),
		MaxSize:                flexInt(knownInt(a.MaxSize)),
		ScaleDownDelayAfterAdd: flexInt(knownInt(a.ScaleDownDelayAfterAdd)),
		ScaleDownUnneededTime:  flexInt(knownInt(a.ScaleDownUnneededTime)),
		Expander:               common.KnownString(a.Expander),
	}
	if b := a.BalanceSimilarNodeGroups; !b.IsNull() && !b.IsUnknown() {
		v := b.ValueBool()
		out.BalanceSimilarNodeGroups = &v
	}
	return out
}

// autoscalerExpanders are the cluster-autoscaler expanders h3k8s accepts.
var autoscalerExpanders = []string{"random", "least-waste", "most-pods"}

// autoscalerHasClusterFlags: expander or balance_similar_node_groups is set.
func autoscalerHasClusterFlags(a *AutoscalerModel) bool {
	return a != nil && (common.KnownString(a.Expander) != "" || (!a.BalanceSimilarNodeGroups.IsNull() && !a.BalanceSimilarNodeGroups.IsUnknown()))
}

func maintenanceBody(m *MaintenanceModel) *MaintenanceDTO {
	if m == nil {
		return nil
	}
	ex := make([]MaintenanceExclusionDTO, 0, len(m.Exclusions))
	for _, e := range m.Exclusions {
		ex = append(ex, MaintenanceExclusionDTO{StartDate: e.StartDate.ValueString(), EndDate: e.EndDate.ValueString(), Reason: common.KnownString(e.Reason)})
	}
	return &MaintenanceDTO{Enabled: m.Enabled.ValueBool(), DayOfWeek: common.KnownString(m.DayOfWeek), StartHour: flexInt(knownInt(m.StartHour)),
		AutoUpgrade: common.KnownString(m.AutoUpgrade), Exclusions: &ex}
}

func exclusionsEqual(a, b []MaintenanceExclusionModel) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].StartDate.Equal(b[i].StartDate) || !a[i].EndDate.Equal(b[i].EndDate) ||
			common.KnownString(a[i].Reason) != common.KnownString(b[i].Reason) {
			return false
		}
	}
	return true
}

func knownInt(v types.Int64) int64 {
	if v.IsNull() || v.IsUnknown() {
		return 0
	}
	return v.ValueInt64()
}

func autoscalerOn(a *AutoscalerModel) bool { return a != nil && a.Enabled.ValueBool() }

func autoscalerEqual(a, b *AutoscalerModel) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Enabled.Equal(b.Enabled) && (a.MaxSize.IsUnknown() || a.MaxSize.Equal(b.MaxSize)) &&
		(a.ScaleDownDelayAfterAdd.IsUnknown() || a.ScaleDownDelayAfterAdd.Equal(b.ScaleDownDelayAfterAdd)) &&
		(a.ScaleDownUnneededTime.IsUnknown() || a.ScaleDownUnneededTime.Equal(b.ScaleDownUnneededTime)) &&
		a.Expander.Equal(b.Expander) && a.BalanceSimilarNodeGroups.Equal(b.BalanceSimilarNodeGroups)
}

func maintenanceEqual(a, b *MaintenanceModel) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Enabled.Equal(b.Enabled) && a.DayOfWeek.Equal(b.DayOfWeek) && a.StartHour.Equal(b.StartHour) &&
		(a.AutoUpgrade.IsUnknown() || a.AutoUpgrade.Equal(b.AutoUpgrade)) && exclusionsEqual(a.Exclusions, b.Exclusions)
}
