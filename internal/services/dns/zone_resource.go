package dns

import (
	"context"
	"fmt"
	"h3terraform/internal/common"
	"time"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &ZoneResource{}
	_ resource.ResourceWithConfigure   = &ZoneResource{}
	_ resource.ResourceWithImportState = &ZoneResource{}
)

func NewZoneResource() resource.Resource { return &ZoneResource{} }

type ZoneResource struct{ client *client.Client }

type ZoneModel struct {
	ID                   types.String `tfsdk:"id"`
	ProjectID            types.String `tfsdk:"project_id"`
	Name                 types.String `tfsdk:"name"`
	DNSSEC               types.Bool   `tfsdk:"dnssec"`
	DefaultTTL           types.Int64  `tfsdk:"default_ttl"`
	Labels               types.Map    `tfsdk:"labels"`
	VerificationMethod   types.String `tfsdk:"verification_method"`
	ForceDestroy         types.Bool   `tfsdk:"force_destroy"`
	Status               types.String `tfsdk:"status"`
	Nameservers          types.List   `tfsdk:"nameservers"`
	VerificationTXTName  types.String `tfsdk:"verification_txt_name"`
	VerificationTXTValue types.String `tfsdk:"verification_txt_value"`
	DNSSECState          types.String `tfsdk:"dnssec_state"`
	DSRecords            types.List   `tfsdk:"ds_records"`
}

func (r *ZoneResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_zone"
}

func (r *ZoneResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Authoritative DNS zone of a project in the h3dns service. A new zone is `pending_verification` " +
			"until its delegation to `nameservers` (or the TXT `verification_txt_name` = `verification_txt_value`) is seen; " +
			"records can be prepared before that.",
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true, MarkdownDescription: "Zone ID", PlanModifiers: keep},
			"project_id": common.ProjectIDAttribute("zone"),
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Zone name, e.g. `example.com` (IDN allowed, stored as A-labels)",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"dnssec": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Sign the zone (ECDSA P-256 CSK, NSEC3). Publish `ds_records` at the registrar. Turning it off is refused while the parent still has a DS."},
			"default_ttl": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(300),
				MarkdownDescription: "Default TTL for record sets without `ttl` (60-86400)"},
			"labels": schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Free-form labels"},
			"verification_method": schema.StringAttribute{Optional: true, Computed: true,
				MarkdownDescription: "`ns` (delegation, default) or `txt`. The service may require `txt` (competing claim or a name already delegated to the platform).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}},
			"force_destroy": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Delete the zone even if platform-managed record sets (LB, EIP, DB, certificates) exist in it"},
			"status":                 schema.StringAttribute{Computed: true, MarkdownDescription: "`pending_verification`, `active`, `suspended`, `deleting`"},
			"nameservers":            schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Delegate the domain to these name servers"},
			"verification_txt_name":  schema.StringAttribute{Computed: true, MarkdownDescription: "TXT owner name for the `txt` method (while pending)"},
			"verification_txt_value": schema.StringAttribute{Computed: true, MarkdownDescription: "TXT value for the `txt` method (while pending)"},
			"dnssec_state":           schema.StringAttribute{Computed: true, MarkdownDescription: "`unsigned`, `signing`, `waiting_ds`, `secure`, `unsigning`"},
			"ds_records":             schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "DS records for the registrar"},
		},
	}
}

