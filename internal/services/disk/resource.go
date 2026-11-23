package disk

import (
	"context"
	"fmt"
	"strings"
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
	_ resource.Resource                   = &DiskResource{}
	_ resource.ResourceWithConfigure      = &DiskResource{}
	_ resource.ResourceWithImportState    = &DiskResource{}
	_ resource.ResourceWithModifyPlan     = &DiskResource{}
	_ resource.ResourceWithValidateConfig = &DiskResource{}
)

var readyTimeout = 10 * time.Minute

const basePath = "/api/disks/v1"

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
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	ProjectID        types.String `tfsdk:"project_id"`
	Size             types.String `tfsdk:"size"`
	StorageClass     types.String `tfsdk:"storage_class"`
	PerformanceClass types.String `tfsdk:"performance_class"`
	Status           types.String `tfsdk:"status"`
	AttachedToVMID   types.String `tfsdk:"attached_to_vm_id"`
	CreatedAt        types.String `tfsdk:"created_at"`
}

// Metadata возвращает метаданные ресурса
func (r *DiskResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_disk"
}

// Schema определяет схему ресурса
func (r *DiskResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a network block disk.\n\n" +
			"`size` grows in place (online, also while attached); shrinking is refused at plan time. " +
			"`performance_class` changes in place. `name`, `project_id` and `storage_class` force a new, empty disk — " +
			"protect disks with data with `lifecycle { prevent_destroy = true }`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Disk ID",
				PlanModifiers:       keep,
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Disk name. Changing it replaces the disk.",
				PlanModifiers:       replace,
			},
			"project_id": common.ProjectIDAttribute("disk"),
			"size": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Disk size, Kubernetes quantity (e.g. `10Gi`). Increased in place without detaching; " +
					"a smaller value is rejected at plan time.",
			},
			"storage_class": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Storage family: `replicated` (network, survives a node failure) or `local` (node NVMe). Changing it replaces the disk.",
				PlanModifiers:       replace,
			},
			"performance_class": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Performance (QoS) class within the storage family, e.g. `R_BASIC`, `R_STANDARD`, `R_FAST`, `R_ULTRA` " +
					"for `replicated` and `L_STANDARD`, `L_FAST` for `local`; IOPS and bandwidth limits grow with the disk size. " +
					"The family default when omitted. Changed in place (no data movement). Available where disk QoS is enabled.",
				PlanModifiers: keep,
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Disk status (`AVAILABLE`, `ATTACHED`, `RESIZING`, ...)",
			},
			"attached_to_vm_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "ID of the VM the disk is attached to; null when detached",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Creation timestamp",
				PlanModifiers:       keep,
			},
		},
	}
}

// Configure инициализирует ресурс с клиентом
func (r *DiskResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *DiskResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var size types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("size"), &size)...)
	if s := common.KnownString(size); s != "" {
		if _, ok := common.QuantityBytes(s); !ok {
			resp.Diagnostics.AddAttributeError(path.Root("size"), "Invalid size", fmt.Sprintf("%q is not a quantity like \"10Gi\"", s))
		}
	}
}

// ModifyPlan refuses a size decrease at plan time (the API answers 500).
func (r *DiskResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state DiskResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || plan.Size.IsUnknown() || state.Size.IsNull() {
		return
	}
	if c, ok := common.QuantityCmp(plan.Size.ValueString(), state.Size.ValueString()); ok && c < 0 {
		resp.Diagnostics.AddAttributeError(path.Root("size"), "Disk size cannot be decreased",
			fmt.Sprintf("current %s, planned %s: a disk can only grow. To shrink, create a new disk and copy the data.",
				state.Size.ValueString(), plan.Size.ValueString()))
	}
}

