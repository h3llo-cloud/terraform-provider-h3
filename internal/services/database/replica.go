package database

// Read replicas of PostgreSQL (h3db TRD 17 §3.6): replica_of, replica_settings,
// promote and the computed replication attributes of h3_database.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

const (
	replicaDelayMin     = 0
	replicaDelayMax     = 3600
	replicaDelayDefault = 900

	replicationPromoting = "PROMOTING"
)

// promoteTimeout bounds the wait for a promotion (CNPG makes the replica a
// primary; usually seconds).
var promoteTimeout = 15 * time.Minute

// ReplicationSummary is the short replication state of a replica in the
// Database object (absent until h3db polled the replica once).
type ReplicationSummary struct {
	State      string `json:"state"`
	LagSeconds int64  `json:"lagSeconds"`
	LagBytes   int64  `json:"lagBytes"`
}

// ReplicaSettingsRequest is the settings field of POST /{id}/replicas and
// the body of PATCH /{id}/settings for a replica (applied by a reload, no
// restart).
type ReplicaSettingsRequest struct {
	MaxStandbyStreamingDelaySeconds int64 `json:"maxStandbyStreamingDelaySeconds"`
	MaxStandbyArchiveDelaySeconds   int64 `json:"maxStandbyArchiveDelaySeconds"`
	HotStandbyFeedback              bool  `json:"hotStandbyFeedback"`
}

// CreateReplicaRequest is POST /api/databases/v1/{source}/replicas. There is
// no version, postgresConfig, backup or initialDatabase: they come from the
// source. The project and network are the source's too.
type CreateReplicaRequest struct {
	Name             string                   `json:"name"`
	CPU              int64                    `json:"cpu"`
	Memory           string                   `json:"memory"`
	SizeGB           string                   `json:"sizeGb"`
	MaxConnections   int64                    `json:"maxConnections,omitempty"`
	WhiteIP          bool                     `json:"whiteIp"`
	EIPID            string                   `json:"eipId,omitempty"`
	Settings         *ReplicaSettingsRequest  `json:"settings,omitempty"`
	StorageAutoscale *StorageAutoscaleRequest `json:"storageAutoscale,omitempty"`
}

// PromoteRequest is POST /api/databases/v1/{id}/promote.
type PromoteRequest struct {
	Backup PromoteBackup `json:"backup"`
}

type PromoteBackup struct {
	Enabled bool `json:"enabled"`
}

// ReplicaSettingsModel is the replica_settings block.
type ReplicaSettingsModel struct {
	MaxStreamingDelay  types.Int64 `tfsdk:"max_standby_streaming_delay_seconds"`
	MaxArchiveDelay    types.Int64 `tfsdk:"max_standby_archive_delay_seconds"`
	HotStandbyFeedback types.Bool  `tfsdk:"hot_standby_feedback"`
}

var replicaSettingsAttrTypes = map[string]attr.Type{
	"max_standby_streaming_delay_seconds": types.Int64Type,
	"max_standby_archive_delay_seconds":   types.Int64Type,
	"hot_standby_feedback":                types.BoolType,
}

func replicaSettingsPath(attr string) path.Path {
	return path.Root("replica_settings").AtName(attr)
}

// replicaAttributes are added to the h3_database schema.
func replicaAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"replica_of": schema.StringAttribute{
			Optional:   true,
			Validators: []validator.String{common.UUIDValidator()},
			MarkdownDescription: "ID of a PostgreSQL `h3_database` to create this database as its **read replica** " +
				"(`POST /api/databases/v1/{replica_of}/replicas`). The version, project, network, users and databases come " +
				"from the source; `version` and `subnet_name` cannot be set. `size` and `max_connections` cannot be below " +
				"the source's. Changing it to another ID replaces the database. Removing it from the configuration of a " +
				"replica is a plan error (set `promote = true` to make the replica standalone). After a promotion the value " +
				"may stay in the configuration (it then names the former source, see `promoted_from`) or be removed; " +
				"neither plans a replacement.",
			PlanModifiers: []planmodifier.String{replicaOfReplace()},
		},
		"promote": schema.BoolAttribute{
			Optional: true,
			MarkdownDescription: "One-shot action. Changing it to `true` on a read replica promotes it to a standalone " +
				"database with writes (`POST /api/databases/v1/{id}/promote`, backups enabled). **Irreversible**: the " +
				"replication stops and the database cannot be attached to a source again. Not allowed on create. " +
				"After the promotion it has no effect and may stay in the configuration.",
		},
		"replication_state": schema.StringAttribute{
			Computed: true,
			MarkdownDescription: "Replication state of a read replica: `BOOTSTRAPPING`, `STREAMING`, `ARCHIVE`, `PAUSED`, " +
				"`BROKEN`, `PROMOTING` or `UNKNOWN`. Null for a database that is not a replica.",
		},
		"replication_lag_seconds": schema.Int64Attribute{
			Computed:            true,
			MarkdownDescription: "Replication lag of a read replica in seconds, as h3db last measured it. Null when not a replica.",
		},
		"promoted_from": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "ID of the former source when this database was a read replica and has been promoted.",
		},
	}
}

