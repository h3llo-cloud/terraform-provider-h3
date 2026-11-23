package lb

import (
	"context"
	"fmt"
	"sort"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &LBResource{}
	_ resource.ResourceWithConfigure      = &LBResource{}
	_ resource.ResourceWithImportState    = &LBResource{}
	_ resource.ResourceWithValidateConfig = &LBResource{}
	_ resource.ResourceWithModifyPlan     = &LBResource{}
)

// Timeouts; tests shorten them.
var (
	createTimeout = 15 * time.Minute
	updateTimeout = 10 * time.Minute
	deleteTimeout = 10 * time.Minute
)

// AccessNote is appended to every LB schema description.
const AccessNote = "Requires API-key (HMAC) access to the load balancer API; as of 0.3.0 h3lb is reachable only from the console " +
	"(backend gap), so calls fail with 401/404 until it is published on api.h3llo.cloud."

func NewLBResource() resource.Resource { return &LBResource{} }

type LBResource struct{ client *client.Client }

type ListenerModel struct {
	Port       types.Int64  `tfsdk:"port"`
	TargetPort types.Int64  `tfsdk:"target_port"`
	Protocol   types.String `tfsdk:"protocol"`
}

var listenerType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"port": types.Int64Type, "target_port": types.Int64Type, "protocol": types.StringType,
}}

type LBModel struct {
	ID         types.String `tfsdk:"id"`
	ProjectID  types.String `tfsdk:"project_id"`
	Name       types.String `tfsdk:"name"`
	Listeners  types.List   `tfsdk:"listener"`
	Targets    types.Set    `tfsdk:"targets"`
	Algorithm  types.String `tfsdk:"algorithm"`
	EIPID      types.String `tfsdk:"eip_id"`
	WhiteIP    types.Bool   `tfsdk:"white_ip"`
	VpcIP      types.String `tfsdk:"vpc_ip"`
	ReleaseEIP types.Bool   `tfsdk:"release_eip_on_destroy"`
	Status     types.String `tfsdk:"status"`
	ExternalIP types.String `tfsdk:"external_ip"`
	Endpoint   types.String `tfsdk:"endpoint"`
	SubnetName types.String `tfsdk:"subnet_name"`
}

func (r *LBResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb"
}

