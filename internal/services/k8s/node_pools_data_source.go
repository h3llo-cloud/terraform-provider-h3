package k8s

import (
	"context"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ---- h3_k8s_node_pools ----

func NewNodePoolsDataSource() datasource.DataSource { return &NodePoolsDataSource{} }

type NodePoolsDataSource struct{ client *client.Client }

type NodePoolsDSModel struct {
	ClusterID types.String      `tfsdk:"cluster_id"`
	ProjectID types.String      `tfsdk:"project_id"`
	NodePools []NodePoolDSModel `tfsdk:"node_pools"`
}

type NodePoolDSModel struct {
	Name               types.String `tfsdk:"name"`
	CPU                types.Int64  `tfsdk:"cpu"`
	MemoryGi           types.Int64  `tfsdk:"memory_gi"`
	DiskGi             types.Int64  `tfsdk:"disk_gi"`
	DiskProfile        types.String `tfsdk:"disk_profile"`
	Replicas           types.Int64  `tfsdk:"replicas"`
	AutoscalingEnabled types.Bool   `tfsdk:"autoscaling_enabled"`
	AutoscalingMinSize types.Int64  `tfsdk:"autoscaling_min_size"`
	AutoscalingMaxSize types.Int64  `tfsdk:"autoscaling_max_size"`
	Labels             types.Map    `tfsdk:"labels"`
	Taints             []TaintModel `tfsdk:"taints"`
	MaxPods            types.Int64  `tfsdk:"max_pods"`
	KubernetesVersion  types.String `tfsdk:"kubernetes_version"`
	Status             types.String `tfsdk:"status"`
	DesiredReplicas    types.Int64  `tfsdk:"desired_replicas"`
	CurrentReplicas    types.Int64  `tfsdk:"current_replicas"`
	ReadyReplicas      types.Int64  `tfsdk:"ready_replicas"`
	BilledReplicas     types.Int64  `tfsdk:"billed_replicas"`
}

func (d *NodePoolsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_k8s_node_pools"
}

func (d *NodePoolsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	num := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Worker pools (node pools) of a managed Kubernetes cluster with their state. A cluster created " +
			"with `workers` shows them as the pool `default`." + AccessNote,
		Attributes: map[string]schema.Attribute{
			"cluster_id": schema.StringAttribute{Required: true, MarkdownDescription: "Cluster ID"},
			"project_id": schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project ID (UUID); the provider default when omitted"},
			"node_pools": schema.ListNestedAttribute{Computed: true, MarkdownDescription: "Pools in the order of the cluster spec",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":                 str("Pool name"),
					"cpu":                  num("vCPU per node"),
					"memory_gi":            num("Memory per node, GiB"),
					"disk_gi":              num("System disk per node, GiB"),
					"disk_profile":         str("`replicated` or `local`"),
					"replicas":             num("Configured nodes (the lower bound with autoscaling)"),
					"autoscaling_enabled":  schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the pool is autoscaled"},
					"autoscaling_min_size": num("Autoscaling lower bound; null without autoscaling"),
					"autoscaling_max_size": num("Autoscaling upper bound; null without autoscaling"),
					"labels":               schema.MapAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Node labels"},
					"taints": schema.ListNestedAttribute{Computed: true, MarkdownDescription: "Node taints",
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"key":    str("Taint key"),
							"value":  str("Taint value"),
							"effect": str("Taint effect"),
						}}},
					"max_pods":           num("Maximum pods per node; null = platform default"),
					"kubernetes_version": str("Kubernetes version of the pool nodes"),
					"status":             str("`READY`, `PROVISIONING`, `SCALING`, `ROLLING_OUT`, `DELETING` or `UNKNOWN`"),
					"desired_replicas":   num("Nodes the pool should have now"),
					"current_replicas":   num("Nodes the pool has now"),
					"ready_replicas":     num("Ready nodes"),
					"billed_replicas":    num("Nodes billed now"),
				}}},
		},
	}
}

func (d *NodePoolsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *NodePoolsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m NodePoolsDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, ok := common.DataSourceProjectID(d.client, m.ProjectID, resp)
	if !ok {
		return
	}
	m.ProjectID = types.StringValue(pid)
	pools, err := listNodePools(ctx, d.client, pid, m.ClusterID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading Kubernetes node pools", poolAPIError(err, true))
		return
	}
	m.NodePools = make([]NodePoolDSModel, 0, len(pools))
	for i := range pools {
		m.NodePools = append(m.NodePools, nodePoolDS(&pools[i]))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func nodePoolDS(p *NodePool) NodePoolDSModel {
	out := NodePoolDSModel{
		Name:               types.StringValue(p.Name),
		CPU:                types.Int64Value(int64(p.CPU)),
		MemoryGi:           types.Int64Value(int64(p.MemoryGi)),
		DiskGi:             types.Int64Value(int64(p.DiskGi)),
		DiskProfile:        common.OptString(p.DiskProfile),
		Replicas:           types.Int64Value(int64(p.Replicas)),
		AutoscalingEnabled: types.BoolValue(p.Autoscaling != nil && p.Autoscaling.Enabled),
		AutoscalingMinSize: types.Int64Null(),
		AutoscalingMaxSize: types.Int64Null(),
		Labels:             mapValue(types.MapNull(types.StringType), p.Labels),
		Taints:             taintModels(p.Taints),
		MaxPods:            optInt(int64(p.MaxPods)),
		KubernetesVersion:  common.OptString(p.KubernetesVersion),
		Status:             common.OptString(p.Status),
		DesiredReplicas:    optPtr(p.DesiredReplicas),
		CurrentReplicas:    optPtr(p.CurrentReplicas),
		ReadyReplicas:      optPtr(p.ReadyReplicas),
		BilledReplicas:     types.Int64Value(int64(p.BilledReplicas)),
	}
	if out.AutoscalingEnabled.ValueBool() {
		out.AutoscalingMinSize = types.Int64Value(int64(p.Replicas))
		if p.Autoscaling.MinSize != nil {
			out.AutoscalingMinSize = types.Int64Value(*p.Autoscaling.MinSize)
		}
		out.AutoscalingMaxSize = optInt(int64(p.Autoscaling.MaxSize))
	}
	if out.Taints == nil {
		out.Taints = []TaintModel{}
	}
	return out
}