// Create создает новый диск
func (r *DiskResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DiskResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var disk Disk
	err := r.client.Do(ctx, "POST", basePath, nil, CreateDiskRequest{
		ProjectID:        plan.ProjectID.ValueString(),
		Name:             plan.Name.ValueString(),
		Size:             plan.Size.ValueString(),
		StorageClass:     plan.StorageClass.ValueString(),
		PerformanceClass: common.KnownString(plan.PerformanceClass),
	}, &disk)
	if err != nil {
		resp.Diagnostics.AddError("Error creating disk", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), disk.ID)...)

	got, err := r.waitForDiskReady(ctx, disk.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for disk", "Disk "+disk.ID+" created but not ready: "+err.Error())
		return
	}
	plan.applyAPI(got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read читает текущее состояние диска
func (r *DiskResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DiskResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	disk, err := r.get(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading disk", err.Error())
		return
	}
	state.applyAPI(disk)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update: size grows online, performance_class changes in place.
func (r *DiskResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state DiskResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()

	if !common.QuantityEqual(plan.Size.ValueString(), state.Size.ValueString()) {
		err := r.client.Do(ctx, "POST", basePath+"/resize", nil, ResizeDiskRequest{DiskID: id, NewSize: plan.Size.ValueString()}, nil)
		if err != nil {
			resp.Diagnostics.AddError("Error resizing disk", err.Error())
			return
		}
	}
	if pc := common.KnownString(plan.PerformanceClass); pc != "" && !sameClass(pc, state.PerformanceClass.ValueString()) {
		err := r.client.Do(ctx, "POST", basePath+"/"+id+"/performance-class", nil, PerformanceClassRequest{PerformanceClass: pc}, nil)
		if err != nil {
			resp.Diagnostics.AddError("Error changing performance class", err.Error())
			return
		}
	}

	got, err := r.waitForDiskReady(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for disk update", err.Error())
		return
	}
	plan.applyAPI(got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete удаляет диск
func (r *DiskResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DiskResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Do(ctx, "DELETE", basePath+"/"+state.ID.ValueString(), nil, nil, nil); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting disk", err.Error())
	}
}

// ImportState импортирует существующий диск по ID
func (r *DiskResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// applyAPI maps the API object onto the model (all arguments: drift and import).
func (m *DiskResourceModel) applyAPI(d *Disk) {
	m.ID = types.StringValue(d.ID)
	m.Name = types.StringValue(d.Name)
	m.ProjectID = types.StringValue(d.ProjectID)
	m.Size = common.Quantity(m.Size, d.Size)
	m.StorageClass = types.StringValue(d.StorageClass)
	switch {
	case d.PerformanceClass == "":
		if m.PerformanceClass.IsUnknown() {
			m.PerformanceClass = types.StringNull()
		}
	case m.PerformanceClass.IsNull() || m.PerformanceClass.IsUnknown() || !sameClass(m.PerformanceClass.ValueString(), d.PerformanceClass):
		m.PerformanceClass = types.StringValue(d.PerformanceClass)
	}
	m.Status = types.StringValue(d.Status)
	m.AttachedToVMID = common.OptString(d.AttachedToVMID)
	m.CreatedAt = types.StringValue(d.CreatedAt)
}

// sameClass compares class codes the way h3disks normalizes them (R-FAST == r_fast == R_FAST).
func sameClass(a, b string) bool {
	n := func(s string) string { return strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(s)), "-", "_") }
	return n(a) == n(b)
}

func (r *DiskResource) get(ctx context.Context, id string) (*Disk, error) {
	var d Disk
	if err := r.client.Do(ctx, "GET", basePath+"/"+id, nil, nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *DiskResource) waitForDiskReady(ctx context.Context, id string) (*Disk, error) {
	var last *Disk
	err := common.WaitFor(ctx, readyTimeout, "disk "+id+" to become AVAILABLE", func(ctx context.Context) (bool, error) {
		d, err := r.get(ctx, id)
		if err != nil {
			return false, err
		}
		last = d
		switch d.Status {
		case "FAILED":
			return false, fmt.Errorf("disk entered FAILED state")
		case "AVAILABLE", "ATTACHED":
			return true, nil
		}
		return false, nil
	})
	return last, err
}