func replicaSettingsBlock() schema.SingleNestedBlock {
	return schema.SingleNestedBlock{
		MarkdownDescription: "Settings of a read replica (only with `replica_of`). Changed in place with " +
			"`PATCH /api/databases/v1/{id}/settings`, without a restart. Not set: the platform defaults (900 / 900 / false); " +
			"removing the block keeps the current values.",
		Attributes: map[string]schema.Attribute{
			"max_standby_streaming_delay_seconds": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "`max_standby_streaming_delay`, 0-3600 seconds (default 900): how long WAL replay waits " +
					"for a conflicting query on the replica before the query is cancelled.",
			},
			"max_standby_archive_delay_seconds": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "`max_standby_archive_delay`, 0-3600 seconds (default 900): the same while WAL comes from the archive.",
			},
			"hot_standby_feedback": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "`hot_standby_feedback` (default false). `true`: long queries on the replica are not " +
					"cancelled, but VACUUM on the source waits for them and its tables can grow.",
			},
		},
	}
}

// replicaOfReplace: a different replica_of replaces the database, except
//   - after a promotion (promoted_from in state): removing replica_of, or
//     keeping the former source, is an in-place no-op;
//   - removing replica_of of a replica together with promote = true (the
//     promotion; without promote ModifyPlan refuses the plan).
func replicaOfReplace() planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(
		func(ctx context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
			var promotedFrom types.String
			resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("promoted_from"), &promotedFrom)...)
			if pf := common.KnownString(promotedFrom); pf != "" {
				resp.RequiresReplace = req.PlanValue.IsUnknown() ||
					(!req.PlanValue.IsNull() && req.PlanValue.ValueString() != pf)
				return
			}
			if !req.StateValue.IsNull() && req.PlanValue.IsNull() {
				return // refused or promoted in ModifyPlan, never a silent re-create
			}
			resp.RequiresReplace = true
		},
		"Changing it replaces the database; removing it after a promotion does not.",
		"Changing it replaces the database; removing it after a promotion does not.",
	)
}

func replicaSettingsFromObject(ctx context.Context, obj types.Object) (*ReplicaSettingsModel, diag.Diagnostics) {
	if obj.IsNull() || obj.IsUnknown() {
		return nil, nil
	}
	var m ReplicaSettingsModel
	diags := obj.As(ctx, &m, basetypes.ObjectAsOptions{UnhandledUnknownAsEmpty: true})
	if diags.HasError() {
		return nil, diags
	}
	return &m, nil
}

func replicaSettingsObject(ctx context.Context, m *ReplicaSettingsModel) (types.Object, diag.Diagnostics) {
	if m == nil {
		return types.ObjectNull(replicaSettingsAttrTypes), nil
	}
	return types.ObjectValueFrom(ctx, replicaSettingsAttrTypes, m)
}

func (m *ReplicaSettingsModel) request() *ReplicaSettingsRequest {
	return &ReplicaSettingsRequest{
		MaxStandbyStreamingDelaySeconds: m.MaxStreamingDelay.ValueInt64(),
		MaxStandbyArchiveDelaySeconds:   m.MaxArchiveDelay.ValueInt64(),
		HotStandbyFeedback:              m.HotStandbyFeedback.ValueBool(),
	}
}

// isReplica: the state is a read replica that has not been promoted.
func (m *DatabaseResourceModel) isReplica() bool {
	return common.KnownString(m.ReplicaOf) != "" && common.KnownString(m.PromotedFrom) == ""
}

