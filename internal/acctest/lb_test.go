package acctest_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"h3terraform/internal/acctest"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func lbConfig(f *acctest.Fake, targets, extra string) string {
	return f.Config(fmt.Sprintf(`
resource "h3_lb" "web" {
  project_id = %[1]q
  name       = "web"
  listener = [
    { port = 80, target_port = 8080 },
    { port = 443, target_port = 8443 },
  ]
  targets = [%[2]s]
%[3]s
}
`, acctest.Project, targets, extra))
}

func TestLB_CreateReplaceImportDataSources(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	two := `"10.0.0.11", "10.0.0.12"`
	ds := fmt.Sprintf(`
data "h3_lb" "by_name" {
  project_id = %[1]q
  name       = h3_lb.web.name
}

data "h3_lb" "by_id" {
  project_id = %[1]q
  id         = h3_lb.web.id
}

data "h3_lbs" "all" {
  project_id = %[1]q
  depends_on = [h3_lb.web]
}
`, acctest.Project)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy: func(*terraform.State) error {
			if n := f.Count("lbs"); n != 0 {
				return fmt.Errorf("%d load balancers left", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: lbConfig(f, two, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_lb.web", "status", "RUNNING"),
					resource.TestCheckResourceAttr("h3_lb.web", "external_ip", "203.0.113.10"),
					resource.TestCheckResourceAttr("h3_lb.web", "listener.#", "2"),
					resource.TestCheckResourceAttr("h3_lb.web", "listener.1.target_port", "8443"),
					resource.TestCheckResourceAttr("h3_lb.web", "listener.0.protocol", "TCP"),
					resource.TestCheckResourceAttr("h3_lb.web", "targets.#", "2"),
					resource.TestCheckResourceAttr("h3_lb.web", "algorithm", "round_robin"),
					resource.TestCheckResourceAttr("h3_lb.web", "subnet_name", "subnet-default"),
				),
			},
			{Config: lbConfig(f, two, ""), PlanOnly: true},
			{
				Config: lbConfig(f, two, "") + ds,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.h3_lb.by_name", "id", "h3_lb.web", "id"),
					resource.TestCheckResourceAttr("data.h3_lb.by_id", "name", "web"),
					resource.TestCheckResourceAttr("data.h3_lb.by_id", "listener.0.port", "80"),
					resource.TestCheckResourceAttr("data.h3_lbs.all", "load_balancers.#", "1"),
					resource.TestCheckResourceAttr("data.h3_lbs.all", "load_balancers.0.targets.#", "2"),
				),
			},
			{
				ResourceName:      "h3_lb.web",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return acctest.Project + "/" + s.RootModule().Resources["h3_lb.web"].Primary.ID, nil
				},
			},
			{
				// No update in h3lb: a target change is an explicit replacement.
				Config: lbConfig(f, `"10.0.0.11", "10.0.0.12", "10.0.0.13"`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_lb.web", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.TestCheckResourceAttr("h3_lb.web", "targets.#", "3"),
			},
		},
	})
}

func TestLB_ValidateConfig(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	cases := []struct{ cfg, err string }{
		{lbConfig(f, `"10.0.0.11"`, `  eip_id = "not-a-uuid"`), `not a valid UUID`},
		{f.Config(fmt.Sprintf(`
resource "h3_lb" "dns" {
  project_id = %q
  name       = "dns"
  listener   = [{ port = 53, target_port = 53, protocol = "UDP" }]
  targets    = ["10.0.0.11"]
}
`, acctest.Project)), `not supported by the current LB backend`},
		{lbConfig(f, `"8.8.8.8"`, ""), `not in a private range`},
		{lbConfig(f, `"10.0.0.11"`, `  algorithm = "least_conn"`), `only round_robin`},
		{f.Config(fmt.Sprintf(`
resource "h3_lb" "x" {
  project_id = %q
  name       = "Bad_Name"
  listener   = [{ port = 80, target_port = 80 }, { port = 80, target_port = 81 }]
  targets    = ["10.0.0.11"]
}
`, acctest.Project)), `(?s)DNS label.*Duplicate port|(?s)Duplicate port.*DNS label`},
	}
	for i, c := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: acctest.Providers(),
				Steps:                    []resource.TestStep{{Config: c.cfg, PlanOnly: true, ExpectError: regexp.MustCompile(c.err)}},
			})
		})
	}
	if n := len(f.Calls("POST ")); n != 0 {
		t.Fatalf("validation errors must not reach the API, got %d POSTs", n)
	}
}

// A load balancer that fails stays in state (tainted) and is replaced next time.
func TestLB_FailedCreateIsTracked(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{{
			Config:      strings.Replace(lbConfig(f, `"10.0.0.11"`, ""), `name       = "web"`, `name       = "fail"`, 1),
			ExpectError: regexp.MustCompile(`FAILED`),
		}},
	})
	if f.Count("lbs") != 0 {
		t.Fatalf("the failed load balancer must be deleted by the test destroy, %d left", f.Count("lbs"))
	}
}

