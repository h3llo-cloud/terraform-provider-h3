package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &DatabaseResource{}
	_ resource.ResourceWithConfigure      = &DatabaseResource{}
	_ resource.ResourceWithImportState    = &DatabaseResource{}
	_ resource.ResourceWithModifyPlan     = &DatabaseResource{}
	_ resource.ResourceWithValidateConfig = &DatabaseResource{}
)

// Polling and wait limits. Variables so tests can shorten them.
var (
	pollInterval    = 10 * time.Second
	createTimeout   = 20 * time.Minute
	resizeTimeout   = 15 * time.Minute
	scaleTimeout    = 20 * time.Minute
	settingsTimeout = 15 * time.Minute
	deleteTimeout   = 10 * time.Minute
	attachIPTimeout = 5 * time.Minute
)

func NewDatabaseResource() resource.Resource { return &DatabaseResource{} }

// DatabaseResource manages an h3db database (PostgreSQL or MariaDB).
type DatabaseResource struct {
	client *client.Client
}

type DatabaseResourceModel struct {
	ID            types.String `tfsdk:"id"`
	ProjectID     types.String `tfsdk:"project_id"`
	Name          types.String `tfsdk:"name"`
	Engine        types.String `tfsdk:"engine"`
	Version       types.String `tfsdk:"version"`
	CPU           types.Int64  `tfsdk:"cpu"`
	Memory        types.String `tfsdk:"memory"`
	Size          types.String `tfsdk:"size"`
	WhiteIP       types.Bool   `tfsdk:"white_ip"`
	SubnetName    types.String `tfsdk:"subnet_name"`
	WaitForResize types.Bool   `tfsdk:"wait_for_resize"`
	Status        types.String `tfsdk:"status"`
	Endpoint      types.String `tfsdk:"endpoint"`
	InternalIP    types.String `tfsdk:"internal_ip"`
	ActualSize    types.String `tfsdk:"actual_size"`
	StorageStatus types.String `tfsdk:"storage_status"`
	ComputeStatus types.String `tfsdk:"compute_status"`
	Username      types.String `tfsdk:"username"`
	Password      types.String `tfsdk:"password"`
	EIPID         types.String `tfsdk:"eip_id"`
	ReleaseEIP    types.Bool   `tfsdk:"release_eip_on_destroy"`
	MaxConn       types.Int64  `tfsdk:"max_connections"`
	CurrentSize   types.String `tfsdk:"current_size"`

	StorageAutoscale types.Object `tfsdk:"storage_autoscale"`

	// Read replicas (replica.go).
	ReplicaOf        types.String `tfsdk:"replica_of"`
	ReplicaSettings  types.Object `tfsdk:"replica_settings"`
	Promote          types.Bool   `tfsdk:"promote"`
	ReplicationState types.String `tfsdk:"replication_state"`
	ReplicationLag   types.Int64  `tfsdk:"replication_lag_seconds"`
	PromotedFrom     types.String `tfsdk:"promoted_from"`
}

func (r *DatabaseResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database"
}