// validateReplicaConfig is the static part of ValidateConfig.
func validateReplicaConfig(ctx context.Context, cfg *DatabaseResourceModel, diags *diag.Diagnostics) {
	rs, d := replicaSettingsFromObject(ctx, cfg.ReplicaSettings)
	diags.Append(d...)
	if rs != nil {
		for attr, v := range map[string]types.Int64{
			"max_standby_streaming_delay_seconds": rs.MaxStreamingDelay,
			"max_standby_archive_delay_seconds":   rs.MaxArchiveDelay,
		} {
			if known(v) && (v.ValueInt64() < replicaDelayMin || v.ValueInt64() > replicaDelayMax) {
				diags.AddAttributeError(replicaSettingsPath(attr), "Invalid replica_settings",
					fmt.Sprintf("%s must be %d..%d seconds (-1, no limit, is not allowed), got %d", attr, replicaDelayMin, replicaDelayMax, v.ValueInt64()))
			}
		}
	}
	if cfg.ReplicaOf.IsNull() {
		if !cfg.ReplicaSettings.IsNull() {
			diags.AddAttributeError(path.Root("replica_settings"), "replica_settings requires replica_of",
				"replica_settings apply to a read replica only. Set replica_of, or remove the block (also after a promotion).")
		}
		return
	}
	if known(cfg.Engine) && cfg.Engine.ValueString() != "postgres" {
		diags.AddAttributeError(path.Root("engine"), "Read replicas are PostgreSQL only",
			fmt.Sprintf("replica_of requires engine = \"postgres\" (got %q; h3db: UNSUPPORTED_ENGINE)", cfg.Engine.ValueString()))
	}
	if !cfg.Version.IsNull() {
		diags.AddAttributeError(path.Root("version"), "version cannot be set on a read replica",
			"A read replica runs the PostgreSQL version of its source (replica_of). Remove version.")
	}
	if !cfg.SubnetName.IsNull() {
		diags.AddAttributeError(path.Root("subnet_name"), "subnet_name cannot be set on a read replica",
			"A read replica is placed in the network of its source (replica_of). Remove subnet_name.")
	}
}

// modifyReplicaPlan is the replica part of ModifyPlan. state is nil on create.
func (r *DatabaseResource) modifyReplicaPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse, plan *DatabaseResourceModel, state *DatabaseResourceModel) {
	planReplicaSettingsDefaults(ctx, req, resp)

	promote := known(plan.Promote) && plan.Promote.ValueBool()
	creating := state == nil || len(resp.RequiresReplace) > 0
	if creating {
		if promote {
			resp.Diagnostics.AddAttributeError(path.Root("promote"), "promote cannot be set on create",
				"promote = true promotes an existing read replica. Create the replica without promote (or with "+
					"promote = false), then set promote = true. A promoted database that is being replaced is created "+
					"as a new replica of replica_of: remove promote from the configuration first.")
		}
		if src := common.KnownString(plan.ReplicaOf); src != "" && r.client != nil {
			r.checkSource(ctx, src, plan, &resp.Diagnostics)
		}
		return
	}

	switch {
	case state.isReplica() && plan.ReplicaOf.IsNull() && !promote:
		resp.Diagnostics.AddAttributeError(path.Root("replica_of"), "Database is a read replica",
			fmt.Sprintf("The database is a read replica of %s; to make it standalone set promote = true; "+
				"removing replica_of would re-create it. Put replica_of back, or set promote = true "+
				"(irreversible) and keep or remove replica_of.", state.ReplicaOf.ValueString()))
	case promote && !state.isReplica() && common.KnownString(state.PromotedFrom) == "":
		resp.Diagnostics.AddAttributeError(path.Root("promote"), "Not a read replica",
			"promote = true applies to a read replica (replica_of) only (h3db: NOT_A_REPLICA). Remove promote.")
	}
	if !state.isReplica() && !plan.ReplicaSettings.IsNull() && !plan.ReplicaSettings.Equal(state.ReplicaSettings) {
		resp.Diagnostics.AddAttributeError(path.Root("replica_settings"), "Not a read replica",
			"replica_settings apply to a read replica only; this database has been promoted. Remove the block.")
	}
}

// planReplicaSettingsDefaults plans 900 / 900 / false for the values the
// configured block omits (the h3db defaults), so an omitted value returns to
// the default instead of keeping an earlier one.
func planReplicaSettingsDefaults(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	var raw types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("replica_settings"), &raw)...)
	if raw.IsNull() || raw.IsUnknown() {
		return
	}
	attrs := raw.Attributes()
	for name, def := range map[string]attr.Value{
		"max_standby_streaming_delay_seconds": types.Int64Value(replicaDelayDefault),
		"max_standby_archive_delay_seconds":   types.Int64Value(replicaDelayDefault),
		"hot_standby_feedback":                types.BoolValue(false),
	} {
		if v, ok := attrs[name]; ok && v.IsNull() {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, replicaSettingsPath(name), def)...)
		}
	}
}

