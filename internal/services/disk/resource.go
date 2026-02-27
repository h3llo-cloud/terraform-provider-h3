package disk

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

var _ resource.Resource = &DiskResource{}
var _ resource.ResourceWithConfigure = &DiskResource{}
var _ resource.ResourceWithImportState = &DiskResource{}

// NewDiskResource создает новый ресурс Disk
func NewDiskResource() resource.Resource {
	return &DiskResource{}
}

// DiskResource - ресурс для управления дисками
type DiskResource struct {
	client *client.Client
}

// DiskResourceModel - модель состояния ресурса
type DiskResourceModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	ProjectID      types.String `tfsdk:"project_id"`
	Size           types.String `tfsdk:"size"`
	StorageClass   types.String `tfsdk:"storage_class"`
	Status         types.String `tfsdk:"status"`
	AttachedToVMID types.String `tfsdk:"attached_to_vm_id"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

// Metadata возвращает метаданные ресурса
func (r *DiskResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_disk"
}

// Schema определяет схему ресурса
func (r *DiskResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a disk resource",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Disk ID",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Disk name",
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
			"size": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Disk size (e.g., '10Gi')",
			},
			"storage_class": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Storage class (e.g., 'replicated')",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Disk status",
			},
			"attached_to_vm_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "VM ID if attached",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Creation timestamp",
			},
		},
	}
}

// Configure инициализирует ресурс с клиентом
func (r *DiskResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	r.client = req.ProviderData.(*client.Client)
}

// Create создает новый диск
func (r *DiskResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DiskResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := CreateDiskRequest{
		ProjectID:    plan.ProjectID.ValueString(),
		Name:         plan.Name.ValueString(),
		Size:         plan.Size.ValueString(),
		StorageClass: plan.StorageClass.ValueString(),
	}

	var disk Disk
	err := r.client.Do(ctx, "POST", "/api/disks/v1", nil, createReq, &disk)
	if err != nil {
		resp.Diagnostics.AddError("Error creating disk", err.Error())
		return
	}

	// Wait for AVAILABLE status
	time.Sleep(5 * time.Second)

	// Map to state
	plan.ID = types.StringValue(disk.ID)
	plan.Status = types.StringValue(disk.Status)
	plan.CreatedAt = types.StringValue(disk.CreatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read читает текущее состояние диска
func (r *DiskResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DiskResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var disk Disk
	err := r.client.Do(ctx, "GET", "/api/disks/v1/"+state.ID.ValueString(), nil, nil, &disk)
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.IsNotFound() {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading disk", err.Error())
		return
	}

	state.Status = types.StringValue(disk.Status)
	state.AttachedToVMID = types.StringValue(disk.AttachedToVMID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update обновляет диск (только размер)
func (r *DiskResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state DiskResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Only size can be updated
	if !plan.Size.Equal(state.Size) {
		resizeReq := ResizeDiskRequest{
			DiskID:  state.ID.ValueString(),
			NewSize: plan.Size.ValueString(),
		}

		err := r.client.Do(ctx, "POST", "/api/disks/v1/resize", nil, resizeReq, nil)
		if err != nil {
			resp.Diagnostics.AddError("Error resizing disk", err.Error())
			return
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete удаляет диск
func (r *DiskResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DiskResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.Do(ctx, "DELETE", "/api/disks/v1/"+state.ID.ValueString(), nil, nil, nil)
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.IsNotFound() {
			// Already deleted
			return
		}
		resp.Diagnostics.AddError("Error deleting disk", err.Error())
	}
}

// ImportState импортирует существующий диск
func (r *DiskResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
