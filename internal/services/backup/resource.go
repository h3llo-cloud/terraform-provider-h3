package backup

import (
	"context"
	"fmt"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &BackupResource{}
	_ resource.ResourceWithConfigure   = &BackupResource{}
	_ resource.ResourceWithImportState = &BackupResource{}
)

// readyTimeout: a backup is uploaded to the backup cluster; large disks take long.
var readyTimeout = 60 * time.Minute

const basePath = "/api/disks/v1/backups/"

func NewBackupResource() resource.Resource {
	return &BackupResource{}
}

type BackupResource struct {
	client *client.Client
}

type BackupResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	SnapshotID types.String `tfsdk:"snapshot_id"`
	ProjectID  types.String `tfsdk:"project_id"`
	DiskID     types.String `tfsdk:"disk_id"`
	Status     types.String `tfsdk:"status"`
	Size       types.String `tfsdk:"size"`
	CreatedAt  types.String `tfsdk:"created_at"`
}

func (r *BackupResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_backup"
}

func (r *BackupResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an off-site backup of an `h3_snapshot` (copied to the separate backup storage). " +
			"Apply waits until the upload is `READY` (up to 60 minutes). Every argument change creates a new backup. " +
			"Import with `<project_id>/<backup_id>`.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Backup ID", PlanModifiers: keep},
			"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Backup name", PlanModifiers: replace},
			"snapshot_id": schema.StringAttribute{Required: true, MarkdownDescription: "ID of the snapshot to back up (must be `READY`)", PlanModifiers: replace},
			"project_id":  common.ProjectIDAttribute("backup"),
			"disk_id":     schema.StringAttribute{Computed: true, MarkdownDescription: "Source disk ID, when reported by the API", PlanModifiers: keep},
			"status":      schema.StringAttribute{Computed: true, MarkdownDescription: "Backup status (`PENDING`, `UPLOADING`, `READY`, `FAILED`)", PlanModifiers: keep},
			"size":        schema.StringAttribute{Computed: true, MarkdownDescription: "Backup size", PlanModifiers: keep},
			"created_at":  schema.StringAttribute{Computed: true, MarkdownDescription: "Creation timestamp", PlanModifiers: keep},
		},
	}
}

func (r *BackupResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *BackupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan BackupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var b Backup
	err := r.client.Do(ctx, "POST", "/api/disks/v1/backups", nil, CreateBackupRequest{
		SnapshotID: plan.SnapshotID.ValueString(),
		ProjectID:  plan.ProjectID.ValueString(),
		Name:       plan.Name.ValueString(),
	}, &b)
	if err != nil {
		if httpErr, ok := client.AsHTTPError(err); ok && httpErr.IsConflict() {
			resp.Diagnostics.AddError("Error creating backup",
				"the snapshot is not ready for a backup yet; make sure h3_snapshot has status READY: "+httpErr.Body)
			return
		}
		resp.Diagnostics.AddError("Error creating backup", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), b.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), plan.ProjectID)...)

	got, err := r.waitReady(ctx, b.ID, plan.ProjectID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for backup", "Backup "+b.ID+" created but not ready: "+err.Error())
		return
	}
	plan.applyAPI(got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *BackupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state BackupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.get(ctx, state.ID.ValueString(), state.ProjectID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading backup", err.Error())
		return
	}
	state.applyAPI(b)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called with a change (every argument forces replacement).
func (r *BackupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan BackupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *BackupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state BackupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	q := map[string]string{"project_id": state.ProjectID.ValueString()}
	if err := r.client.Do(ctx, "DELETE", basePath+state.ID.ValueString(), q, nil, nil); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting backup", err.Error())
	}
}

// ImportState: "<project_id>/<backup_id>" (the API needs project_id to read a backup).
func (r *BackupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, id, err := common.ImportProjectScoped(req.ID, "<backup_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func (m *BackupResourceModel) applyAPI(b *Backup) {
	m.ID = types.StringValue(b.ID)
	m.Name = types.StringValue(b.Name)
	if b.SnapshotID != "" {
		m.SnapshotID = types.StringValue(b.SnapshotID)
	}
	if b.ProjectID != "" {
		m.ProjectID = types.StringValue(b.ProjectID)
	}
	if b.DiskID != "" {
		m.DiskID = types.StringValue(b.DiskID)
	} else if m.DiskID.IsUnknown() {
		m.DiskID = types.StringNull()
	}
	m.Status = types.StringValue(b.Status)
	m.Size = types.StringValue(b.Size)
	m.CreatedAt = types.StringValue(b.CreatedAt)
}

func (r *BackupResource) get(ctx context.Context, id, project string) (*Backup, error) {
	var b Backup
	if err := r.client.Do(ctx, "GET", basePath+id, map[string]string{"project_id": project}, nil, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *BackupResource) waitReady(ctx context.Context, id, project string) (*Backup, error) {
	var last *Backup
	err := common.WaitFor(ctx, readyTimeout, "backup "+id+" to become READY", func(ctx context.Context) (bool, error) {
		b, err := r.get(ctx, id, project)
		if err != nil {
			return false, err
		}
		last = b
		switch b.Status {
		case "FAILED":
			return false, fmt.Errorf("backup entered FAILED state")
		case "READY":
			return true, nil
		}
		return false, nil
	})
	return last, err
}

// ModifyPlan fills project_id from the provider default.
func (r *BackupResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
}