func (r *DatabaseResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keepComputed := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a managed database (PostgreSQL or MariaDB) in h3db. " +
			"`size` can be increased in place (online, no restart). `cpu`, `memory` and `max_connections` change in place, " +
			"with a restart of the database. `white_ip`, `eip_id` and `release_eip_on_destroy` " +
			"change in place; every other argument forces a new database. Storage cannot be decreased. " +
			"With `storage_autoscale` the platform grows the disk itself; a disk larger than `size` is then not a diff. " +
			"Destroying the database keeps its Elastic IP in the project (billed) unless `release_eip_on_destroy` is true. " +
			"With `replica_of` the database is a PostgreSQL read replica of another database; `promote = true` makes it standalone.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Database ID", PlanModifiers: keepComputed,
			},
			"project_id": common.ProjectIDAttribute("database"),
			"name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Database name (lowercase letters, digits and '-', max 63)", PlanModifiers: replace,
			},
			"engine": schema.StringAttribute{
				Required: true, MarkdownDescription: "Engine: `postgres` or `mariadb`", PlanModifiers: replace,
			},
			"version": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Engine version (e.g. `17.5`, `11.8`); the service default when omitted",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"cpu": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "vCPU (1-256). Changed in place (up or down); the database restarts.",
			},
			"memory": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Memory, Gi quantity (e.g. `4Gi`); 1-16 Gi per vCPU. " +
					"Changed in place (up or down); the database restarts.",
			},
			"size": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Storage size, Gi quantity (e.g. `20Gi`, max `1024Gi`). " +
					"Can be increased in place; a smaller value is rejected at plan time. With `storage_autoscale` enabled " +
					"this is the configured (minimum) size: the disk may be larger, see `current_size`.",
			},
			"storage_autoscale": storageAutoscaleAttribute(),
			"current_size": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Storage size the database has now (h3db `sizeGb`). Larger than `size` after an " +
					"automatic grow by `storage_autoscale`.",
			},
			"max_connections": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "PostgreSQL `max_connections` (20-5000; h3db also caps it at 100 per Gi of `memory`). " +
					"The service default when omitted. Changed in place; **the database restarts** (one instance: " +
					"PostgreSQL restarts in place, about a minute without new connections; with replicas: a switchover, seconds). " +
					"With a connection pooler it must be at least pooler instances × `max_db_connections` + 10.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"white_ip": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Expose the database on a public IP. Changed in place. Without `eip_id` " +
					"a new Elastic IP is leased in the project (billed).",
			},
			"eip_id": schema.StringAttribute{
				Optional: true, Computed: true,
				Validators: []validator.String{common.UUIDValidator()},
				MarkdownDescription: "ID (UUID) of an existing, free Elastic IP of the project to use as the public IP. " +
					"Requires `white_ip = true`. When omitted with `white_ip = true`, the ID of the leased EIP is exported here. " +
					"Changing it moves the public address in place (the previous EIP stays in the project); " +
					"removing it from the configuration keeps the current EIP.",
			},
			"release_eip_on_destroy": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Delete the database's Elastic IP together with the database (default: false). " +
					"By default destroying the database keeps the EIP in the project, where it is billed until released.",
			},
			"subnet_name": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Subnet to place the database in; the project default subnet when omitted",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"wait_for_resize": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "Wait (up to 15 min) until the volume has actually grown after a size increase. " +
					"When false, apply returns once the resize is accepted (`storage_status` = `RESIZING`).",
			},
			"status": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Database status (`RUNNING`, `CREATING`, ...)",
			},
			"endpoint": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Public endpoint when `white_ip` is true",
			},
			"internal_ip": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Address inside the project network", PlanModifiers: keepComputed,
			},
			"actual_size": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Current volume capacity reported by the cluster",
			},
			"storage_status": schema.StringAttribute{
				Computed: true, MarkdownDescription: "`READY`, or `RESIZING` while a size increase is in progress",
			},
			"compute_status": schema.StringAttribute{
				Computed: true, MarkdownDescription: "`READY`, or `SCALING` while a cpu/memory change is being applied",
			},
			"username": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Client user", PlanModifiers: keepComputed,
			},
			"password": schema.StringAttribute{
				Computed: true, Sensitive: true, MarkdownDescription: "Client password", PlanModifiers: keepComputed,
			},
		},
	}
	for k, v := range replicaAttributes() {
		resp.Schema.Attributes[k] = v
	}
	resp.Schema.Blocks = map[string]schema.Block{"replica_settings": replicaSettingsBlock()}
}

