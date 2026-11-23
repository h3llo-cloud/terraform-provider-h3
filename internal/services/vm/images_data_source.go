package vm

import (
	"context"
	"fmt"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ImagesPath is the h3vm endpoint with the OS image catalog
// (the source of truth is the h3vm config, see h3vm images.catalog).
const ImagesPath = "/api/vms/v1/images"

var (
	_ datasource.DataSource              = &ImagesDataSource{}
	_ datasource.DataSourceWithConfigure = &ImagesDataSource{}
)

// NewImagesDataSource creates the h3_vm_images data source.
func NewImagesDataSource() datasource.DataSource {
	return &ImagesDataSource{}
}

// ImagesDataSource reads the catalog of OS images offered for new VMs.
type ImagesDataSource struct {
	client *client.Client
}

// ImagesResponse is the h3vm GET /api/vms/v1/images answer.
type ImagesResponse struct {
	Revision        string            `json:"revision"`
	GeneratedAt     string            `json:"generatedAt"`
	Default         string            `json:"default"`
	DefaultFallback bool              `json:"defaultFallback"`
	Families        []ImageFamily     `json:"families"`
	Labels          map[string]string `json:"labels"`
}

type ImageFamily struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	OSType   string         `json:"osType"`
	IconKey  string         `json:"iconKey"`
	Versions []ImageVersion `json:"versions"`
}

type ImageVersion struct {
	ID           string             `json:"id"`
	Version      string             `json:"version"`
	Name         string             `json:"name"`
	Arch         string             `json:"arch"`
	Status       string             `json:"status"`
	EOL          string             `json:"eol,omitempty"`
	Usage        []string           `json:"usage"`
	Min          ImageMin           `json:"min"`
	CloudInit    *ImageCloudInit    `json:"cloudInit,omitempty"`
	Requirements *ImageRequirements `json:"requirements,omitempty"`
	Tags         []string           `json:"tags"`
	License      string             `json:"license,omitempty"`
}

type ImageMin struct {
	DiskGi   int64 `json:"diskGi"`
	MemoryGi int64 `json:"memoryGi"`
	CPU      int64 `json:"cpu"`
}

type ImageCloudInit struct {
	Supported bool `json:"supported"`
	SSHKey    bool `json:"sshKey"`
	Password  bool `json:"password"`
}

type ImageRequirements struct {
	CPULevel    string   `json:"cpuLevel,omitempty"`
	CPUFeatures []string `json:"cpuFeatures,omitempty"`
}

// ImagesDataSourceModel is the Terraform state of h3_vm_images.
type ImagesDataSourceModel struct {
	ID       types.String      `tfsdk:"id"`
	Usage    types.String      `tfsdk:"usage"`
	Lang     types.String      `tfsdk:"lang"`
	Family   types.String      `tfsdk:"family"`
	OSType   types.String      `tfsdk:"os_type"`
	Status   types.String      `tfsdk:"status"`
	Revision types.String      `tfsdk:"revision"`
	Default  types.String      `tfsdk:"default"`
	Images   []ImageEntryModel `tfsdk:"images"`
}

// ImageEntryModel is one flat image entry.
type ImageEntryModel struct {
	ID          types.String `tfsdk:"id"`
	Family      types.String `tfsdk:"family"`
	FamilyName  types.String `tfsdk:"family_name"`
	OSType      types.String `tfsdk:"os_type"`
	Version     types.String `tfsdk:"version"`
	Name        types.String `tfsdk:"name"`
	Status      types.String `tfsdk:"status"`
	EOL         types.String `tfsdk:"eol"`
	MinDiskGi   types.Int64  `tfsdk:"min_disk_gi"`
	MinMemoryGi types.Int64  `tfsdk:"min_memory_gi"`
	MinCPU      types.Int64  `tfsdk:"min_cpu"`
	Usage       []string     `tfsdk:"usage"`
	Tags        []string     `tfsdk:"tags"`
}

func (d *ImagesDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_images"
}

