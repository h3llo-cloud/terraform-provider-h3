package acctest_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"h3terraform/internal/acctest"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// h3_eip.description: set on create, changed and removed in place (PATCH), never
// replaces the EIP (same id and address); visible in h3_eip and h3_eips.
func TestEIP_DescriptionInPlace(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	cfg := func(descLine string) string {
		return f.Config(fmt.Sprintf(`
resource "h3_eip" "web" {
  project_id = %[1]q
  name       = "web-ip"
%[2]s
}

data "h3_eip" "byid" {
  id = h3_eip.web.id
}

data "h3_eips" "all" {
  project_id = %[1]q
  depends_on = [h3_eip.web]
}
`, acctest.Project, descLine))
	}
	var firstID string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config: cfg(`  description = "фронт прод"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_eip.web", "description", "фронт прод"),
					resource.TestCheckResourceAttr("data.h3_eip.byid", "description", "фронт прод"),
					resource.TestCheckResourceAttr("data.h3_eips.all", "eips.0.description", "фронт прод"),
					resource.TestCheckResourceAttrWith("h3_eip.web", "id", func(v string) error { firstID = v; return nil }),
				),
			},
			{
				Config:      cfg(`  description = "line1\nline2"`),
				ExpectError: regexp.MustCompile(`Invalid EIP description`),
			},
			{
				Config: cfg(`  description = "кэш"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_eip.web", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_eip.web", "description", "кэш"),
					resource.TestCheckResourceAttrWith("h3_eip.web", "id", func(v string) error {
						if v != firstID {
							return fmt.Errorf("EIP replaced: %s -> %s", firstID, v)
						}
						return nil
					}),
				),
			},
			{
				// Removing the argument clears the comment in place.
				Config: cfg(""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_eip.web", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("h3_eip.web", "description"),
					resource.TestCheckNoResourceAttr("data.h3_eip.byid", "description"),
				),
			},
			{Config: cfg(""), PlanOnly: true},
			{ResourceName: "h3_eip.web", ImportState: true, ImportStateVerify: true},
		},
	})
	if n := len(f.Calls("PATCH /api/ovn/v1/eips/")); n != 2 {
		t.Fatalf("PATCH calls = %d, want 2", n)
	}
}

func TestCheckEIPDescription(t *testing.T) {
	t.Parallel()
	for in, ok := range map[string]bool{
		"":                       true,
		"веб 🚀":                  true,
		" lead":                  false,
		"trail ":                 false,
		"a\tb":                   false,
		"a\nb":                   false,
		strings.Repeat("я", 255): true,
		strings.Repeat("я", 256): false,
	} {
		if got := common.CheckEIPDescription(in) == ""; got != ok {
			t.Errorf("%q: valid=%v, want %v", in, got, ok)
		}
	}
}
