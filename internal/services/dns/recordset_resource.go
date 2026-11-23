package dns

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &RecordSetResource{}
	_ resource.ResourceWithConfigure   = &RecordSetResource{}
	_ resource.ResourceWithImportState = &RecordSetResource{}
)

func NewRecordSetResource() resource.Resource { return &RecordSetResource{} }

// RecordSetResource is one RRset (like aws_route53_record /
// openstack_dns_recordset_v2): the whole set of records for (name, type).
type RecordSetResource struct{ client *client.Client }

type RecordSetModel struct {
	ID             types.String `tfsdk:"id"`
	ZoneID         types.String `tfsdk:"zone_id"`
	Name           types.String `tfsdk:"name"`
	Type           types.String `tfsdk:"type"`
	TTL            types.Int64  `tfsdk:"ttl"`
	Records        types.Set    `tfsdk:"records"`
	AllowOverwrite types.Bool   `tfsdk:"allow_overwrite"`
	FQDN           types.String `tfsdk:"fqdn"`
	Etag           types.String `tfsdk:"etag"`
}

func (r *RecordSetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_recordset"
}

func (r *RecordSetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "One DNS record set (all records of a name and type) in an `h3_dns_zone`. " +
			"Create never overwrites an existing record set unless `allow_overwrite = true` (`If-None-Match: *`); update and delete are " +
			"conditional on the version read at refresh (`If-Match`). Record sets managed by the platform (apex NS, LB, EIP, database " +
			"records) cannot be changed. Import: `terraform import h3_dns_recordset.www <zone_id>/www/A` (`@` is the apex).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "`<zone_id>/<name>/<type>`",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"zone_id": schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Zone ID (`h3_dns_zone.<x>.id`)"},
			"name": schema.StringAttribute{Required: true, PlanModifiers: replace,
				MarkdownDescription: "Name relative to the zone: `www`, `@` for the apex, `*.app` for a wildcard, `_acme-challenge.www`"},
			"type": schema.StringAttribute{Required: true, PlanModifiers: replace,
				MarkdownDescription: "A, AAAA, CNAME, MX, TXT, SRV, NS, CAA or ALIAS"},
			"ttl": schema.Int64Attribute{Optional: true, Computed: true, MarkdownDescription: "TTL in seconds (60-86400); default: the zone's default TTL"},
			"records": schema.SetAttribute{ElementType: types.StringType, Required: true,
				MarkdownDescription: "Record values. Host names may omit the trailing dot. TXT is plain text (long values are split by the service). " +
					"MX `10 mail.example.com`, SRV `10 5 5060 sip.example.com`, CAA `0 issue \"letsencrypt.org\"`."},
			"allow_overwrite": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Take over an existing record set with the same name and type instead of failing"},
			"fqdn": schema.StringAttribute{Computed: true, MarkdownDescription: "Fully qualified name",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"etag": schema.StringAttribute{Computed: true, MarkdownDescription: "Version (ETag) of the record set on the server. Updates and deletes are conditional on it (`If-Match`): a change made outside Terraform after the last refresh is never overwritten."},
		},
	}
}

func (r *RecordSetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData))
		return
	}
	r.client = c
}

func (m *RecordSetModel) key() (zone, name, typ string) {
	return m.ZoneID.ValueString(), normalizeName(m.Name.ValueString()), strings.ToUpper(m.Type.ValueString())
}

func (m *RecordSetModel) path() string {
	z, n, t := m.key()
	return recordSetPath(z, url.PathEscape(n), t)
}

func setOf(ctx context.Context, v []string) types.Set {
	sort.Strings(v)
	s, _ := types.SetValueFrom(ctx, types.StringType, v)
	return s
}

func (m *RecordSetModel) fromAPI(ctx context.Context, rs *RecordSet, prior []string) {
	z, n, t := m.key()
	m.ID = types.StringValue(z + "/" + n + "/" + t)
	m.TTL = types.Int64Value(rs.TTL)
	m.Records = setOf(ctx, mergeRecords(rs.Type, prior, rs.Records))
	m.FQDN = types.StringValue(rs.FQDN)
	m.Etag = types.StringValue(rs.Etag)
	if m.AllowOverwrite.IsNull() || m.AllowOverwrite.IsUnknown() {
		m.AllowOverwrite = types.BoolValue(false)
	}
}

func recordsOf(ctx context.Context, s types.Set) []string {
	var out []string
	if s.IsNull() || s.IsUnknown() {
		return out
	}
	s.ElementsAs(ctx, &out, false)
	return out
}

// put writes the whole RRset. h3dns versions an RRset with a strong ETag:
// ifMatch = "*" is "create only" (If-None-Match: *), another non-empty value
// is "only if unchanged since that version" (If-Match); a mismatch is 412.
func (r *RecordSetResource) put(ctx context.Context, plan *RecordSetModel, ifMatch string) (*RecordSet, error) {
	body := RecordSetBody{Records: recordsOf(ctx, plan.Records)}
	if !plan.TTL.IsNull() && !plan.TTL.IsUnknown() {
		body.TTL = plan.TTL.ValueInt64()
	}
	opts := &client.Options{Headers: map[string]string{}}
	switch ifMatch {
	case "":
	case "*":
		opts.Headers["If-None-Match"] = "*"
	default:
		opts.Headers["If-Match"] = ifMatch
	}
	var rs RecordSet
	var hdr http.Header
	opts.RespHeader = &hdr
	err := r.client.DoWith(ctx, "PUT", plan.path(), nil, body, &rs, opts)
	if err == nil && rs.Etag == "" {
		rs.Etag = hdr.Get("ETag")
	}
	return &rs, err
}

