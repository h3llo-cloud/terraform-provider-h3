package vm

import (
	"context"
	"fmt"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                   = &VMResource{}
	_ resource.ResourceWithConfigure      = &VMResource{}
	_ resource.ResourceWithImportState    = &VMResource{}
	_ resource.ResourceWithValidateConfig = &VMResource{}
	_ resource.ResourceWithModifyPlan     = &VMResource{}
	_ resource.ResourceWithUpgradeState   = &VMResource{}
)

// Wait limits. Variables so tests can shorten them.
var (
	eipSwapTimeout = 3 * time.Minute
	readyTimeout   = 15 * time.Minute
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
	ID                 types.String `tfsdk:"id"`
	ProjectID          types.String `tfsdk:"project_id"`
	Name               types.String `tfsdk:"name"`
	CPU                types.Int64  `tfsdk:"cpu"`
	Memory             types.String `tfsdk:"memory"`
	DiskSize           types.String `tfsdk:"disk_size"`
	Image              types.String `tfsdk:"image"`
	SSHKeyID           types.String `tfsdk:"ssh_key_id"`
	SSHKeyIDs          types.Set    `tfsdk:"ssh_key_ids"`
	SSHAccessManaged   types.Bool   `tfsdk:"ssh_access_managed"`
	SubnetName         types.String `tfsdk:"subnet_name"`
	WhiteIP            types.Bool   `tfsdk:"white_ip"`
	SourceSnapshotID   types.String `tfsdk:"source_snapshot_id"`
	SourceBackupID     types.String `tfsdk:"source_backup_id"`
	Status             types.String `tfsdk:"status"`
	Endpoint           types.String `tfsdk:"endpoint"`
	EIPID              types.String `tfsdk:"eip_id"`
	ReleaseEIP         types.Bool   `tfsdk:"release_eip_on_destroy"`
	AllowRestart       types.Bool   `tfsdk:"allow_restart_on_resize"`
	InternalIP         types.String `tfsdk:"internal_ip"`
	SubnetID           types.String `tfsdk:"subnet_id"`
	VPCID              types.String `tfsdk:"vpc_id"`
	UserData           types.String `tfsdk:"user_data"`
	UserDataEncoding   types.String `tfsdk:"user_data_encoding"`
	PlacementGroupID   types.String `tfsdk:"placement_group_id"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
}

// Metadata возвращает метаданные ресурса
func (r *VMResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}

// Schema определяет схему ресурса
func (r *VMResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	createOnly := []planmodifier.String{common.RequiresReplaceIfPriorSet()}
	resp.Schema = schema.Schema{
		Version: 1,
		MarkdownDescription: "Manages an h3llo cloud virtual machine.\n\n" +
			"Changed in place: `cpu`, `memory`, `allow_restart_on_resize`, `ssh_key_ids`/`ssh_key_id` (live), " +
			"`ssh_access_managed` (a running VM restarts), `eip_id`, `release_eip_on_destroy`, `deletion_protection` (live). " +
			"An increase of `cpu`/`memory` within the live reserve of a running VM is applied without a restart; " +
			"other CPU/memory changes restart the VM (see `allow_restart_on_resize`). " +
			"Any other argument change **replaces the VM, and its root disk with all data is deleted** — " +
			"keep data on a separate `h3_disk` and consider `lifecycle { prevent_destroy = true }`.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "VM ID",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"project_id": common.ProjectIDAttribute("VM"),
			"name": schema.StringAttribute{
				MarkdownDescription: "VM name (1-63 chars: lowercase letters, digits, `-`). Changing it replaces the VM.",
				Required:            true,
				PlanModifiers:       replace,
			},
			"cpu": schema.Int64Attribute{
				MarkdownDescription: "Number of vCPU. Changed in place. An increase up to the live reserve of the running VM " +
					"(new VMs start with a reserve of 4x their vCPU) is applied **without a restart**; a decrease, an increase above " +
					"the reserve, an image without CPU hotplug or a VM that cannot be live-migrated needs a **restart** " +
					"(allowed by `allow_restart_on_resize`, default true). Platforms without live resize always restart the VM.",
				Required: true,
			},
			"memory": schema.StringAttribute{
				MarkdownDescription: "Memory, Kubernetes quantity (e.g. `4Gi`, `2048Mi`). Changed in place. An increase up to the " +
					"live reserve of the running VM (new VMs start with a reserve of 4x their memory; live changes in multiples of `2Mi`) is " +
					"applied **without a restart**; a decrease or any other change needs a **restart** (allowed by " +
					"`allow_restart_on_resize`, default true). Platforms without live resize always restart the VM.",
				Required: true,
			},
			"disk_size": schema.StringAttribute{
				MarkdownDescription: "Root disk size (e.g. `25Gi`); requires `image`. Defaults to the image minimum. " +
					"Changing it **replaces the VM** — the root disk cannot be resized through the VM API.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					common.RequiresReplaceIfQuantityChanged(),
				},
			},
			"image": schema.StringAttribute{
				MarkdownDescription: "OS image id (e.g. `ubuntu:24.04`). Available ids, their minimums (disk, memory, CPU) and status come from the `h3_vm_images` data source; " +
					"an image missing from it is rejected (400 unknown/not offered, 409 temporarily unavailable). " +
					"A custom image of the project is `custom:<id>` (the `image` attribute of `h3_vm_image`); it needs `disk_size` " +
					"of at least its `size_gi`. Changing it replaces the VM.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"ssh_key_ids": schema.SetAttribute{
				MarkdownDescription: "IDs of your `h3_ssh_key` keys that may log in as `user`. Changed in place, without a restart, " +
					"when SSH access is managed by the platform (`ssh_access_managed`); otherwise the keys are written only on " +
					"the first boot. This is your own grant on the VM (mode `selected`); `[]` revokes it. " +
					"Without the argument Terraform does not change the keys. The current keys are read from the VM's SSH access " +
					"once the provider knows which grant is yours (after it created the VM or changed the keys; after an import, " +
					"the grant made at creation). One key works on platforms without SSH access management; " +
					"two or more need it. Keys of other project members are granted with `h3_vm_ssh_access`.",
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.UseStateForUnknown(),
				},
			},
			"ssh_key_id": schema.StringAttribute{
				MarkdownDescription: "ID of one `h3_ssh_key`. **Deprecated:** use `ssh_key_ids`. Conflicts with `ssh_key_ids`. " +
					"Changed in place (it no longer replaces the VM).",
				DeprecationMessage: "Use ssh_key_ids = [<id>] instead; ssh_key_id is removed in 1.0.",
				Optional:           true,
			},
			"ssh_access_managed": schema.BoolAttribute{
				MarkdownDescription: "The platform keeps `/home/user/.ssh/authorized_keys` in sync with `ssh_key_ids` and " +
					"`h3_vm_ssh_access` grants through qemu-guest-agent. Defaults to `true` on images with the agent; " +
					"null where the platform does not offer SSH access management. " +
					"**Changing it on a running VM restarts the VM.** When it is on, keys added to `authorized_keys` " +
					"by hand inside the VM are removed; keep your own keys in `~/.ssh/authorized_keys2`.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"subnet_name": schema.StringAttribute{
				MarkdownDescription: "Subnet to place the VM in: the `subnet_name` of an `h3_network`. The project default subnet when omitted. Changing it replaces the VM.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"white_ip": schema.BoolAttribute{
				MarkdownDescription: "Give the VM a public IP (default: false). Without `eip_id` a new Elastic IP is allocated in the project. " +
					"Changed in place: `true` attaches `eip_id` (or a newly leased EIP), `false` detaches the current EIP, which stays in " +
					"the project as a free, billed EIP unless `release_eip_on_destroy = true`.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"source_snapshot_id": schema.StringAttribute{
				MarkdownDescription: "Create the VM from this snapshot (UUID). Changing it replaces the VM.",
				Optional:            true,
				PlanModifiers:       createOnly,
			},
			"source_backup_id": schema.StringAttribute{
				MarkdownDescription: "Create the VM from this backup. Changing it replaces the VM.",
				Optional:            true,
				PlanModifiers:       createOnly,
			},
			"eip_id": schema.StringAttribute{
				MarkdownDescription: "ID (UUID) of an existing, free Elastic IP of the project to use as the VM public IP. " +
					"Requires `white_ip = true`. If omitted with `white_ip = true`, a new EIP is allocated and its ID is exported here " +
					"(on platform versions that report it). " +
					"Changing it moves the public IP in place (the previous EIP is detached and stays in the project); " +
					"removing it from the configuration keeps the current EIP. An EIP already attached to or reserved by another resource is rejected (409 `EIP_ALREADY_BOUND`).",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{common.UUIDValidator()},
				PlanModifiers: keep,
			},
			"release_eip_on_destroy": schema.BoolAttribute{
				MarkdownDescription: "Delete the VM's Elastic IP together with the VM (default: false). " +
					"By default destroying the VM keeps its public IP in the project as a free EIP, and it continues to be billed until released.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"allow_restart_on_resize": schema.BoolAttribute{
				MarkdownDescription: "Allow the platform to restart the VM when a `cpu`/`memory` change cannot be applied to the running VM " +
					"(default: true, as before live resize). With `false` such a change fails with `RESIZE_RESTART_REQUIRED` and nothing " +
					"is changed, so an apply never restarts the VM; only changes that need no restart succeed (a live increase within " +
					"the reserve, or any change of a stopped VM, which applies at its next start). " +
					"`false` requires a platform with live resize (the apply fails before any change otherwise). " +
					"When the VM is busy (live migration or another change in progress), the provider retries for a few minutes.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "VM status (`PENDING`, `RUNNING`, ...)",
				Computed:            true,
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "Public IP address when `white_ip` is true, empty otherwise",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"internal_ip": schema.StringAttribute{
				MarkdownDescription: "Private IP address in the project network",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"subnet_id": schema.StringAttribute{
				MarkdownDescription: "ID of the subnet the VM is placed in",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"vpc_id": schema.StringAttribute{
				MarkdownDescription: "ID of the VPC the VM is placed in",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"user_data": schema.StringAttribute{
				MarkdownDescription: "Your cloud-init user-data (up to 64 KiB): a `#cloud-config` YAML document, a `#!` shell script " +
					"or a `#cloud-boothook`. It runs once, on the first boot, after the platform configuration (user `user`, " +
					"SSH keys, console password), which keeps working: your `users`, `runcmd`, `write_files` and other lists are " +
					"appended to the platform ones. Only for a new Linux VM from an image (not with `source_snapshot_id`/" +
					"`source_backup_id`). gzip, MIME multipart and `#include` are not accepted. " +
					"The API never returns user-data, so the provider keeps the configured value in state and does not log it; " +
					"**changing it replaces the VM** (cloud-init would not run it again). After `terraform import` the value is " +
					"unknown: add `lifecycle { ignore_changes = [user_data] }` to keep the imported VM.",
				Optional:      true,
				PlanModifiers: replace,
			},
			"user_data_encoding": schema.StringAttribute{
				MarkdownDescription: "Encoding of `user_data`: `plain` (default) or `base64` (e.g. `base64encode(file(\"init.yaml\"))`). " +
					"Requires `user_data`. Changing it replaces the VM.",
				Optional:      true,
				PlanModifiers: replace,
			},
			"placement_group_id": schema.StringAttribute{
				MarkdownDescription: "ID of an `h3_vm_placement_group` (strategy `spread`: every VM of the group runs on its own host). " +
					"Set at creation only: changing it **replaces the VM**. Removing it from the configuration keeps the VM in its group. " +
					"A full group answers 409 `PLACEMENT_GROUP_FULL`.",
				Optional:   true,
				Computed:   true,
				Validators: []validator.String{common.UUIDValidator()},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"deletion_protection": schema.BoolAttribute{
				MarkdownDescription: "Protect the VM from deletion (default: false). While it is `true`, the API refuses to delete the VM " +
					"(409 `VM_DELETION_PROTECTED`), so `terraform destroy` and a replacement fail: set it to `false` and apply first. " +
					"Changed in place, the VM is not restarted.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
		},
	}
}

