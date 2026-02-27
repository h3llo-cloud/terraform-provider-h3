package backup

import (
	"context"
	"time"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &BackupResource{}
var _ resource.ResourceWithConfigure = &BackupResource{}
var _ resource.ResourceWithImportState = &BackupResource{}

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
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a backup resource",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Backup ID",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Backup name",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"snapshot_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Snapshot ID",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"project_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Project ID",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"disk_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Disk ID",
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Backup status",
			},
			"size": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Backup size",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Creation timestamp",
			},
		},
	}
}

func (r *BackupResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	r.client = req.ProviderData.(*client.Client)
}

func (r *BackupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan BackupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := CreateBackupRequest{
		SnapshotID: plan.SnapshotID.ValueString(),
		ProjectID:  plan.ProjectID.ValueString(),
		Name:       plan.Name.ValueString(),
	}

	var backup Backup
	err := r.client.Do(ctx, "POST", "/api/disks/v1/backups", nil, createReq, &backup)
	if err != nil {
		resp.Diagnostics.AddError("Error creating backup", err.Error())
		return
	}

	time.Sleep(2 * time.Second)

	plan.ID = types.StringValue(backup.ID)
	plan.DiskID = types.StringValue(backup.DiskID)
	plan.Status = types.StringValue(backup.Status)
	plan.Size = types.StringValue(backup.Size)
	plan.CreatedAt = types.StringValue(backup.CreatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *BackupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state BackupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	queryParams := map[string]string{
		"project_id": state.ProjectID.ValueString(),
	}

	var backup Backup
	err := r.client.Do(ctx, "GET", "/api/disks/v1/backups/"+state.ID.ValueString(), queryParams, nil, &backup)
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.IsNotFound() {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading backup", err.Error())
		return
	}

	state.Status = types.StringValue(backup.Status)
	state.Size = types.StringValue(backup.Size)
	state.DiskID = types.StringValue(backup.DiskID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *BackupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan BackupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *BackupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state BackupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.Do(ctx, "DELETE", "/api/disks/v1/backups/"+state.ID.ValueString(), nil, nil, nil)
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.IsNotFound() {
			return
		}
		resp.Diagnostics.AddError("Error deleting backup", err.Error())
	}
}

func (r *BackupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
