package provider

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/services/backup"
	"h3terraform/internal/services/database"
	"h3terraform/internal/services/disk"
	"h3terraform/internal/services/dns"
	"h3terraform/internal/services/iam"
	"h3terraform/internal/services/k8s"
	"h3terraform/internal/services/lb"
	"h3terraform/internal/services/net"
	"h3terraform/internal/services/s3"
	"h3terraform/internal/services/sg"
	"h3terraform/internal/services/snapshot"
	"h3terraform/internal/services/ssh"
	"h3terraform/internal/services/vm"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = &H3Provider{}

// H3Provider - основной провайдер
type H3Provider struct {
	version string
}

// H3ProviderModel - модель конфигурации провайдера
type H3ProviderModel struct {
	APIEndpoint types.String `tfsdk:"api_endpoint"`
	KeyID       types.String `tfsdk:"key_id"`
	SecretKey   types.String `tfsdk:"secret_key"`
	Timeout     types.Int64  `tfsdk:"timeout"`
	MaxRetries  types.Int64  `tfsdk:"max_retries"`
	ProjectID   types.String `tfsdk:"project_id"`
}

// New создает новый экземпляр провайдера
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &H3Provider{version: version}
	}
}

// Metadata возвращает метаданные провайдера
func (p *H3Provider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "h3"
	resp.Version = p.version
}

// Schema определяет схему конфигурации провайдера
func (p *H3Provider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "H3 Cloud Terraform Provider with HMAC authentication",
		Attributes: map[string]schema.Attribute{
			"api_endpoint": schema.StringAttribute{
				MarkdownDescription: "h3llo cloud API endpoint. Default: `https://api.h3llo.cloud`. Can also be set with the `H3_API_ENDPOINT` environment variable.",
				Optional:            true,
			},
			"key_id": schema.StringAttribute{
				MarkdownDescription: "API key ID. Can also be set with the `H3_KEY_ID` environment variable (recommended).",
				Optional:            true,
				Sensitive:           true,
			},
			"secret_key": schema.StringAttribute{
				MarkdownDescription: "API key secret used to sign requests (HMAC-SHA256). Can also be set with the `H3_SECRET_KEY` environment variable (recommended).",
				Optional:            true,
				Sensitive:           true,
			},
			"timeout": schema.Int64Attribute{
				MarkdownDescription: "Timeout of a single HTTP request in seconds (default: 30, env `H3_TIMEOUT`). Waiting for resources to become ready is not limited by it.",
				Optional:            true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Default project (UUID) of resources and data sources that do not set `project_id`. " +
					"Can also be set with the `H3_PROJECT_ID` environment variable. Changing it later does not move existing resources: " +
					"they keep the project recorded in state.",
				Optional: true,
			},
			"max_retries": schema.Int64Attribute{
				MarkdownDescription: "Retries after a failed request (default: 3, env `H3_MAX_RETRIES`; `0` disables retries). " +
					"Reads and deletes are retried on network errors and 429/5xx; creates (POST) only on 429/502/503/504, honoring `Retry-After`.",
				Optional: true,
			},
		},
	}
}

