package vm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Custom images (h3vm TRD 08): GET/POST /api/vms/v1/images/custom,
// GET/DELETE /api/vms/v1/images/custom/{id}?project_id=. An image is imported
// over HTTPS or copied from the root disk of a stopped VM; a VM uses it with
// image = "custom:<id>".

const customImagesBase = "/api/vms/v1/images/custom"

// Custom image statuses.
const (
	imageImporting = "IMPORTING"
	imageReady     = "READY"
	imageFailed    = "FAILED"
	imageDeleting  = "DELETING"
)

// Default waits. Variables so tests can shorten them.
var (
	imageCreateTimeout = 60 * time.Minute
	imageDeleteTimeout = 10 * time.Minute
)

// CustomImage is the h3vm public custom image (CustomImageResponse).
type CustomImage struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Image       string `json:"image"`
	Name        string `json:"name"`
	Description string `json:"description"`
	OSType      string `json:"os_type"`
	Firmware    string `json:"firmware"`
	SourceType  string `json:"source_type"`
	SourceURL   string `json:"source_url"`
	SourceVMID  string `json:"source_vm_id"`
	Status      string `json:"status"`
	Progress    string `json:"progress"`
	Message     string `json:"message"`
	SizeGi      int64  `json:"size_gi"`
	CreatedAt   string `json:"created_at"`
}

type createCustomImageRequest struct {
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Firmware    string `json:"firmware,omitempty"`
	URL         string `json:"url,omitempty"`
	SizeGi      int64  `json:"size_gi,omitempty"`
	SourceVMID  string `json:"source_vm_id,omitempty"`
}

func getCustomImage(ctx context.Context, c *client.Client, projectID, id string) (*CustomImage, error) {
	var img CustomImage
	if err := c.Do(ctx, "GET", customImagesBase+"/"+id, map[string]string{"project_id": projectID}, nil, &img); err != nil {
		return nil, err
	}
	return &img, nil
}

