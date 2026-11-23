package snapshot

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
	_ resource.Resource                = &SnapshotResource{}
	_ resource.ResourceWithConfigure   = &SnapshotResource{}
	_ resource.ResourceWithImportState = &SnapshotResource{}
)

var readyTimeout = 10 * time.Minute

const basePath = "/api/disks/v1/snapshots/"

// NewSnapshotResource создает новый ресурс Snapshot
func NewSnapshotResource() resource.Resource {
	return &SnapshotResource{}
}

// SnapshotResource - ресурс для управления снапшотами
type SnapshotResource struct {
	client *client.Client
}

// SnapshotResourceModel - модель состояния ресурса
type SnapshotResourceModel struct {
	ID        types.String `tfsdk:"id"`
	DiskID    types.String `tfsdk:"disk_id"`
	Name      types.String `tfsdk:"name"`
	ProjectID types.String `tfsdk:"project_id"`
	Status    types.String `tfsdk:"status"`
	Size      types.String `tfsdk:"size"`
	CreatedAt types.String `tfsdk:"created_at"`
}

func (r *SnapshotResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_snapshot"
}

func (r *SnapshotResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a point-in-time snapshot of an `h3_disk`. A snapshot is immutable: every argument change creates a new one. " +
			"Import with `<project_id>/<snapshot_id>`.",
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true, MarkdownDescription: "Snapshot ID", PlanModifiers: keep},
			"disk_id":    schema.StringAttribute{Required: true, MarkdownDescription: "ID of the disk to snapshot", PlanModifiers: replace},
			"name":       schema.StringAttribute{Required: true, MarkdownDescription: "Snapshot name", PlanModifiers: replace},
			"project_id": common.ProjectIDAttribute("snapshot"),
			"status":     schema.StringAttribute{Computed: true, MarkdownDescription: "Snapshot status (`CREATING`, `READY`, `FAILED`)", PlanModifiers: keep},
			"size":       schema.StringAttribute{Computed: true, MarkdownDescription: "Snapshot size", PlanModifiers: keep},
			"created_at": schema.StringAttribute{Computed: true, MarkdownDescription: "Creation timestamp", PlanModifiers: keep},
		},
	}
}

func (r *SnapshotResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *SnapshotResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SnapshotResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var s Snapshot
	err := r.client.Do(ctx, "POST", "/api/disks/v1/snapshots", nil, CreateSnapshotRequest{
		DiskID:    plan.DiskID.ValueString(),
		ProjectID: plan.ProjectID.ValueString(),
		Name:      plan.Name.ValueString(),
	}, &s)
	if err != nil {
		resp.Diagnostics.AddError("Error creating snapshot", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), s.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), plan.ProjectID)...)

	got, err := r.waitReady(ctx, s.ID, plan.ProjectID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for snapshot", "Snapshot "+s.ID+" created but not ready: "+err.Error())
		return
	}
	plan.applyAPI(got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SnapshotResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SnapshotResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, err := r.get(ctx, state.ID.ValueString(), state.ProjectID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading snapshot", err.Error())
		return
	}
	state.applyAPI(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called with a change (every argument forces replacement).
func (r *SnapshotResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan SnapshotResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SnapshotResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SnapshotResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	q := map[string]string{"project_id": state.ProjectID.ValueString()}
	if err := r.client.Do(ctx, "DELETE", basePath+state.ID.ValueString(), q, nil, nil); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting snapshot", err.Error())
	}
}

// ImportState: "<project_id>/<snapshot_id>" (the API needs project_id to read a snapshot).
func (r *SnapshotResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, id, err := common.ImportProjectScoped(req.ID, "<snapshot_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func (m *SnapshotResourceModel) applyAPI(s *Snapshot) {
	m.ID = types.StringValue(s.ID)
	m.Name = types.StringValue(s.Name)
	if s.DiskID != "" {
		m.DiskID = types.StringValue(s.DiskID)
	}
	if s.ProjectID != "" {
		m.ProjectID = types.StringValue(s.ProjectID)
	}
	m.Status = types.StringValue(s.Status)
	m.Size = types.StringValue(s.Size)
	m.CreatedAt = types.StringValue(s.CreatedAt)
}

func (r *SnapshotResource) get(ctx context.Context, id, project string) (*Snapshot, error) {
	var s Snapshot
	if err := r.client.Do(ctx, "GET", basePath+id, map[string]string{"project_id": project}, nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SnapshotResource) waitReady(ctx context.Context, id, project string) (*Snapshot, error) {
	var last *Snapshot
	err := common.WaitFor(ctx, readyTimeout, "snapshot "+id+" to become READY", func(ctx context.Context) (bool, error) {
		s, err := r.get(ctx, id, project)
		if err != nil {
			return false, err
		}
		last = s
		switch s.Status {
		case "FAILED":
			return false, fmt.Errorf("snapshot entered FAILED state")
		case "READY":
			return true, nil
		}
		return false, nil
	})
	return last, err
}

// ModifyPlan fills project_id from the provider default.
func (r *SnapshotResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
}
