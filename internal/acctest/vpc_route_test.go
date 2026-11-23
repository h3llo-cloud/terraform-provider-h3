package acctest_test

import (
	"fmt"
	"regexp"
	"testing"

	"h3terraform/internal/acctest"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestNet_VPCRoutes(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	cfg := func(extra string) string {
		return f.Config(fmt.Sprintf(`
resource "h3_vpc" "main" {
  project_id = %[1]q
  name       = "main"
}

resource "h3_vpc_route" "office" {
  vpc_id      = h3_vpc.main.id
  destination = "10.50.0.0/16"
  target_type = "ip"
  next_hop_ip = "10.1.0.10"
  description = "VPN to the office"
}

resource "h3_vpc_route" "drop" {
  vpc_id      = h3_vpc.main.id
  destination = "10.50.5.0/24"
  target_type = "blackhole"
}
%[2]s
`, acctest.Project, extra))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy: func(*terraform.State) error {
			if n := f.Count("vpc_routes"); n != 0 {
				return fmt.Errorf("%d routes left", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_vpc_route.office", "status", "active"),
					resource.TestCheckResourceAttr("h3_vpc_route.office", "next_hop_ip", "10.1.0.10"),
					resource.TestCheckResourceAttr("h3_vpc_route.drop", "target_type", "blackhole"),
					resource.TestCheckNoResourceAttr("h3_vpc_route.drop", "next_hop_ip"),
					resource.TestMatchResourceAttr("h3_vpc_route.office", "id", regexp.MustCompile(`^rt-`)),
				),
			},
			{
				// vm target: the platform fills next_hop_ip
				Config: cfg(`
resource "h3_vpc_route" "vm" {
  vpc_id      = h3_vpc.main.id
  destination = "192.168.77.0/24"
  target_type = "vm"
  vm_id       = "vm-1"
}
`),
				Check: resource.TestCheckResourceAttr("h3_vpc_route.vm", "next_hop_ip", "10.1.0.50"),
			},
			{
				ResourceName:      "h3_vpc_route.office",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					r := s.RootModule().Resources["h3_vpc_route.office"]
					return r.Primary.Attributes["vpc_id"] + "/" + r.Primary.ID, nil
				},
			},
			{
				Config: cfg(`
resource "h3_vpc_route" "dup" {
  vpc_id      = h3_vpc.main.id
  destination = "10.50.0.0/16"
  target_type = "blackhole"
}
`),
				ExpectError: regexp.MustCompile(`ROUTE_DUPLICATE`),
			},
		},
	})
}