func listCustomImages(ctx context.Context, c *client.Client, projectID string) ([]CustomImage, error) {
	var out []CustomImage
	if err := c.Do(ctx, "GET", customImagesBase, map[string]string{"project_id": projectID}, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

var (
	_ resource.Resource                   = &CustomImageResource{}
	_ resource.ResourceWithConfigure      = &CustomImageResource{}
	_ resource.ResourceWithImportState    = &CustomImageResource{}
	_ resource.ResourceWithModifyPlan     = &CustomImageResource{}
	_ resource.ResourceWithValidateConfig = &CustomImageResource{}
)

// NewCustomImageResource is h3_vm_image.
func NewCustomImageResource() resource.Resource { return &CustomImageResource{} }

// CustomImageResource manages a custom VM image of a project.
type CustomImageResource struct{ client *client.Client }

// CustomImageModel is the state of h3_vm_image.
type CustomImageModel struct {
	ID          types.String   `tfsdk:"id"`
	ProjectID   types.String   `tfsdk:"project_id"`
	Name        types.String   `tfsdk:"name"`
	Description types.String   `tfsdk:"description"`
	Firmware    types.String   `tfsdk:"firmware"`
	URL         types.String   `tfsdk:"url"`
	SizeGi      types.Int64    `tfsdk:"size_gi"`
	SourceVMID  types.String   `tfsdk:"source_vm_id"`
	Image       types.String   `tfsdk:"image"`
	Status      types.String   `tfsdk:"status"`
	Message     types.String   `tfsdk:"message"`
	SourceType  types.String   `tfsdk:"source_type"`
	OSType      types.String   `tfsdk:"os_type"`
	CreatedAt   types.String   `tfsdk:"created_at"`
	Timeouts    timeouts.Value `tfsdk:"timeouts"`
}

// apply maps the API object. url keeps the configured value: the API shows
// the URL without its query string (a presigned token stays out of the API).
func (m *CustomImageModel) apply(img *CustomImage) {
	m.ID = types.StringValue(img.ID)
	if img.ProjectID != "" {
		m.ProjectID = types.StringValue(img.ProjectID)
	}
	m.Name = types.StringValue(img.Name)
	m.Description = types.StringValue(img.Description)
	m.Firmware = common.OptString(img.Firmware)
	if m.URL.IsNull() || m.URL.IsUnknown() {
		m.URL = common.OptString(img.SourceURL)
	}
	if img.SizeGi > 0 {
		m.SizeGi = types.Int64Value(img.SizeGi)
	} else if m.SizeGi.IsUnknown() {
		m.SizeGi = types.Int64Null()
	}
	if m.SourceVMID.IsNull() || m.SourceVMID.IsUnknown() {
		m.SourceVMID = common.OptString(img.SourceVMID)
	}
	ref := img.Image
	if ref == "" {
		ref = "custom:" + img.ID
	}
	m.Image = types.StringValue(ref)
	m.Status = types.StringValue(img.Status)
	m.Message = types.StringValue(img.Message)
	m.SourceType = common.OptString(img.SourceType)
	m.OSType = common.OptString(img.OSType)
	m.CreatedAt = types.StringValue(img.CreatedAt)
}

func (r *CustomImageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_image"
}

func (r *CustomImageResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	replaceKeep := []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a custom VM image of a project: imported from an HTTPS URL (qcow2 or raw, optionally " +
			"`.gz`/`.xz`) or copied from the root disk of a **stopped** VM of the project. Create a VM from it with " +
			"`h3_vm.image = h3_vm_image.x.image` (`custom:<id>`) and `disk_size` not below `size_gi`. The image must be a Linux " +
			"image with cloud-init. Apply waits until the image is `READY` (up to 60 minutes, see `timeouts`); a failed import " +
			"fails the apply with the platform message, and the image is then replaced on the next apply. " +
			"Every argument replaces the image. VMs made from an image are full copies and do not depend on it. " +
			"Limits: 10 images per project, up to 256 GiB each.",
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{MarkdownDescription: "Image ID.", Computed: true, PlanModifiers: keep},
			"project_id": common.ProjectIDAttribute("image"),
			"name": schema.StringAttribute{
				MarkdownDescription: "Image name. Changing it replaces the image.",
				Required:            true,
				PlanModifiers:       replace,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Description. Changing it replaces the image.",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       replaceKeep,
			},
			"firmware": schema.StringAttribute{
				MarkdownDescription: "Boot firmware of VMs made from the image: `bios` (default) or `efi`. Changing it replaces the image.",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       replaceKeep,
			},
			"url": schema.StringAttribute{
				MarkdownDescription: "HTTPS URL to import from (public address, port 443/8443/80/8080, no credentials in the URL; " +
					"a presigned S3 URL works, its query string is not shown by the API). qcow2 or raw, optionally `.gz` or `.xz`. " +
					"Exactly one of `url` and `source_vm_id`. Changing it replaces the image.",
				Optional:      true,
				PlanModifiers: replace,
			},
			"size_gi": schema.Int64Attribute{
				MarkdownDescription: "Virtual disk size of the image in GiB, for `url` (default 20, at most 256): not below the virtual " +
					"size of the file. For `source_vm_id` it is the size of the VM's root disk (computed). A VM from the image needs " +
					"`disk_size` of at least this size. Changing it replaces the image.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
					int64planmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"source_vm_id": schema.StringAttribute{
				MarkdownDescription: "Copy the root disk of this VM of the project. The VM must be **stopped** (409 `VM_NOT_STOPPED`) " +
					"and must be a Linux VM. Exactly one of `url` and `source_vm_id`. Changing it replaces the image.",
				Optional:      true,
				Validators:    []validator.String{common.UUIDValidator()},
				PlanModifiers: replace,
			},
			"image": schema.StringAttribute{
				MarkdownDescription: "Image reference for `h3_vm.image`: `custom:<id>`.",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "`IMPORTING`, `READY`, `FAILED` or `DELETING`.",
				Computed:            true,
			},
			"message": schema.StringAttribute{
				MarkdownDescription: "Platform message about the import (the reason of `FAILED`).",
				Computed:            true,
			},
			"source_type": schema.StringAttribute{MarkdownDescription: "Source of the image: `url` or `disk` (a VM root disk).", Computed: true, PlanModifiers: keep},
			"os_type":     schema.StringAttribute{MarkdownDescription: "OS family (`linux`).", Computed: true, PlanModifiers: keep},
			"created_at":  schema.StringAttribute{MarkdownDescription: "Creation time (RFC 3339).", Computed: true, PlanModifiers: keep},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (r *CustomImageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *CustomImageResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
}

func (r *CustomImageResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg CustomImageModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	hasURL, hasVM := !cfg.URL.IsNull(), !cfg.SourceVMID.IsNull()
	if hasURL == hasVM && !cfg.URL.IsUnknown() && !cfg.SourceVMID.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "Set exactly one image source",
			"Set either url (import over HTTPS) or source_vm_id (copy of a stopped VM's root disk).")
	}
	if hasVM && !cfg.SizeGi.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("size_gi"), "size_gi is for url",
			"An image from a VM takes the size of the VM's root disk; remove size_gi.")
	}
	if u := common.KnownString(cfg.URL); u != "" && !strings.HasPrefix(strings.ToLower(u), "https://") {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "url must be https", "Images are imported over HTTPS only.")
	}
	if v := cfg.SizeGi; !v.IsNull() && !v.IsUnknown() && (v.ValueInt64() < 1 || v.ValueInt64() > 256) {
		resp.Diagnostics.AddAttributeError(path.Root("size_gi"), "Invalid size_gi", "size_gi must be between 1 and 256.")
	}
	if f := common.KnownString(cfg.Firmware); f != "" && f != "bios" && f != "efi" {
		resp.Diagnostics.AddAttributeError(path.Root("firmware"), "Invalid firmware", fmt.Sprintf("expected bios or efi, got %q", f))
	}
}