// ValidateConfig проверяет аргументы на этапе plan
func (r *VMResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg VMResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateEIPConfig(cfg.EIPID, cfg.WhiteIP)...)
	if !cfg.SSHKeyID.IsNull() && !cfg.SSHKeyIDs.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("ssh_key_id"), "Conflicting SSH key arguments",
			"ssh_key_id and ssh_key_ids are mutually exclusive: move the id into ssh_key_ids.")
	}
	if !cfg.UserDataEncoding.IsNull() && cfg.UserData.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("user_data_encoding"), "user_data_encoding requires user_data",
			"Set user_data, or remove user_data_encoding.")
	}
	if !cfg.UserData.IsNull() && (!cfg.SourceSnapshotID.IsNull() || !cfg.SourceBackupID.IsNull()) {
		resp.Diagnostics.AddAttributeError(path.Root("user_data"), "user_data needs a VM from an image",
			"user_data runs on the first boot of a new VM from an image; a VM restored from a snapshot or a backup does not take it.")
	}
	resp.Diagnostics.Append(validateUserData(cfg.UserData, cfg.UserDataEncoding)...)
	if common.KnownString(cfg.DiskSize) != "" && cfg.Image.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("disk_size"), "disk_size requires image",
			"The API accepts disk_size only together with image.")
	}
	for _, a := range []struct {
		p path.Path
		v types.String
	}{{path.Root("memory"), cfg.Memory}, {path.Root("disk_size"), cfg.DiskSize}} {
		if s := common.KnownString(a.v); s != "" {
			if _, ok := common.QuantityBytes(s); !ok {
				resp.Diagnostics.AddAttributeError(a.p, "Invalid quantity", fmt.Sprintf("%q is not a quantity like \"4Gi\" or \"2048Mi\"", s))
			}
		}
	}
}

