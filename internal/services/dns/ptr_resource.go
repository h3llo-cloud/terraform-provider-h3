package dns

import (
	"context"
	"fmt"
	"h3terraform/internal/common"
	"strings"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &PTRResource{}
	_ resource.ResourceWithConfigure   = &PTRResource{}
	_ resource.ResourceWithImportState = &PTRResource{}
)

func NewPTRResource() resource.Resource { return &PTRResource{} }

// PTRResource sets the reverse DNS name of an EIP of the project. The host
// name must already resolve to the EIP address (forward-confirmed); destroy
// returns the PTR to the platform default.
type PTRResource struct{ client *client.Client }

type PTRModel struct {
	ID           types.String `tfsdk:"id"`
	ProjectID    types.String `tfsdk:"project_id"`
	EIPID        types.String `tfsdk:"eip_id"`
	Hostname     types.String `tfsdk:"hostname"`
	IPAddress    types.String `tfsdk:"ip_address"`
	ForwardCheck types.String `tfsdk:"forward_check"`
}

func (r *PTRResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_ptr"
}

func (r *PTRResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reverse DNS (PTR) of an EIP. `hostname` must already resolve to the EIP address (create the A/AAAA first, " +
			"e.g. with `h3_dns_recordset` and `depends_on`). Destroy resets the PTR to the platform default. " +
			"Import: `terraform import h3_dns_ptr.mail <project_id>/<eip_id>`.",
		Attributes: map[string]schema.Attribute{
			"id":            schema.StringAttribute{Computed: true, MarkdownDescription: "`<project_id>/<eip_id>`", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id":    common.ProjectIDAttribute("PTR record"),
			"eip_id":        schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "EIP ID (`h3_eip.<x>.id`)"},
			"hostname":      schema.StringAttribute{Required: true, MarkdownDescription: "PTR target, e.g. `mail.example.com`"},
			"ip_address":    schema.StringAttribute{Computed: true, MarkdownDescription: "EIP address", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"forward_check": schema.StringAttribute{Computed: true, MarkdownDescription: "`ok` when the host name resolves to the address"},
		},
	}
}

func (r *PTRResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (m *PTRModel) fromAPI(p *PTR) {
	m.ID = types.StringValue(m.ProjectID.ValueString() + "/" + m.EIPID.ValueString())
	m.IPAddress = types.StringValue(p.IP)
	m.ForwardCheck = types.StringValue(p.ForwardCheck)
	if stringsTrimDotLower(p.Hostname) != stringsTrimDotLower(m.Hostname.ValueString()) {
		m.Hostname = types.StringValue(p.Hostname)
	}
}

func (r *PTRResource) put(ctx context.Context, m *PTRModel) (*PTR, error) {
	var p PTR
	err := r.client.Do(ctx, "PUT", ptrPath(m.ProjectID.ValueString(), m.EIPID.ValueString()), nil,
		map[string]string{"hostname": m.Hostname.ValueString()}, &p)
	return &p, err
}

func (r *PTRResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PTRModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.put(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError("Error setting PTR", err.Error())
		return
	}
	plan.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *PTRResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PTRModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var p PTR
	if err := r.client.Do(ctx, "GET", ptrPath(state.ProjectID.ValueString(), state.EIPID.ValueString()), nil, nil, &p); err != nil {
		if he, ok := err.(*client.HTTPError); ok && he.IsNotFound() {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading PTR", err.Error())
		return
	}
	if p.Source == "default" {
		// reset elsewhere (EIP detached, operator): the tenant PTR is gone
		resp.State.RemoveResource(ctx)
		return
	}
	state.fromAPI(&p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *PTRResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan PTRModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.put(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError("Error updating PTR", err.Error())
		return
	}
	plan.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *PTRResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state PTRModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Do(ctx, "DELETE", ptrPath(state.ProjectID.ValueString(), state.EIPID.ValueString()), nil, nil, nil); err != nil {
		if he, ok := err.(*client.HTTPError); ok && he.IsNotFound() {
			return
		}
		resp.Diagnostics.AddError("Error resetting PTR", err.Error())
	}
}

func (r *PTRResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, eip, ok := strings.Cut(req.ID, "/")
	if !ok || project == "" || eip == "" {
		resp.Diagnostics.AddError("Invalid import ID", "expected <project_id>/<eip_id>")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("eip_id"), eip)...)
}

// ModifyPlan fills project_id from the provider default.
func (r *PTRResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
}
