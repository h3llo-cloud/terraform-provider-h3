package database

import (
	"context"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ---- h3_database_poolers ----

func NewPoolersDataSource() datasource.DataSource { return &PoolersDataSource{} }

type PoolersDataSource struct{ client *client.Client }

type PoolersDSModel struct {
	DatabaseID types.String    `tfsdk:"database_id"`
	Poolers    []PoolerDSModel `tfsdk:"poolers"`
	Budget     types.Object    `tfsdk:"budget"`
	Limits     types.Object    `tfsdk:"limits"`
}

type PoolerDSModel struct {
	PoolerID            types.String `tfsdk:"pooler_id"`
	DisplayName         types.String `tfsdk:"display_name"`
	Role                types.String `tfsdk:"role"`
	Mode                types.String `tfsdk:"mode"`
	Instances           types.Int64  `tfsdk:"instances"`
	ReadyInstances      types.Int64  `tfsdk:"ready_instances"`
	Default             types.Bool   `tfsdk:"default"`
	Paused              types.Bool   `tfsdk:"paused"`
	Phase               types.String `tfsdk:"phase"`
	K8sName             types.String `tfsdk:"k8s_name"`
	Adopted             types.Bool   `tfsdk:"adopted"`
	Parameters          types.Map    `tfsdk:"parameters"`
	EffectiveParameters types.Map    `tfsdk:"effective_parameters"`
	VPCMode             types.String `tfsdk:"vpc_mode"`
	VPCPort             types.Int64  `tfsdk:"vpc_port"`
	VPCIP               types.String `tfsdk:"vpc_ip"`
	VPCStatus           types.String `tfsdk:"vpc_status"`
	VPCEndpoint         types.String `tfsdk:"vpc_endpoint"`
	PublicAddresses     types.List   `tfsdk:"public_addresses"`
	PublicEndpoints     types.List   `tfsdk:"public_endpoints"`
}

var (
	budgetUsedAttrTypes = map[string]attr.Type{"used": types.Int64Type, "max": types.Int64Type}
	budgetAttrTypes     = map[string]attr.Type{
		"max_connections": types.Int64Type,
		"reserve":         types.Int64Type,
		"primary":         types.ObjectType{AttrTypes: budgetUsedAttrTypes},
		"replica":         types.ObjectType{AttrTypes: budgetUsedAttrTypes},
	}
	limitsAttrTypes = map[string]attr.Type{
		"max_poolers":   types.Int64Type,
		"max_instances": types.Int64Type,
		"port_min":      types.Int64Type,
		"port_max":      types.Int64Type,
		"default_ports": types.MapType{ElemType: types.Int64Type},
	}
)

func (d *PoolersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_poolers"
}

func (d *PoolersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	num := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Computed: true, MarkdownDescription: desc}
	}
	boolean := func(desc string) schema.BoolAttribute {
		return schema.BoolAttribute{Computed: true, MarkdownDescription: desc}
	}
	strMap := func(desc string) schema.MapAttribute {
		return schema.MapAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: desc}
	}
	used := func(desc string) schema.SingleNestedAttribute {
		return schema.SingleNestedAttribute{Computed: true, MarkdownDescription: desc, Attributes: map[string]schema.Attribute{
			"used": num("Connections taken by the poolers (instances × max_db_connections) plus the reserve"),
			"max":  num("max_connections of the instance"),
		}}
	}
	pub := publicAddressesAttribute()
	resp.Schema = schema.Schema{
		MarkdownDescription: "Connection poolers (PgBouncer) of a PostgreSQL `h3_database`, including poolers created in " +
			"the console or taken over by h3db (`adopted`), with the connection budget and the limits of the database.",
		Attributes: map[string]schema.Attribute{
			"database_id": schema.StringAttribute{
				Required: true, MarkdownDescription: "ID of the `h3_database`",
				Validators: []validator.String{common.UUIDValidator()},
			},
			"poolers": schema.ListNestedAttribute{
				Computed: true, MarkdownDescription: "Poolers, in the order h3db returns them",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"pooler_id":            str("Pooler ID"),
					"display_name":         str("Name shown in the console"),
					"role":                 str("`rw`, `ro` or `r`"),
					"mode":                 str("`session` or `transaction`"),
					"instances":            num("PgBouncer pods requested"),
					"ready_instances":      num("Ready PgBouncer pods"),
					"default":              boolean("The default pooler of the database"),
					"paused":               boolean("Paused (PgBouncer `PAUSE`)"),
					"phase":                str("`READY`, `PROGRESSING`, `PAUSED` or `STOPPED`"),
					"k8s_name":             str("Name of the Kubernetes Pooler object"),
					"adopted":              boolean("Created outside h3db and taken over"),
					"parameters":           strMap("Parameters set by the user"),
					"effective_parameters": strMap("Parameters in effect"),
					"vpc_mode":             str("`port`, `address` or `none`"),
					"vpc_port":             num("Port of the VPC listener"),
					"vpc_ip":               str("IP of the VPC listener"),
					"vpc_status":           str("`assigned`, `pending` or `unavailable`"),
					"vpc_endpoint":         str("`<ip>:<port>` in the VPC"),
					"public_addresses": schema.ListNestedAttribute{
						Computed: true, MarkdownDescription: pub.MarkdownDescription,
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"ip":       str("Public IP"),
							"port":     num("Port"),
							"via":      str("`port-sources`, `service` or `external`"),
							"eip_id":   str("Elastic IP ID"),
							"endpoint": str("`<ip>:<port>`"),
						}},
					},
					"public_endpoints": schema.ListAttribute{Computed: true, ElementType: types.StringType,
						MarkdownDescription: "`<ip>:<port>` on the public EIP"},
				}},
			},
			"budget": schema.SingleNestedAttribute{
				Computed: true, MarkdownDescription: "Server connection budget of the database; null when h3db does not report it",
				Attributes: map[string]schema.Attribute{
					"max_connections": num("max_connections of the database"),
					"reserve":         num("Connections kept free for direct sessions"),
					"primary":         used("Budget of the primary (poolers `rw` and `r`)"),
					"replica":         used("Budget of each replica (poolers `ro` and `r`)"),
				},
			},
			"limits": schema.SingleNestedAttribute{
				Computed: true, MarkdownDescription: "Limits of h3db; null when not reported",
				Attributes: map[string]schema.Attribute{
					"max_poolers":   num("Poolers per database"),
					"max_instances": num("PgBouncer pods per pooler"),
					"port_min":      num("Lowest listener port"),
					"port_max":      num("Highest listener port"),
					"default_ports": schema.MapAttribute{Computed: true, ElementType: types.Int64Type,
						MarkdownDescription: "Default VPC port per role (`rw`, `ro`, `r`)"},
				},
			},
		},
	}
}