// ModifyPlan: the public IP changes when eip_id changes.
func (r *VMResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state VMResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// ssh_key_ids is computed: when only the deprecated ssh_key_id changes, the
	// key set changes too.
	var cfgKeyIDs types.Set
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("ssh_key_ids"), &cfgKeyIDs)...)
	if cfgKeyIDs.IsNull() && !plan.SSHKeyID.IsNull() && !plan.SSHKeyID.Equal(state.SSHKeyID) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("ssh_key_ids"), types.SetUnknown(types.StringType))...)
	}
	if !plan.EIPID.IsUnknown() && !plan.EIPID.Equal(state.EIPID) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("endpoint"), types.StringUnknown())...)
	}
	// white_ip in place: on -> the (new) EIP is known after apply; off -> no EIP.
	if !plan.WhiteIP.IsUnknown() && !state.WhiteIP.IsNull() && plan.WhiteIP.ValueBool() != state.WhiteIP.ValueBool() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("endpoint"), types.StringUnknown())...)
		if !plan.WhiteIP.ValueBool() {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("eip_id"), types.StringNull())...)
		} else {
			var cfgEIP types.String
			resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("eip_id"), &cfgEIP)...)
			if cfgEIP.IsNull() {
				resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("eip_id"), types.StringUnknown())...)
			}
		}
	}
}

