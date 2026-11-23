package acctest_test

import (
	"fmt"
	"regexp"
	"testing"

	"h3terraform/internal/acctest"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func internalLBConfig(f *acctest.Fake, whiteIP, eip string) string {
	return f.Config(fmt.Sprintf(`
resource "h3_eip" "a" {
  project_id = %[1]q
  name       = "lb-a"
}

resource "h3_lb" "web" {
  project_id = %[1]q
  name       = "web"
  listener   = [{ port = 80, target_port = 8080 }]
  targets    = ["10.0.0.11"]
  white_ip   = %[2]s
%[3]s
}
`, acctest.Project, whiteIP, eip))
}

// Internal LB: created without a public IP, public IP attached and detached in place.
func TestLB_WhiteIPToggle(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	var eipA string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config: internalLBConfig(f, "false", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_lb.web", "white_ip", "false"),
					resource.TestCheckResourceAttr("h3_lb.web", "vpc_ip", "10.0.0.253"),
					resource.TestCheckResourceAttr("h3_lb.web", "endpoint", "10.0.0.253"),
					resource.TestCheckNoResourceAttr("h3_lb.web", "eip_id"),
					resource.TestCheckNoResourceAttr("h3_lb.web", "external_ip"),
					func(s *terraform.State) error {
						eipA = s.RootModule().Resources["h3_eip.a"].Primary.ID
						return nil
					},
				),
			},
			{Config: internalLBConfig(f, "false", ""), PlanOnly: true},
			{
				// on with a chosen EIP: in place
				Config: internalLBConfig(f, "true", "  eip_id = h3_eip.a.id"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_lb.web", plancheck.ResourceActionUpdate),
					plancheck.ExpectUnknownValue("h3_lb.web", tfjsonpath.New("external_ip")),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_lb.web", "white_ip", "true"),
					resource.TestCheckResourceAttrPair("h3_lb.web", "eip_id", "h3_eip.a", "id"),
					resource.TestCheckResourceAttrPair("h3_lb.web", "external_ip", "h3_eip.a", "ip_address"),
					resource.TestCheckResourceAttr("h3_lb.web", "vpc_ip", "10.0.0.253"),
				),
			},
			{
				// off: the EIP is detached and stays in the project
				Config: internalLBConfig(f, "false", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_lb.web", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_lb.web", "white_ip", "false"),
					resource.TestCheckNoResourceAttr("h3_lb.web", "eip_id"),
					func(*terraform.State) error {
						if a := f.Get("eips", eipA); a == nil || a["resourceId"] != "" {
							return fmt.Errorf("the EIP must stay in the project, unbound: %v", a)
						}
						return nil
					},
				),
			},
			{
				// on without eip_id: a new EIP is allocated and exported
				Config: internalLBConfig(f, "true", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_lb.web", "white_ip", "true"),
					resource.TestCheckResourceAttrSet("h3_lb.web", "eip_id"),
					resource.TestCheckResourceAttr("h3_lb.web", "external_ip", "203.0.113.77"),
				),
			},
			{Config: internalLBConfig(f, "true", ""), PlanOnly: true},
		},
	})
}

func TestLB_WhiteIPFalseRejectsEIP(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{{
			Config:      internalLBConfig(f, "false", "  eip_id = h3_eip.a.id"),
			ExpectError: regexp.MustCompile(`eip_id requires white_ip`),
		}},
	})
}
