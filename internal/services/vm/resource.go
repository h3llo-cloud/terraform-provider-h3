package vm

import (
	"context"
	"fmt"
	"log"
	"time"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &VMResource{}
	_ resource.ResourceWithConfigure   = &VMResource{}
	_ resource.ResourceWithImportState = &VMResource{}
)

// NewVMResource создает новый ресурс VM
func NewVMResource() resource.Resource {
	return &VMResource{}
}

// VMResource - ресурс для управления VM
type VMResource struct {
	client *client.Client
}

// VMResourceModel - модель состояния ресурса
type VMResourceModel struct {
	ID               types.String `tfsdk:"id"`
	ProjectID        types.String `tfsdk:"project_id"`
	Name             types.String `tfsdk:"name"`
	CPU              types.Int64  `tfsdk:"cpu"`
	Memory           types.String `tfsdk:"memory"`
	DiskSize         types.String `tfsdk:"disk_size"`
	Image            types.String `tfsdk:"image"`
	SSHKey           types.String `tfsdk:"ssh_key"`
	SSHKeyID         types.String `tfsdk:"ssh_key_id"`
	SubnetName       types.String `tfsdk:"subnet_name"`
	WhiteIP          types.Bool   `tfsdk:"white_ip"`
	SourceSnapshotID types.String `tfsdk:"source_snapshot_id"`
	SourceBackupID   types.String `tfsdk:"source_backup_id"`
	Status           types.String `tfsdk:"status"`
	Endpoint         types.String `tfsdk:"endpoint"`
}

// Metadata возвращает метаданные ресурса
func (r *VMResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}

// Schema определяет схему ресурса
func (r *VMResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages H3 Cloud virtual machine",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "VM ID",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Project ID (UUID)",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "VM name (1-63 chars, lowercase, alphanumeric)",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cpu": schema.Int64Attribute{
				MarkdownDescription: "Number of CPU cores",
				Required:            true,
			},
			"memory": schema.StringAttribute{
				MarkdownDescription: "Memory size (e.g., 4Gi, 2048Mi)",
				Required:            true,
			},
			"disk_size": schema.StringAttribute{
				MarkdownDescription: "Disk size (e.g., 25Gi)",
				Optional:            true,
				Computed:            true,
			},
			"image": schema.StringAttribute{
				MarkdownDescription: "OS image (e.g., ubuntu:24.04)",
				Optional:            true,
				Computed:            true,
			},
			"ssh_key": schema.StringAttribute{
				MarkdownDescription: "SSH public key (mutually exclusive with ssh_key_id)",
				Optional:            true,
				Sensitive:           true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"ssh_key_id": schema.StringAttribute{
				MarkdownDescription: "SSH key ID from h3ssh service (mutually exclusive with ssh_key)",
				Optional:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"subnet_name": schema.StringAttribute{
				MarkdownDescription: "Subnet name (optional)",
				Optional:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"white_ip": schema.BoolAttribute{
				MarkdownDescription: "Enable public IP (default: false)",
				Optional:            true,
				Computed:            true,
			},
			"source_snapshot_id": schema.StringAttribute{
				MarkdownDescription: "Create VM from snapshot (UUID)",
				Optional:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"source_backup_id": schema.StringAttribute{
				MarkdownDescription: "Create VM from backup",
				Optional:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "VM status (PENDING, RUNNING, etc.)",
				Computed:            true,
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "VM endpoint/IP address",
				Computed:            true,
			},
		},
	}
}

// Configure инициализирует ресурс с клиентом
func (r *VMResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}

	r.client = client
}

