package net

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Data sources h3_vpc, h3_network, h3_eip and h3_eips (0.3.0). They look up
// objects made outside this configuration (console, another state) by ID or
// by project + name. The h3ovn list endpoints return a bare JSON array.

const ovnBase = "/api/ovn/v1"

// decodeList accepts a bare array or an object wrapping it under key.
func decodeList[T any](raw json.RawMessage, key string) ([]T, error) {
	var arr []T
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if v, ok := obj[key]; ok {
		if err := json.Unmarshal(v, &arr); err != nil {
			return nil, err
		}
	}
	return arr, nil
}

func listOVN[T any](ctx context.Context, c *client.Client, coll, projectID string, extra map[string]string) ([]T, error) {
	q := map[string]string{"project_id": projectID}
	for k, v := range extra {
		q[k] = v
	}
	var raw json.RawMessage
	if err := c.Do(ctx, "GET", ovnBase+"/"+coll, q, nil, &raw); err != nil {
		return nil, err
	}
	return decodeList[T](raw, coll)
}

func sortEIPs(e []EIP) {
	sort.SliceStable(e, func(i, j int) bool {
		if e[i].Name != e[j].Name {
			return e[i].Name < e[j].Name
		}
		return e[i].ID < e[j].ID
	})
}

func pickOne[T any](items []T, what string, match func(T) bool) (*T, error) {
	var found []T
	for _, it := range items {
		if match(it) {
			found = append(found, it)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no %s matches the arguments", what)
	case 1:
		return &found[0], nil
	}
	return nil, fmt.Errorf("%d %ss match the arguments; narrow the lookup (e.g. by `id`)", len(found), what)
}

// ---- h3_vpc -----------------------------------------------------------------

type vpcDataSource struct{ client *client.Client }

type vpcDataModel struct {
	ID         types.String `tfsdk:"id"`
	ProjectID  types.String `tfsdk:"project_id"`
	Name       types.String `tfsdk:"name"`
	Namespaces types.List   `tfsdk:"namespaces"`
	Status     types.String `tfsdk:"status"`
}

func NewVPCDataSource() datasource.DataSource { return &vpcDataSource{} }

func (d *vpcDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc"
}

func (d *vpcDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a VPC by `id`, or by `project_id` (and `name` when the project has several VPCs). " +
			"The project VPC has the project ID as its ID.",
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "VPC ID"},
			"project_id": schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project ID (UUID); the provider default when omitted"},
			"name":       schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "VPC name"},
			"namespaces": schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Namespaces attached to the VPC"},
			"status":     schema.StringAttribute{Computed: true, MarkdownDescription: "VPC status"},
		},
	}
}