// checkSource refuses at plan time what h3db would refuse on create. A
// failed GET is left to the API (apply reports its error).
func (r *DatabaseResource) checkSource(ctx context.Context, src string, plan *DatabaseResourceModel, diags *diag.Diagnostics) {
	s, err := r.get(ctx, src)
	if err != nil {
		if isNotFound(err) {
			diags.AddAttributeError(path.Root("replica_of"), "Source database not found",
				fmt.Sprintf("replica_of: database %s does not exist (or is not visible with this API key).", src))
		}
		return
	}
	if s.ReplicaOf != "" {
		diags.AddAttributeError(path.Root("replica_of"), "SOURCE_IS_REPLICA",
			fmt.Sprintf("Database %s is itself a read replica of %s; a replica of a replica is not supported. "+
				"Use replica_of = %q.", src, s.ReplicaOf, s.ReplicaOf))
		return
	}
	if s.Engine != "" && s.Engine != "postgres" {
		diags.AddAttributeError(path.Root("replica_of"), "UNSUPPORTED_ENGINE",
			fmt.Sprintf("Database %s runs %s; read replicas are PostgreSQL only.", src, s.Engine))
	}
	if p := common.KnownString(plan.ProjectID); p != "" && s.ProjectID != "" && p != s.ProjectID {
		diags.AddAttributeError(path.Root("project_id"), "Replica in another project",
			fmt.Sprintf("A read replica is created in the project of its source (%s), not %s. Set project_id = %q or remove it.",
				s.ProjectID, p, s.ProjectID))
	}
	if known(plan.Size) {
		want, err1 := parseGi(plan.Size.ValueString())
		have, err2 := parseGi(s.SizeGB)
		if err1 == nil && err2 == nil && want < have {
			diags.AddAttributeError(path.Root("size"), "REPLICA_SIZE_TOO_SMALL",
				fmt.Sprintf("size of a read replica cannot be below the size of its source: the source %s has %s, planned %s. "+
					"Set size to at least %s.", src, s.SizeGB, plan.Size.ValueString(), s.SizeGB))
		}
	}
	if n := common.KnownInt64(plan.MaxConn); n > 0 && s.maxConnections() > n {
		diags.AddAttributeError(path.Root("max_connections"), "REPLICA_MAX_CONNECTIONS_TOO_LOW",
			fmt.Sprintf("max_connections of a read replica cannot be below the source's (%d, PostgreSQL hot standby), planned %d.",
				s.maxConnections(), n))
	}
}

// replicaOfFromAPI: the source of a replica; after a promotion the value in
// the plan or state is kept when it names the former source (the
// configuration may keep replica_of), otherwise null.
func replicaOfFromAPI(db *Database, prior types.String) types.String {
	if db.ReplicaOf != "" {
		return types.StringValue(db.ReplicaOf)
	}
	if known(prior) && db.PromotedFrom != "" && prior.ValueString() == db.PromotedFrom {
		return prior
	}
	return types.StringNull()
}

func (m *DatabaseResourceModel) applyReplication(db *Database) {
	m.ReplicaOf = replicaOfFromAPI(db, m.ReplicaOf)
	m.PromotedFrom = types.StringNull()
	if db.PromotedFrom != "" {
		m.PromotedFrom = types.StringValue(db.PromotedFrom)
	}
	m.ReplicationState, m.ReplicationLag = types.StringNull(), types.Int64Null()
	if db.ReplicaOf != "" && db.Replication != nil {
		m.ReplicationState = types.StringValue(db.Replication.State)
		m.ReplicationLag = types.Int64Value(db.Replication.LagSeconds)
	}
	if m.Promote.IsUnknown() {
		m.Promote = types.BoolNull()
	}
}

// readReplicaSettings refreshes a replica_settings block that is in state
// (an omitted block stays null). Only while the database is a replica.
func (r *DatabaseResource) readReplicaSettings(ctx context.Context, m *DatabaseResourceModel, db *Database, warn func(string, string)) diag.Diagnostics {
	cur, diags := replicaSettingsFromObject(ctx, m.ReplicaSettings)
	if diags.HasError() || cur == nil || db.ReplicaOf == "" {
		return diags
	}
	var st Settings
	if err := r.client.Do(ctx, "GET", basePath+"/"+m.ID.ValueString()+"/settings", nil, nil, &st); err != nil {
		warn("Could not read replica_settings", err.Error())
		return diags
	}
	if st.MaxStandbyStreamingDelaySeconds != nil {
		cur.MaxStreamingDelay = types.Int64Value(*st.MaxStandbyStreamingDelaySeconds)
	}
	if st.MaxStandbyArchiveDelaySeconds != nil {
		cur.MaxArchiveDelay = types.Int64Value(*st.MaxStandbyArchiveDelaySeconds)
	}
	cur.HotStandbyFeedback = types.BoolValue(st.HotStandbyFeedback)
	m.ReplicaSettings, diags = replicaSettingsObject(ctx, cur)
	return diags
}

