package acctest_test

import (
	"fmt"
	"strings"
	"testing"

	"h3terraform/internal/acctest"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// 0.3.0 rename: h3_ovn_vpc/h3_ovn_network/h3_ovn_eip -> h3_vpc/h3_network/h3_eip.
// State made with the old names moves to the new ones without any API call
// that creates or deletes something.

func renameConfig(prefix, extra string) string {
	return fmt.Sprintf(`
resource "h3_%[1]svpc" "main" {
  project_id = %[2]q
  name       = "main"
}

resource "h3_%[1]snetwork" "app" {
  project_id = %[2]q
  name       = "app"
  vpc_id     = h3_%[1]svpc.main.id
  cidr_block = "10.1.0.0/24"
}

resource "h3_%[1]seip" "web" {
  project_id = %[2]q
  name       = "web-ip"
}
%[3]s`, prefix, acctest.Project, extra)
}

func noWrites(f *acctest.Fake) func() error {
	return func() error {
		for _, m := range []string{"POST ", "DELETE ", "PATCH ", "PUT "} {
			if c := f.Calls(m); len(c) > 0 {
				return fmt.Errorf("unexpected API writes during the move: %v", c)
			}
		}
		return nil
	}
}

// moved {} across resource types needs Terraform >= 1.8 (MoveResourceState).
func TestRename_MovedBlock(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	var writesBefore int
	moved := `
moved {
  from = h3_ovn_vpc.main
  to   = h3_vpc.main
}
moved {
  from = h3_ovn_network.app
  to   = h3_network.app
}
moved {
  from = h3_ovn_eip.web
  to   = h3_eip.web
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_8_0)},
		Steps: []resource.TestStep{
			{
				Config: f.Config(renameConfig("ovn_", "")),
				Check: func(*terraform.State) error {
					writesBefore = len(f.Calls("POST ")) + len(f.Calls("DELETE "))
					return nil
				},
			},
			{
				Config: f.Config(renameConfig("", moved)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_vpc.main", plancheck.ResourceActionNoop),
					plancheck.ExpectResourceAction("h3_network.app", plancheck.ResourceActionNoop),
					plancheck.ExpectResourceAction("h3_eip.web", plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_vpc.main", "id", acctest.Project),
					resource.TestCheckResourceAttrSet("h3_network.app", "subnet_id"),
					resource.TestCheckResourceAttrSet("h3_eip.web", "ip_address"),
					func(*terraform.State) error {
						if n := len(f.Calls("POST ")) + len(f.Calls("DELETE ")); n != writesBefore {
							return fmt.Errorf("the move created or deleted objects: %d writes, want %d", n, writesBefore)
						}
						return nil
					},
				),
			},
			{Config: f.Config(renameConfig("", "")), PlanOnly: true},
		},
	})
}

// Terraform 1.7 / OpenTofu: removed {} + import {} instead of moved {}.
func TestRename_RemovedAndImport(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	steps := make([]resource.TestStep, 2)
	steps[0] = resource.TestStep{
		Config: f.Config(renameConfig("ovn_", "")),
		// The import ids are known only now: build the next step's config.
		Check: func(s *terraform.State) error {
			var b strings.Builder
			for _, r := range [][2]string{{"vpc", "main"}, {"network", "app"}, {"eip", "web"}} {
				id := s.RootModule().Resources["h3_ovn_"+r[0]+"."+r[1]].Primary.ID
				fmt.Fprintf(&b, "removed {\n  from = h3_ovn_%[1]s.%[2]s\n  lifecycle {\n    destroy = false\n  }\n}\nimport {\n  to = h3_%[1]s.%[2]s\n  id = %[3]q\n}\n", r[0], r[1], id)
			}
			steps[1].Config = f.Config(renameConfig("", b.String()))
			f.ResetCalls()
			return nil
		},
	}
	steps[1] = resource.TestStep{
		Config: "# replaced by step 1",
		ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
			plancheck.ExpectResourceAction("h3_vpc.main", plancheck.ResourceActionNoop),
			plancheck.ExpectResourceAction("h3_network.app", plancheck.ResourceActionNoop),
			plancheck.ExpectResourceAction("h3_eip.web", plancheck.ResourceActionNoop),
		}},
		Check: func(*terraform.State) error { return noWrites(f)() },
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_7_0)},
		Steps:                    steps,
	})
}