func (d *ImagesDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: desc, Computed: true}
	}
	num := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{MarkdownDescription: desc, Computed: true}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "OS images available for new VMs. The catalog is served by h3vm " +
			"(`GET /api/vms/v1/images`); only images that are offered and present in the cluster are listed. " +
			"Use `id` as the `image` of `h3_vm`.",
		Attributes: map[string]schema.Attribute{
			"id": str("Catalog revision (same as `revision`)."),
			"usage": schema.StringAttribute{
				MarkdownDescription: "Where the image is used: `vm` (default) or `capsule`.",
				Optional:            true,
			},
			"lang": schema.StringAttribute{
				MarkdownDescription: "Language of `name`/`family_name`: `ru` (default) or `en`.",
				Optional:            true,
			},
			"family": schema.StringAttribute{
				MarkdownDescription: "Filter: family id, e.g. `ubuntu`.",
				Optional:            true,
			},
			"os_type": schema.StringAttribute{
				MarkdownDescription: "Filter: `linux` or `windows`.",
				Optional:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Filter: `supported` or `deprecated`.",
				Optional:            true,
			},
			"revision": str("Catalog revision."),
			"default":  str("Default image id."),
			"images": schema.ListNestedAttribute{
				MarkdownDescription: "Images in catalog order (families, then versions; newest first).",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":            str("Image id, the `image` of `h3_vm` (e.g. `ubuntu:24.04`)."),
						"family":        str("Family id."),
						"family_name":   str("Family display name."),
						"os_type":       str("`linux` or `windows`."),
						"version":       str("Version, e.g. `24.04`."),
						"name":          str("Version display name."),
						"status":        str("`supported` or `deprecated`."),
						"eol":           str("End of upstream support, `YYYY-MM-DD` (empty if unknown)."),
						"min_disk_gi":   num("Minimum root disk, GiB."),
						"min_memory_gi": num("Minimum memory, GiB."),
						"min_cpu":       num("Minimum vCPU."),
						"usage": schema.ListAttribute{
							MarkdownDescription: "Where offered: `vm`, `capsule`.",
							ElementType:         types.StringType,
							Computed:            true,
						},
						"tags": schema.ListAttribute{
							MarkdownDescription: "Tags, e.g. `lts`, `evaluation`, `preview`.",
							ElementType:         types.StringType,
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *ImagesDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData))
		return
	}
	d.client = c
}

func (d *ImagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state ImagesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if v := state.Usage.ValueString(); v != "" && v != "vm" && v != "capsule" {
		resp.Diagnostics.AddAttributeError(path.Root("usage"), "Invalid usage", "usage must be vm or capsule")
	}
	if v := state.Lang.ValueString(); v != "" && v != "ru" && v != "en" {
		resp.Diagnostics.AddAttributeError(path.Root("lang"), "Invalid lang", "lang must be ru or en")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	query := map[string]string{}
	if v := state.Usage.ValueString(); v != "" {
		query["usage"] = v
	}
	if v := state.Lang.ValueString(); v != "" {
		query["lang"] = v
	}

	var catalog ImagesResponse
	if err := d.client.Do(ctx, "GET", ImagesPath, query, nil, &catalog); err != nil {
		resp.Diagnostics.AddError("Error reading VM images", "Could not read the image catalog: "+err.Error())
		return
	}

	state.Revision = types.StringValue(catalog.Revision)
	state.ID = types.StringValue(catalog.Revision)
	state.Default = types.StringValue(catalog.Default)
	state.Images = flattenImages(catalog, imageFilter{
		Family: state.Family.ValueString(),
		OSType: state.OSType.ValueString(),
		Status: state.Status.ValueString(),
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

type imageFilter struct {
	Family, OSType, Status string
}

// flattenImages turns families[].versions[] into a flat list, keeping order.
func flattenImages(c ImagesResponse, f imageFilter) []ImageEntryModel {
	out := []ImageEntryModel{}
	for _, fam := range c.Families {
		if f.Family != "" && fam.ID != f.Family {
			continue
		}
		if f.OSType != "" && fam.OSType != f.OSType {
			continue
		}
		for _, v := range fam.Versions {
			if f.Status != "" && v.Status != f.Status {
				continue
			}
			out = append(out, ImageEntryModel{
				ID:          types.StringValue(v.ID),
				Family:      types.StringValue(fam.ID),
				FamilyName:  types.StringValue(fam.Name),
				OSType:      types.StringValue(fam.OSType),
				Version:     types.StringValue(v.Version),
				Name:        types.StringValue(v.Name),
				Status:      types.StringValue(v.Status),
				EOL:         types.StringValue(v.EOL),
				MinDiskGi:   types.Int64Value(v.Min.DiskGi),
				MinMemoryGi: types.Int64Value(v.Min.MemoryGi),
				MinCPU:      types.Int64Value(v.Min.CPU),
				Usage:       nonNil(v.Usage),
				Tags:        nonNil(v.Tags),
			})
		}
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
