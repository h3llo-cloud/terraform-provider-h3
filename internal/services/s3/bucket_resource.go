package s3

import (
	"context"
	"fmt"
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
	_ resource.Resource                = &BucketResource{}
	_ resource.ResourceWithConfigure   = &BucketResource{}
	_ resource.ResourceWithImportState = &BucketResource{}
)

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
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages H3 Cloud S3 Bucket",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Bucket ID",
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
				MarkdownDescription: "Bucket name",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "Bucket slug",
				Computed:            true,
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Bucket region",
				Computed:            true,
			},
			"access_key_id": schema.StringAttribute{
				MarkdownDescription: "S3 Access Key ID",
				Computed:            true,
				Sensitive:           true,
			},
			"secret_access_key": schema.StringAttribute{
				MarkdownDescription: "S3 Secret Access Key",
				Computed:            true,
				Sensitive:           true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "Bucket creation timestamp",
				Computed:            true,
			},
		},
	}
}

func (r *BucketResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *BucketResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan BucketResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := CreateBucketRequest{
		ProjectID: plan.ProjectID.ValueString(),
		Name:      plan.Name.ValueString(),
	}

	var createResp CreateBucketResponse
	err := r.client.Do(ctx, "POST", "/api/s3/v1/buckets", nil, createReq, &createResp)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating bucket",
			fmt.Sprintf("Could not create bucket: %s", err.Error()),
		)
		return
	}

	time.Sleep(3 * time.Second)

	bucket, err := r.getBucket(ctx, plan.ProjectID.ValueString(), plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading bucket",
			fmt.Sprintf("Could not read created bucket: %s", err.Error()),
		)
		return
	}

	plan.ID = types.StringValue(bucket.ID)
	plan.Slug = types.StringValue(bucket.Slug)
	plan.Region = types.StringValue(bucket.Region)
	plan.AccessKeyID = types.StringValue(createResp.Credentials.AccessKeyID)
	plan.SecretAccessKey = types.StringValue(createResp.Credentials.SecretAccessKey)
	plan.CreatedAt = types.StringValue(bucket.CreatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *BucketResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state BucketResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	bucket, err := r.getBucket(ctx, state.ProjectID.ValueString(), state.Name.ValueString())
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.StatusCode == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Error reading bucket",
			fmt.Sprintf("Could not read bucket: %s", err.Error()),
		)
		return
	}

	state.ID = types.StringValue(bucket.ID)
	state.Slug = types.StringValue(bucket.Slug)
	state.Region = types.StringValue(bucket.Region)
	state.CreatedAt = types.StringValue(bucket.CreatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *BucketResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update not supported",
		"Bucket resources cannot be updated. They must be replaced.",
	)
}

func (r *BucketResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state BucketResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	queryParams := map[string]string{
		"project_id": state.ProjectID.ValueString(),
	}

	err := r.client.Do(ctx, "DELETE", "/api/s3/v1/buckets/"+state.Name.ValueString(), queryParams, nil, nil)
	if err != nil {
		if httpErr, ok := err.(*client.HTTPError); ok && httpErr.StatusCode == 404 {
			return
		}
		resp.Diagnostics.AddError(
			"Error deleting bucket",
			fmt.Sprintf("Could not delete bucket: %s", err.Error()),
		)
		return
	}
}

func (r *BucketResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *BucketResource) getBucket(ctx context.Context, projectID, bucketName string) (*Bucket, error) {
	queryParams := map[string]string{
		"project_id": projectID,
	}

	var getBucketResp GetBucketResponse
	err := r.client.Do(ctx, "GET", "/api/s3/v1/buckets/"+bucketName, queryParams, nil, &getBucketResp)
	if err != nil {
		return nil, err
	}

	return &getBucketResp.Bucket, nil
}
