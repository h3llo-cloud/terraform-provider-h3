package sg

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"h3terraform/internal/client"
)

var (
	_ datasource.DataSource              = &GroupDataSource{}
	_ datasource.DataSourceWithConfigure = &GroupDataSource{}
)

func NewGroupDataSource() datasource.DataSource { return &GroupDataSource{} }

type GroupDataSource struct{ client *client.Client }

type DataRuleModel struct {
	ID                    types.String `tfsdk:"id"`
	Direction             types.String `tfsdk:"direction"`
	Action                types.String `tfsdk:"action"`
	Protocol              types.String `tfsdk:"protocol"`
	PortFrom              types.Int64  `tfsdk:"port_from"`
	PortTo                types.Int64  `tfsdk:"port_to"`
	RemoteCIDR            types.String `tfsdk:"remote_cidr"`
	RemoteSecurityGroupID types.String `tfsdk:"remote_security_group_id"`
	Priority              types.Int64  `tfsdk:"priority"`
	Description           types.String `tfsdk:"description"`
}

type GroupDataSourceModel struct {
	ID            types.String    `tfsdk:"id"`
	ProjectID     types.String    `tfsdk:"project_id"`
	Name          types.String    `tfsdk:"name"`
	Description   types.String    `tfsdk:"description"`
	IsDefault     types.Bool      `tfsdk:"is_default"`
	Status        types.String    `tfsdk:"status"`
	AttachedCount types.Int64     `tfsdk:"attached_count"`
	Rules         []DataRuleModel `tfsdk:"rules"`
}

func (d *GroupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group"
}

func (d *GroupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: desc, Computed: true}
	}
	num := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{MarkdownDescription: desc, Computed: true}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a security group by `id`, or by `project_id` + `name` (`name = \"default\"` finds the project default group).",
		Attributes: map[string]schema.Attribute{
			"id":             schema.StringAttribute{MarkdownDescription: "Security group ID.", Optional: true, Computed: true},
			"project_id":     schema.StringAttribute{MarkdownDescription: "Project ID for a lookup by `name`; the provider default when omitted.", Optional: true, Computed: true},
			"name":           schema.StringAttribute{MarkdownDescription: "Group name (case-insensitive).", Optional: true, Computed: true},
			"description":    str("Description."),
			"is_default":     schema.BoolAttribute{MarkdownDescription: "True for the project `default` group.", Computed: true},
			"status":         str("`ACTIVE`, `SYNCING` or `ERROR`."),
			"attached_count": num("Number of resources the group is attached to."),
			"rules": schema.ListNestedAttribute{
				MarkdownDescription: "Rules of the group.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":                       str("Rule ID (use with `h3_security_group_rule` import)."),
					"direction":                str("`ingress` or `egress`."),
					"action":                   str("`allow` or `deny`."),
					"protocol":                 str("`all`, `tcp`, `udp` or `icmp`."),
					"port_from":                num("First port."),
					"port_to":                  num("Last port."),
					"remote_cidr":              str("Peer CIDR."),
					"remote_security_group_id": str("Peer security group."),
					"priority":                 num("Priority."),
					"description":              str("Description."),
				}},
			},
		},
	}
}

func (d *GroupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, err := configure(req.ProviderData)
	if err != nil {
		resp.Diagnostics.AddError("Unexpected Data Source Configure Type", err.Error())
		return
	}
	d.client = c
}

// findGroup resolves the lookup arguments to one group.
func findGroup(ctx context.Context, c *client.Client, id, projectID, name string) (*Group, error) {
	if id != "" {
		return getGroup(ctx, c, id)
	}
	if projectID == "" || name == "" {
		return nil, fmt.Errorf("set either id, or project_id and name")
	}
	groups, err := listGroups(ctx, c, projectID)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if strings.EqualFold(groups[i].Name, name) {
			return &groups[i], nil
		}
	}
	return nil, fmt.Errorf("no security group named %q in project %s", name, projectID)
}

func (d *GroupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m GroupDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := m.ProjectID.ValueString()
	if pid == "" && d.client != nil && m.ID.ValueString() == "" {
		pid = d.client.ProjectID()
	}
	g, err := findGroup(ctx, d.client, m.ID.ValueString(), pid, m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading security group", err.Error())
		return
	}
	m.ID = types.StringValue(g.ID)
	m.ProjectID = types.StringValue(g.ProjectID)
	m.Name = types.StringValue(g.Name)
	m.Description = types.StringValue(g.Description)
	m.IsDefault = types.BoolValue(g.IsDefault)
	m.Status = types.StringValue(g.Status)
	m.AttachedCount = types.Int64Value(g.AttachedCount)
	m.Rules = make([]DataRuleModel, 0, len(g.Rules))
	for _, r := range g.Rules {
		m.Rules = append(m.Rules, DataRuleModel{
			ID:                    types.StringValue(r.ID),
			Direction:             types.StringValue(r.Direction),
			Action:                types.StringValue(r.Action),
			Protocol:              types.StringValue(r.Protocol),
			PortFrom:              types.Int64Value(r.PortFrom),
			PortTo:                types.Int64Value(r.PortTo),
			RemoteCIDR:            types.StringValue(r.RemoteCIDR),
			RemoteSecurityGroupID: types.StringValue(r.RemoteSecurityGroupID),
			Priority:              types.Int64Value(r.Priority),
			Description:           types.StringValue(r.Description),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}