func (d *vpcDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *vpcDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m vpcDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var v *VPC
	if id := common.KnownString(m.ID); id != "" {
		var got VPC
		if err := d.client.Do(ctx, "GET", ovnBase+"/vpcs/"+url.PathEscape(id), nil, nil, &got); err != nil {
			resp.Diagnostics.AddError("Error reading VPC", err.Error())
			return
		}
		v = &got
	} else {
		pid, ok := common.DataSourceProjectID(d.client, m.ProjectID, resp)
		if !ok {
			return
		}
		all, err := listOVN[VPC](ctx, d.client, "vpcs", pid, nil)
		if err != nil {
			resp.Diagnostics.AddError("Error listing VPCs", err.Error())
			return
		}
		name := common.KnownString(m.Name)
		if v, err = pickOne(all, "VPC", func(x VPC) bool { return name == "" || x.Name == name }); err != nil {
			resp.Diagnostics.AddError("VPC not found", err.Error())
			return
		}
	}
	m.ID = types.StringValue(v.ID)
	m.Name = types.StringValue(v.Name)
	m.ProjectID = types.StringValue(first(v.ProjectID, m.ProjectID.ValueString()))
	ns, diags := types.ListValueFrom(ctx, types.StringType, v.Namespaces)
	resp.Diagnostics.Append(diags...)
	m.Namespaces = ns
	m.Status = types.StringValue(v.Status)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---- h3_network -------------------------------------------------------------

type networkDataSource struct{ client *client.Client }

type networkDataModel struct {
	ID          types.String `tfsdk:"id"`
	ProjectID   types.String `tfsdk:"project_id"`
	Name        types.String `tfsdk:"name"`
	SubnetID    types.String `tfsdk:"subnet_id"`
	SubnetName  types.String `tfsdk:"subnet_name"`
	VPCID       types.String `tfsdk:"vpc_id"`
	VPCName     types.String `tfsdk:"vpc_name"`
	CIDRBlock   types.String `tfsdk:"cidr_block"`
	Protocol    types.String `tfsdk:"protocol"`
	GatewayID   types.String `tfsdk:"gateway_id"`
	GatewayName types.String `tfsdk:"gateway_name"`
	GatewayIP   types.String `tfsdk:"gateway_ip"`
	Status      types.String `tfsdk:"status"`
}

func NewNetworkDataSource() datasource.DataSource { return &networkDataSource{} }

func (d *networkDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (d *networkDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	c := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a network (subnet) by `id`, or by `project_id` + `name` (or `cidr_block`). " +
			"Use `subnet_name` for `h3_vm.subnet_name` and `id` for `h3_eip.network_id`.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Network (subnet) ID"},
			"project_id":   schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project ID (UUID)"},
			"name":         schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Network name"},
			"cidr_block":   schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "CIDR block"},
			"subnet_id":    c("Subnet ID (same as `id`)"),
			"subnet_name":  c("Subnet name in the platform (for `h3_vm.subnet_name`)"),
			"vpc_id":       c("VPC ID"),
			"vpc_name":     c("VPC name in the platform"),
			"protocol":     c("IP protocol"),
			"gateway_id":   c("NAT gateway ID"),
			"gateway_name": c("NAT gateway name"),
			"gateway_ip":   c("Gateway IP address"),
			"status":       c("Network status"),
		},
	}
}