// promote makes the replica standalone and waits until h3db reports it is
// no longer a replica.
func (r *DatabaseResource) promote(ctx context.Context, id string) error {
	if err := r.client.Do(ctx, "POST", basePath+"/"+id+"/promote", nil,
		PromoteRequest{Backup: PromoteBackup{Enabled: true}}, nil); err != nil {
		return err
	}
	_, err := r.waitFor(ctx, id, promoteTimeout, func(d *Database) (bool, error) {
		if d.Status == StatusFailed {
			return false, errors.New("database status is FAILED")
		}
		return d.ReplicaOf == "" && (d.Replication == nil || d.Replication.State != replicationPromoting), nil
	})
	if err != nil {
		return fmt.Errorf("promotion accepted but not finished: %w; the next apply checks it again", err)
	}
	return nil
}

// replicaErrorText explains the h3db read replica error codes (TRD 17 §3.4).
var replicaErrorText = map[string]string{
	"READ_REPLICAS_DISABLED": "Read replicas are not enabled on this platform (the feature is off or there is no backup " +
		"storage). Create the database without replica_of.",
	"UNSUPPORTED_ENGINE": "Read replicas are available for PostgreSQL only.",
	"SOURCE_IS_REPLICA":  "The source (replica_of) is itself a read replica; a replica of a replica is not supported. Use the primary database.",
	"REPLICA_SIZE_TOO_SMALL": "size of a read replica cannot be below the size of its source (and a replica cannot be " +
		"shrunk). Set size to at least the source's size.",
	"REPLICA_MAX_CONNECTIONS_TOO_LOW": "max_connections of a read replica cannot be below the source's (PostgreSQL hot standby).",
	"INVALID_PARAMETER": "A parameter was refused: replica_settings delays must be 0..3600 seconds and apply to a read " +
		"replica only.",
	"NOT_A_REPLICA": "The database is not a read replica (promote and replica_settings need replica_of).",
	"SOURCE_BACKUPS_REQUIRED": "The source needs backups enabled, WAL archiving in state OK and at least one completed " +
		"base backup before a replica can be created.",
	"REPLICA_LIMIT_REACHED": "The source already has the maximum number of read replicas (3).",
	"SOURCE_HAS_REPLICAS":   "The database has read replicas. Delete them or promote them (promote = true) first.",
	"REPLICA_READ_ONLY": "The database is a read replica: users, databases, grants and backups are managed on its source " +
		"and appear on the replica by themselves.",
	"REPLICA_HA_UNSUPPORTED": "A read replica runs one instance; high availability is not supported for replicas.",
	"CHANGE_IN_PROGRESS":     "Another change (scale, resize or promotion) is in progress. Wait for it to finish and apply again.",
	"DATABASE_NOT_RUNNING": "The database (the source on create, the replica on promote or settings) is not RUNNING. Wait " +
		"until it runs and apply again.",
}

// apiErrorDiag is the diagnostic of an h3db error: a readable text for the
// read replica codes, the EIP errors, or the raw error.
func apiErrorDiag(action string, err error) (string, string) {
	he, ok := client.AsHTTPError(err)
	if !ok {
		return common.EIPErrorDiag(action, err)
	}
	code := he.Code()
	text, ok := replicaErrorText[code]
	if !ok {
		return common.EIPErrorDiag(action, err)
	}
	if code == "SOURCE_HAS_REPLICAS" {
		var b struct {
			Replicas []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"replicas"`
		}
		if json.Unmarshal([]byte(he.Body), &b) == nil && len(b.Replicas) > 0 {
			items := make([]string, 0, len(b.Replicas))
			for _, rep := range b.Replicas {
				items = append(items, fmt.Sprintf("%s (%s)", rep.Name, rep.ID))
			}
			text += " Replicas: " + strings.Join(items, ", ") + "."
		}
	}
	return "Error " + action + ": " + code, text + "\n\n" + err.Error()
}
