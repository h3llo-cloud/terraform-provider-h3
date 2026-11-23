package database

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &UserResource{}
	_ resource.ResourceWithConfigure      = &UserResource{}
	_ resource.ResourceWithImportState    = &UserResource{}
	_ resource.ResourceWithValidateConfig = &UserResource{}
	_ resource.ResourceWithModifyPlan     = &UserResource{}
)

// grantsRetryTimeout bounds the retries of PUT grants after a create that
// answered 202 NOT_READY_RETRY (the role is not created in PostgreSQL yet).
var grantsRetryTimeout = 2 * time.Minute

const (
	minConnectionLimit = 1
	maxConnectionLimit = 5000
)

var userNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

func NewUserResource() resource.Resource { return &UserResource{} }

// UserResource manages a user (login role) of an h3db database.
type UserResource struct {
	client *client.Client
}

type UserResourceModel struct {
	ID              types.String `tfsdk:"id"`
	DatabaseID      types.String `tfsdk:"database_id"`
	Name            types.String `tfsdk:"name"`
	ConnectionLimit types.Int64  `tfsdk:"connection_limit"`
	Grants          types.Set    `tfsdk:"grants"`
	SystemRoles     types.Set    `tfsdk:"system_roles"`
	Settings        types.Map    `tfsdk:"settings"`
	Password        types.String `tfsdk:"password"`
	Status          types.String `tfsdk:"status"`
}

// DBUser is an item of GET /api/databases/v1/{id}/users.
type DBUser struct {
	Name            string    `json:"name"`
	BuiltIn         bool      `json:"builtIn"`
	ConnectionLimit int64     `json:"connectionLimit"`
	Grants          []DBGrant `json:"grants"`
	// SystemRoles and Settings (PostgreSQL, h3db PG-ROLES-PARAMS); absent
	// on an older h3db = none.
	SystemRoles []string          `json:"systemRoles"`
	Settings    map[string]string `json:"settings"`
	Status      string            `json:"status"`
}

type DBGrant struct {
	Database string `json:"database"`
	Preset   string `json:"preset"`
}

type createUserRequest struct {
	Name            string            `json:"name"`
	ConnectionLimit int64             `json:"connectionLimit"`
	Grants          []DBGrant         `json:"grants,omitempty"`
	SystemRoles     []string          `json:"systemRoles,omitempty"`
	Settings        map[string]string `json:"settings,omitempty"`
}

type userCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Code     string `json:"code,omitempty"`
}

var grantAttrTypes = map[string]attr.Type{"database": types.StringType, "preset": types.StringType}

func (r *UserResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_user"
}

func (r *UserResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a user (login role) of an `h3_database` (PostgreSQL or MariaDB). " +
			"`connection_limit`, `grants`, `system_roles` and `settings` change in place without a restart; `name` and `database_id` force a new user. " +
			"Users never get superuser, createdb, createrole or replication. A read replica (`h3_database` with `replica_of`) " +
			"has the users of its source: `database_id` of a replica is a plan-time error `REPLICA_READ_ONLY`, create the user on the source.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "`<database_id>/<name>`",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"database_id": schema.StringAttribute{
				Required: true, MarkdownDescription: "ID of the `h3_database`", PlanModifiers: replace,
				Validators: []validator.String{common.UUIDValidator()},
			},
			"name": schema.StringAttribute{
				Required: true, PlanModifiers: replace,
				MarkdownDescription: "User name: 1-63 of `a-z`, `0-9`, `_`, starting with a letter or `_`; not reserved " +
					"(`postgres`, `app`, `root`, `pg_*`, ...)",
			},
			"connection_limit": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(-1),
				MarkdownDescription: "Maximum concurrent connections of the user: `-1` (default) = unlimited, or 1-5000. " +
					"Changed in place **without a restart**; sessions already open stay. PostgreSQL: CNPG applies " +
					"`ALTER ROLE ... CONNECTION LIMIT`; MariaDB: `MAX_USER_CONNECTIONS`.",
			},
			"grants": schema.SetNestedAttribute{
				Optional: true, Computed: true,
				PlanModifiers:       []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Access presets per database inside the cluster. Replaces all access of the user on change.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"database": schema.StringAttribute{Required: true, MarkdownDescription: "Database name inside the cluster"},
					"preset": schema.StringAttribute{
						Required: true, MarkdownDescription: "`owner`, `readwrite`, `readonly` or `monitor` (PostgreSQL: CONNECT only, for a monitoring user with `pg_monitor`)",
					},
				}},
			},
			"system_roles": schema.SetAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "PostgreSQL only. Built-in roles the user is a member of: `pg_monitor`, `pg_read_all_stats`, " +
					"`pg_read_all_settings`, `pg_stat_scan_tables`, `pg_signal_backend`, `pg_read_all_data` (the current list is " +
					"`systemRoles` of `GET /api/databases/v1/{id}/limits`). Replaces the set on change (`[]` removes all); " +
					"GRANT/REVOKE online, no restart. Not set = not managed: roles given in the console stay.",
			},
			"settings": schema.MapAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "PostgreSQL only. Defaults of the user's new sessions (`ALTER ROLE ... SET`): " +
					"`default_transaction_isolation` (`read committed`, `repeatable read`, `serializable`), `statement_timeout`, " +
					"`lock_timeout`, `idle_in_transaction_session_timeout` (0..`24h`), `idle_session_timeout` (0..`7d`), " +
					"`work_mem` (`64kB`..1/8 of the database RAM, at most `2GB`). Write values the way the API returns them: " +
					"`30s`, `5min`, `1500ms`, `64MB`; `0` = no limit. Replaces the whole set on change (`{}` resets all). " +
					"Not set = not managed.",
			},
			"password": schema.StringAttribute{
				Computed: true, Sensitive: true, MarkdownDescription: "Generated password",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Computed: true, MarkdownDescription: "`READY`, `PENDING` or `ERROR`",
			},
		},
	}
}