func (r *CustomImageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan CustomImageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, imageCreateTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := createCustomImageRequest{
		ProjectID:   plan.ProjectID.ValueString(),
		Name:        plan.Name.ValueString(),
		Description: common.KnownString(plan.Description),
		Firmware:    common.KnownString(plan.Firmware),
		URL:         common.KnownString(plan.URL),
		SizeGi:      common.KnownInt64(plan.SizeGi),
		SourceVMID:  common.KnownString(plan.SourceVMID),
	}
	var img CustomImage
	if err := r.client.Do(ctx, "POST", customImagesBase, nil, body, &img); err != nil {
		resp.Diagnostics.AddError("Error creating image", err.Error())
		return
	}
	// Keep the id even if the import fails: the image is tracked (tainted) and
	// removed by the next apply instead of staying as an orphan.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), img.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), plan.ProjectID)...)

	project := plan.ProjectID.ValueString()
	last := &img
	err := common.WaitFor(ctx, timeout, "image "+img.ID+" to become READY", func(ctx context.Context) (bool, error) {
		got, err := getCustomImage(ctx, r.client, project, img.ID)
		if err != nil {
			return false, err
		}
		last = got
		tflog.Debug(ctx, "waiting for image", map[string]any{"id": img.ID, "status": got.Status, "progress": got.Progress})
		switch got.Status {
		case imageReady:
			return true, nil
		case imageFailed:
			return false, fmt.Errorf("import failed: %s", orDefault(got.Message, "no message from the platform"))
		case imageDeleting:
			return false, fmt.Errorf("the image is being deleted")
		}
		return false, nil
	})
	plan.apply(last)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for image", "Image "+img.ID+": "+err.Error())
	}
}

