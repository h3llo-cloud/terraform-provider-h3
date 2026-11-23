package database

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &PoolerResource{}
	_ resource.ResourceWithConfigure      = &PoolerResource{}
	_ resource.ResourceWithImportState    = &PoolerResource{}
	_ resource.ResourceWithValidateConfig = &PoolerResource{}
	_ resource.ResourceWithModifyPlan     = &PoolerResource{}
)

// Pooler wait limits: PgBouncer pods start or roll in about a minute.
var (
	poolerTimeout       = 10 * time.Minute
	poolerDeleteTimeout = 5 * time.Minute
)

func NewPoolerResource() resource.Resource { return &PoolerResource{} }

// PoolerResource manages a PgBouncer pooler of an h3db PostgreSQL database.
type PoolerResource struct {
	client *client.Client
}

type PoolerResourceModel struct {
	ID                  types.String `tfsdk:"id"`
	DatabaseID          types.String `tfsdk:"database_id"`
	PoolerID            types.String `tfsdk:"pooler_id"`
	DisplayName         types.String `tfsdk:"display_name"`
	Role                types.String `tfsdk:"role"`
	Mode                types.String `tfsdk:"mode"`
	Instances           types.Int64  `tfsdk:"instances"`
	Default             types.Bool   `tfsdk:"default"`
	Paused              types.Bool   `tfsdk:"paused"`
	Parameters          types.Map    `tfsdk:"parameters"`
	VPCMode             types.String `tfsdk:"vpc_mode"`
	VPCPort             types.Int64  `tfsdk:"vpc_port"`
	PublicPort          types.Int64  `tfsdk:"public_port"`
	PublicVia           types.String `tfsdk:"public_via"`
	K8sName             types.String `tfsdk:"k8s_name"`
	Phase               types.String `tfsdk:"phase"`
	ReadyInstances      types.Int64  `tfsdk:"ready_instances"`
	EffectiveParameters types.Map    `tfsdk:"effective_parameters"`
	Adopted             types.Bool   `tfsdk:"adopted"`
	VPCIP               types.String `tfsdk:"vpc_ip"`
	VPCStatus           types.String `tfsdk:"vpc_status"`
	VPCEndpoint         types.String `tfsdk:"vpc_endpoint"`
	PublicAddresses     types.List   `tfsdk:"public_addresses"`
	PublicEndpoints     types.List   `tfsdk:"public_endpoints"`
}

var publicAddressAttrTypes = map[string]attr.Type{
	"ip":       types.StringType,
	"port":     types.Int64Type,
	"via":      types.StringType,
	"eip_id":   types.StringType,
	"endpoint": types.StringType,
}

func (r *PoolerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_pooler"
}

func publicAddressesAttribute() schema.ListNestedAttribute {
	str := func(d string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: d}
	}
	return schema.ListNestedAttribute{
		Computed:            true,
		MarkdownDescription: "Listeners of the pooler on the public EIP of the database.",
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"ip":       str("Public IP (the EIP of the database)"),
			"port":     schema.Int64Attribute{Computed: true, MarkdownDescription: "Port"},
			"via":      str("`port-sources`, `service` or `external` (taken over from an older setup)"),
			"eip_id":   str("Elastic IP ID"),
			"endpoint": str("`<ip>:<port>`"),
		}},
	}
}