// Create создает новую VM
func (r *VMResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan VMResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Validate SSH key fields: either ssh_key or ssh_key_id must be provided, but not both
	hasSSHKey := !plan.SSHKey.IsNull() && plan.SSHKey.ValueString() != ""
	hasSSHKeyID := !plan.SSHKeyID.IsNull() && plan.SSHKeyID.ValueString() != ""

	if !hasSSHKey && !hasSSHKeyID {
		resp.Diagnostics.AddError(
			"Missing SSH Key",
			"Either ssh_key or ssh_key_id must be provided",
		)
		return
	}

	if hasSSHKey && hasSSHKeyID {
		resp.Diagnostics.AddError(
			"Conflicting SSH Key Fields",
			"ssh_key and ssh_key_id are mutually exclusive - provide only one",
		)
		return
	}

	// Формируем запрос
	createReq := CreateVMRequest{
		ProjectID: plan.ProjectID.ValueString(),
		Name:      plan.Name.ValueString(),
		CPU:       int(plan.CPU.ValueInt64()),
		Memory:    plan.Memory.ValueString(),
		WhiteIP:   plan.WhiteIP.ValueBool(),
	}

	if hasSSHKey {
		createReq.SSHKey = plan.SSHKey.ValueString()
	}
	if hasSSHKeyID {
		createReq.SSHKeyID = plan.SSHKeyID.ValueString()
	}

	if !plan.DiskSize.IsNull() {
		createReq.DiskSize = plan.DiskSize.ValueString()
	}
	if !plan.Image.IsNull() {
		createReq.Image = plan.Image.ValueString()
	}
	if !plan.SubnetName.IsNull() {
		createReq.SubnetName = plan.SubnetName.ValueString()
	}
	if !plan.SourceSnapshotID.IsNull() {
		createReq.SourceSnapshotID = plan.SourceSnapshotID.ValueString()
	}
	if !plan.SourceBackupID.IsNull() {
		createReq.SourceBackupID = plan.SourceBackupID.ValueString()
	}

	// Вызываем API (с HMAC подписью автоматически!)
	var vm VM
	err := r.client.Do(ctx, "POST", "/api/vms/v1", nil, createReq, &vm)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating VM",
			"Could not create VM: "+err.Error(),
		)
		return
	}
	log.Printf("[DEBUG] VM created, ID=%s, initial WhiteIP=%v (requested: %v)", vm.ID, vm.WhiteIP, createReq.WhiteIP)

	// Ждем готовности VM (только RUNNING статус, не WhiteIP т.к. backend не обновляет это поле)
	if err := r.waitForVMReady(ctx, vm.ID, false, 10*time.Minute); err != nil {
		resp.Diagnostics.AddError(
			"Error waiting for VM",
			"VM created but not ready: "+err.Error(),
		)
		return
	}
	log.Printf("[DEBUG] VM %s is RUNNING, reading final state...", vm.ID)

	// Читаем финальное состояние
	if err := r.client.Do(ctx, "GET", "/api/vms/v1/"+vm.ID, nil, nil, &vm); err != nil {
		resp.Diagnostics.AddError(
			"Error reading VM after creation",
			err.Error(),
		)
		return
	}
	log.Printf("[DEBUG] VM %s final state: WhiteIP=%v, Endpoint=%s, Status=%s", vm.ID, vm.WhiteIP, vm.Endpoint, vm.Status)

	// Обновляем state
	plan.ID = types.StringValue(vm.ID)
	plan.Status = types.StringValue(vm.Status)
	plan.Endpoint = types.StringValue(vm.Endpoint)

	// backend не обновляет поле white_ip после создания FIP,
	// поэтому сохраняем значение которое было запрошено пользователем
	// (FIP создается корректно даже если white_ip в ответе null/false)
	plan.WhiteIP = types.BoolValue(createReq.WhiteIP)
	log.Printf("[DEBUG] Setting state: WhiteIP=%v (backend returned: %v)", createReq.WhiteIP, vm.WhiteIP)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read читает текущее состояние VM
