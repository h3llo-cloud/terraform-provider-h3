package provider

import (
	"context"
	"os"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/services/backup"
	"h3terraform/internal/services/disk"
	"h3terraform/internal/services/net"
	"h3terraform/internal/services/s3"
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
				MarkdownDescription: "H3 Cloud API endpoint (default: http://127.0.0.1:4001)",
				Optional:            true,
			},
			"key_id": schema.StringAttribute{
				MarkdownDescription: "API Key ID for HMAC authentication",
				Optional:            true,
				Sensitive:           true,
			},
			"secret_key": schema.StringAttribute{
				MarkdownDescription: "API Secret Key for HMAC signing",
				Optional:            true,
				Sensitive:           true,
			},
			"timeout": schema.Int64Attribute{
				MarkdownDescription: "Request timeout in seconds (default: 30)",
				Optional:            true,
			},
			"max_retries": schema.Int64Attribute{
				MarkdownDescription: "Maximum retry attempts (default: 3)",
				Optional:            true,
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
		apiEndpoint = "http://127.0.0.1:4001"
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

	// Timeout
	timeout := int64(30)
	if !config.Timeout.IsNull() {
		timeout = config.Timeout.ValueInt64()
	}

	// Max Retries
	maxRetries := int64(3)
	if !config.MaxRetries.IsNull() {
		maxRetries = config.MaxRetries.ValueInt64()
	}

	// Создаем HTTP клиент с HMAC
	httpClient, err := client.NewClient(client.Config{
		BaseURL:    apiEndpoint,
		KeyID:      keyID,
		SecretKey:  secretKey,
		Timeout:    time.Duration(timeout) * time.Second,
		MaxRetries: int(maxRetries),
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
		disk.NewDiskResource,
		snapshot.NewSnapshotResource,
		backup.NewBackupResource,
		net.NewVPCResource,
		net.NewNetworkResource,
		net.NewEIPResource,
		s3.NewBucketResource,
		ssh.NewSSHKeyResource,
	}
}

// DataSources возвращает список data sources провайдера
func (p *H3Provider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}