func TestLB_AccessDenied(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	f.Hook("POST /api/loadbalancers/v1", func(w http.ResponseWriter, _ *acctest.Req) bool {
		acctest.WriteJSON(w, 401, acctest.Obj{"error": "missing X-User-Id"})
		return true
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{{
			Config:      lbConfig(f, `"10.0.0.11"`, ""),
			ExpectError: regexp.MustCompile(`(?s)accepts only\s+console sessions|accepts only console`),
		}},
	})
}

func eipConfig(f *acctest.Fake, eipRef, extra string) string {
	return f.Config(fmt.Sprintf(`
resource "h3_eip" "a" {
  project_id = %[1]q
  name       = "lb-a"
}

resource "h3_eip" "b" {
  project_id = %[1]q
  name       = "lb-b"
}

resource "h3_lb" "web" {
  project_id = %[1]q
  name       = "web"
  listener   = [{ port = 80, target_port = 8080 }]
  targets    = ["10.0.0.11"]
  eip_id     = %[2]s
%[3]s
}
`, acctest.Project, eipRef, extra))
}

// LB Basic: create on a chosen EIP, move to another EIP in place, keep it on destroy.
func TestLB_EIPSelectMoveKeep(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	var eipA, eipB string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy: func(*terraform.State) error {
			if n := f.Count("lbs"); n != 0 {
				return fmt.Errorf("%d load balancers left", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: eipConfig(f, "h3_eip.a.id", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("h3_lb.web", "eip_id", "h3_eip.a", "id"),
					resource.TestCheckResourceAttrPair("h3_lb.web", "external_ip", "h3_eip.a", "ip_address"),
					resource.TestCheckResourceAttr("h3_lb.web", "release_eip_on_destroy", "false"),
					func(s *terraform.State) error {
						eipA = s.RootModule().Resources["h3_eip.a"].Primary.ID
						eipB = s.RootModule().Resources["h3_eip.b"].Primary.ID
						return nil
					},
				),
			},
			{Config: eipConfig(f, "h3_eip.a.id", ""), PlanOnly: true},
			{
				// in place: PATCH eipId, no replacement, the address follows the EIP
				Config: eipConfig(f, "h3_eip.b.id", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_lb.web", plancheck.ResourceActionUpdate),
					plancheck.ExpectUnknownValue("h3_lb.web", tfjsonpath.New("external_ip")),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("h3_lb.web", "eip_id", "h3_eip.b", "id"),
					resource.TestCheckResourceAttrPair("h3_lb.web", "external_ip", "h3_eip.b", "ip_address"),
					func(*terraform.State) error {
						if n := len(f.Calls("PATCH /api/loadbalancers/v1/")); n != 1 {
							return fmt.Errorf("want 1 PATCH, got %d", n)
						}
						if a := f.Get("eips", eipA); a == nil || a["resourceId"] != "" {
							return fmt.Errorf("previous EIP must be unbound and stay in the project: %v", a)
						}
						return nil
					},
				),
			},
			{
				// destroy only the LB: its EIP stays in the project, unbound
				Config: f.Config(fmt.Sprintf(`
resource "h3_eip" "a" {
  project_id = %[1]q
  name       = "lb-a"
}

resource "h3_eip" "b" {
  project_id = %[1]q
  name       = "lb-b"
}
`, acctest.Project)),
				Check: func(*terraform.State) error {
					b := f.Get("eips", eipB)
					if b == nil || b["resourceId"] != "" {
						return fmt.Errorf("the LB EIP must stay in the project, unbound: %v", b)
					}
					return nil
				},
			},
		},
	})
}

func TestLB_EIPReleaseOnDestroy(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	var releaseQuery []string
	f.Hook("DELETE /api/loadbalancers/v1/", func(_ http.ResponseWriter, q *acctest.Req) bool {
		releaseQuery = append(releaseQuery, q.Query("releaseEip"))
		return false // the fake handler deletes it
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{Config: eipConfig(f, "h3_eip.a.id", "  release_eip_on_destroy = true")},
		},
	})
	if len(releaseQuery) != 1 || releaseQuery[0] != "true" {
		t.Fatalf("LB delete must send releaseEip=true, got %v", releaseQuery)
	}
}

func TestLB_EIPAlreadyBound(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	f.Hook("POST /api/loadbalancers/v1", func(w http.ResponseWriter, _ *acctest.Req) bool {
		acctest.WriteJSON(w, 409, acctest.Obj{"error": "Conflict", "code": "EIP_ALREADY_BOUND",
			"errors": []acctest.Obj{{"field": "eipId", "message": "EIP is bound to another resource"}}})
		return true
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{{
			Config:      eipConfig(f, "h3_eip.a.id", ""),
			ExpectError: regexp.MustCompile(`Elastic IP already in use`),
		}},
	})
}

// An eip_id that is not an EIP of the project: h3lb answers 400 EIP_INVALID (as h3vm/h3db).
func TestLB_EIPNotInProject(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{{
			Config:      lbConfig(f, `"10.0.0.11"`, `  eip_id = "11111111-0000-4000-8000-000000000099"`),
			ExpectError: regexp.MustCompile(`(?s)Error creating load balancer.*EIP_INVALID`),
		}},
	})
	if f.Count("lbs") != 0 {
		t.Fatal("no load balancer must be created")
	}
}
