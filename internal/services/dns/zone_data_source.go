package dns

import (
	"context"
	"fmt"
	"strings"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = &ZoneDataSource{}
	_ datasource.DataSourceWithConfigure = &ZoneDataSource{}
)

func NewZoneDataSource() datasource.DataSource { return &ZoneDataSource{} }

type ZoneDataSource struct{ client *client.Client }

type ZoneDataModel struct {
	ProjectID   types.String `tfsdk:"project_id"`
	Name        types.String `tfsdk:"name"`
	ID          types.String `tfsdk:"id"`
	Status      types.String `tfsdk:"status"`
	Nameservers types.List   `tfsdk:"nameservers"`
	DNSSEC      types.Bool   `tfsdk:"dnssec"`
	DSRecords   types.List   `tfsdk:"ds_records"`
}

func (d *ZoneDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_zone"
}

func (d *ZoneDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up an existing zone of a project by name.",
		Attributes: map[string]schema.Attribute{
			"project_id":  schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Project ID; the provider default when omitted"},
			"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Zone name"},
			"id":          schema.StringAttribute{Computed: true},
			"status":      schema.StringAttribute{Computed: true},
			"nameservers": schema.ListAttribute{ElementType: types.StringType, Computed: true},
			"dnssec":      schema.BoolAttribute{Computed: true},
			"ds_records":  schema.ListAttribute{ElementType: types.StringType, Computed: true},
		},
	}
}

func (d *ZoneDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Data Source Configure Type", fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData))
		return
	}
	d.client = c
}

func (d *ZoneDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg ZoneDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid, ok := common.DataSourceProjectID(d.client, cfg.ProjectID, resp)
	if !ok {
		return
	}
	cfg.ProjectID = types.StringValue(pid)
	want := stringsTrimDotLower(cfg.Name.ValueString())
	token := ""
	for {
		q := map[string]string{"page_size": "1000"}
		if token != "" {
			q["page_token"] = token
		}
		var page ZoneList
		if err := d.client.Do(ctx, "GET", projectZonesPath(pid), q, nil, &page); err != nil {
			resp.Diagnostics.AddError("Error listing DNS zones", err.Error())
			return
		}
		for _, z := range page.Items {
			if strings.EqualFold(z.Name, want) {
				cfg.ID = types.StringValue(z.ID)
				cfg.Status = types.StringValue(z.Status)
				cfg.Nameservers = strList(ctx, z.Nameservers)
				cfg.DNSSEC = types.BoolValue(z.DNSSEC.Enabled)
				cfg.DSRecords = strList(ctx, z.DNSSEC.DS)
				resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
				return
			}
		}
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}
	resp.Diagnostics.AddError("DNS zone not found", fmt.Sprintf("no zone %q in project %s", cfg.Name.ValueString(), cfg.ProjectID.ValueString()))
}