func (r *UserResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

// validConnectionLimit: -1 or 1..5000 (0 means "no connections at all" in
// PostgreSQL and is refused by h3db).
func validConnectionLimit(n int64) error {
	if n == -1 || (n >= minConnectionLimit && n <= maxConnectionLimit) {
		return nil
	}
	return fmt.Errorf("connection_limit %d must be -1 (unlimited) or %d..%d", n, minConnectionLimit, maxConnectionLimit)
}

func (r *UserResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg UserResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cfg.ConnectionLimit.IsUnknown() && !cfg.ConnectionLimit.IsNull() {
		if err := validConnectionLimit(cfg.ConnectionLimit.ValueInt64()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("connection_limit"), "Invalid connection_limit", err.Error())
		}
	}
	if !cfg.Name.IsUnknown() && !cfg.Name.IsNull() && !userNameRe.MatchString(cfg.Name.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid name",
			"name must be 1-63 of a-z, 0-9 and _, starting with a letter or _")
	}
	if grants, err := cfg.grantsList(ctx); err == nil {
		for _, g := range grants {
			if g.Preset != "" && g.Preset != "owner" && g.Preset != "readwrite" && g.Preset != "readonly" && g.Preset != "monitor" {
				resp.Diagnostics.AddAttributeError(path.Root("grants"), "Invalid preset",
					fmt.Sprintf("preset %q of database %q must be owner, readwrite, readonly or monitor", g.Preset, g.Database))
			}
		}
	}
	if settings, ok := cfg.settingsMap(ctx); ok {
		for k, v := range settings {
			if msg := checkUserSetting(k, v); msg != "" {
				resp.Diagnostics.AddAttributeError(path.Root("settings").AtMapKey(k), "Invalid setting", msg)
			}
		}
	}
	if roles, ok := cfg.systemRolesList(ctx); ok {
		for _, r := range roles {
			if !strings.HasPrefix(r, "pg_") {
				resp.Diagnostics.AddAttributeError(path.Root("system_roles"), "Invalid system role",
					fmt.Sprintf("%q is not a built-in PostgreSQL role (pg_*)", r))
			}
		}
	}
}

// ModifyPlan refuses a user on a read replica at plan time (h3db answers 409
// REPLICA_READ_ONLY): users of a replica are those of its source. Checked on
// create and on a change, when database_id is known; a failed lookup is left
// to the API.
func (r *UserResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	if !req.State.Raw.IsNull() && req.Plan.Raw.Equal(req.State.Raw) {
		return
	}
	var dbID types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("database_id"), &dbID)...)
	if !known(dbID) {
		return
	}
	var db Database
	if err := r.client.Do(ctx, "GET", basePath+"/"+dbID.ValueString(), nil, nil, &db); err != nil || db.ReplicaOf == "" {
		return
	}
	resp.Diagnostics.AddAttributeError(path.Root("database_id"), "REPLICA_READ_ONLY: the database is a read replica",
		fmt.Sprintf("Database %s is a read replica of %s. Users and grants of a replica are those of its source: "+
			"manage the user on the source (database_id = %q); it appears on the replica by itself.",
			dbID.ValueString(), db.ReplicaOf, db.ReplicaOf))
}