// Configure инициализирует ресурс с клиентом
func (r *VMResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

// Create создает новую VM
func (r *VMResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan VMResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := buildCreateRequest(plan)
	var vm VM
	if err := r.client.Do(ctx, "POST", "/api/vms/v1", nil, createReq, &vm); err != nil {
		summary, detail := eipError("creating VM", err)
		resp.Diagnostics.AddError(summary, "Could not create VM: "+detail)
		return
	}
	tflog.Debug(ctx, "VM created", map[string]any{"id": vm.ID, "white_ip": createReq.WhiteIP})

	// Keep the id in state even if waiting fails: the VM is tracked (and
	// tainted) instead of becoming an unmanaged, billed orphan.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), vm.ID)...)

	got, err := r.waitForVMReady(ctx, vm.ID, createReq.WhiteIP, readyTimeout)
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for VM", "VM "+vm.ID+" created but not ready: "+err.Error())
		return
	}
	if createReq.EIPID != "" && got.EIPID == nil {
		got.EIPID = &createReq.EIPID
	}
	plan.applyAPI(got)
	sent := createReq.SSHKeyIDs
	if createReq.SSHKeyID != "" {
		sent = []string{createReq.SSHKeyID}
	}
	caller := r.syncSSH(ctx, &plan, "", sent, &resp.Diagnostics)
	resp.Diagnostics.Append(setSSHCaller(ctx, resp.Private, caller)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read читает текущее состояние VM
func (r *VMResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VMResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vm, err := r.get(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading VM", err.Error())
		return
	}
	state.applyAPI(vm)
	caller, d := getSSHCaller(ctx, req.Private)
	resp.Diagnostics.Append(d...)
	if learned := r.syncSSH(ctx, &state, caller, nil, &resp.Diagnostics); learned != caller {
		resp.Diagnostics.Append(setSSHCaller(ctx, resp.Private, learned)...)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update: cpu/memory through PATCH (live or with a restart, see resize.go), eip_id through h3ovn attach/detach.
// Other arguments are either RequiresReplace or only recorded in state.
func (r *VMResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state VMResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()

	updateReq := UpdateVMRequest{}
	if !plan.CPU.Equal(state.CPU) {
		cpu := int(plan.CPU.ValueInt64())
		updateReq.CPU = &cpu
	}
	if !common.QuantityEqual(plan.Memory.ValueString(), state.Memory.ValueString()) {
		memory := plan.Memory.ValueString()
		updateReq.Memory = &memory
	}
	var cfg VMResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if keys, ok := cfg.sshKeyIDs(ctx); ok {
		if prior, _ := state.sshKeyIDs(ctx); !sameIDs(keys, prior) {
			updateReq.SSHKeyIDs = &keys
			// An explicit ssh_key_ids = [] revokes your own grant even when it was the last key.
			updateReq.AllowEmpty = len(keys) == 0
		}
	}
	if !plan.SSHAccessManaged.IsUnknown() && !plan.SSHAccessManaged.IsNull() && !plan.SSHAccessManaged.Equal(state.SSHAccessManaged) {
		managed := plan.SSHAccessManaged.ValueBool()
		updateReq.SSHAccessManaged = &managed
	}
	// Deletion protection is a separate PATCH: it never restarts the VM and is
	// applied first, so turning it on protects the VM during the other changes.
	if !plan.DeletionProtection.IsUnknown() && !plan.DeletionProtection.IsNull() && !plan.DeletionProtection.Equal(state.DeletionProtection) {
		on := plan.DeletionProtection.ValueBool()
		if err := r.client.Do(ctx, "PATCH", "/api/vms/v1/"+id, nil, UpdateVMRequest{DeletionProtection: &on}, nil); err != nil {
			resp.Diagnostics.AddError("Error updating VM", "Could not change deletion_protection: "+err.Error())
			return
		}
	}
	var resized resizeResult
	if updateReq.CPU != nil || updateReq.Memory != nil {
		// SSH changes, if any, go in the same PATCH.
		allow := plan.AllowRestart.IsNull() || plan.AllowRestart.IsUnknown() || plan.AllowRestart.ValueBool()
		var d diag.Diagnostics
		resized, d = r.resize(ctx, id, updateReq, allow)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
	} else if !updateReq.empty() {
		if err := r.client.Do(ctx, "PATCH", "/api/vms/v1/"+id, nil, updateReq, nil); err != nil {
			resp.Diagnostics.AddError("Error updating VM", "Could not change SSH access: "+err.Error())
			return
		}
	}

	newEIP := common.KnownString(plan.EIPID)
	oldEIP := common.KnownString(state.EIPID)
	wantWhite := plan.WhiteIP.ValueBool()
	whiteChanged := !plan.WhiteIP.IsUnknown() && !state.WhiteIP.IsNull() && wantWhite != state.WhiteIP.ValueBool()
	if whiteChanged {
		var err error
		if wantWhite {
			newEIP, err = r.publicIPOn(ctx, state, newEIP)
		} else {
			err = r.publicIPOff(ctx, state, oldEIP, plan.ReleaseEIP.ValueBool())
			newEIP = ""
		}
		if err != nil {
			summary, detail := eipError("changing white_ip", err)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
	}
	eipChanged := !whiteChanged && newEIP != "" && newEIP != oldEIP
	if eipChanged {
		if summary, detail, ok := r.swapEIP(ctx, id, oldEIP, newEIP, eipSwapTimeout); !ok {
			resp.Diagnostics.AddError(summary, detail)
			return
		}
	}

	var got *VM
	var err error
	changed := updateReq.CPU != nil || updateReq.Memory != nil || !updateReq.empty() || whiteChanged || eipChanged
	if !changed || (resized.Applied == resizeAppliedNextStart && !whiteChanged && !eipChanged) {
		// Nothing that restarts or rewires the VM (deletion protection, arguments
		// kept in state), or a stopped VM whose new size applies at its next
		// start: nothing to wait for.
		got, err = r.get(ctx, id)
	} else {
		got, err = r.waitForVMReady(ctx, id, wantWhite, readyTimeout)
	}
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for VM update", err.Error())
		return
	}
	if whiteChanged && !wantWhite {
		got.Endpoint, got.WhiteIP = "", false
		empty := ""
		got.EIPID = &empty
	}
	if (eipChanged || (whiteChanged && wantWhite)) && (got.EIPID == nil || *got.EIPID == "") {
		got.EIPID = &newEIP
	}
	if whiteChanged {
		plan.WhiteIP = types.BoolValue(wantWhite)
	}
	plan.applyAPI(got)
	caller, d := getSSHCaller(ctx, req.Private)
	resp.Diagnostics.Append(d...)
	var sent []string
	if updateReq.SSHKeyIDs != nil {
		sent = *updateReq.SSHKeyIDs
	}
	if learned := r.syncSSH(ctx, &plan, caller, sent, &resp.Diagnostics); learned != caller {
		resp.Diagnostics.Append(setSSHCaller(ctx, resp.Private, learned)...)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete удаляет VM
func (r *VMResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VMResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.Do(ctx, "DELETE", "/api/vms/v1/"+state.ID.ValueString(), deleteQuery(state.ReleaseEIP.ValueBool()), nil, nil)
	if err != nil && !client.IsNotFound(err) {
		if isDeletionProtected(err) {
			resp.Diagnostics.AddError("VM is protected from deletion",
				"VM "+state.ID.ValueString()+" has deletion protection on (409 VM_DELETION_PROTECTED). "+
					"Set deletion_protection = false, apply, then destroy or replace the VM.")
			return
		}
		resp.Diagnostics.AddError("Error deleting VM", err.Error())
	}
}

// ImportState импортирует существующую VM по ID; Read заполняет все аргументы.
func (r *VMResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// applyAPI maps the API object onto the model. Values the API does not return
// (ssh_key_id, source_*) keep the configured/prior value; SSH access is
// applied separately by applySSH; quantities
// keep the configured spelling when equal; white_ip keeps a known prior value
// because h3vm reports it only after the FIP watcher has run.
func (m *VMResourceModel) applyAPI(vm *VM) {
	m.ID = types.StringValue(vm.ID)
	m.ProjectID = types.StringValue(vm.ProjectID)
	m.Name = types.StringValue(vm.Name)
	m.CPU = types.Int64Value(int64(vm.CPU))
	m.Memory = common.Quantity(m.Memory, vm.Memory)
	m.DiskSize = common.Quantity(m.DiskSize, vm.DiskSize)
	if vm.Image != "" {
		m.Image = types.StringValue(vm.Image)
	} else if m.Image.IsUnknown() {
		m.Image = types.StringNull()
	}
	if s := vm.subnetName(); s != "" {
		m.SubnetName = types.StringValue(s)
	} else if m.SubnetName.IsUnknown() {
		m.SubnetName = types.StringNull()
	}
	if m.WhiteIP.IsNull() || m.WhiteIP.IsUnknown() {
		m.WhiteIP = types.BoolValue(vm.WhiteIP || vm.Endpoint != "")
	}
	m.EIPID = eipIDFromResponse(vm.EIPID, m.EIPID)
	if m.ReleaseEIP.IsNull() || m.ReleaseEIP.IsUnknown() {
		m.ReleaseEIP = types.BoolValue(false) // import / state from 0.1.x
	}
	if m.AllowRestart.IsNull() || m.AllowRestart.IsUnknown() {
		m.AllowRestart = types.BoolValue(true) // import / state before live resize
	}
	m.Status = types.StringValue(vm.Status)
	m.Endpoint = types.StringValue(vm.Endpoint)
	m.InternalIP = common.OptString(vm.NetConfig.InternalIP)
	m.SubnetID = common.OptString(vm.NetConfig.SubnetID)
	m.VPCID = common.OptString(vm.NetConfig.VPCID)
	// user_data and user_data_encoding are write-only: kept as configured.
	m.DeletionProtection = types.BoolValue(vm.DeletionProtection)
	if vm.PlacementGroupID != "" {
		m.PlacementGroupID = types.StringValue(vm.PlacementGroupID)
	} else if m.PlacementGroupID.IsUnknown() || m.PlacementGroupID.IsNull() {
		m.PlacementGroupID = types.StringNull()
	}
}

func (r *VMResource) get(ctx context.Context, id string) (*VM, error) {
	var vm VM
	if err := r.client.Do(ctx, "GET", "/api/vms/v1/"+id, nil, nil, &vm); err != nil {
		return nil, err
	}
	return &vm, nil
}

// waitForVMReady waits until the VM is RUNNING and, when waitForEndpoint is
// true, until it has a public endpoint. It returns the last object read.
func (r *VMResource) waitForVMReady(ctx context.Context, vmID string, waitForEndpoint bool, timeout time.Duration) (*VM, error) {
	var last *VM
	err := common.WaitFor(ctx, timeout, "VM "+vmID+" to become RUNNING", func(ctx context.Context) (bool, error) {
		vm, err := r.get(ctx, vmID)
		if err != nil {
			return false, err
		}
		last = vm
		tflog.Debug(ctx, "waiting for VM", map[string]any{"id": vmID, "status": vm.Status, "endpoint": vm.Endpoint})
		switch vm.Status {
		case "ERROR", "FAILED":
			return false, fmt.Errorf("VM entered %s state", vm.Status)
		case "RUNNING":
			return !waitForEndpoint || vm.Endpoint != "", nil
		}
		return false, nil
	})
	return last, err
}