func (r *LBResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a network load balancer (TCP, L4) that spreads traffic from a public address over private targets " +
			"(VM internal IPs). Import ID: `<project_id>/<id>`.\n\n" +
			"**Changes replace the load balancer**, except `eip_id`, `white_ip` and `release_eip_on_destroy`. Listener and target changes force " +
			"replacement; use `lifecycle { create_before_destroy = true }` (with a different `name`) to avoid downtime, and pin the " +
			"address with `eip_id` so a replacement keeps it.\n\n" +
			"**Public IP (LB Basic, OVN-native projects).** The address is a project Elastic IP: `eip_id` picks a free one " +
			"(`h3_eip.<name>.id`), without it a new one is allocated and exported in `eip_id`. Changing `eip_id` moves the load " +
			"balancer to that EIP in place. Destroying the load balancer keeps the EIP in the project (billed) unless " +
			"`release_eip_on_destroy = true`. Projects still on the legacy kube-ovn lb-svc dataplane assign the address themselves " +
			"and reject `eip_id` (HTTP 400 `EIP_SELECTION_UNAVAILABLE`).\n\n" +
			"**Without a public IP.** `white_ip = false` creates an internal load balancer: it gets an address inside the VPC of " +
			"its targets (`vpc_ip`) and is reachable only from that VPC; no EIP is allocated or billed. `white_ip` changes in place: " +
			"`true` attaches `eip_id` (or a new EIP), `false` detaches the EIP, which stays in the project. Needs a platform with " +
			"internal load balancers enabled (otherwise HTTP 400 `INTERNAL_LB_UNAVAILABLE`).\n\n" + AccessNote,
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{MarkdownDescription: "Load balancer ID.", Computed: true, PlanModifiers: keep},
			"project_id": common.ProjectIDAttribute("load balancer"),
			"name": schema.StringAttribute{MarkdownDescription: "Name: DNS label, 1-63 lowercase letters, digits and `-`, not all digits.",
				Required: true, PlanModifiers: replace},
			"listener": schema.ListNestedAttribute{
				MarkdownDescription: "Listeners (1-32): public `port` forwarded to `target_port` on every target. Ports must be unique.",
				Required:            true,
				PlanModifiers:       []planmodifier.List{listplanmodifier.RequiresReplace()},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"port":        schema.Int64Attribute{MarkdownDescription: "Public (frontend) port, 1-65535.", Required: true},
					"target_port": schema.Int64Attribute{MarkdownDescription: "Target (backend) port, 1-65535.", Required: true},
					"protocol": schema.StringAttribute{
						MarkdownDescription: "`TCP` (default). `UDP` is reserved for the OVN-native LB and refused by the current backend.",
						Optional:            true, Computed: true, Default: stringdefault.StaticString("TCP"),
					},
				}},
			},
			"targets": schema.SetAttribute{
				MarkdownDescription: "Target private IPv4 addresses (1-64), e.g. `h3_vm.<name>.internal_ip`. Ranges: 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10.",
				ElementType:         types.StringType, Required: true,
				PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
			},
			"algorithm": schema.StringAttribute{
				MarkdownDescription: "Balancing algorithm; only `round_robin` is supported.",
				Optional:            true, Computed: true, Default: stringdefault.StaticString("round_robin"), PlanModifiers: replace,
			},
			"eip_id": schema.StringAttribute{
				MarkdownDescription: "ID (UUID) of a free Elastic IP of the project to publish the load balancer on (`h3_eip.<name>.id`). " +
					"If omitted, a new EIP is allocated and its ID is exported here. Changing it moves the load balancer to the new EIP " +
					"in place (the previous EIP is unbound and stays in the project); removing it from the configuration keeps the current EIP. " +
					"An EIP bound to another resource is rejected (409 `EIP_ALREADY_BOUND`). LB Basic (OVN-native projects) only.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{common.UUIDValidator()},
				PlanModifiers: keep,
			},
			"white_ip": schema.BoolAttribute{
				MarkdownDescription: "Publish the load balancer on a public IP (default: true). `false`: internal load balancer, " +
					"reachable only inside the VPC of its targets at `vpc_ip`, no EIP. Changed in place: `true` attaches `eip_id` (or a " +
					"newly allocated EIP), `false` detaches the current EIP, which stays in the project as a free, billed EIP. LB Basic only.",
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
			},
			"vpc_ip": schema.StringAttribute{
				MarkdownDescription: "Address of the load balancer inside its VPC (internal load balancers; kept when a public IP is attached later).",
				Computed:            true, PlanModifiers: keep,
			},
			"release_eip_on_destroy": schema.BoolAttribute{
				MarkdownDescription: "Delete the load balancer's Elastic IP together with it (default: false). By default destroying " +
					"the load balancer keeps the EIP in the project as a free, billed EIP, so a re-created load balancer can take the same address.",
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
			},
			"status":      schema.StringAttribute{MarkdownDescription: "`PENDING`, `CREATING`, `RUNNING`, `DELETING` or `FAILED`.", Computed: true},
			"external_ip": schema.StringAttribute{MarkdownDescription: "Public IP address.", Computed: true, PlanModifiers: keep},
			"endpoint":    schema.StringAttribute{MarkdownDescription: "Endpoint (address the platform publishes for the LB).", Computed: true, PlanModifiers: keep},
			"subnet_name": schema.StringAttribute{MarkdownDescription: "Subnet the load balancer runs in.", Computed: true, PlanModifiers: keep},
		},
	}
}

