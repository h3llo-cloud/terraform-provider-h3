package k8s

import (
	"context"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ---- h3_k8s_versions ----

func NewVersionsDataSource() datasource.DataSource { return &VersionsDataSource{} }

type VersionsDataSource struct{ client *client.Client }

type VersionsModel struct {
	From           types.String   `tfsdk:"from"`
	Versions       []VersionModel `tfsdk:"versions"`
	Latest         types.String   `tfsdk:"latest"`
	UpgradeTargets types.List     `tfsdk:"upgrade_targets"`
	NextUpgrade    types.String   `tfsdk:"next_upgrade"`
}

type VersionModel struct {
	Version     types.String `tfsdk:"version"`
	NodeVMImage types.String `tfsdk:"node_vm_image"`
}

func (d *VersionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_k8s_versions"
}

func (d *VersionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Kubernetes versions offered by the managed Kubernetes service. With `from`, also the versions a " +
			"cluster on that version can be upgraded to (`upgrade_targets`) and the recommended next step (`next_upgrade`)." + AccessNote,
		Attributes: map[string]schema.Attribute{
			"from": schema.StringAttribute{Optional: true, MarkdownDescription: "Current cluster version, e.g. `v1.34.1`"},
			"versions": schema.ListNestedAttribute{Computed: true, MarkdownDescription: "Available versions",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"version":       schema.StringAttribute{Computed: true, MarkdownDescription: "Version, e.g. `v1.35.9`"},
					"node_vm_image": schema.StringAttribute{Computed: true, MarkdownDescription: "Node VM image of the version"},
				}}},
			"latest":          schema.StringAttribute{Computed: true, MarkdownDescription: "Latest available version"},
			"upgrade_targets": schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Versions reachable from `from` in one upgrade"},
			"next_upgrade":    schema.StringAttribute{Computed: true, MarkdownDescription: "Recommended next upgrade from `from`"},
		},
	}
}

func (d *VersionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *VersionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m VersionsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var q map[string]string
	if from := common.KnownString(m.From); from != "" {
		q = map[string]string{"from": NormalizeVersion(from)}
	}
	var out VersionsResponse
	if err := d.client.Do(ctx, "GET", apiBase+"/clusters/versions", q, nil, &out); err != nil {
		resp.Diagnostics.AddError("Error reading Kubernetes versions", apiError(err, true))
		return
	}
	m.Versions = make([]VersionModel, 0, len(out.Versions))
	for _, v := range out.Versions {
		m.Versions = append(m.Versions, VersionModel{Version: types.StringValue(v.Version), NodeVMImage: common.OptString(v.NodeVMImage)})
	}
	m.Latest = common.OptString(out.Latest)
	m.NextUpgrade = common.OptString(out.NextUpgrade)
	targets := make([]attr.Value, 0, len(out.UpgradeTargets))
	for _, t := range out.UpgradeTargets {
		targets = append(targets, types.StringValue(t))
	}
	m.UpgradeTargets = types.ListValueMust(types.StringType, targets)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---- h3_k8s_cluster ----

func NewClusterDataSource() datasource.DataSource { return &ClusterDataSource{} }

type ClusterDataSource struct{ client *client.Client }

type ClusterDSModel struct {
	ID                   types.String `tfsdk:"id"`
	ProjectID            types.String `tfsdk:"project_id"`
	Name                 types.String `tfsdk:"name"`
	KubernetesVersion    types.String `tfsdk:"kubernetes_version"`
	ClusterType          types.String `tfsdk:"cluster_type"`
	ControlPlaneReplicas types.Int64  `tfsdk:"control_plane_replicas"`
	PodCIDR              types.String `tfsdk:"pod_cidr"`
	ServiceCIDR          types.String `tfsdk:"service_cidr"`
	WorkerReplicas       types.Int64  `tfsdk:"worker_replicas"`
	WorkerCPU            types.Int64  `tfsdk:"worker_cpu"`
	WorkerMemoryGi       types.Int64  `tfsdk:"worker_memory_gi"`
	WorkerDiskGi         types.Int64  `tfsdk:"worker_disk_gi"`
	AutoscalerEnabled    types.Bool   `tfsdk:"autoscaler_enabled"`
	NodeLocalDNSCache    types.Bool   `tfsdk:"node_local_dns_cache"`
	NodeLocalDNSState    types.String `tfsdk:"node_local_dns_cache_state"`
	Status               types.String `tfsdk:"status"`
	Namespace            types.String `tfsdk:"namespace"`
	APIAllowedCIDRs      types.Set    `tfsdk:"api_allowed_cidrs"`
	NetworkMode          types.String `tfsdk:"network_mode"`
	NetworkID            types.String `tfsdk:"network_id"`
}

func (d *ClusterDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_k8s_cluster"
}

func (d *ClusterDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	num := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads a managed Kubernetes cluster by ID. Use `h3_k8s_kubeconfig` for credentials." + AccessNote,
		Attributes: map[string]schema.Attribute{
			"id":                     schema.StringAttribute{Required: true, MarkdownDescription: "Cluster ID"},
			"project_id":             schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project ID (UUID); the provider default when omitted"},
			"name":                   str("Cluster name"),
			"kubernetes_version":     str("Kubernetes version"),
			"cluster_type":           str("`DEV` or `PROD`"),
			"control_plane_replicas": num("Control plane replicas"),
			"pod_cidr":               str("Pod CIDR"),
			"service_cidr":           str("Service CIDR"),
			"worker_replicas":        num("Current worker count"),
			"worker_cpu":             num("vCPU per worker"),
			"worker_memory_gi":       num("Memory per worker, GiB"),
			"worker_disk_gi":         num("Disk per worker, GiB"),
			"autoscaler_enabled":     schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the autoscaler is enabled"},
			"node_local_dns_cache":   schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether NodeLocal DNSCache is enabled (`false` on platforms without the feature)"},
			"node_local_dns_cache_state": str("NodeLocal DNSCache state: `DISABLED`, `ENABLING`, `READY`, `DISABLING` or `ERROR`; " +
				"null on platforms without the feature"),
			"network_mode": str("Worker network: `vpc` (a network of the project VPC) or `shared` (shared platform network); " +
				"null on platforms without workers in the tenant VPC"),
			"network_id": str("h3ovn network ID of the workers when `network_mode` is `vpc`"),
			"status":     str("Cluster status"),
			"namespace":  str("Platform namespace of the cluster"),
			"api_allowed_cidrs": schema.SetAttribute{Computed: true, ElementType: types.StringType,
				MarkdownDescription: "Source CIDRs allowed to reach the Kubernetes API; empty = open to everyone"},
		},
	}
}

