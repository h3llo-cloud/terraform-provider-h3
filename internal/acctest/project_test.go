package acctest_test

import (
	"fmt"
	"regexp"
	"testing"

	"h3terraform/internal/acctest"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

const otherProject = "00000000-0000-4000-8000-0000000000cc"

// Provider default project: used on create, kept afterwards (changing the
// provider default does not replace anything), explicit project_id wins.
func TestProject_ProviderDefault(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	eip := `
resource "h3_eip" "web" {
  name = "web-ip"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config:      f.Config(eip),
				ExpectError: regexp.MustCompile(`Missing project_id`),
			},
			{
				Config: f.ConfigWithProject(acctest.Project, eip),
				Check:  resource.TestCheckResourceAttr("h3_eip.web", "project_id", acctest.Project),
			},
			{Config: f.ConfigWithProject(acctest.Project, eip), PlanOnly: true},
			{
				// Another provider default: existing resources stay where they are.
				Config: f.ConfigWithProject(otherProject, eip),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_eip.web", plancheck.ResourceActionNoop),
				}},
				Check: resource.TestCheckResourceAttr("h3_eip.web", "project_id", acctest.Project),
			},
			{
				// Same project written explicitly: no change.
				Config: f.ConfigWithProject(otherProject, fmt.Sprintf(`
resource "h3_eip" "web" {
  project_id = %q
  name       = "web-ip"
}
`, acctest.Project)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_eip.web", plancheck.ResourceActionNoop),
				}},
			},
			{
				// A different explicit project replaces (objects cannot move).
				Config: f.ConfigWithProject(acctest.Project, fmt.Sprintf(`
resource "h3_eip" "web" {
  project_id = %q
  name       = "web-ip"
}
`, otherProject)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_eip.web", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.TestCheckResourceAttr("h3_eip.web", "project_id", otherProject),
			},
		},
	})
}

// Data sources h3_vpc, h3_network, h3_eip, h3_eips (the EIP selector).
func TestNet_DataSources(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	base := `
resource "h3_vpc" "main" {
  name = "main"
}

resource "h3_network" "app" {
  name       = "app"
  vpc_id     = h3_vpc.main.id
  cidr_block = "10.1.0.0/24"
}

resource "h3_eip" "a" {
  name = "a-ip"
}

resource "h3_eip" "b" {
  name  = "b-ip"
  vm_id = "33333333-0000-4000-8000-000000000001"
}
`
	lookups := `
data "h3_vpc" "by_project" {}

data "h3_network" "by_name" {
  name = "app"
}

data "h3_eip" "by_ip" {
  ip_address = h3_eip.a.ip_address
}

data "h3_eips" "free" {
  bound = false
}

data "h3_eips" "all" {}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{Config: f.ConfigWithProject(acctest.Project, base)},
			{
				Config: f.ConfigWithProject(acctest.Project, base+lookups),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.h3_vpc.by_project", "id", acctest.Project),
					resource.TestCheckResourceAttrPair("data.h3_network.by_name", "id", "h3_network.app", "id"),
					resource.TestCheckResourceAttrPair("data.h3_network.by_name", "subnet_name", "h3_network.app", "subnet_name"),
					resource.TestCheckResourceAttrPair("data.h3_eip.by_ip", "id", "h3_eip.a", "id"),
					resource.TestCheckResourceAttr("data.h3_eip.by_ip", "bound", "false"),
					resource.TestCheckResourceAttr("data.h3_eips.free", "ids.#", "1"),
					resource.TestCheckResourceAttrPair("data.h3_eips.free", "ids.0", "h3_eip.a", "id"),
					resource.TestCheckResourceAttr("data.h3_eips.all", "eips.#", "2"),
					resource.TestCheckResourceAttr("data.h3_eips.all", "eips.1.bound", "true"),
					resource.TestCheckResourceAttr("data.h3_eips.all", "eips.1.bound_resource_type", "vm"),
				),
			},
			{
				Config:      f.ConfigWithProject(acctest.Project, base+`data "h3_eip" "none" { name = "nope" }`),
				ExpectError: regexp.MustCompile(`no EIP matches`),
			},
		},
	})
}