// Configure инициализирует провайдера с конфигурацией
func (p *H3Provider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config H3ProviderModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// API Endpoint
	apiEndpoint := os.Getenv("H3_API_ENDPOINT")
	if !config.APIEndpoint.IsNull() {
		apiEndpoint = config.APIEndpoint.ValueString()
	}
	if apiEndpoint == "" {
		apiEndpoint = client.DefaultEndpoint
	}

	// Key ID
	keyID := os.Getenv("H3_KEY_ID")
	if !config.KeyID.IsNull() {
		keyID = config.KeyID.ValueString()
	}
	if keyID == "" {
		resp.Diagnostics.AddError(
			"Missing API Key ID",
			"Set key_id in provider config or H3_KEY_ID environment variable",
		)
		return
	}

	// Secret Key
	secretKey := os.Getenv("H3_SECRET_KEY")
	if !config.SecretKey.IsNull() {
		secretKey = config.SecretKey.ValueString()
	}
	if secretKey == "" {
		resp.Diagnostics.AddError(
			"Missing Secret Key",
			"Set secret_key in provider config or H3_SECRET_KEY environment variable",
		)
		return
	}

	timeout, err := intSetting(config.Timeout, "H3_TIMEOUT", 30)
	if err != nil {
		resp.Diagnostics.AddError("Invalid timeout", err.Error())
		return
	}
	// -1 = not set: the client applies its default; 0 disables retries.
	maxRetries, err := intSetting(config.MaxRetries, "H3_MAX_RETRIES", -1)
	if err != nil {
		resp.Diagnostics.AddError("Invalid max_retries", err.Error())
		return
	}

	projectID := os.Getenv("H3_PROJECT_ID")
	if !config.ProjectID.IsNull() && !config.ProjectID.IsUnknown() {
		projectID = config.ProjectID.ValueString()
	}

	// Создаем HTTP клиент с HMAC
	httpClient, err := client.NewClient(client.Config{
		BaseURL:    apiEndpoint,
		KeyID:      keyID,
		SecretKey:  secretKey,
		Timeout:    time.Duration(timeout) * time.Second,
		MaxRetries: int(maxRetries),
		ProjectID:  projectID,
		UserAgent:  fmt.Sprintf("terraform-provider-h3/%s (+terraform %s)", p.version, req.TerraformVersion),
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to create H3 API client",
			"Error: "+err.Error(),
		)
		return
	}

	resp.DataSourceData = httpClient
	resp.ResourceData = httpClient
}

func (p *H3Provider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		vm.NewVMResource,
		vm.NewSSHAccessResource,
		vm.NewPlacementGroupResource,
		vm.NewCustomImageResource,
		disk.NewDiskResource,
		snapshot.NewSnapshotResource,
		backup.NewBackupResource,
		net.NewVPCResource,
		net.NewNetworkResource,
		net.NewEIPResource,
		net.NewVPCRouteResource,
		// Deprecated aliases (0.3.0), removed in 1.0.
		net.NewOVNVPCResource,
		net.NewOVNNetworkResource,
		net.NewOVNEIPResource,
		s3.NewBucketResource,
		ssh.NewSSHKeyResource,
		dns.NewZoneResource,
		dns.NewRecordSetResource,
		dns.NewPTRResource,
		database.NewDatabaseResource,
		database.NewUserResource,
		database.NewPoolerResource,
		sg.NewGroupResource,
		sg.NewRuleResource,
		sg.NewVMGroupsResource,
		sg.NewAttachmentResource,
		k8s.NewClusterResource,
		k8s.NewNodePoolResource,
		lb.NewLBResource,
		iam.NewServiceAccountResource,
		iam.NewKeyResource,
	}
}

// DataSources возвращает список data sources провайдера
func (p *H3Provider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		vm.NewImagesDataSource,
		vm.NewCustomImageDataSource,
		vm.NewPlacementGroupDataSource,
		sg.NewGroupDataSource,
		dns.NewZoneDataSource,
		dns.NewNameserversDataSource,
		net.NewVPCDataSource,
		net.NewNetworkDataSource,
		net.NewEIPDataSource,
		net.NewEIPsDataSource,
		k8s.NewVersionsDataSource,
		k8s.NewClusterDataSource,
		k8s.NewKubeconfigDataSource,
		k8s.NewNodePoolsDataSource,
		lb.NewLBDataSource,
		lb.NewLBsDataSource,
		database.NewPoolersDataSource,
		iam.NewServiceAccountsDataSource,
		iam.NewAuditEventsDataSource,
	}
}

// intSetting returns the provider attribute, else the environment variable, else def.
func intSetting(attr types.Int64, env string, def int64) (int64, error) {
	if !attr.IsNull() && !attr.IsUnknown() {
		if attr.ValueInt64() < 0 {
			return 0, fmt.Errorf("value must be >= 0, got %d", attr.ValueInt64())
		}
		return attr.ValueInt64(), nil
	}
	if v := os.Getenv(env); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%s must be a non-negative integer, got %q", env, v)
		}
		return n, nil
	}
	return def, nil
}