func (r *PoolerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	keepInt := []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a connection pooler (PgBouncer) of a PostgreSQL `h3_database`. A database can have " +
			"several poolers: `rw` (to the primary), `ro` (to the replicas only) and `r` (to every instance), each with its " +
			"own mode, number of pods, parameters and listeners. `display_name`, `mode`, `instances`, `default`, `paused`, " +
			"`parameters`, `vpc_mode`, `vpc_port`, `public_port` and `public_via` change in place: PgBouncer pods roll one " +
			"by one, PostgreSQL is not restarted. `pooler_id`, `role` and `database_id` force a new pooler. Apply waits " +
			"(up to 10 minutes) until the pooler is `READY` (`PAUSED` with `paused = true`).\n\n" +
			"Where clients connect: `vpc_mode`/`vpc_port` (inside the project VPC) and `public_port` (on the public EIP of " +
			"the database, set with `white_ip`/`eip_id` of `h3_database`; the pooler does not lease an EIP). Pooler pods " +
			"are billed per pod. Needs h3db with several poolers per database enabled for the project " +
			"(`POOLER_MULTI_DISABLED` otherwise).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "`<database_id>/<pooler_id>`", PlanModifiers: keep,
			},
			"database_id": schema.StringAttribute{
				Required: true, MarkdownDescription: "ID of the PostgreSQL `h3_database`", PlanModifiers: replace,
				Validators: []validator.String{common.UUIDValidator()},
			},
			"pooler_id": schema.StringAttribute{
				Required: true, PlanModifiers: replace,
				MarkdownDescription: "Pooler ID, unique in the database: 1-20 of `a-z`, `0-9` and `-`, starting and ending " +
					"with a letter or digit (e.g. `main`, `ro`, `reports`). The Kubernetes object is `db-<database>-pooler-<pooler_id>`. " +
					"Changing it replaces the pooler.",
			},
			"display_name": schema.StringAttribute{
				Optional: true, Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Name shown in the console, at most 64 characters. Not set: chosen by h3db. Changed in place.",
			},
			"role": schema.StringAttribute{
				Required: true, PlanModifiers: replace,
				MarkdownDescription: "`rw` (the primary), `ro` (read replicas and HA standbys only) or `r` (every instance). " +
					"`ro` and `r` need at least two instances of the database (`ROLE_NEEDS_REPLICA`). Changing it replaces the pooler.",
			},
			"mode": schema.StringAttribute{
				Optional: true, Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Pool mode: `session` (the h3db default) or `transaction`. Changed in place (the pods " +
					"roll; in `transaction` mode session state such as `SET`, advisory locks and `LISTEN` is not kept between transactions).",
			},
			"instances": schema.Int64Attribute{
				Optional: true, Computed: true, PlanModifiers: keepInt,
				MarkdownDescription: "Number of PgBouncer pods, at least 1. The upper bound is set by h3db (16 today, see " +
					"`limits.max_instances` of the data source `h3_database_poolers`; `INVALID_INSTANCES`). Not set: the h3db " +
					"default (2). Changed in place.",
			},
			"default": schema.BoolAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "The default pooler of the database (`rw` only; exactly one). It is the pooler of the " +
					"older `/pooler` API and of `h3 db pooler get`. Setting `true` moves the flag from the other pooler, whose " +
					"`default` reads `false` on its next refresh; do not set `true` on two poolers. Not set: chosen by h3db.",
			},
			"paused": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "`true` pauses the pooler (PgBouncer `PAUSE`: clients stay connected, new queries wait); " +
					"`false` resumes it. Changed in place.",
			},
			"parameters": schema.MapAttribute{
				Optional: true, ElementType: types.StringType,
				MarkdownDescription: "PgBouncer parameters set by you, as strings (e.g. `{ default_pool_size = \"50\" }`). " +
					"Allowed keys: `" + strings.Join(poolerParamKeys, "`, `") + "`. Values are non-negative integers, " +
					"`client_tls_sslmode` is `require` or `prefer`. The map is authoritative: a key removed from it returns " +
					"to the h3db default. Ranges and the connection budget of the database (sum of `instances` × " +
					"`max_db_connections` + 10 within `max_connections`, per instance) are checked by h3db " +
					"(`INVALID_PARAMETER`, `INVALID_LIMITS`). The values in effect are in `effective_parameters`.",
			},
			"vpc_mode": schema.StringAttribute{
				Optional: true, Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Listener in the project VPC: `port` (a port on the VPC address of the database), " +
					"`address` (an own VPC address of the pooler) or `none`. Not set: h3db chooses (`port` when the database " +
					"has a VPC address); removing it from the configuration keeps the current value. Changed in place. " +
					"Free of charge.",
			},
			"vpc_port": schema.Int64Attribute{
				Optional: true, Computed: true, PlanModifiers: keepInt,
				MarkdownDescription: "Port of the VPC listener, 1024-65535. Not set: the h3db default (6432 for `rw`, 6433 " +
					"for `ro`, 6434 for `r` with `vpc_mode = \"port\"`; 5432 with `address`). Not allowed with `vpc_mode = \"none\"`.",
			},
			"public_port": schema.Int64Attribute{
				Optional: true, Computed: true, PlanModifiers: keepInt,
				MarkdownDescription: "Port of the pooler on the public EIP of the database, 1024-65535; `0` removes the " +
					"public listener. `5432` puts the pooler on the main port of the database (instead of PostgreSQL). Needs " +
					"a public address of the database (`white_ip = true`, `PUBLIC_NEEDS_EIP`). Not set: no public listener " +
					"on create, the current one later.",
			},
			"public_via": schema.StringAttribute{
				Optional: true, Computed: true, PlanModifiers: keep,
				MarkdownDescription: "How the public port is set up on the EIP: `port-sources` (the default: a port of the " +
					"database load balancer) or `service` (a separate load balancer on the same EIP). Changed in place.",
			},
			"k8s_name": schema.StringAttribute{
				Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Name of the Kubernetes Pooler object (shown in the console under the name).",
			},
			"phase": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`READY`, `PROGRESSING`, `PAUSED` or `STOPPED` (the database is stopped).",
			},
			"ready_instances": schema.Int64Attribute{
				Computed: true, MarkdownDescription: "Ready PgBouncer pods.",
			},
			"effective_parameters": schema.MapAttribute{
				Computed: true, ElementType: types.StringType,
				MarkdownDescription: "PgBouncer parameters in effect (yours plus the h3db defaults for the mode, the size of " +
					"the database and the other poolers).",
			},
			"adopted": schema.BoolAttribute{
				Computed: true, MarkdownDescription: "The pooler was created outside h3db and taken over (older databases).",
			},
			"vpc_ip": schema.StringAttribute{
				Computed: true, MarkdownDescription: "IP of the VPC listener; null without one.",
			},
			"vpc_status": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "State of the VPC listener: `assigned`, `pending` or `unavailable` (the address could " +
					"not be set up, e.g. the database has no VPC address); null with `vpc_mode = \"none\"`.",
			},
			"vpc_endpoint": schema.StringAttribute{
				Computed: true, MarkdownDescription: "`<ip>:<port>` of the pooler in the VPC; null without a VPC listener.",
			},
			"public_addresses": publicAddressesAttribute(),
			"public_endpoints": schema.ListAttribute{
				Computed: true, ElementType: types.StringType,
				MarkdownDescription: "`<ip>:<port>` of the pooler on the public EIP of the database.",
			},
		},
	}
}