func (r *DatabaseResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *DatabaseResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg DatabaseResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cfg.Engine.IsUnknown() && !cfg.Engine.IsNull() {
		if e := cfg.Engine.ValueString(); e != "postgres" && e != "mariadb" {
			resp.Diagnostics.AddAttributeError(path.Root("engine"), "Invalid engine", "engine must be \"postgres\" or \"mariadb\"")
		}
	}
	if !cfg.Size.IsUnknown() && !cfg.Size.IsNull() {
		if err := validateSize(cfg.Size.ValueString()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("size"), "Invalid size", err.Error())
		}
	}
	if !cfg.Memory.IsUnknown() && !cfg.Memory.IsNull() {
		if _, err := parseGi(cfg.Memory.ValueString()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("memory"), "Invalid memory", err.Error())
			return
		}
		if !cfg.CPU.IsUnknown() && !cfg.CPU.IsNull() {
			if err := validateShape(cfg.CPU.ValueInt64(), cfg.Memory.ValueString()); err != nil {
				resp.Diagnostics.AddAttributeError(path.Root("memory"), "Invalid cpu/memory shape", err.Error())
			}
		}
	}
	if !cfg.MaxConn.IsUnknown() && !cfg.MaxConn.IsNull() {
		engine := ""
		if !cfg.Engine.IsUnknown() && !cfg.Engine.IsNull() {
			engine = cfg.Engine.ValueString()
		}
		if err := validateMaxConnections(cfg.MaxConn.ValueInt64(), engine); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("max_connections"), "Invalid max_connections", err.Error())
		}
	}
	if err := validateEIP(cfg.EIPID, cfg.WhiteIP); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("eip_id"), "eip_id requires white_ip = true", err.Error())
	}
	as, diags := autoscaleFromObject(ctx, cfg.StorageAutoscale)
	resp.Diagnostics.Append(diags...)
	if as != nil {
		var sizeGi int64
		if known(cfg.Size) {
			sizeGi, _ = parseGi(cfg.Size.ValueString())
		}
		for _, fe := range validateAutoscale(*as, sizeGi) {
			resp.Diagnostics.AddAttributeError(autoscalePath(fe.attr), "Invalid storage_autoscale", fe.msg)
		}
	}
	validateReplicaConfig(ctx, &cfg, &resp.Diagnostics)
}

// ModifyPlan refuses a size decrease at plan time instead of failing in apply.
// It also plans eip_id when it is not configured (see planEIPID).
func (r *DatabaseResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan DatabaseResourceModel
	var cfgEIP types.String
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("eip_id"), &cfgEIP)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state *DatabaseResourceModel
	if !req.State.Raw.IsNull() {
		state = &DatabaseResourceModel{}
		resp.Diagnostics.Append(req.State.Get(ctx, state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	r.modifyReplicaPlan(ctx, req, resp, &plan, state)
	if eip := planEIPID(cfgEIP, plan.WhiteIP, state); !eip.Equal(plan.EIPID) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("eip_id"), eip)...)
	}
	// Checked only when configured: an omitted storage_autoscale keeps
	// whatever the platform has (a disk resized above its max_size_gb only
	// stops the automatic growth).
	var cfgAS types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("storage_autoscale"), &cfgAS)...)
	as, diags := autoscaleFromObject(ctx, plan.StorageAutoscale)
	resp.Diagnostics.Append(diags...)
	if !cfgAS.IsNull() && as != nil && known(as.Enabled) && as.Enabled.ValueBool() {
		var current types.String
		if state != nil {
			current = state.CurrentSize
		}
		if !known(as.MaxSizeGB) {
			// Enabled without max_size_gb and nothing in state: 2 × size.
			if max := planMaxSize(plan.Size, current); known(max) {
				resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, autoscalePath("max_size_gb"), max)...)
			}
		} else if err := checkMaxSize(as.MaxSizeGB, current); err != nil {
			resp.Diagnostics.AddAttributeError(autoscalePath("max_size_gb"), "Invalid storage_autoscale", err.Error())
		}
	}
	if state == nil || plan.Size.IsUnknown() || state.Size.IsNull() {
		return
	}
	if err := checkResize(state.Size.ValueString(), plan.Size.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("size"), "Database storage cannot be decreased", err.Error())
	}
}

