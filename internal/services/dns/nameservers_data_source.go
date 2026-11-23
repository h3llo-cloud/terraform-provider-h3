package dns

import (
	"context"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// NameserversDataSource is h3_dns_nameservers: the authoritative name servers
// of h3llo DNS (GET /api/dns/v1/nameservers). Use it to delegate a domain
// before the zone exists, e.g. in the registrar's provider.
type NameserversDataSource struct{ client *client.Client }

type nameserversModel struct {
	ID                   types.String `tfsdk:"id"`
	Nameservers          types.List   `tfsdk:"nameservers"`
	VerificationTXTLabel types.String `tfsdk:"verification_txt_label"`
}

func NewNameserversDataSource() datasource.DataSource { return &NameserversDataSource{} }

func (d *NameserversDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_nameservers"
}

func (d *NameserversDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The authoritative name servers of h3llo DNS (`ns1`..`ns4.h3llo.cloud`). Point the domain's NS records " +
			"at the registrar to them; the zone is verified when the delegation is seen. Every `h3_dns_zone` also exports " +
			"`nameservers`; this data source is for configurations that delegate before the zone exists. " +
			"Requires the DNS API with `GET /api/dns/v1/nameservers` (h3dns v2).",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Constant `h3dns`"},
			"nameservers": schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Name server host names, in order"},
			"verification_txt_label": schema.StringAttribute{Computed: true,
				MarkdownDescription: "Label of the TXT record used for `verification_method = \"txt\"` (`_h3dns-verification`)"},
		},
	}
}

func (d *NameserversDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *NameserversDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	var ns Nameservers
	if err := d.client.Do(ctx, "GET", apiBase+"/nameservers", nil, nil, &ns); err != nil {
		detail := err.Error()
		if client.IsNotFound(err) {
			detail = "The DNS API on this endpoint does not serve /api/dns/v1/nameservers yet (h3dns v2 is being rolled out). " +
				"Use h3_dns_zone.<name>.nameservers instead. " + detail
		}
		resp.Diagnostics.AddError("Error reading DNS name servers", detail)
		return
	}
	m := nameserversModel{
		ID:                   types.StringValue("h3dns"),
		Nameservers:          strList(ctx, ns.Nameservers),
		VerificationTXTLabel: types.StringValue(ns.VerificationTXTLabel),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
