package s3

import (
	"context"
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
	_ resource.Resource                = &BucketResource{}
	_ resource.ResourceWithConfigure   = &BucketResource{}
	_ resource.ResourceWithImportState = &BucketResource{}
)

var visibleTimeout = 2 * time.Minute

func NewBucketResource() resource.Resource {
	return &BucketResource{}
}

type BucketResource struct {
	client *client.Client
}

type BucketResourceModel struct {
	ID              types.String `tfsdk:"id"`
	ProjectID       types.String `tfsdk:"project_id"`
	Name            types.String `tfsdk:"name"`
	Slug            types.String `tfsdk:"slug"`
	Region          types.String `tfsdk:"region"`
	AccessKeyID     types.String `tfsdk:"access_key_id"`
	SecretAccessKey types.String `tfsdk:"secret_access_key"`
	CreatedAt       types.String `tfsdk:"created_at"`
}

func (r *BucketResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_s3_bucket"
}

func (r *BucketResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an S3 bucket. `name` and `project_id` force a new (empty) bucket. " +
			"The S3 access key of the project is returned only when a bucket is created; it is shared by all buckets of the project " +
			"and is kept in state (mark outputs sensitive, protect the state). After import it is empty. " +
			"Import with `<project_id>/<bucket_name>`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`<project_id>/<name>`",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"project_id": common.ProjectIDAttribute("bucket"),
			"name": schema.StringAttribute{
				MarkdownDescription: "Bucket name",
				Required:            true,
				PlanModifiers:       replace,
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "Bucket name in the S3 storage",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Bucket region, when reported",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"access_key_id": schema.StringAttribute{
				MarkdownDescription: "S3 access key ID of the project (returned only on create)",
				Computed:            true,
				Sensitive:           true,
				PlanModifiers:       keep,
			},
			"secret_access_key": schema.StringAttribute{
				MarkdownDescription: "S3 secret access key of the project (returned only on create)",
				Computed:            true,
				Sensitive:           true,
				PlanModifiers:       keep,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "Bucket creation timestamp",
				Computed:            true,
				PlanModifiers:       keep,
			},
		},
	}
}

func (r *BucketResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = common.ClientFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *BucketResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan BucketResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, name := plan.ProjectID.ValueString(), plan.Name.ValueString()

	var created CreateBucketResponse
	if err := r.client.Do(ctx, "POST", "/api/s3/v1/buckets", nil, CreateBucketRequest{ProjectID: project, Name: name}, &created); err != nil {
		resp.Diagnostics.AddError("Error creating bucket", "Could not create bucket: "+err.Error())
		return
	}
	plan.ID = types.StringValue(project + "/" + name)
	plan.AccessKeyID = types.StringValue(created.Credentials.AccessKeyID)
	plan.SecretAccessKey = types.StringValue(created.Credentials.SecretAccessKey)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)

	var bucket *Bucket
	err := common.WaitFor(ctx, visibleTimeout, "bucket "+name+" to appear", func(ctx context.Context) (bool, error) {
		b, err := r.find(ctx, project, name)
		bucket = b
		return b != nil, err
	})
	if err != nil {
		resp.Diagnostics.AddError("Error reading bucket", "Bucket created but not listed: "+err.Error())
		return
	}
	plan.applyAPI(bucket)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *BucketResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state BucketResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.find(ctx, state.ProjectID.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading bucket", err.Error())
		return
	}
	if b == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.ID = types.StringValue(state.ProjectID.ValueString() + "/" + state.Name.ValueString())
	state.applyAPI(b)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called with a change: both arguments force replacement.
func (r *BucketResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan BucketResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *BucketResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state BucketResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	q := map[string]string{"project_id": state.ProjectID.ValueString()}
	if err := r.client.Do(ctx, "DELETE", "/api/s3/v1/buckets/"+state.Name.ValueString(), q, nil, nil); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting bucket", err.Error())
	}
}

// ImportState: "<project_id>/<bucket_name>".
func (r *BucketResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, name, err := common.ImportProjectScoped(req.ID, "<bucket_name>")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

// applyAPI: the access key is not returned by GET/list and keeps its prior value.
func (m *BucketResourceModel) applyAPI(b *Bucket) {
	m.Name = types.StringValue(b.Name)
	m.Slug = types.StringValue(b.Slug)
	if m.Region.IsNull() || m.Region.IsUnknown() || b.Region != "" {
		m.Region = types.StringValue(b.Region)
	}
	// h3s3 synthesizes createdAt on read; keep the first value seen.
	if m.CreatedAt.IsNull() || m.CreatedAt.IsUnknown() || m.CreatedAt.ValueString() == "" {
		m.CreatedAt = types.StringValue(b.CreatedAt)
	}
	if m.AccessKeyID.IsUnknown() {
		m.AccessKeyID = types.StringNull()
	}
	if m.SecretAccessKey.IsUnknown() {
		m.SecretAccessKey = types.StringNull()
	}
}

// find looks the bucket up in the project list. GET /buckets/{name} answers
// 500 (not 404) for a missing bucket, so the list is the reliable check.
func (r *BucketResource) find(ctx context.Context, project, name string) (*Bucket, error) {
	var list ListBucketsResponse
	if err := r.client.Do(ctx, "GET", "/api/s3/v1/buckets", map[string]string{"project_id": project}, nil, &list); err != nil {
		return nil, err
	}
	for i := range list.Buckets {
		if list.Buckets[i].Name == name {
			return &list.Buckets[i], nil
		}
	}
	return nil, nil
}

// ModifyPlan fills project_id from the provider default.
func (r *BucketResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	common.DefaultProjectID(ctx, r.client, req, resp)
}