func (d *ClusterDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *ClusterDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m ClusterDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, ok := common.DataSourceProjectID(d.client, m.ProjectID, resp)
	if !ok {
		return
	}
	m.ProjectID = types.StringValue(pid)
	c, err := getCluster(ctx, d.client, pid, m.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading Kubernetes cluster", apiError(err, false))
		return
	}
	m.Name = types.StringValue(c.Name)
	m.KubernetesVersion = types.StringValue(c.KubernetesVersion)
	m.ClusterType = common.OptString(c.ClusterType)
	m.ControlPlaneReplicas = types.Int64Value(int64(c.ControlPlane.Replicas))
	m.PodCIDR = common.OptString(c.PodCIDR)
	m.ServiceCIDR = common.OptString(c.ServiceCIDR)
	m.WorkerReplicas = types.Int64Value(int64(c.Workers.Replicas))
	m.WorkerCPU = types.Int64Value(int64(c.Workers.CPU))
	m.WorkerMemoryGi = types.Int64Value(int64(c.Workers.MemoryGi))
	m.WorkerDiskGi = types.Int64Value(int64(c.Workers.DiskGi))
	m.AutoscalerEnabled = types.BoolValue(c.Autoscaler != nil && c.Autoscaler.Enabled)
	m.NodeLocalDNSCache = types.BoolValue(c.NodeLocalDNSCache != nil && c.NodeLocalDNSCache.Enabled)
	m.NodeLocalDNSState = types.StringNull()
	if c.NodeLocalDNSCache != nil {
		m.NodeLocalDNSState = common.OptString(c.NodeLocalDNSCache.State)
	}
	m.NetworkMode, m.NetworkID = types.StringNull(), types.StringNull()
	if c.Network != nil {
		m.NetworkMode = common.OptString(c.Network.Mode)
		m.NetworkID = common.OptString(c.Network.NetworkID)
	}
	m.Status = types.StringValue(c.Status)
	m.Namespace = common.OptString(c.Namespace)
	m.APIAllowedCIDRs = allowedCIDRsValue(c, types.SetNull(types.StringType))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---- h3_k8s_kubeconfig ----

func NewKubeconfigDataSource() datasource.DataSource { return &KubeconfigDataSource{} }

type KubeconfigDataSource struct{ client *client.Client }

type KubeconfigModel struct {
	ClusterID            types.String `tfsdk:"cluster_id"`
	ProjectID            types.String `tfsdk:"project_id"`
	Kubeconfig           types.String `tfsdk:"kubeconfig"`
	Endpoint             types.String `tfsdk:"endpoint"`
	ClusterCACertificate types.String `tfsdk:"cluster_ca_certificate"`
}

func (d *KubeconfigDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_k8s_kubeconfig"
}

func (d *KubeconfigDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Admin kubeconfig of a managed Kubernetes cluster, with the API endpoint and CA parsed out for " +
			"the `kubernetes`/`helm` providers. The kubeconfig is sensitive and ends up in the state." + AccessNote,
		Attributes: map[string]schema.Attribute{
			"cluster_id":             schema.StringAttribute{Required: true, MarkdownDescription: "Cluster ID"},
			"project_id":             schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project ID (UUID); the provider default when omitted"},
			"kubeconfig":             schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Kubeconfig (YAML)"},
			"endpoint":               schema.StringAttribute{Computed: true, MarkdownDescription: "Kubernetes API server URL"},
			"cluster_ca_certificate": schema.StringAttribute{Computed: true, MarkdownDescription: "Cluster CA certificate (PEM)"},
		},
	}
}

func (d *KubeconfigDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *KubeconfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m KubeconfigModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, ok := common.DataSourceProjectID(d.client, m.ProjectID, resp)
	if !ok {
		return
	}
	m.ProjectID = types.StringValue(pid)
	kc, err := getKubeconfig(ctx, d.client, pid, m.ClusterID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading kubeconfig", apiError(err, false))
		return
	}
	host, ca := KubeconfigEndpoint(kc)
	m.Kubeconfig = types.StringValue(kc)
	m.Endpoint = common.OptString(host)
	m.ClusterCACertificate = common.OptString(ca)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