func (r *RecordSetResource) get(ctx context.Context, m *RecordSetModel) (*RecordSet, error) {
	var rs RecordSet
	var hdr http.Header
	if err := r.client.DoWith(ctx, "GET", m.path(), nil, nil, &rs, &client.Options{RespHeader: &hdr}); err != nil {
		return nil, err
	}
	if rs.Etag == "" {
		rs.Etag = hdr.Get("ETag")
	}
	return &rs, nil
}

// sameContent reports whether the server RRset already has the planned
// records and TTL (a retried PUT that was applied the first time, or a
// concurrent writer that made the same change).
func sameContent(ctx context.Context, plan *RecordSetModel, rs *RecordSet) bool {
	if !plan.TTL.IsNull() && !plan.TTL.IsUnknown() && plan.TTL.ValueInt64() != rs.TTL {
		return false
	}
	want := map[string]bool{}
	for _, v := range recordsOf(ctx, plan.Records) {
		want[canonicalRecord(rs.Type, v)] = true
	}
	if len(want) != len(rs.Records) {
		return false
	}
	for _, v := range rs.Records {
		if !want[canonicalRecord(rs.Type, v)] {
			return false
		}
	}
	return true
}

func rrsetError(err error) string {
	if he, ok := client.AsHTTPError(err); ok {
		switch he.Code() {
		case "MANAGED_RECORDSET":
			return "the record set is managed by the platform (load balancer, Elastic IP, database or certificate) and cannot be changed here: " + he.Detail()
		case "CNAME_CONFLICT", "RRTYPE_CONFLICT":
			return "conflicting record type at this name: " + he.Detail()
		}
		if he.StatusCode == 409 && strings.Contains(he.Body, "managed-recordset") {
			return "the record set is managed by the platform and cannot be changed here: " + he.Detail()
		}
	}
	return err.Error()
}

func (r *RecordSetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RecordSetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cond := "*" // create only: never overwrite a record set made elsewhere
	if plan.AllowOverwrite.ValueBool() {
		cond = ""
	}
	rs, err := r.put(ctx, &plan, cond)
	if client.IsPreconditionFailed(err) {
		// Exists already. A retried PUT that went through is not a conflict.
		if cur, gerr := r.get(ctx, &plan); gerr == nil && sameContent(ctx, &plan, cur) && cur.ManagedBy != "platform" {
			rs, err = cur, nil
		} else {
			_, n, t := plan.key()
			managed := ""
			if gerr == nil {
				managed = " (managed_by=" + cur.ManagedBy + ")"
			}
			resp.Diagnostics.AddError("DNS record set already exists",
				fmt.Sprintf("%s %s already exists%s. Import it (terraform import ... %s/%s/%s) or set allow_overwrite = true.",
					n, t, managed, plan.ZoneID.ValueString(), n, t))
			return
		}
	}
	if err != nil {
		resp.Diagnostics.AddError("Error creating DNS record set", rrsetError(err))
		return
	}
	plan.fromAPI(ctx, rs, recordsOf(ctx, plan.Records))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RecordSetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RecordSetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rs, err := r.get(ctx, &state)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading DNS record set", err.Error())
		return
	}
	state.fromAPI(ctx, rs, recordsOf(ctx, state.Records))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update replaces the RRset only if it is still the version Terraform read
// (If-Match: etag from state), so a change made in the console between plan
// and apply is not silently overwritten.
func (r *RecordSetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state RecordSetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	etag := state.Etag.ValueString()
	rs, err := r.put(ctx, &plan, etag)
	if client.IsPreconditionFailed(err) {
		cur, gerr := r.get(ctx, &plan)
		switch {
		case gerr == nil && sameContent(ctx, &plan, cur):
			rs, err = cur, nil
		case client.IsNotFound(gerr):
			resp.Diagnostics.AddError("DNS record set was deleted outside Terraform",
				"The record set was deleted after the last refresh. Run terraform apply again to recreate it.")
			return
		default:
			now := ""
			if gerr == nil {
				now = cur.Etag
			}
			resp.Diagnostics.AddError("DNS record set changed outside Terraform",
				fmt.Sprintf("The record set changed after the last refresh (version %s, now %s). Nothing was written. "+
					"Run terraform plan again to review the current records.", etag, now))
			return
		}
	}
	if err != nil {
		resp.Diagnostics.AddError("Error updating DNS record set", rrsetError(err))
		return
	}
	plan.fromAPI(ctx, rs, recordsOf(ctx, plan.Records))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RecordSetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RecordSetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var opts *client.Options
	if etag := state.Etag.ValueString(); etag != "" {
		opts = &client.Options{Headers: map[string]string{"If-Match": etag}}
	}
	err := r.client.DoWith(ctx, "DELETE", state.path(), nil, nil, nil, opts)
	switch {
	case err == nil, client.IsNotFound(err):
	case client.IsPreconditionFailed(err):
		resp.Diagnostics.AddError("DNS record set changed outside Terraform",
			"The record set was changed outside Terraform after it was last saved in state, so it was not deleted. "+
				"Review it, then run `terraform apply -refresh-only` to accept the current version and destroy again.")
	default:
		resp.Diagnostics.AddError("Error deleting DNS record set", rrsetError(err))
	}
}

// ImportState: <zone_id>/<name>/<type>
func (r *RecordSetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		resp.Diagnostics.AddError("Invalid import ID", "expected <zone_id>/<name>/<type>, e.g. 0192.../www/A")
		return
	}
	name := normalizeName(parts[1])
	typ := strings.ToUpper(parts[2])
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0]+"/"+name+"/"+typ)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("type"), typ)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("allow_overwrite"), false)...)
}
