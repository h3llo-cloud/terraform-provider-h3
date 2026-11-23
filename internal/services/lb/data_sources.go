package lb

import (
	"context"
	"fmt"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSourceWithConfigure = &LBDataSource{}
	_ datasource.DataSourceWithConfigure = &LBsDataSource{}
)

// LBData is one load balancer in the data sources.
type LBData struct {
	ID         types.String   `tfsdk:"id"`
	ProjectID  types.String   `tfsdk:"project_id"`
	Name       types.String   `tfsdk:"name"`
	Status     types.String   `tfsdk:"status"`
	ExternalIP types.String   `tfsdk:"external_ip"`
	EIPID      types.String   `tfsdk:"eip_id"`
	Endpoint   types.String   `tfsdk:"endpoint"`
	SubnetName types.String   `tfsdk:"subnet_name"`
	Algorithm  types.String   `tfsdk:"algorithm"`
	Listeners  []DataListener `tfsdk:"listener"`
	Targets    []types.String `tfsdk:"targets"`
}

type DataListener struct {
	Port       types.Int64  `tfsdk:"port"`
	TargetPort types.Int64  `tfsdk:"target_port"`
	Protocol   types.String `tfsdk:"protocol"`
}

func toData(lb LoadBalancer) LBData {
	alg := "round_robin"
	if lb.Config != nil && lb.Config.Algorithm != "" {
		alg = lb.Config.Algorithm
	}
	d := LBData{
		ID: types.StringValue(lb.ID), ProjectID: types.StringValue(lb.ProjectID), Name: types.StringValue(lb.Name),
		Status: types.StringValue(lb.Status), ExternalIP: common.OptString(lb.ExternalIP), EIPID: common.OptString(lb.EipID), Endpoint: common.OptString(lb.Endpoint),
		SubnetName: common.OptString(lb.SubnetName), Algorithm: types.StringValue(alg),
		Listeners: []DataListener{}, Targets: []types.String{},
	}
	for _, p := range lb.Ports {
		d.Listeners = append(d.Listeners, DataListener{types.Int64Value(p.Frontend), types.Int64Value(p.Backend), types.StringValue("TCP")})
	}
	for _, b := range lb.Backends {
		d.Targets = append(d.Targets, types.StringValue(b))
	}
	return d
}

func lbAttrs(lookup bool) map[string]schema.Attribute {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: desc, Computed: true}
	}
	a := map[string]schema.Attribute{
		"id":          str("Load balancer ID."),
		"project_id":  str("Project ID."),
		"name":        str("Name."),
		"status":      str("`PENDING`, `CREATING`, `RUNNING`, `DELETING` or `FAILED`."),
		"external_ip": str("Public IP address."),
		"eip_id":      str("Elastic IP of the load balancer (LB Basic); null for legacy lb-svc load balancers."),
		"endpoint":    str("Endpoint."),
		"subnet_name": str("Subnet the load balancer runs in."),
		"algorithm":   str("Balancing algorithm."),
		"listener": schema.ListNestedAttribute{MarkdownDescription: "Listeners.", Computed: true,
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"port":        schema.Int64Attribute{MarkdownDescription: "Public port.", Computed: true},
				"target_port": schema.Int64Attribute{MarkdownDescription: "Target port.", Computed: true},
				"protocol":    str("Protocol."),
			}}},
		"targets": schema.ListAttribute{MarkdownDescription: "Target IP addresses.", ElementType: types.StringType, Computed: true},
	}
	if lookup {
		a["id"] = schema.StringAttribute{MarkdownDescription: "Load balancer ID; or set `name`.", Optional: true, Computed: true}
		a["project_id"] = schema.StringAttribute{MarkdownDescription: "Project ID (UUID); the provider default when omitted.", Optional: true, Computed: true}
		a["name"] = schema.StringAttribute{MarkdownDescription: "Name; or set `id`.", Optional: true, Computed: true}
	}
	return a
}

func listLBs(ctx context.Context, c *client.Client, project string) ([]LoadBalancer, error) {
	var out []LoadBalancer
	err := c.Do(ctx, "GET", apiBase, map[string]string{"projectId": project}, nil, &out)
	return out, err
}

// ---- h3_lb ----

func NewLBDataSource() datasource.DataSource { return &LBDataSource{} }

type LBDataSource struct{ client *client.Client }

func (d *LBDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb"
}

func (d *LBDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a load balancer by `project_id` + `id` or `project_id` + `name`. " + AccessNote,
		Attributes:          lbAttrs(true),
	}
}

func (d *LBDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *LBDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg LBData
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, ok := common.DataSourceProjectID(d.client, cfg.ProjectID, resp)
	if !ok {
		return
	}
	id, name := common.KnownString(cfg.ID), common.KnownString(cfg.Name)
	if (id == "") == (name == "") {
		resp.Diagnostics.AddError("Invalid lookup", "set exactly one of id or name")
		return
	}
	var found *LoadBalancer
	if id != "" {
		lb, err := getLB(ctx, d.client, id)
		if err != nil {
			resp.Diagnostics.AddError("Error reading load balancer", explain(err))
			return
		}
		if lb.ProjectID != "" && lb.ProjectID != project {
			resp.Diagnostics.AddError("Load balancer not found", fmt.Sprintf("load balancer %s is not in project %s", id, project))
			return
		}
		found = lb
	} else {
		all, err := listLBs(ctx, d.client, project)
		if err != nil {
			resp.Diagnostics.AddError("Error listing load balancers", explain(err))
			return
		}
		for i := range all {
			if all[i].Name == name {
				found = &all[i]
				break
			}
		}
		if found == nil {
			resp.Diagnostics.AddError("Load balancer not found", fmt.Sprintf("no load balancer named %q in project %s", name, project))
			return
		}
	}
	out := toData(*found)
	if out.ProjectID.ValueString() == "" {
		out.ProjectID = types.StringValue(project)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}

// ---- h3_lbs ----

func NewLBsDataSource() datasource.DataSource { return &LBsDataSource{} }

type LBsDataSource struct{ client *client.Client }

type LBsModel struct {
	ProjectID     types.String `tfsdk:"project_id"`
	LoadBalancers []LBData     `tfsdk:"load_balancers"`
}

func (d *LBsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lbs"
}

func (d *LBsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the load balancers of a project. " + AccessNote,
		Attributes: map[string]schema.Attribute{
			"project_id": schema.StringAttribute{MarkdownDescription: "Project ID (UUID); the provider default when omitted.", Optional: true, Computed: true},
			"load_balancers": schema.ListNestedAttribute{MarkdownDescription: "Load balancers of the project.", Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: lbAttrs(false)}},
		},
	}
}

func (d *LBsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *LBsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg LBsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, ok := common.DataSourceProjectID(d.client, cfg.ProjectID, resp)
	if !ok {
		return
	}
	cfg.ProjectID = types.StringValue(project)
	all, err := listLBs(ctx, d.client, project)
	if err != nil {
		resp.Diagnostics.AddError("Error listing load balancers", explain(err))
		return
	}
	cfg.LoadBalancers = []LBData{}
	for _, lb := range all {
		cfg.LoadBalancers = append(cfg.LoadBalancers, toData(lb))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