func (r *LBResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

// ValidateConfig mirrors the h3lb DTO validation so mistakes show at plan time.
func (r *LBResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg LBModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if v := common.KnownString(cfg.Name); v != "" {
		if err := ValidName(v); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid name", err.Error())
		}
	}
	if !cfg.WhiteIP.IsNull() && !cfg.WhiteIP.IsUnknown() && !cfg.WhiteIP.ValueBool() && common.KnownString(cfg.EIPID) != "" {
		resp.Diagnostics.AddAttributeError(path.Root("eip_id"), "eip_id requires white_ip", "an internal load balancer (white_ip = false) has no EIP; remove eip_id or set white_ip = true")
	}
	if a := common.KnownString(cfg.Algorithm); a != "" && a != "round_robin" {
		resp.Diagnostics.AddAttributeError(path.Root("algorithm"), "Unsupported algorithm", fmt.Sprintf("only round_robin is supported, got %q", a))
	}
	if !cfg.Listeners.IsNull() && !cfg.Listeners.IsUnknown() {
		var ls []ListenerModel
		resp.Diagnostics.Append(cfg.Listeners.ElementsAs(ctx, &ls, false)...)
		if n := len(ls); n < 1 || n > 32 {
			resp.Diagnostics.AddAttributeError(path.Root("listener"), "Invalid listeners", fmt.Sprintf("1 to 32 listeners required, got %d", n))
		}
		seen := map[int64]bool{}
		for i, l := range ls {
			p := path.Root("listener").AtListIndex(i)
			for _, pv := range []struct {
				v    types.Int64
				name string
			}{{l.Port, "port"}, {l.TargetPort, "target_port"}} {
				if !pv.v.IsNull() && !pv.v.IsUnknown() && (pv.v.ValueInt64() < 1 || pv.v.ValueInt64() > 65535) {
					resp.Diagnostics.AddAttributeError(p.AtName(pv.name), "Invalid port", fmt.Sprintf("must be 1-65535, got %d", pv.v.ValueInt64()))
				}
			}
			if !l.Port.IsNull() && !l.Port.IsUnknown() {
				if seen[l.Port.ValueInt64()] {
					resp.Diagnostics.AddAttributeError(p.AtName("port"), "Duplicate port", fmt.Sprintf("port %d is used by another listener", l.Port.ValueInt64()))
				}
				seen[l.Port.ValueInt64()] = true
			}
			if pr := common.KnownString(l.Protocol); pr != "" && pr != "TCP" {
				resp.Diagnostics.AddAttributeError(p.AtName("protocol"), "Protocol not supported by the current LB backend",
					fmt.Sprintf("%q is not supported by the current LB backend (h3lb: TCP only); UDP comes with the OVN-native LB Basic", pr))
			}
		}
	}
	if !cfg.Targets.IsNull() && !cfg.Targets.IsUnknown() {
		var ts []types.String
		resp.Diagnostics.Append(cfg.Targets.ElementsAs(ctx, &ts, false)...)
		if n := len(ts); n < 1 || n > 64 {
			resp.Diagnostics.AddAttributeError(path.Root("targets"), "Invalid targets", fmt.Sprintf("1 to 64 targets required, got %d", n))
		}
		for _, t := range ts {
			if t.IsNull() || t.IsUnknown() {
				continue
			}
			if err := ValidTarget(t.ValueString()); err != nil {
				resp.Diagnostics.AddAttributeError(path.Root("targets"), "Invalid target", err.Error())
			}
		}
	}
}