func (r *PoolerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *PoolerResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg PoolerResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validatePoolerConfig(ctx, &cfg, &resp.Diagnostics)
}

func validatePoolerConfig(ctx context.Context, cfg *PoolerResourceModel, diags *diag.Diagnostics) {
	attrErr := func(attr, summary string, err error) {
		diags.AddAttributeError(path.Root(attr), summary, err.Error())
	}
	if known(cfg.PoolerID) {
		if err := validatePoolerID(cfg.PoolerID.ValueString()); err != nil {
			attrErr("pooler_id", "Invalid pooler_id", err)
		}
	}
	if known(cfg.DisplayName) {
		if err := validateDisplayName(cfg.DisplayName.ValueString()); err != nil {
			attrErr("display_name", "Invalid display_name", err)
		}
	}
	if known(cfg.Role) && !oneOf(cfg.Role.ValueString(), poolerRoles) {
		attrErr("role", "Invalid role", fmt.Errorf("role %q must be rw, ro or r", cfg.Role.ValueString()))
	}
	if known(cfg.Mode) && !oneOf(cfg.Mode.ValueString(), poolerModes) {
		attrErr("mode", "Invalid mode", fmt.Errorf("mode %q must be session or transaction", cfg.Mode.ValueString()))
	}
	if known(cfg.Instances) && cfg.Instances.ValueInt64() < 1 {
		attrErr("instances", "Invalid instances", fmt.Errorf("instances must be at least 1, got %d", cfg.Instances.ValueInt64()))
	}
	if known(cfg.Default) && cfg.Default.ValueBool() && known(cfg.Role) && cfg.Role.ValueString() != "rw" {
		attrErr("default", "Invalid default", errors.New("default = true is allowed for a pooler with role = \"rw\" only"))
	}
	if known(cfg.VPCMode) && !oneOf(cfg.VPCMode.ValueString(), vpcModes) {
		attrErr("vpc_mode", "Invalid vpc_mode", fmt.Errorf("vpc_mode %q must be port, address or none", cfg.VPCMode.ValueString()))
	}
	if known(cfg.VPCPort) {
		if err := validateListenerPort("vpc_port", cfg.VPCPort.ValueInt64(), false); err != nil {
			attrErr("vpc_port", "Invalid vpc_port", err)
		}
		if known(cfg.VPCMode) && cfg.VPCMode.ValueString() == vpcModeNone {
			attrErr("vpc_port", "vpc_port with vpc_mode = \"none\"",
				errors.New("vpc_mode = \"none\" removes the VPC listener; remove vpc_port"))
		}
	}
	if known(cfg.PublicPort) {
		if err := validateListenerPort("public_port", cfg.PublicPort.ValueInt64(), true); err != nil {
			attrErr("public_port", "Invalid public_port", err)
		}
	}
	if known(cfg.PublicVia) && !oneOf(cfg.PublicVia.ValueString(), publicVias) {
		attrErr("public_via", "Invalid public_via", fmt.Errorf("public_via %q must be port-sources or service", cfg.PublicVia.ValueString()))
	}
	params, d := stringMap(ctx, cfg.Parameters)
	diags.Append(d...)
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if params[k] == nil {
			continue // unknown value
		}
		if err := validatePoolerParameter(k, *params[k]); err != nil {
			diags.AddAttributeError(path.Root("parameters").AtMapKey(k), "Invalid pooler parameter", err.Error())
		}
	}
}