func (r *ZoneResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func strList(ctx context.Context, v []string) types.List {
	if v == nil {
		v = []string{}
	}
	l, _ := types.ListValueFrom(ctx, types.StringType, v)
	return l
}

func (m *ZoneModel) fromAPI(ctx context.Context, z *Zone) {
	m.ID = types.StringValue(z.ID)
	m.ProjectID = types.StringValue(z.ProjectID)
	m.Name = types.StringValue(z.Name)
	m.DNSSEC = types.BoolValue(z.DNSSEC.Enabled)
	m.DefaultTTL = types.Int64Value(z.DefaultTTL)
	if len(z.Labels) > 0 {
		m.Labels, _ = types.MapValueFrom(ctx, types.StringType, z.Labels)
	} else if !m.Labels.IsNull() {
		m.Labels, _ = types.MapValueFrom(ctx, types.StringType, map[string]string{})
	}
	m.VerificationMethod = types.StringValue(z.Verification.Method)
	m.Status = types.StringValue(z.Status)
	m.Nameservers = strList(ctx, z.Nameservers)
	if z.VerificationTXT != nil {
		m.VerificationTXTName = types.StringValue(z.VerificationTXT.Name)
		m.VerificationTXTValue = types.StringValue(z.VerificationTXT.Value)
	} else {
		m.VerificationTXTName = types.StringValue("")
		m.VerificationTXTValue = types.StringValue("")
	}
	m.DNSSECState = types.StringValue(z.DNSSEC.State)
	m.DSRecords = strList(ctx, z.DNSSEC.DS)
	if m.ForceDestroy.IsNull() || m.ForceDestroy.IsUnknown() {
		m.ForceDestroy = types.BoolValue(false)
	}
}

func labelsOf(ctx context.Context, m types.Map) map[string]string {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	out := map[string]string{}
	m.ElementsAs(ctx, &out, false)
	return out
}

func (r *ZoneResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ZoneModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := ZoneCreate{Name: plan.Name.ValueString(), DNSSEC: plan.DNSSEC.ValueBool(), DefaultTTL: plan.DefaultTTL.ValueInt64(),
		Labels: labelsOf(ctx, plan.Labels)}
	if !plan.VerificationMethod.IsNull() && !plan.VerificationMethod.IsUnknown() {
		body.VerificationMethod = plan.VerificationMethod.ValueString()
	}
	var z Zone
	if err := r.client.Do(ctx, "POST", projectZonesPath(plan.ProjectID.ValueString()), nil, body, &z); err != nil {
		resp.Diagnostics.AddError("Error creating DNS zone", err.Error())
		return
	}
	name := plan.Name
	plan.fromAPI(ctx, &z)
	plan.Name = name // keep the configured spelling (e.g. U-labels)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ZoneResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ZoneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var z Zone
	if err := r.client.Do(ctx, "GET", zonePath(state.ID.ValueString()), nil, nil, &z); err != nil {
		if he, ok := err.(*client.HTTPError); ok && he.IsNotFound() {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading DNS zone", err.Error())
		return
	}
	if z.Status == "deleting" {
		resp.State.RemoveResource(ctx)
		return
	}
	name := state.Name
	state.fromAPI(ctx, &z)
	if !name.IsNull() && sameZoneName(name.ValueString(), z.Name) {
		state.Name = name
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func sameZoneName(a, b string) bool {
	trim := func(s string) string { return stringsTrimDotLower(s) }
	return trim(a) == trim(b) || len(a) > 0 && !isASCII(a) // U-label vs A-label: the server is the authority on the name
}

func (r *ZoneResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ZoneModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var patch ZonePatch
	if !plan.DNSSEC.Equal(state.DNSSEC) {
		v := plan.DNSSEC.ValueBool()
		patch.DNSSEC = &v
	}
	if !plan.DefaultTTL.Equal(state.DefaultTTL) {
		v := plan.DefaultTTL.ValueInt64()
		patch.DefaultTTL = &v
	}
	if !plan.Labels.Equal(state.Labels) {
		labels := labelsOf(ctx, plan.Labels)
		if labels == nil {
			labels = map[string]string{}
		}
		patch.Labels = &labels
	}
	var z Zone
	var err error
	if patch.DNSSEC == nil && patch.DefaultTTL == nil && patch.Labels == nil {
		err = r.client.Do(ctx, "GET", zonePath(plan.ID.ValueString()), nil, nil, &z) // only force_destroy changed
	} else {
		err = r.client.Do(ctx, "PATCH", zonePath(plan.ID.ValueString()), nil, patch, &z)
	}
	if err != nil {
		detail := err.Error()
		if he, ok := client.AsHTTPError(err); ok && (he.Code() == "DNSSEC_DS_PUBLISHED" || (he.StatusCode == 409 && patch.DNSSEC != nil && !*patch.DNSSEC)) {
			detail = "DNSSEC cannot be turned off while the parent zone still publishes a DS record for it. " +
				"Remove the DS record at the registrar, wait for the parent TTL, then apply again. (" + he.Detail() + ")"
		}
		resp.Diagnostics.AddError("Error updating DNS zone", detail)
		return
	}
	name, force := plan.Name, plan.ForceDestroy
	plan.fromAPI(ctx, &z)
	plan.Name, plan.ForceDestroy = name, force
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ZoneResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ZoneModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	q := map[string]string{}
	if state.ForceDestroy.ValueBool() {
		q["force"] = "true"
	}
	if err := r.client.Do(ctx, "DELETE", zonePath(state.ID.ValueString()), q, nil, nil); err != nil {
		if he, ok := err.(*client.HTTPError); ok && he.IsNotFound() {
			return
		}
		resp.Diagnostics.AddError("Error deleting DNS zone", err.Error())
		return
	}
	// deletion is asynchronous: wait until the zone is gone
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var z Zone
		err := r.client.Do(ctx, "GET", zonePath(state.ID.ValueString()), nil, nil, &z)
		if he, ok := err.(*client.HTTPError); ok && he.IsNotFound() {
			return
		}
		select {
		case <-ctx.Done():
			resp.Diagnostics.AddError("Error deleting DNS zone", ctx.Err().Error())
			return
		case <-time.After(2 * time.Second):
		}
	}
	resp.Diagnostics.AddWarning("DNS zone deletion still in progress", "the service accepted the deletion; it completes in the background")
}

// ImportState: terraform import h3_dns_zone.main <zone_id>
func (r *ZoneResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("force_destroy"), false)...)
}

// ModifyPlan fills project_id from the provider default.
func (r *ZoneResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
}