func (d *networkDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *networkDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m networkDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var n *Network
	if id := common.KnownString(m.ID); id != "" {
		var got Network
		if err := d.client.Do(ctx, "GET", ovnBase+"/networks/"+url.PathEscape(id), nil, nil, &got); err != nil {
			resp.Diagnostics.AddError("Error reading network", err.Error())
			return
		}
		n = &got
	} else {
		pid, ok := common.DataSourceProjectID(d.client, m.ProjectID, resp)
		if !ok {
			return
		}
		all, err := listOVN[Network](ctx, d.client, "networks", pid, nil)
		if err != nil {
			resp.Diagnostics.AddError("Error listing networks", err.Error())
			return
		}
		name, cidr := common.KnownString(m.Name), common.KnownString(m.CIDRBlock)
		if n, err = pickOne(all, "network", func(x Network) bool {
			return (name == "" || x.Name == name) && (cidr == "" || x.CIDRBlock == cidr)
		}); err != nil {
			resp.Diagnostics.AddError("Network not found", err.Error())
			return
		}
	}
	m.ID = types.StringValue(n.SubnetID)
	m.SubnetID = types.StringValue(n.SubnetID)
	m.ProjectID = types.StringValue(first(n.ProjectID, m.ProjectID.ValueString()))
	m.Name = types.StringValue(n.Name)
	m.SubnetName = types.StringValue(n.SubnetName)
	m.VPCID = types.StringValue(n.VPCID)
	m.VPCName = types.StringValue(n.VPCName)
	m.CIDRBlock = types.StringValue(n.CIDRBlock)
	m.Protocol = types.StringValue(n.Protocol)
	m.GatewayID = types.StringValue(n.GatewayID)
	m.GatewayName = types.StringValue(n.GatewayName)
	m.GatewayIP = types.StringValue(n.GatewayIP)
	m.Status = types.StringValue(n.Status)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---- h3_eip / h3_eips -------------------------------------------------------

var eipAttrTypes = map[string]attr.Type{
	"id":                  types.StringType,
	"name":                types.StringType,
	"description":         types.StringType,
	"ip_address":          types.StringType,
	"status":              types.StringType,
	"bound":               types.BoolType,
	"bound_resource_type": types.StringType,
	"bound_resource_id":   types.StringType,
}

type eipDataModel struct {
	ID                types.String `tfsdk:"id"`
	ProjectID         types.String `tfsdk:"project_id"`
	Name              types.String `tfsdk:"name"`
	Description       types.String `tfsdk:"description"`
	IPAddress         types.String `tfsdk:"ip_address"`
	GatewayName       types.String `tfsdk:"gateway_name"`
	Status            types.String `tfsdk:"status"`
	Bound             types.Bool   `tfsdk:"bound"`
	BoundResourceType types.String `tfsdk:"bound_resource_type"`
	BoundResourceID   types.String `tfsdk:"bound_resource_id"`
}

func eipBound(e EIP) (bool, string, string) {
	t, id := e.binding()
	if e.Bound != nil {
		return *e.Bound, t, id
	}
	return id != "", t, id
}

type eipDataSource struct{ client *client.Client }

func NewEIPDataSource() datasource.DataSource { return &eipDataSource{} }

func (d *eipDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_eip"
}

func (d *eipDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up one Elastic IP by `id`, or by `name` or `ip_address` in a project. " +
			"Pass its `id` to `h3_vm.eip_id` or `h3_database.eip_id` to use an address reserved outside this configuration.",
		Attributes: map[string]schema.Attribute{
			"id":                  schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "EIP ID"},
			"project_id":          schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project ID (UUID)"},
			"name":                schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "EIP name"},
			"description":         schema.StringAttribute{Computed: true, MarkdownDescription: "EIP comment (null when not set)"},
			"ip_address":          schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Public IPv4 address"},
			"gateway_name":        schema.StringAttribute{Computed: true, MarkdownDescription: "Gateway name in the platform"},
			"status":              schema.StringAttribute{Computed: true, MarkdownDescription: "EIP status (DETACHED, ATTACHED, PENDING, ERROR)"},
			"bound":               schema.BoolAttribute{Computed: true, MarkdownDescription: "True when the EIP is attached or reserved"},
			"bound_resource_type": schema.StringAttribute{Computed: true, MarkdownDescription: "Type of the bound resource (`vm`, `db`, `lb`)"},
			"bound_resource_id":   schema.StringAttribute{Computed: true, MarkdownDescription: "ID of the bound resource"},
		},
	}
}