// ModifyPlan: a value h3db derives from another one is unknown when that one
// changes and the value is not configured (vpc_port follows vpc_mode,
// public_via follows public_port).
func (r *PoolerResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state, cfg PoolerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.VPCPort.IsNull() && !plan.VPCMode.Equal(state.VPCMode) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("vpc_port"), types.Int64Unknown())...)
	}
	if cfg.PublicVia.IsNull() && !plan.PublicPort.Equal(state.PublicPort) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("public_via"), types.StringUnknown())...)
	}
}

// stringMap returns the values of a map (nil for an unknown element).
func stringMap(ctx context.Context, m types.Map) (map[string]*string, diag.Diagnostics) {
	if m.IsNull() || m.IsUnknown() {
		return nil, nil
	}
	var raw map[string]types.String
	diags := m.ElementsAs(ctx, &raw, false)
	out := make(map[string]*string, len(raw))
	for k, v := range raw {
		if v.IsUnknown() {
			out[k] = nil
			continue
		}
		s := v.ValueString()
		out[k] = &s
	}
	return out, diags
}

func knownStringMap(ctx context.Context, m types.Map) (map[string]string, diag.Diagnostics) {
	in, diags := stringMap(ctx, m)
	if in == nil {
		return nil, diags
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if v != nil {
			out[k] = *v
		}
	}
	return out, diags
}