func (r *DatabaseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DatabaseResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	eipID := ""
	if plan.WhiteIP.ValueBool() {
		eipID = common.KnownString(plan.EIPID)
	}
	wantMaxConn := common.KnownInt64(plan.MaxConn)
	var pgConfig *EngineConfig
	if wantMaxConn > 0 && wantMaxConn <= createMaxConnectionsLimit {
		pgConfig = &EngineConfig{MaxConnections: wantMaxConn}
	}
	planAS, diags := autoscaleFromObject(ctx, plan.StorageAutoscale)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	var db Database
	var err error
	if src := common.KnownString(plan.ReplicaOf); src != "" {
		var settings *ReplicaSettingsRequest
		rs, diags := replicaSettingsFromObject(ctx, plan.ReplicaSettings)
		resp.Diagnostics.Append(diags...)
		if rs != nil {
			settings = rs.request()
		}
		err = r.client.Do(ctx, "POST", basePath+"/"+src+"/replicas", nil, CreateReplicaRequest{
			Name:             plan.Name.ValueString(),
			CPU:              plan.CPU.ValueInt64(),
			Memory:           plan.Memory.ValueString(),
			SizeGB:           plan.Size.ValueString(),
			MaxConnections:   wantMaxConn,
			WhiteIP:          plan.WhiteIP.ValueBool(),
			EIPID:            eipID,
			Settings:         settings,
			StorageAutoscale: createAutoscale(planAS),
		}, &db)
	} else {
		err = r.client.Do(ctx, "POST", basePath, nil, CreateRequest{
			ProjectID:        plan.ProjectID.ValueString(),
			Name:             plan.Name.ValueString(),
			Engine:           plan.Engine.ValueString(),
			Version:          plan.Version.ValueString(),
			CPU:              plan.CPU.ValueInt64(),
			SizeGB:           plan.Size.ValueString(),
			Memory:           plan.Memory.ValueString(),
			WhiteIP:          plan.WhiteIP.ValueBool(),
			SubnetName:       plan.SubnetName.ValueString(),
			EIPID:            eipID,
			PostgresConfig:   pgConfig,
			StorageAutoscale: createAutoscale(planAS),
		}, &db)
	}
	if err != nil {
		if planAS != nil && planAS.Enabled.ValueBool() && isAutoscaleDisabled(err) {
			resp.Diagnostics.AddError("Error creating database", autoscaleUnavailable(err).Error())
			return
		}
		resp.Diagnostics.AddError(apiErrorDiag("creating database", err))
		return
	}

	// Keep the id in state even if waiting fails, so the database is tracked
	// (and tainted) instead of leaking.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), db.ID)...)

	got, err := r.waitFor(ctx, db.ID, createTimeout, func(d *Database) (bool, error) {
		if d.Status == StatusFailed {
			return false, errors.New("database status is FAILED")
		}
		return d.Status == StatusRunning, nil
	})
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for database to become RUNNING", err.Error())
		return
	}
	// Above the create bound: applied once the database runs (one restart
	// of the empty database).
	if wantMaxConn > 0 && got.maxConnections() != wantMaxConn {
		if err := r.setMaxConnections(ctx, db.ID, wantMaxConn, resp.Diagnostics.AddWarning); err != nil {
			resp.Diagnostics.AddError("Error setting max_connections", err.Error())
			return
		}
		if got, err = r.get(ctx, db.ID); err != nil {
			resp.Diagnostics.AddError("Error reading database", err.Error())
			return
		}
	}
	planned := plan.Size
	plan.applyAPI(got)
	_, diags = r.readAutoscale(ctx, &plan, resp.Diagnostics.AddWarning)
	resp.Diagnostics.Append(diags...)
	plan.Size = keepConfiguredSize(planned, got.SizeGB, true)
	r.readCredentials(ctx, &plan, resp.Diagnostics.AddWarning)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *DatabaseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DatabaseResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	db, err := r.get(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading database", err.Error())
		return
	}
	prior := state.Size
	state.applyAPI(db)
	as, diags := r.readAutoscale(ctx, &state, resp.Diagnostics.AddWarning)
	resp.Diagnostics.Append(diags...)
	cur, diags := autoscaleFromObject(ctx, state.StorageAutoscale)
	resp.Diagnostics.Append(diags...)
	// A replica grows with its source (h3db, decision В1): not a diff either.
	state.Size = keepConfiguredSize(prior, db.SizeGB, autoscaleGrows(as, cur) || db.ReplicaOf != "")
	resp.Diagnostics.Append(r.readReplicaSettings(ctx, &state, db, resp.Diagnostics.AddWarning)...)
	if state.WaitForResize.IsNull() || state.WaitForResize.IsUnknown() {
		state.WaitForResize = types.BoolValue(true) // after import
	}
	if state.ReleaseEIP.IsNull() || state.ReleaseEIP.IsUnknown() {
		state.ReleaseEIP = types.BoolValue(false) // after import / older state
	}
	r.readCredentials(ctx, &state, resp.Diagnostics.AddWarning)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *DatabaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state DatabaseResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()

	if !plan.Size.Equal(state.Size) && !r.alreadyAtLeast(ctx, id, plan.Size.ValueString()) {
		if err := r.client.Do(ctx, "POST", basePath+"/"+id+"/resize", nil, ResizeRequest{SizeGB: plan.Size.ValueString()}, nil); err != nil {
			resp.Diagnostics.AddError(apiErrorDiag("increasing database storage", err))
			return
		}
		if plan.WaitForResize.ValueBool() {
			if _, err := r.waitFor(ctx, id, resizeTimeout, func(d *Database) (bool, error) {
				return d.StorageStatus != StorageResizing, nil
			}); err != nil {
				resp.Diagnostics.AddWarning("Storage resize accepted but not finished",
					fmt.Sprintf("The new size %s is applied and billed; the volume is still growing (%v). "+
						"Check storage_status later; a stuck resize is an operator alert, not a Terraform error.",
						plan.Size.ValueString(), err))
			}
		}
	}

	// After the resize: h3db refuses a scale (409) while storage is RESIZING.
	if !plan.CPU.Equal(state.CPU) || !plan.Memory.Equal(state.Memory) {
		if err := r.scale(ctx, id, plan.CPU.ValueInt64(), plan.Memory.ValueString()); err != nil {
			resp.Diagnostics.AddError(apiErrorDiag("changing database cpu/memory", err))
			return
		}
	}

	// After the scale: the max_connections bound follows the memory.
	if want := common.KnownInt64(plan.MaxConn); want > 0 && !plan.MaxConn.Equal(state.MaxConn) {
		if err := r.setMaxConnections(ctx, id, want, resp.Diagnostics.AddWarning); err != nil {
			resp.Diagnostics.AddError(apiErrorDiag("changing max_connections", err))
			return
		}
	}

	for _, step := range whiteIPSteps(state.WhiteIP.ValueBool(), plan.WhiteIP.ValueBool(),
		common.KnownString(state.EIPID), common.KnownString(plan.EIPID)) {
		var body any
		if step.EIPID != "" {
			body = WhiteIPAttachRequest{EIPID: step.EIPID}
		}
		ctxIP, cancel := context.WithTimeout(ctx, attachIPTimeout)
		err := r.client.Do(ctxIP, "POST", basePath+"/"+id+"/whiteip/"+step.Action, nil, body, nil)
		cancel()
		if err != nil {
			summary, detail := common.EIPErrorDiag("changing white_ip/eip_id ("+step.Action+")", err)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
	}

	// After the resize: maxSizeGb must be at least the current size.
	planAS, diags := autoscaleFromObject(ctx, plan.StorageAutoscale)
	resp.Diagnostics.Append(diags...)
	stateAS, diags := autoscaleFromObject(ctx, state.StorageAutoscale)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if autoscaleChanged(planAS, stateAS) {
		if _, err := r.putAutoscale(ctx, id, planAS); err != nil {
			resp.Diagnostics.AddError(apiErrorDiag("changing storage_autoscale", err))
			return
		}
	}

	// replica_settings: reload, no restart. Removing the block keeps them.
	if !plan.ReplicaSettings.IsNull() && !plan.ReplicaSettings.Equal(state.ReplicaSettings) && state.isReplica() {
		rs, diags := replicaSettingsFromObject(ctx, plan.ReplicaSettings)
		resp.Diagnostics.Append(diags...)
		if rs != nil {
			if err := r.client.Do(ctx, "PATCH", basePath+"/"+id+"/settings", nil, rs.request(), nil); err != nil {
				resp.Diagnostics.AddError(apiErrorDiag("changing replica_settings", err))
				return
			}
		}
	}

	// Last: the promotion. On error the prior state is kept, so the next
	// apply checks (and, if needed, repeats) it.
	if plan.Promote.ValueBool() && state.isReplica() {
		if err := r.promote(ctx, id); err != nil {
			resp.Diagnostics.AddError(apiErrorDiag("promoting the read replica", err))
			return
		}
	}

	db, err := r.get(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Error reading database after update", err.Error())
		return
	}
	planned := plan.Size
	plan.applyAPI(db)
	// Not configured (the plan keeps the state): left as planned, Read
	// refreshes it; otherwise read back what h3db applied.
	if !plan.StorageAutoscale.Equal(state.StorageAutoscale) {
		_, diags = r.readAutoscale(ctx, &plan, resp.Diagnostics.AddWarning)
		resp.Diagnostics.Append(diags...)
	}
	plan.Size = keepConfiguredSize(planned, db.SizeGB, true)
	plan.Username, plan.Password = state.Username, state.Password
	r.readCredentials(ctx, &plan, resp.Diagnostics.AddWarning)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *DatabaseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DatabaseResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.client.Do(ctx, "DELETE", basePath+"/"+id, deleteQuery(state.ReleaseEIP.ValueBool()), nil, nil); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError(apiErrorDiag("deleting database", err))
		return
	}
	if err := r.waitGone(ctx, id); err != nil {
		resp.Diagnostics.AddError("Error waiting for database deletion", err.Error())
	}
}