func (r *CustomImageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state CustomImageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	img, err := getCustomImage(ctx, r.client, state.ProjectID.ValueString(), state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) && !isFeatureDisabled(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Error reading image", err.Error())
		return
	}
	if img.Status == imageDeleting {
		resp.State.RemoveResource(ctx)
		return
	}
	state.apply(img)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update only records timeouts: every argument replaces the image.
func (r *CustomImageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan CustomImageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *CustomImageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state CustomImageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, imageDeleteTimeout)
	resp.Diagnostics.Append(d...)
	project, id := state.ProjectID.ValueString(), state.ID.ValueString()
	err := r.client.Do(ctx, "DELETE", customImagesBase+"/"+id, map[string]string{"project_id": project}, nil, nil)
	if err != nil {
		if client.IsNotFound(err) && !isFeatureDisabled(err) {
			return
		}
		resp.Diagnostics.AddError("Error deleting image", err.Error())
		return
	}
	err = common.WaitFor(ctx, timeout, "image "+id+" to be deleted", func(ctx context.Context) (bool, error) {
		_, err := getCustomImage(ctx, r.client, project, id)
		if client.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for image deletion", err.Error())
	}
}

// ImportState: "<project_id>/<image_id>", or "<image_id>" with the provider default project.
func (r *CustomImageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, id, err := importProjectID(r.client, req.ID, "<image_id>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// ---- data source h3_vm_image ----

var (
	_ datasource.DataSource              = &CustomImageDataSource{}
	_ datasource.DataSourceWithConfigure = &CustomImageDataSource{}
)

// NewCustomImageDataSource is data h3_vm_image.
func NewCustomImageDataSource() datasource.DataSource { return &CustomImageDataSource{} }

// CustomImageDataSource looks up a custom image by id or name.
type CustomImageDataSource struct{ client *client.Client }

// CustomImageDataModel is the state of data h3_vm_image.
type CustomImageDataModel struct {
	ID          types.String `tfsdk:"id"`
	ProjectID   types.String `tfsdk:"project_id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Firmware    types.String `tfsdk:"firmware"`
	SourceURL   types.String `tfsdk:"source_url"`
	SourceVMID  types.String `tfsdk:"source_vm_id"`
	SizeGi      types.Int64  `tfsdk:"size_gi"`
	Image       types.String `tfsdk:"image"`
	Status      types.String `tfsdk:"status"`
	Message     types.String `tfsdk:"message"`
	SourceType  types.String `tfsdk:"source_type"`
	OSType      types.String `tfsdk:"os_type"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func (d *CustomImageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_image"
}

func (d *CustomImageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	computed := func(desc string) dsschema.StringAttribute {
		return dsschema.StringAttribute{MarkdownDescription: desc, Computed: true}
	}
	resp.Schema = dsschema.Schema{
		MarkdownDescription: "Looks up a custom VM image of the project by `id`, or by `name` (the newest image with that name). " +
			"Use `image` (`custom:<id>`) as `h3_vm.image`. Catalog images (`ubuntu:24.04`, ...) are listed by `h3_vm_images`.",
		Attributes: map[string]dsschema.Attribute{
			"id":           dsschema.StringAttribute{MarkdownDescription: "Image ID. Set `id` or `name`.", Optional: true, Computed: true},
			"project_id":   dsschema.StringAttribute{MarkdownDescription: "Project ID; the provider default when omitted.", Optional: true, Computed: true},
			"name":         dsschema.StringAttribute{MarkdownDescription: "Image name. Set `id` or `name`.", Optional: true, Computed: true},
			"description":  computed("Description."),
			"firmware":     computed("`bios` or `efi`."),
			"source_url":   computed("URL the image was imported from (without its query string)."),
			"source_vm_id": computed("VM the image was copied from."),
			"size_gi":      dsschema.Int64Attribute{MarkdownDescription: "Virtual size in GiB (the minimum `disk_size` of a VM).", Computed: true},
			"image":        computed("Image reference for `h3_vm.image`: `custom:<id>`."),
			"status":       computed("`IMPORTING`, `READY`, `FAILED` or `DELETING`."),
			"message":      computed("Platform message about the import."),
			"source_type":  computed("`url` or `disk` (a VM root disk)."),
			"os_type":      computed("OS family (`linux`)."),
			"created_at":   computed("Creation time (RFC 3339)."),
		},
	}
}

func (d *CustomImageDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *CustomImageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m CustomImageDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, ok := common.DataSourceProjectID(d.client, m.ProjectID, resp)
	if !ok {
		return
	}
	id, name := common.KnownString(m.ID), common.KnownString(m.Name)
	var img *CustomImage
	var err error
	switch {
	case id != "":
		img, err = getCustomImage(ctx, d.client, project, id)
	case name != "":
		var all []CustomImage
		all, err = listCustomImages(ctx, d.client, project)
		// The API lists the newest first; take the newest with that name.
		for i := range all {
			if all[i].Name == name && (img == nil || all[i].CreatedAt > img.CreatedAt) {
				img = &all[i]
			}
		}
		if err == nil && img == nil {
			err = fmt.Errorf("no custom image named %q in project %s", name, project)
		}
	default:
		err = fmt.Errorf("set id or name")
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading image", err.Error())
		return
	}
	ref := img.Image
	if ref == "" {
		ref = "custom:" + img.ID
	}
	m = CustomImageDataModel{
		ID: types.StringValue(img.ID), ProjectID: types.StringValue(project), Name: types.StringValue(img.Name),
		Description: types.StringValue(img.Description), Firmware: types.StringValue(img.Firmware),
		SourceURL: types.StringValue(img.SourceURL), SourceVMID: types.StringValue(img.SourceVMID),
		SizeGi: types.Int64Value(img.SizeGi), Image: types.StringValue(ref), Status: types.StringValue(img.Status),
		Message: types.StringValue(img.Message), SourceType: types.StringValue(img.SourceType),
		OSType: types.StringValue(img.OSType), CreatedAt: types.StringValue(img.CreatedAt),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