func (d *eipDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *eipDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m eipDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, name, ip := common.KnownString(m.ID), common.KnownString(m.Name), common.KnownString(m.IPAddress)
	if id == "" && name == "" && ip == "" {
		resp.Diagnostics.AddError("Invalid EIP lookup", "set `id`, `name` or `ip_address`")
		return
	}
	var e *EIP
	if id != "" {
		var got EIP
		if err := d.client.Do(ctx, "GET", ovnBase+"/eips/"+url.PathEscape(id), nil, nil, &got); err != nil {
			resp.Diagnostics.AddError("Error reading EIP", err.Error())
			return
		}
		e = &got
	} else {
		pid, ok := common.DataSourceProjectID(d.client, m.ProjectID, resp)
		if !ok {
			return
		}
		all, err := listOVN[EIP](ctx, d.client, "eips", pid, nil)
		if err != nil {
			resp.Diagnostics.AddError("Error listing EIPs", err.Error())
			return
		}
		if e, err = pickOne(all, "EIP", func(x EIP) bool {
			return (name == "" || x.Name == name) && (ip == "" || x.IPAddress == ip)
		}); err != nil {
			resp.Diagnostics.AddError("EIP not found", err.Error())
			return
		}
	}
	bound, bt, bid := eipBound(*e)
	m.ID = types.StringValue(e.ID)
	m.ProjectID = types.StringValue(first(e.ProjectID, m.ProjectID.ValueString()))
	m.Name = types.StringValue(e.Name)
	m.Description = common.OptString(e.Description)
	m.IPAddress = types.StringValue(e.IPAddress)
	m.GatewayName = types.StringValue(e.GatewayName)
	m.Status = types.StringValue(e.Status)
	m.Bound = types.BoolValue(bound)
	m.BoundResourceType = common.OptString(bt)
	m.BoundResourceID = common.OptString(bid)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

type eipsDataSource struct{ client *client.Client }

type eipsDataModel struct {
	ProjectID types.String `tfsdk:"project_id"`
	Bound     types.Bool   `tfsdk:"bound"`
	EIPs      types.List   `tfsdk:"eips"`
	IDs       types.List   `tfsdk:"ids"`
}

func NewEIPsDataSource() datasource.DataSource { return &eipsDataSource{} }

func (d *eipsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_eips"
}

func (d *eipsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the Elastic IPs of a project, optionally only free (`bound = false`) or only bound ones. " +
			"This is the Terraform side of the console's EIP selector: pick a free address and pass it to `eip_id`. " +
			"The list is read at plan time; two configurations picking `ids[0]` of the same pool can race, " +
			"in which case the second apply fails with `EIP_ALREADY_BOUND` and nothing is changed.",
		Attributes: map[string]schema.Attribute{
			"project_id": schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project ID (UUID); the provider default when omitted"},
			"bound":      schema.BoolAttribute{Optional: true, MarkdownDescription: "`false`: only free EIPs; `true`: only bound ones; unset: all"},
			"ids":        schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "EIP IDs, sorted by name"},
			"eips": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "EIPs, sorted by name",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":                  schema.StringAttribute{Computed: true, MarkdownDescription: "EIP ID"},
					"name":                schema.StringAttribute{Computed: true, MarkdownDescription: "EIP name"},
					"description":         schema.StringAttribute{Computed: true, MarkdownDescription: "EIP comment (null when not set)"},
					"ip_address":          schema.StringAttribute{Computed: true, MarkdownDescription: "Public IPv4 address"},
					"status":              schema.StringAttribute{Computed: true, MarkdownDescription: "EIP status"},
					"bound":               schema.BoolAttribute{Computed: true, MarkdownDescription: "Attached or reserved"},
					"bound_resource_type": schema.StringAttribute{Computed: true, MarkdownDescription: "Type of the bound resource"},
					"bound_resource_id":   schema.StringAttribute{Computed: true, MarkdownDescription: "ID of the bound resource"},
				}},
			},
		},
	}
}

func (d *eipsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *eipsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m eipsDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var extra map[string]string
	if !m.Bound.IsNull() && !m.Bound.IsUnknown() {
		extra = map[string]string{"bound": fmt.Sprint(m.Bound.ValueBool())}
	}
	pid, ok := common.DataSourceProjectID(d.client, m.ProjectID, resp)
	if !ok {
		return
	}
	m.ProjectID = types.StringValue(pid)
	all, err := listOVN[EIP](ctx, d.client, "eips", pid, extra)
	if err != nil {
		resp.Diagnostics.AddError("Error listing EIPs", err.Error())
		return
	}
	sortEIPs(all)
	objType := types.ObjectType{AttrTypes: eipAttrTypes}
	objs := make([]attr.Value, 0, len(all))
	ids := make([]attr.Value, 0, len(all))
	for _, e := range all {
		bound, bt, bid := eipBound(e)
		// Older backends ignore the bound filter: apply it here too.
		if extra != nil && bound != m.Bound.ValueBool() {
			continue
		}
		o, diags := types.ObjectValue(eipAttrTypes, map[string]attr.Value{
			"id": types.StringValue(e.ID), "name": types.StringValue(e.Name), "description": common.OptString(e.Description),
			"ip_address": types.StringValue(e.IPAddress),
			"status":     types.StringValue(e.Status), "bound": types.BoolValue(bound),
			"bound_resource_type": common.OptString(bt), "bound_resource_id": common.OptString(bid),
		})
		resp.Diagnostics.Append(diags...)
		objs = append(objs, o)
		ids = append(ids, types.StringValue(e.ID))
	}
	l, d1 := types.ListValue(objType, objs)
	resp.Diagnostics.Append(d1...)
	il, d2 := types.ListValue(types.StringType, ids)
	resp.Diagnostics.Append(d2...)
	m.EIPs, m.IDs = l, il
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