func (r *VMResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VMResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var vm VM
	err := r.client.Do(ctx, "GET", "/api/vms/v1/"+state.ID.ValueString(), nil, nil, &vm)
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.IsNotFound() {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading VM", err.Error())
		return
	}

	// Debug logging
	log.Printf("DEBUG Read VM: WhiteIP=%v (state has: %v)", vm.WhiteIP, state.WhiteIP.ValueBool())

	state.Status = types.StringValue(vm.Status)
	state.Endpoint = types.StringValue(vm.Endpoint)
	// Backend не обновляет white_ip, оставляем значение из state
	// (если backend вернул white_ip=true, то обновим)
	if vm.WhiteIP {
		state.WhiteIP = types.BoolValue(true)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update обновляет VM (пока не поддерживается)
func (r *VMResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan VMResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state VMResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Формируем запрос обновления
	updateReq := UpdateVMRequest{}

	// Проверяем, изменился ли CPU
	if !plan.CPU.Equal(state.CPU) {
		cpu := int(plan.CPU.ValueInt64())
		updateReq.CPU = &cpu
	}

	// Проверяем, изменилась ли Memory
	if !plan.Memory.Equal(state.Memory) {
		memory := plan.Memory.ValueString()
		updateReq.Memory = &memory
	}

	// Если ничего не изменилось (только ForceNew поля), возвращаем ошибку
	if updateReq.CPU == nil && updateReq.Memory == nil {
		resp.Diagnostics.AddError(
			"Update not supported for these changes",
			"Only CPU and memory can be updated in-place. Other changes require resource replacement.",
		)
		return
	}

	// Вызываем API для обновления
	path := "/api/vms/v1/" + state.ID.ValueString()
	err := r.client.Do(ctx, "PATCH", path, nil, updateReq, nil)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error updating VM",
			"Could not update VM: "+err.Error(),
		)
		return
	}

	// Ждем пока обновление применится (VM может остановиться и запуститься)
	// При Update не ждем WhiteIP, т.к. он не меняется
	if err := r.waitForVMReady(ctx, state.ID.ValueString(), false, 10*time.Minute); err != nil {
		resp.Diagnostics.AddError(
			"Error waiting for VM update",
			"VM update initiated but not completed: "+err.Error(),
		)
		return
	}

	// Читаем финальное состояние
	var vm VM
	if err := r.client.Do(ctx, "GET", "/api/vms/v1/"+state.ID.ValueString(), nil, nil, &vm); err != nil {
		resp.Diagnostics.AddError(
			"Error reading VM after update",
			err.Error(),
		)
		return
	}

	// Конвертируем VM в VMResourceModel
	plan.ID = types.StringValue(vm.ID)
	plan.ProjectID = state.ProjectID
	plan.Name = state.Name
	plan.CPU = types.Int64Value(int64(vm.CPU))
	plan.Memory = types.StringValue(vm.Memory)
	plan.DiskSize = state.DiskSize
	plan.Image = state.Image
	plan.SSHKey = state.SSHKey
	plan.SubnetName = state.SubnetName
	plan.SourceSnapshotID = state.SourceSnapshotID
	plan.SourceBackupID = state.SourceBackupID
	plan.Status = types.StringValue(vm.Status)
	plan.Endpoint = types.StringValue(vm.Endpoint)
	plan.WhiteIP = types.BoolValue(vm.WhiteIP)

	// Обновляем state
	plan.ID = state.ID
	plan.ProjectID = state.ProjectID
	plan.Name = state.Name
	plan.DiskSize = state.DiskSize
	plan.Image = state.Image
	plan.SSHKey = state.SSHKey
	plan.SubnetName = state.SubnetName
	plan.SourceSnapshotID = state.SourceSnapshotID
	plan.SourceBackupID = state.SourceBackupID

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete удаляет VM
func (r *VMResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VMResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	queryParams := map[string]string{
		"preserve_disk": "false",
	}

	err := r.client.Do(ctx, "DELETE", "/api/vms/v1/"+state.ID.ValueString(), queryParams, nil, nil)
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.IsNotFound() {
			// Уже удален - OK
			return
		}
		resp.Diagnostics.AddError("Error deleting VM", err.Error())
		return
	}
}

// ImportState импортирует существующую VM
func (r *VMResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// waitForVMReady ждет пока VM станет RUNNING и опционально пока назначится WhiteIP
func (r *VMResource) waitForVMReady(ctx context.Context, vmID string, waitForWhiteIP bool, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for VM to become ready")
		case <-ticker.C:
			var vm VM
			if err := r.client.Do(ctx, "GET", "/api/vms/v1/"+vmID, nil, nil, &vm); err != nil {
				return err
			}

			log.Printf("[DEBUG] waitForVMReady: VM %s Status=%s, WhiteIP=%v, Endpoint=%s", vmID, vm.Status, vm.WhiteIP, vm.Endpoint)

			if vm.Status == "ERROR" {
				return fmt.Errorf("VM entered ERROR state")
			}

			if vm.Status == "RUNNING" {
				// Если WhiteIP не запрошен, VM готова
				if !waitForWhiteIP {
					log.Printf("[DEBUG] waitForVMReady: VM %s reached RUNNING state", vmID)
					return nil
				}
				// Если WhiteIP запрошен, ждем пока он появится
				if vm.WhiteIP {
					log.Printf("[DEBUG] waitForVMReady: VM %s reached RUNNING state with WhiteIP assigned", vmID)
					return nil
				}
				log.Printf("[DEBUG] waitForVMReady: VM %s is RUNNING but WhiteIP not yet assigned, waiting...", vmID)
			}
		}
	}
}