func usersPath(dbID string) string { return basePath + "/" + dbID + "/users" }
func userPath(dbID, name string) string {
	return usersPath(dbID) + "/" + url.PathEscape(name)
}

func (m *UserResourceModel) grantsList(ctx context.Context) ([]DBGrant, error) {
	if m.Grants.IsNull() || m.Grants.IsUnknown() {
		return nil, nil
	}
	var items []struct {
		Database types.String `tfsdk:"database"`
		Preset   types.String `tfsdk:"preset"`
	}
	if d := m.Grants.ElementsAs(ctx, &items, false); d.HasError() {
		return nil, errors.New("invalid grants")
	}
	out := make([]DBGrant, 0, len(items))
	for _, it := range items {
		out = append(out, DBGrant{Database: it.Database.ValueString(), Preset: it.Preset.ValueString()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Database < out[j].Database })
	return out, nil
}

// systemRolesList: the configured roles, ok=false when not set or unknown.
func (m *UserResourceModel) systemRolesList(ctx context.Context) ([]string, bool) {
	if m.SystemRoles.IsNull() || m.SystemRoles.IsUnknown() {
		return nil, false
	}
	var out []string
	if d := m.SystemRoles.ElementsAs(ctx, &out, false); d.HasError() {
		return nil, false
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out, true
}

// settingsMap: the configured settings, ok=false when not set or unknown.
func (m *UserResourceModel) settingsMap(ctx context.Context) (map[string]string, bool) {
	if m.Settings.IsNull() || m.Settings.IsUnknown() {
		return nil, false
	}
	out := map[string]string{}
	if d := m.Settings.ElementsAs(ctx, &out, false); d.HasError() {
		return nil, false
	}
	return out, true
}

func systemRolesValue(roles []string) types.Set {
	elems := make([]attr.Value, 0, len(roles))
	for _, r := range roles {
		elems = append(elems, types.StringValue(r))
	}
	return types.SetValueMust(types.StringType, elems)
}

func settingsValue(m map[string]string) types.Map {
	elems := make(map[string]attr.Value, len(m))
	for k, v := range m {
		elems[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, elems)
}

func grantsValue(grants []DBGrant) types.Set {
	elems := make([]attr.Value, 0, len(grants))
	for _, g := range grants {
		elems = append(elems, types.ObjectValueMust(grantAttrTypes, map[string]attr.Value{
			"database": types.StringValue(g.Database), "preset": types.StringValue(g.Preset),
		}))
	}
	return types.SetValueMust(types.ObjectType{AttrTypes: grantAttrTypes}, elems)
}

func (r *UserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan UserResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	grants, err := plan.grantsList(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Invalid grants", err.Error())
		return
	}
	dbID, name := plan.DatabaseID.ValueString(), plan.Name.ValueString()
	roles, _ := plan.systemRolesList(ctx)
	settings, _ := plan.settingsMap(ctx)
	var creds userCredentials
	if err := r.client.Do(ctx, "POST", usersPath(dbID), nil, createUserRequest{
		Name: name, ConnectionLimit: plan.ConnectionLimit.ValueInt64(), Grants: grants,
		SystemRoles: roles, Settings: settings,
	}, &creds); err != nil {
		resp.Diagnostics.AddError(apiErrorDiag("creating database user", err))
		return
	}
	plan.ID = types.StringValue(dbID + "/" + name)
	plan.Password = types.StringValue(creds.Password)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

	// 202 NOT_READY_RETRY: the user exists, the settings and grants were not
	// applied yet (h3db applies the settings first).
	if creds.Code == "NOT_READY_RETRY" && len(settings) > 0 {
		if err := r.retryNotReady(ctx, func(ctx context.Context) error {
			return r.patchUser(ctx, dbID, name, map[string]any{"settings": settings})
		}); err != nil {
			resp.Diagnostics.AddError("User created, settings not applied", err.Error())
			return
		}
	}
	if creds.Code == "NOT_READY_RETRY" && len(grants) > 0 {
		if err := r.retryGrants(ctx, dbID, name, grants); err != nil {
			resp.Diagnostics.AddError("User created, grants not applied", err.Error())
			return
		}
	}
	if err := r.read(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Error reading database user", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *UserResource) retryGrants(ctx context.Context, dbID, name string, grants []DBGrant) error {
	return r.retryNotReady(ctx, func(ctx context.Context) error { return r.setGrants(ctx, dbID, name, grants) })
}

func (r *UserResource) patchUser(ctx context.Context, dbID, name string, body map[string]any) error {
	return r.client.Do(ctx, "PATCH", userPath(dbID, name), nil, body, nil)
}

// retryNotReady repeats call while h3db answers NOT_READY_RETRY.
func (r *UserResource) retryNotReady(ctx context.Context, call func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, grantsRetryTimeout)
	defer cancel()
	for {
		err := call(ctx)
		var he *client.HTTPError
		if err == nil || !errors.As(err, &he) || he.Code() != "NOT_READY_RETRY" {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("still NOT_READY_RETRY after %s: %w", grantsRetryTimeout, err)
		case <-time.After(pollInterval):
		}
	}
}

func (r *UserResource) setGrants(ctx context.Context, dbID, name string, grants []DBGrant) error {
	if grants == nil {
		grants = []DBGrant{}
	}
	return r.client.Do(ctx, "PUT", userPath(dbID, name)+"/grants", nil, map[string]any{"grants": grants}, nil)
}

// read refreshes the model from GET /users (and the password from the
// credentials endpoint when it is not known). errUserGone when absent.
func (r *UserResource) read(ctx context.Context, m *UserResourceModel) error {
	dbID, name := m.DatabaseID.ValueString(), m.Name.ValueString()
	var users []DBUser
	if err := r.client.Do(ctx, "GET", usersPath(dbID), nil, nil, &users); err != nil {
		return err
	}
	var u *DBUser
	for i := range users {
		if users[i].Name == name {
			u = &users[i]
		}
	}
	if u == nil {
		return errUserGone
	}
	m.ID = types.StringValue(dbID + "/" + name)
	m.ConnectionLimit = types.Int64Value(u.ConnectionLimit)
	grants := append([]DBGrant(nil), u.Grants...)
	sort.Slice(grants, func(i, j int) bool { return grants[i].Database < grants[j].Database })
	m.Grants = grantsValue(grants)
	roles := append([]string(nil), u.SystemRoles...)
	sort.Strings(roles)
	m.SystemRoles = systemRolesValue(roles)
	m.Settings = settingsValue(u.Settings)
	m.Status = types.StringValue(u.Status)
	if m.Password.IsNull() || m.Password.IsUnknown() || m.Password.ValueString() == "" {
		var c userCredentials
		if err := r.client.Do(ctx, "GET", userPath(dbID, name)+"/credentials", nil, nil, &c); err == nil {
			m.Password = types.StringValue(c.Password)
		} else if m.Password.IsUnknown() {
			m.Password = types.StringNull()
		}
	}
	return nil
}

var errUserGone = errors.New("user not found")

func (r *UserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state UserResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.read(ctx, &state); err != nil {
		if errors.Is(err, errUserGone) || isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading database user", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *UserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state UserResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	dbID, name := state.DatabaseID.ValueString(), state.Name.ValueString()
	if !plan.ConnectionLimit.Equal(state.ConnectionLimit) {
		if err := r.client.Do(ctx, "PATCH", userPath(dbID, name), nil,
			map[string]int64{"connectionLimit": plan.ConnectionLimit.ValueInt64()}, nil); err != nil {
			resp.Diagnostics.AddError(apiErrorDiag("changing connection_limit", err))
			return
		}
	}
	if roles, ok := plan.systemRolesList(ctx); ok && !plan.SystemRoles.Equal(state.SystemRoles) {
		if err := r.patchUser(ctx, dbID, name, map[string]any{"systemRoles": roles}); err != nil {
			resp.Diagnostics.AddError(apiErrorDiag("changing system_roles", err))
			return
		}
	}
	if settings, ok := plan.settingsMap(ctx); ok && !plan.Settings.Equal(state.Settings) {
		if err := r.patchUser(ctx, dbID, name, map[string]any{"settings": settings}); err != nil {
			resp.Diagnostics.AddError(apiErrorDiag("changing settings", err))
			return
		}
	}
	if !plan.Grants.IsUnknown() && !plan.Grants.Equal(state.Grants) {
		grants, err := plan.grantsList(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Invalid grants", err.Error())
			return
		}
		if err := r.setGrants(ctx, dbID, name, grants); err != nil {
			resp.Diagnostics.AddError(apiErrorDiag("changing grants", err))
			return
		}
	}
	plan.Password = state.Password
	if err := r.read(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Error reading database user", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *UserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state UserResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.Do(ctx, "DELETE", userPath(state.DatabaseID.ValueString(), state.Name.ValueString()), nil, nil, nil)
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError(apiErrorDiag("deleting database user", err))
	}
}

// ImportState: `<database_id>/<name>`.
func (r *UserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	dbID, name, ok := strings.Cut(req.ID, "/")
	if !ok || dbID == "" || name == "" {
		resp.Diagnostics.AddError("Invalid import ID", "expected <database_id>/<name>")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("database_id"), dbID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}
