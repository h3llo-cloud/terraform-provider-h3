package acctest_test

import (
	"fmt"
	"testing"

	"h3terraform/internal/acctest"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// R4/R5: VPC without namespaces and network without external_subnets failed
// with "Value Conversion Error" before any API call.
func TestNet_MinimalVPCNetworkEIP(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	cfg := func(vmLine string) string {
		return f.Config(fmt.Sprintf(`
resource "h3_vpc" "main" {
  project_id = %[1]q
  name       = "main"
}

resource "h3_network" "app" {
  project_id = %[1]q
  name       = "app"
  vpc_id     = h3_vpc.main.id
  cidr_block = "10.1.0.0/24"
}

resource "h3_eip" "web" {
  project_id = %[1]q
  name       = "web-ip"
%[2]s
}
`, acctest.Project, vmLine))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy: func(*terraform.State) error {
			for _, c := range []string{"vpcs", "networks", "eips"} {
				if n := f.Count(c); n != 0 {
					return fmt.Errorf("%d %s left", n, c)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_vpc.main", "id", acctest.Project),
					resource.TestCheckResourceAttr("h3_vpc.main", "status", "ACTIVE"),
					resource.TestCheckResourceAttr("h3_vpc.main", "namespaces.#", "1"),
					resource.TestCheckResourceAttr("h3_network.app", "status", "ACTIVE"),
					resource.TestCheckResourceAttr("h3_network.app", "protocol", "IPv4"),
					resource.TestCheckResourceAttr("h3_network.app", "subnet_name", "subnet-app"),
					resource.TestCheckResourceAttrPair("h3_network.app", "id", "h3_network.app", "subnet_id"),
					resource.TestCheckNoResourceAttr("h3_network.app", "external_subnets"),
					resource.TestCheckResourceAttr("h3_eip.web", "status", "DETACHED"),
					resource.TestCheckResourceAttrSet("h3_eip.web", "ip_address"),
				),
			},
			{Config: cfg(""), PlanOnly: true},
			{
				// Attach in place.
				Config: cfg(`  vm_id = "33333333-0000-4000-8000-000000000001"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_eip.web", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_eip.web", "status", "ATTACHED"),
					resource.TestCheckResourceAttr("h3_eip.web", "bound_resource_type", "vm"),
				),
			},
			{ResourceName: "h3_vpc.main", ImportState: true, ImportStateVerify: true},
			{ResourceName: "h3_network.app", ImportState: true, ImportStateVerify: true},
			{ResourceName: "h3_eip.web", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"vm_id"}},
		},
	})
}

// static_routes with a defaulted policy: the computed nested value must be
// known after apply.
func TestNet_VPCStaticRoutes(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	cfg := f.Config(fmt.Sprintf(`
resource "h3_vpc" "main" {
  project_id    = %q
  name          = "main"
  static_routes = [{ cidr = "0.0.0.0/0", next_hop_ip = "10.0.0.254" }]
}
`, acctest.Project))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{Config: cfg, Check: resource.TestCheckResourceAttr("h3_vpc.main", "static_routes.0.policy", "policyDst")},
			{Config: cfg, PlanOnly: true},
		},
	})
}