// parametersPatch returns the parameters to send for a change from old to
// new: changed and added keys with their values, removed keys with "" (back
// to the default). nil when nothing changed.
func parametersPatch(old, new map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range new {
		if ov, ok := old[k]; !ok || ov != v {
			out[k] = v
		}
	}
	for k := range old {
		if _, ok := new[k]; !ok {
			out[k] = ""
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mapValue(m map[string]string) types.Map {
	elems := make(map[string]attr.Value, len(m))
	for k, v := range m {
		elems[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, elems)
}

func joinHostPort(ip string, port int64) string {
	if strings.Contains(ip, ":") {
		return "[" + ip + "]:" + strconv.FormatInt(port, 10)
	}
	return ip + ":" + strconv.FormatInt(port, 10)
}

func optPort(n int64) types.Int64 {
	if n <= 0 {
		return types.Int64Null()
	}
	return types.Int64Value(n)
}

// applyPooler maps the API object to the model. The user parameters keep a
// null map when the configuration had none and h3db reports none;
// public_via keeps the planned value while there is no public listener.
func (m *PoolerResourceModel) applyPooler(p *Pooler) {
	m.ID = types.StringValue(m.DatabaseID.ValueString() + "/" + p.ID)
	m.PoolerID = types.StringValue(p.ID)
	m.DisplayName = types.StringValue(p.DisplayName)
	m.Role = types.StringValue(p.Role)
	m.Mode = types.StringValue(p.Mode)
	m.Instances = types.Int64Value(p.Instances)
	m.Default = types.BoolValue(p.Default)
	m.Paused = types.BoolValue(p.Paused)
	if len(p.UserParameters) == 0 && (m.Parameters.IsNull() || m.Parameters.IsUnknown()) {
		m.Parameters = types.MapNull(types.StringType)
	} else {
		m.Parameters = mapValue(p.UserParameters)
	}
	m.K8sName = common.OptString(p.K8sName)
	m.Phase = types.StringValue(p.Phase)
	m.ReadyInstances = types.Int64Value(p.ReadyInstances)
	m.EffectiveParameters = mapValue(p.Parameters)
	m.Adopted = types.BoolValue(p.Adopted)

	m.VPCMode, m.VPCPort = types.StringValue(vpcModeNone), types.Int64Null()
	m.VPCIP, m.VPCStatus, m.VPCEndpoint = types.StringNull(), types.StringNull(), types.StringNull()
	if v := p.VPC; v != nil && v.Mode != "" {
		m.VPCMode = types.StringValue(v.Mode)
		if v.Mode != vpcModeNone {
			m.VPCPort = optPort(v.Port)
			m.VPCIP = common.OptString(v.IP)
			m.VPCStatus = common.OptString(v.Status)
			if v.IP != "" && v.Port > 0 {
				m.VPCEndpoint = types.StringValue(joinHostPort(v.IP, v.Port))
			}
		}
	}

	objs := make([]attr.Value, 0, len(p.Public))
	eps := make([]attr.Value, 0, len(p.Public))
	for _, pub := range p.Public {
		ep := joinHostPort(pub.IP, pub.Port)
		objs = append(objs, types.ObjectValueMust(publicAddressAttrTypes, map[string]attr.Value{
			"ip": types.StringValue(pub.IP), "port": types.Int64Value(pub.Port), "via": common.OptString(pub.Via),
			"eip_id": common.OptString(pub.EIPID), "endpoint": types.StringValue(ep),
		}))
		eps = append(eps, types.StringValue(ep))
	}
	m.PublicAddresses = types.ListValueMust(types.ObjectType{AttrTypes: publicAddressAttrTypes}, objs)
	m.PublicEndpoints = types.ListValueMust(types.StringType, eps)
	if pub := mainPublic(p.Public); pub != nil {
		m.PublicPort = types.Int64Value(pub.Port)
		m.PublicVia = common.OptString(pub.Via)
	} else {
		m.PublicPort = types.Int64Value(0)
		if m.PublicVia.IsUnknown() {
			m.PublicVia = types.StringNull()
		}
	}
}

// mainPublic is the public listener that public_port manages: the first one
// set up by h3db, else the first one (an adopted "external").
func mainPublic(ls []PoolerPublic) *PoolerPublic {
	for i := range ls {
		if ls[i].Via != "external" {
			return &ls[i]
		}
	}
	if len(ls) > 0 {
		return &ls[0]
	}
	return nil
}

func (r *PoolerResource) getPooler(ctx context.Context, dbID, poolerID string) (*Pooler, error) {
	var p Pooler
	if err := r.client.Do(ctx, "GET", poolerPath(dbID, poolerID), nil, nil, &p); err != nil {
		return nil, err
	}
	if p.ID == "" {
		p.ID = poolerID
	}
	return &p, nil
}

func listPoolers(ctx context.Context, c *client.Client, dbID string) (*PoolerList, error) {
	var rm rawJSON
	if err := c.Do(ctx, "GET", poolersPath(dbID), nil, nil, &rm); err != nil {
		return nil, err
	}
	l, err := decodePoolerList(rm)
	if err != nil {
		return nil, fmt.Errorf("decoding poolers: %w", err)
	}
	return l, nil
}

// rawJSON keeps a response body as is.
type rawJSON []byte

func (r *rawJSON) UnmarshalJSON(b []byte) error {
	*r = append((*r)[:0], b...)
	return nil
}

var errPoolerGone = errors.New("pooler not found")

// read refreshes the model; errPoolerGone when the pooler does not exist.
func (r *PoolerResource) read(ctx context.Context, m *PoolerResourceModel) error {
	p, err := r.getPooler(ctx, m.DatabaseID.ValueString(), m.PoolerID.ValueString())
	if err != nil {
		if isPoolerGone(err) {
			return errPoolerGone
		}
		return err
	}
	m.applyPooler(p)
	return nil
}

// poolerSettled: READY with every pod ready (PAUSED when paused) and the VPC
// listener no longer pending. STOPPED (the database is stopped) ends the
// wait too: the pods start with the database.
func poolerSettled(p *Pooler, wantPaused bool) bool {
	if p.VPC != nil && p.VPC.Mode != vpcModeNone && p.VPC.Status == vpcStatusWait && p.Phase != poolerPhaseStopped {
		return false
	}
	switch p.Phase {
	case poolerPhaseStopped:
		return true
	case poolerPhasePaused:
		return wantPaused
	case poolerPhaseReady:
		return !wantPaused && p.ReadyInstances >= p.Instances
	}
	return false
}

func (r *PoolerResource) waitSettled(ctx context.Context, dbID, poolerID string, wantPaused bool) (*Pooler, error) {
	var last *Pooler
	err := waitPoll(ctx, poolerTimeout, func(ctx context.Context) (bool, error) {
		p, err := r.getPooler(ctx, dbID, poolerID)
		if err != nil {
			return false, err
		}
		last = p
		if strings.EqualFold(p.Phase, "FAILED") || strings.EqualFold(p.Phase, "ERROR") {
			return false, fmt.Errorf("pooler phase is %s", p.Phase)
		}
		return poolerSettled(p, wantPaused), nil
	})
	if err != nil {
		state := ""
		if last != nil {
			state = fmt.Sprintf(" (phase %s, %d/%d pods ready)", last.Phase, last.ReadyInstances, last.Instances)
		}
		return last, fmt.Errorf("pooler %s is not ready%s: %w", poolerID, state, err)
	}
	return last, nil
}

// waitPoll polls check every pollInterval until done, an error or timeout.
func waitPoll(ctx context.Context, timeout time.Duration, check func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		done, err := check(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("timed out after %s", timeout)
			}
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s", timeout)
		case <-time.After(pollInterval):
		}
	}
}

func (r *PoolerResource) setPaused(ctx context.Context, dbID, poolerID string, paused bool) error {
	action := "resume"
	if paused {
		action = "pause"
	}
	return r.client.Do(ctx, "POST", poolerPath(dbID, poolerID)+"/"+action, nil, nil, nil)
}

// vpcFor is the vpc field of a request from the plan, nil when not set.
func vpcFor(plan *PoolerResourceModel) *vpcRequest {
	if !known(plan.VPCMode) && !known(plan.VPCPort) {
		return nil
	}
	return &vpcRequest{Mode: common.KnownString(plan.VPCMode), Port: common.KnownInt64(plan.VPCPort)}
}

func (r *PoolerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PoolerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	params, d := knownStringMap(ctx, plan.Parameters)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	dbID, poolerID := plan.DatabaseID.ValueString(), plan.PoolerID.ValueString()
	body := createPoolerRequest{
		ID:          poolerID,
		DisplayName: common.KnownString(plan.DisplayName),
		Role:        plan.Role.ValueString(),
		Mode:        common.KnownString(plan.Mode),
		Parameters:  params,
		VPC:         vpcFor(&plan),
	}
	if known(plan.Instances) {
		n := plan.Instances.ValueInt64()
		body.Instances = &n
	}
	if port := common.KnownInt64(plan.PublicPort); port > 0 {
		body.Public = &publicRequest{Port: port, Via: common.KnownString(plan.PublicVia)}
	}
	if err := r.client.Do(ctx, "POST", poolersPath(dbID), nil, body, nil); err != nil {
		resp.Diagnostics.AddError(poolerErrorDiag("creating database pooler", err))
		return
	}
	plan.ID = types.StringValue(dbID + "/" + poolerID)
	// Track the pooler at once: a failed wait leaves it in state (tainted).
	partial := plan
	partial.nullUnknowns()
	resp.Diagnostics.Append(resp.State.Set(ctx, &partial)...)

	got, err := r.waitSettled(ctx, dbID, poolerID, false)
	if err != nil {
		resp.Diagnostics.AddError("Pooler created, not ready", err.Error())
		return
	}
	if known(plan.Default) && plan.Default.ValueBool() != got.Default {
		v := plan.Default.ValueBool()
		if err := r.client.Do(ctx, "PATCH", poolerPath(dbID, poolerID), nil, patchPoolerRequest{Default: &v}, nil); err != nil {
			resp.Diagnostics.AddError(poolerErrorDiag("setting default of the database pooler", err))
			return
		}
	}
	if plan.Paused.ValueBool() {
		if err := r.setPaused(ctx, dbID, poolerID, true); err != nil {
			resp.Diagnostics.AddError(poolerErrorDiag("pausing database pooler", err))
			return
		}
		if _, err := r.waitSettled(ctx, dbID, poolerID, true); err != nil {
			resp.Diagnostics.AddError("Pooler not paused", err.Error())
			return
		}
	}
	r.finish(ctx, &plan, &resp.Diagnostics, &resp.State)
}

// nullUnknowns nulls the unknown values so a partial state can be saved.
func (m *PoolerResourceModel) nullUnknowns() {
	for _, s := range []*types.String{&m.DisplayName, &m.Mode, &m.VPCMode, &m.PublicVia} {
		if s.IsUnknown() {
			*s = types.StringNull()
		}
	}
	for _, n := range []*types.Int64{&m.Instances, &m.VPCPort, &m.PublicPort} {
		if n.IsUnknown() {
			*n = types.Int64Null()
		}
	}
	if m.Default.IsUnknown() {
		m.Default = types.BoolNull()
	}
	m.K8sName, m.Phase = types.StringNull(), types.StringNull()
	m.VPCIP, m.VPCStatus, m.VPCEndpoint = types.StringNull(), types.StringNull(), types.StringNull()
	m.ReadyInstances = types.Int64Null()
	m.EffectiveParameters = types.MapNull(types.StringType)
	m.Adopted = types.BoolNull()
	m.PublicAddresses = types.ListNull(types.ObjectType{AttrTypes: publicAddressAttrTypes})
	m.PublicEndpoints = types.ListNull(types.StringType)
}

type stateSetter interface {
	Set(context.Context, interface{}) diag.Diagnostics
}

// finish reads the pooler back and saves it; a VPC listener h3db could not
// set up is a warning.
func (r *PoolerResource) finish(ctx context.Context, m *PoolerResourceModel, diags *diag.Diagnostics, st stateSetter) {
	if err := r.read(ctx, m); err != nil {
		diags.AddError("Error reading database pooler", err.Error())
		return
	}
	if m.VPCStatus.ValueString() == vpcStatusNoVIP {
		diags.AddAttributeWarning(path.Root("vpc_status"), "VPC listener unavailable",
			fmt.Sprintf("h3db could not set up the VPC listener of pooler %s (vpc_mode %s): the database has no VPC "+
				"address, or the address pool is exhausted. Use vpc_mode = \"address\" or \"none\".",
				m.PoolerID.ValueString(), m.VPCMode.ValueString()))
	}
	diags.Append(st.Set(ctx, m)...)
}

func (r *PoolerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PoolerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.read(ctx, &state); err != nil {
		if errors.Is(err, errPoolerGone) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(poolerErrorDiag("reading database pooler", err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// patchFor builds the PATCH body for the differences between plan and state.
func patchFor(ctx context.Context, plan, state *PoolerResourceModel) (patchPoolerRequest, diag.Diagnostics) {
	var p patchPoolerRequest
	changedStr := func(a, b types.String) *string {
		if known(a) && !a.Equal(b) {
			v := a.ValueString()
			return &v
		}
		return nil
	}
	p.DisplayName = changedStr(plan.DisplayName, state.DisplayName)
	p.Mode = changedStr(plan.Mode, state.Mode)
	if known(plan.Instances) && !plan.Instances.Equal(state.Instances) {
		v := plan.Instances.ValueInt64()
		p.Instances = &v
	}
	if known(plan.Default) && !plan.Default.Equal(state.Default) {
		v := plan.Default.ValueBool()
		p.Default = &v
	}
	if (known(plan.VPCMode) && !plan.VPCMode.Equal(state.VPCMode)) || (known(plan.VPCPort) && !plan.VPCPort.Equal(state.VPCPort)) {
		p.VPC = vpcFor(plan)
	}
	if (known(plan.PublicPort) && !plan.PublicPort.Equal(state.PublicPort)) ||
		(known(plan.PublicVia) && !plan.PublicVia.Equal(state.PublicVia) && common.KnownInt64(plan.PublicPort) > 0) {
		p.Public = &publicRequest{Port: common.KnownInt64(plan.PublicPort), Via: common.KnownString(plan.PublicVia)}
	}
	newP, diags := knownStringMap(ctx, plan.Parameters)
	oldP, d := knownStringMap(ctx, state.Parameters)
	diags.Append(d...)
	p.Parameters = parametersPatch(oldP, newP)
	return p, diags
}

func (r *PoolerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state PoolerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	dbID, poolerID := state.DatabaseID.ValueString(), state.PoolerID.ValueString()
	patch, d := patchFor(ctx, &plan, &state)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	paused, wasPaused := plan.Paused.ValueBool(), state.Paused.ValueBool()
	// Resume before a change, pause after it.
	if !paused && wasPaused {
		if err := r.setPaused(ctx, dbID, poolerID, false); err != nil {
			resp.Diagnostics.AddError(poolerErrorDiag("resuming database pooler", err))
			return
		}
	}
	if !patch.empty() {
		if err := r.client.Do(ctx, "PATCH", poolerPath(dbID, poolerID), nil, patch, nil); err != nil {
			resp.Diagnostics.AddError(poolerErrorDiag("changing database pooler", err))
			return
		}
	}
	if _, err := r.waitSettled(ctx, dbID, poolerID, paused && wasPaused); err != nil {
		resp.Diagnostics.AddError("Pooler changed, not ready", err.Error())
		return
	}
	if paused && !wasPaused {
		if err := r.setPaused(ctx, dbID, poolerID, true); err != nil {
			resp.Diagnostics.AddError(poolerErrorDiag("pausing database pooler", err))
			return
		}
		if _, err := r.waitSettled(ctx, dbID, poolerID, true); err != nil {
			resp.Diagnostics.AddError("Pooler not paused", err.Error())
			return
		}
	}
	r.finish(ctx, &plan, &resp.Diagnostics, &resp.State)
}

func (r *PoolerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state PoolerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	dbID, poolerID := state.DatabaseID.ValueString(), state.PoolerID.ValueString()
	if err := r.client.Do(ctx, "DELETE", poolerPath(dbID, poolerID), nil, nil, nil); err != nil {
		if isPoolerGone(err) {
			return
		}
		resp.Diagnostics.AddError(poolerErrorDiag("deleting database pooler", err))
		return
	}
	err := waitPoll(ctx, poolerDeleteTimeout, func(ctx context.Context) (bool, error) {
		_, err := r.getPooler(ctx, dbID, poolerID)
		if isPoolerGone(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		resp.Diagnostics.AddError("Error deleting database pooler", fmt.Sprintf("pooler %s still exists: %s", poolerID, err))
	}
}

// ImportState: `<database_id>/<pooler_id>`.
func (r *PoolerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	dbID, poolerID, ok := strings.Cut(req.ID, "/")
	if !ok || dbID == "" || poolerID == "" || strings.Contains(poolerID, "/") {
		resp.Diagnostics.AddError("Invalid import ID", "expected <database_id>/<pooler_id>, got "+strconv.Quote(req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("database_id"), dbID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pooler_id"), poolerID)...)
}