func (d *PoolersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *PoolersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m PoolersDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	list, err := listPoolers(ctx, d.client, m.DatabaseID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(poolerErrorDiag("reading database poolers", err))
		return
	}
	m.Poolers = make([]PoolerDSModel, 0, len(list.Poolers))
	for i := range list.Poolers {
		m.Poolers = append(m.Poolers, poolerDS(m.DatabaseID, &list.Poolers[i]))
	}
	m.Budget, m.Limits = budgetValue(list.Budget), limitsValue(list.Limits)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// poolerDS reuses the resource mapping.
func poolerDS(dbID types.String, p *Pooler) PoolerDSModel {
	m := PoolerResourceModel{DatabaseID: dbID, Parameters: types.MapValueMust(types.StringType, nil), PublicVia: types.StringNull()}
	m.applyPooler(p)
	return PoolerDSModel{
		PoolerID: m.PoolerID, DisplayName: m.DisplayName, Role: m.Role, Mode: m.Mode,
		Instances: m.Instances, ReadyInstances: m.ReadyInstances, Default: m.Default, Paused: m.Paused,
		Phase: m.Phase, K8sName: m.K8sName, Adopted: m.Adopted,
		Parameters: m.Parameters, EffectiveParameters: m.EffectiveParameters,
		VPCMode: m.VPCMode, VPCPort: m.VPCPort, VPCIP: m.VPCIP, VPCStatus: m.VPCStatus, VPCEndpoint: m.VPCEndpoint,
		PublicAddresses: m.PublicAddresses, PublicEndpoints: m.PublicEndpoints,
	}
}

func budgetUsedValue(b *BudgetUsed) types.Object {
	if b == nil {
		return types.ObjectNull(budgetUsedAttrTypes)
	}
	return types.ObjectValueMust(budgetUsedAttrTypes, map[string]attr.Value{
		"used": types.Int64Value(b.Used), "max": types.Int64Value(b.Max),
	})
}

func budgetValue(b *PoolerBudget) types.Object {
	if b == nil {
		return types.ObjectNull(budgetAttrTypes)
	}
	return types.ObjectValueMust(budgetAttrTypes, map[string]attr.Value{
		"max_connections": types.Int64Value(b.MaxConnections),
		"reserve":         types.Int64Value(b.Reserve),
		"primary":         budgetUsedValue(b.Primary),
		"replica":         budgetUsedValue(b.Replica),
	})
}

func limitsValue(l *PoolerLimits) types.Object {
	if l == nil {
		return types.ObjectNull(limitsAttrTypes)
	}
	ports := make(map[string]attr.Value, len(l.DefaultPorts))
	for k, v := range l.DefaultPorts {
		ports[k] = types.Int64Value(v)
	}
	return types.ObjectValueMust(limitsAttrTypes, map[string]attr.Value{
		"max_poolers":   types.Int64Value(l.MaxPoolers),
		"max_instances": types.Int64Value(l.MaxInstances),
		"port_min":      types.Int64Value(l.PortMin),
		"port_max":      types.Int64Value(l.PortMax),
		"default_ports": types.MapValueMust(types.Int64Type, ports),
	})
}