func (r *DatabaseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// applyAPI copies the API object into the model (all arguments, for import).
func (m *DatabaseResourceModel) applyAPI(db *Database) {
	m.ID = types.StringValue(db.ID)
	m.ProjectID = types.StringValue(db.ProjectID)
	m.Name = types.StringValue(db.Name)
	m.Engine = types.StringValue(db.Engine)
	m.Version = types.StringValue(db.Version)
	m.CPU = types.Int64Value(db.CPU)
	m.Memory = types.StringValue(db.Memory)
	m.Size = types.StringValue(db.SizeGB)
	m.CurrentSize = types.StringValue(db.SizeGB)
	m.WhiteIP = types.BoolValue(db.WhiteIP)
	m.SubnetName = types.StringValue(db.NetConfig.SubnetName)
	m.EIPID = eipFromAPI(db, m.EIPID)
	m.Status = types.StringValue(db.Status)
	m.Endpoint = types.StringValue(db.Endpoint)
	m.InternalIP = types.StringValue(db.InternalIP)
	m.ActualSize = types.StringValue(db.ActualSizeGB)
	m.StorageStatus = types.StringValue(db.StorageStatus)
	m.ComputeStatus = types.StringValue(db.ComputeStatus)
	m.applyReplication(db)
	if n := db.maxConnections(); n > 0 {
		m.MaxConn = types.Int64Value(n)
	} else if m.MaxConn.IsUnknown() {
		m.MaxConn = types.Int64Null()
	}
}

// setMaxConnections changes max_connections (PUT /settings, PostgreSQL
// restarts) and waits until the instances run with it. A 409 (resize or
// scale in progress) is retried after that operation. Not finishing within
// settingsTimeout is a warning: the value is accepted and CNPG keeps
// applying it.
func (r *DatabaseResource) setMaxConnections(ctx context.Context, id string, n int64, warn func(string, string)) error {
	ctx, cancel := context.WithTimeout(ctx, settingsTimeout)
	defer cancel()
	for {
		err := r.client.Do(ctx, "PUT", basePath+"/"+id+"/settings", nil, SettingsRequest{MaxConnections: n}, nil)
		if err == nil {
			break
		}
		var he *client.HTTPError
		if !errors.As(err, &he) || !he.IsConflict() || he.Code() == "POOLER_LIMITS" {
			if isNotFound(err) {
				return fmt.Errorf("%w (an h3db without PUT /settings also answers 404)", err)
			}
			return err
		}
		if _, werr := r.waitFor(ctx, id, settingsTimeout, func(d *Database) (bool, error) {
			return d.StorageStatus != StorageResizing && d.ComputeStatus != ComputeScaling, nil
		}); werr != nil {
			return fmt.Errorf("another storage resize or scale is still in progress: %w", werr)
		}
	}
	for {
		var st Settings
		err := r.client.Do(ctx, "GET", basePath+"/"+id+"/settings", nil, nil, &st)
		if err == nil && !st.PendingRestart && (st.RunningMaxConnections == 0 || st.RunningMaxConnections == n) {
			return nil
		}
		select {
		case <-ctx.Done():
			warn("max_connections accepted but the restart is not finished",
				fmt.Sprintf("max_connections=%d is written; PostgreSQL has not restarted with it within %s. "+
					"Check GET /settings (pendingRestart); a stuck restart is an operator alert.", n, settingsTimeout))
			return nil
		case <-time.After(pollInterval):
		}
	}
}

// scale applies a new cpu/memory and waits until h3db reports the database
// RUNNING with computeStatus READY and the new shape. A 409 (storage resize
// or another scale in progress) is retried after that operation finishes.
// The whole operation is bounded by scaleTimeout. On error the prior state is
// kept, so the next apply repeats the (idempotent) request.
func (r *DatabaseResource) scale(ctx context.Context, id string, cpu int64, memory string) error {
	ctx, cancel := context.WithTimeout(ctx, scaleTimeout)
	defer cancel()
	for {
		err := r.client.Do(ctx, "POST", basePath+"/"+id+"/scale", nil, ScaleRequest{CPU: cpu, Memory: memory}, nil)
		if err == nil {
			break
		}
		var he *client.HTTPError
		if !errors.As(err, &he) || !he.IsConflict() {
			if isNotFound(err) {
				return fmt.Errorf("%w (an h3db without POST /scale also answers 404)", err)
			}
			return err
		}
		// Another resize/scale is running: wait for it, then retry.
		if _, werr := r.waitFor(ctx, id, scaleTimeout, func(d *Database) (bool, error) {
			return d.StorageStatus != StorageResizing && d.ComputeStatus != ComputeScaling, nil
		}); werr != nil {
			return fmt.Errorf("another storage resize or scale is still in progress: %w", werr)
		}
	}
	_, err := r.waitFor(ctx, id, scaleTimeout, func(d *Database) (bool, error) {
		if d.Status == StatusFailed {
			return false, errors.New("database status is FAILED")
		}
		return d.Status == StatusRunning && d.ComputeStatus != ComputeScaling &&
			d.CPU == cpu && d.Memory == memory, nil
	})
	if err != nil {
		return fmt.Errorf("cpu=%d memory=%s accepted (billed from now) but not applied yet: %w; "+
			"a stuck scale is an operator alert, the next apply repeats the request", cpu, memory, err)
	}
	return nil
}

// alreadyAtLeast reports that the disk is already at least the planned size
// (storage autoscale grew it): no resize request is needed, and h3db would
// refuse a smaller one.
func (r *DatabaseResource) alreadyAtLeast(ctx context.Context, id, planned string) bool {
	db, err := r.get(ctx, id)
	if err != nil {
		return false
	}
	cur, err1 := parseGi(db.SizeGB)
	want, err2 := parseGi(planned)
	return err1 == nil && err2 == nil && cur >= want
}

func (r *DatabaseResource) get(ctx context.Context, id string) (*Database, error) {
	var db Database
	if err := r.client.Do(ctx, "GET", basePath+"/"+id, nil, nil, &db); err != nil {
		return nil, err
	}
	return &db, nil
}

// readCredentials refreshes username/password; a failure keeps the previous
// values and is reported as a warning (credentials are not an argument).
func (r *DatabaseResource) readCredentials(ctx context.Context, m *DatabaseResourceModel, warn func(string, string)) {
	var c Credentials
	if err := r.client.Do(ctx, "GET", basePath+"/"+m.ID.ValueString()+"/credentials", nil, nil, &c); err != nil {
		warn("Could not read database credentials", err.Error())
		if m.Username.IsUnknown() {
			m.Username = types.StringNull()
		}
		if m.Password.IsUnknown() {
			m.Password = types.StringNull()
		}
		return
	}
	m.Username = types.StringValue(c.Username)
	m.Password = types.StringValue(c.Password)
}

func (r *DatabaseResource) waitFor(ctx context.Context, id string, timeout time.Duration, done func(*Database) (bool, error)) (*Database, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		db, err := r.get(ctx, id)
		if err != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, err
		}
		if db != nil {
			ok, derr := done(db)
			if derr != nil {
				return db, derr
			}
			if ok {
				return db, nil
			}
		}
		select {
		case <-ctx.Done():
			return db, fmt.Errorf("timed out after %s", timeout)
		case <-time.After(pollInterval):
		}
	}
}

func (r *DatabaseResource) waitGone(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()
	for {
		if _, err := r.get(ctx, id); isNotFound(err) {
			return nil
		} else if err != nil && ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("database %s still exists after %s", id, deleteTimeout)
		case <-time.After(pollInterval):
		}
	}
}

func isNotFound(err error) bool {
	var he *client.HTTPError
	return errors.As(err, &he) && he.IsNotFound()
}