func (r *LBResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan LBModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var ls []ListenerModel
	var targets []string
	resp.Diagnostics.Append(plan.Listeners.ElementsAs(ctx, &ls, false)...)
	resp.Diagnostics.Append(plan.Targets.ElementsAs(ctx, &targets, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := CreateRequest{
		ProjectID: plan.ProjectID.ValueString(),
		Name:      plan.Name.ValueString(),
		Backends:  targets,
		Config:    &Config{Algorithm: plan.Algorithm.ValueString()},
		EipID:     common.KnownString(plan.EIPID),
	}
	if !plan.WhiteIP.ValueBool() {
		off := false
		body.WhiteIP, body.EipID = &off, ""
	}
	sort.Strings(body.Backends)
	for _, l := range ls {
		body.Ports = append(body.Ports, Port{Frontend: l.Port.ValueInt64(), Backend: l.TargetPort.ValueInt64()})
	}
	var created LoadBalancer
	if err := r.client.Do(ctx, "POST", apiBase, nil, body, &created); err != nil {
		resp.Diagnostics.AddError(lbError("creating load balancer", err))
		return
	}
	if created.ID == "" {
		resp.Diagnostics.AddError("Error creating load balancer", "the API response has no id")
		return
	}
	// Track the LB right away: if it never becomes RUNNING it stays in state (tainted) and is deleted on the next apply.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), created.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), plan.ProjectID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), plan.Name)...)

	var last *LoadBalancer
	err := common.WaitFor(ctx, createTimeout, "load balancer "+created.ID+" to become RUNNING", func(ctx context.Context) (bool, error) {
		lb, err := r.get(ctx, created.ID)
		if err != nil {
			return false, err
		}
		last = lb
		switch lb.Status {
		case "RUNNING":
			return true, nil
		case "FAILED":
			return false, fmt.Errorf("load balancer entered FAILED state")
		}
		return false, nil
	})
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for load balancer", "Load balancer created but not ready (it stays in state and is replaced on the next apply): "+explain(err))
		return
	}
	resp.Diagnostics.Append(plan.applyAPI(last)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *LBResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state LBModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	lb, err := r.get(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading load balancer", explain(err))
		return
	}
	if lb.Status == "DELETING" {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(state.applyAPI(lb)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update turns the public IP on/off (PATCH whiteIp) or moves the LB to another EIP in place
// (PATCH eipId, h3lb + h3ovn LB Basic); every other argument forces replacement,
// release_eip_on_destroy is state only.
func (r *LBResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state LBModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if on := plan.WhiteIP.ValueBool(); on != stateWhiteIP(state) {
		id := state.ID.ValueString()
		body := UpdatePublicIPRequest{WhiteIP: on}
		if on {
			body.EipID = common.KnownString(plan.EIPID)
		}
		if err := r.client.Do(ctx, "PATCH", apiBase+"/"+id, nil, body, nil); err != nil {
			resp.Diagnostics.AddError(lbError("changing white_ip", err))
			return
		}
		last, err := r.waitRunning(ctx, id, updateTimeout, func(lb *LoadBalancer) bool { return lb.Internal != on })
		if err != nil {
			resp.Diagnostics.AddError("Error waiting for load balancer", "The white_ip change was accepted but the load balancer is not RUNNING with it: "+explain(err))
			return
		}
		resp.Diagnostics.Append(plan.applyAPI(last)...)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	newEIP, oldEIP := common.KnownString(plan.EIPID), common.KnownString(state.EIPID)
	if newEIP == "" || newEIP == oldEIP {
		// eip_id removed from the configuration keeps the current EIP (UseStateForUnknown).
		if plan.EIPID.IsUnknown() {
			plan.EIPID = state.EIPID
		}
		plan.Status, plan.ExternalIP, plan.Endpoint, plan.SubnetName = state.Status, state.ExternalIP, state.Endpoint, state.SubnetName
		plan.VpcIP = state.VpcIP
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	id := state.ID.ValueString()
	if err := r.client.Do(ctx, "PATCH", apiBase+"/"+id, nil, UpdateEIPRequest{EipID: newEIP}, nil); err != nil {
		resp.Diagnostics.AddError(lbError("changing the load balancer EIP", err))
		return
	}
	last, err := r.waitRunning(ctx, id, updateTimeout, func(lb *LoadBalancer) bool { return lb.EipID == "" || lb.EipID == newEIP })
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for load balancer", "The EIP change was accepted but the load balancer is not RUNNING on it: "+explain(err))
		return
	}
	resp.Diagnostics.Append(plan.applyAPI(last)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// waitRunning polls until the LB is RUNNING and done(lb) holds; FAILED stops the wait.
func (r *LBResource) waitRunning(ctx context.Context, id string, timeout time.Duration, done func(*LoadBalancer) bool) (*LoadBalancer, error) {
	var last *LoadBalancer
	err := common.WaitFor(ctx, timeout, "load balancer "+id+" to become RUNNING", func(ctx context.Context) (bool, error) {
		lb, err := r.get(ctx, id)
		if err != nil {
			return false, err
		}
		last = lb
		switch lb.Status {
		case "RUNNING":
			return done == nil || done(lb), nil
		case "FAILED":
			return false, fmt.Errorf("load balancer entered FAILED state")
		}
		return false, nil
	})
	return last, err
}

// lbError: EIP errors (409 EIP_ALREADY_BOUND) as for h3_vm/h3_database, access errors explained.
func lbError(action string, err error) (string, string) {
	if h, ok := client.AsHTTPError(err); ok && h.IsEIPAlreadyBound() {
		return common.EIPErrorDiag(action, err)
	}
	return "Error " + action, explain(err)
}

func (r *LBResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state LBModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	// h3lb keeps an LB Basic EIP in the project by default; releaseEip=true deletes it once unbound.
	var query map[string]string
	if state.ReleaseEIP.ValueBool() {
		query = map[string]string{"releaseEip": "true"}
	}
	if err := r.client.Do(ctx, "DELETE", apiBase+"/"+id, query, nil, nil); err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Error deleting load balancer", explain(err))
		return
	}
	err := common.WaitFor(ctx, deleteTimeout, "load balancer "+id+" to be deleted", func(ctx context.Context) (bool, error) {
		_, err := r.get(ctx, id)
		if client.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for load balancer deletion", explain(err))
	}
}

// ImportState takes "<project_id>/<id>".
func (r *LBResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, id, err := common.ImportProjectScoped(req.ID, "<lb_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func (r *LBResource) get(ctx context.Context, id string) (*LoadBalancer, error) {
	return getLB(ctx, r.client, id)
}

func getLB(ctx context.Context, c *client.Client, id string) (*LoadBalancer, error) {
	var lb LoadBalancer
	if err := c.Do(ctx, "GET", apiBase+"/"+id, nil, nil, &lb); err != nil {
		return nil, err
	}
	return &lb, nil
}

// applyAPI fills the model from the API. Protocol is not returned (TCP only)
// and keeps the configured value. eip_id is the LB Basic EIP; an lb-svc LB (or an
// h3lb that predates the field) reports none and the prior value is kept.
func (m *LBModel) applyAPI(lb *LoadBalancer) (d diag.Diagnostics) {
	m.ID = types.StringValue(lb.ID)
	if lb.ProjectID != "" {
		m.ProjectID = types.StringValue(lb.ProjectID)
	}
	m.Name = types.StringValue(lb.Name)
	alg := "round_robin"
	if lb.Config != nil && lb.Config.Algorithm != "" {
		alg = lb.Config.Algorithm
	}
	m.Algorithm = types.StringValue(alg)
	switch {
	case lb.EipID != "":
		m.EIPID = types.StringValue(lb.EipID)
	case m.EIPID.IsUnknown(), lb.Internal:
		m.EIPID = types.StringNull()
	}
	m.WhiteIP = types.BoolValue(!lb.Internal)
	m.VpcIP = common.OptString(lb.VpcIP)
	if m.ReleaseEIP.IsNull() || m.ReleaseEIP.IsUnknown() {
		m.ReleaseEIP = types.BoolValue(false) // import
	}
	m.Status = types.StringValue(lb.Status)
	m.ExternalIP = common.OptString(lb.ExternalIP)
	m.Endpoint = common.OptString(lb.Endpoint)
	m.SubnetName = common.OptString(lb.SubnetName)

	// Listeners: keep the configured protocol per port; API order is kept.
	proto := map[int64]string{}
	if !m.Listeners.IsNull() && !m.Listeners.IsUnknown() {
		var prior []ListenerModel
		d.Append(m.Listeners.ElementsAs(context.Background(), &prior, false)...)
		for _, l := range prior {
			if !l.Protocol.IsNull() && !l.Protocol.IsUnknown() {
				proto[l.Port.ValueInt64()] = l.Protocol.ValueString()
			}
		}
	}
	elems := make([]attr.Value, 0, len(lb.Ports))
	for _, p := range lb.Ports {
		pr := proto[p.Frontend]
		if pr == "" {
			pr = "TCP"
		}
		o, dd := types.ObjectValue(listenerType.AttrTypes, map[string]attr.Value{
			"port": types.Int64Value(p.Frontend), "target_port": types.Int64Value(p.Backend), "protocol": types.StringValue(pr),
		})
		d.Append(dd...)
		elems = append(elems, o)
	}
	l, dd := types.ListValue(listenerType, elems)
	d.Append(dd...)
	m.Listeners = l

	ts := make([]attr.Value, 0, len(lb.Backends))
	for _, b := range lb.Backends {
		ts = append(ts, types.StringValue(b))
	}
	s, dd := types.SetValue(types.StringType, ts)
	d.Append(dd...)
	m.Targets = s
	return d
}

// ModifyPlan fills project_id from the provider default; an eip_id change moves the address.
func (r *LBResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state LBModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.EIPID.IsUnknown() && !plan.EIPID.IsNull() && !plan.EIPID.Equal(state.EIPID) {
		for _, a := range []string{"external_ip", "endpoint", "status"} {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(a), types.StringUnknown())...)
		}
	}
	// white_ip toggled: the addresses change; off drops the EIP, on without eip_id gets a new one
	if !plan.WhiteIP.IsUnknown() && plan.WhiteIP.ValueBool() != stateWhiteIP(state) {
		for _, a := range []string{"external_ip", "endpoint", "status", "vpc_ip"} {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(a), types.StringUnknown())...)
		}
		var cfgEIP types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("eip_id"), &cfgEIP)...)
		switch {
		case !plan.WhiteIP.ValueBool():
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("eip_id"), types.StringNull())...)
		case cfgEIP.IsNull():
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("eip_id"), types.StringUnknown())...)
		}
	}
}

// stateWhiteIP: white_ip of the prior state; null (state of an older provider) = public.
func stateWhiteIP(s LBModel) bool {
	return s.WhiteIP.IsNull() || s.WhiteIP.IsUnknown() || s.WhiteIP.ValueBool()
}
