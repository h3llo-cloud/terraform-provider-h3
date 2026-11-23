package sg_test

import (
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"h3terraform/internal/provider"
	"h3terraform/internal/services/sg"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func providers() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"h3": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
}

func cfg(endpoint, body string) string {
	return fmt.Sprintf(`
provider "h3" {
  api_endpoint = %q
  key_id       = "test-key"
  secret_key   = "test-secret"
  project_id   = %q
}
`, endpoint, sg.TestProject) + body
}

// Inline rules written the "lazy" way (no action/priority, bare address) must
// not produce a diff after the server canonicalises them.
func TestSecurityGroupInlineRulesLifecycle(t *testing.T) {
	fake := sg.NewFakeOVN()
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()

	web := func(extra string) string {
		return cfg(srv.URL, fmt.Sprintf(`
resource "h3_security_group" "web" {
  project_id  = %q
  name        = "web"
  description = "web servers"

  rule {
    direction   = "ingress"
    protocol    = "tcp"
    port_from   = 22
    remote_cidr = "203.0.113.7"
  }
  rule {
    direction   = "ingress"
    protocol    = "tcp"
    port_from   = 443
    remote_cidr = "0.0.0.0/0"
  }
  rule {
    direction   = "egress"
    remote_cidr = "0.0.0.0/0"
  }
%s
}
`, sg.TestProject, extra))
	}
	addr := "h3_security_group.web"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				Config: web(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", "web"),
					resource.TestCheckResourceAttr(addr, "is_default", "false"),
					resource.TestCheckResourceAttr(addr, "status", "ACTIVE"),
					resource.TestCheckResourceAttr(addr, "rule.#", "3"),
					resource.TestCheckResourceAttr(addr, "rule.0.remote_cidr", "203.0.113.7"),
				),
			},
			{ // re-plan: no diff despite server defaults / canonical CIDR
				Config:   web(""),
				PlanOnly: true,
			},
			{ // add a rule: in-place update, ids of unchanged rules are kept
				Config: web(`  rule {
    direction = "ingress"
    protocol  = "icmp"
    remote_cidr = "10.0.0.0/8"
  }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(addr, "rule.#", "4"),
			},
			{ // import by id
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"rule", "revoke_default_egress"},
			},
		},
	})
	for _, g := range fake.Groups() {
		if g.Name == "web" {
			t.Fatal("group must be deleted on destroy")
		}
	}
}

// Separate rule resources on a group without inline rules, VM attachment and
// the data source; destroy puts the VM back on the default group.
func TestSecurityGroupRulesAttachmentAndDataSource(t *testing.T) {
	fake := sg.NewFakeOVN()
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()

	body := cfg(srv.URL, fmt.Sprintf(`
data "h3_security_group" "default" {
  project_id = %[1]q
  name       = "default"
}

resource "h3_security_group" "db" {
  project_id            = %[1]q
  name                  = "db"
  revoke_default_egress = true
}

resource "h3_security_group_rule" "pg" {
  security_group_id        = h3_security_group.db.id
  direction                = "ingress"
  protocol                 = "tcp"
  port_from                = 5432
  remote_security_group_id = data.h3_security_group.default.id
}

resource "h3_security_group_rule" "ssh" {
  security_group_id = h3_security_group.db.id
  direction         = "ingress"
  protocol          = "tcp"
  port_from         = 22
  remote_cidr       = "198.51.100.10"
  priority          = 10
  description       = "bastion"
}

resource "h3_vm_security_groups" "vm" {
  vm_id              = %[2]q
  security_group_ids = [data.h3_security_group.default.id, h3_security_group.db.id]
}
`, sg.TestProject, sg.TestVM))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		CheckDestroy: func(*terraform.State) error {
			got := fake.VMGroups(sg.TestVM)
			if len(got) != 1 || got[0] != fake.DefaultID() {
				return fmt.Errorf("VM must be back on the default group, got %v", got)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: body,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.h3_security_group.default", "is_default", "true"),
					resource.TestCheckResourceAttr("data.h3_security_group.default", "rules.#", "4"),
					resource.TestCheckResourceAttrSet("h3_security_group_rule.pg", "id"),
					resource.TestCheckResourceAttr("h3_security_group_rule.ssh", "action", "allow"),
					resource.TestCheckResourceAttr("h3_security_group_rule.ssh", "remote_cidr", "198.51.100.10"),
					resource.TestCheckResourceAttr("h3_vm_security_groups.vm", "security_group_ids.#", "2"),
					resource.TestCheckResourceAttr("h3_vm_security_groups.vm", "foreign_groups.0", "sg-project-x"),
					func(*terraform.State) error {
						for _, g := range fake.Groups() {
							if g.Name == "db" && len(g.Rules) != 2 {
								return fmt.Errorf("db group must hold exactly the 2 rule resources, got %d", len(g.Rules))
							}
						}
						return nil
					},
				),
			},
			{Config: body, PlanOnly: true},
			{
				ResourceName:      "h3_security_group_rule.ssh",
				ImportState:       true,
				ImportStateVerify: true,
				// Import reads the server's canonical form (bare address -> /32,
				// single port -> port_to = port_from); config and import are
				// equivalent and plan stays empty.
				ImportStateVerifyIgnore: []string{"remote_cidr", "port_to"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					r := s.RootModule().Resources["h3_security_group_rule.ssh"].Primary
					return r.Attributes["security_group_id"] + "/" + r.ID, nil
				},
			},
		},
	})
	for _, c := range fake.Calls() {
		if strings.HasPrefix(c, "DELETE") && strings.Contains(c, fake.DefaultID()) {
			t.Fatalf("the default group must never be deleted: %s", c)
		}
	}
}

func TestVMSecurityGroupsEmptyDetachesAndTooManyRejected(t *testing.T) {
	fake := sg.NewFakeOVN()
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()
	vmCfg := func(ids string) string {
		return cfg(srv.URL, fmt.Sprintf(`
resource "h3_vm_security_groups" "vm" {
  vm_id              = %q
  security_group_ids = [%s]
}
`, sg.TestVM, ids))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				Config:      vmCfg(`"a", "b", "c", "d", "e", "f"`),
				ExpectError: regexp.MustCompile(`0 to 5 groups`),
			},
			{
				// [] = detached (h3ovn: no automatic default for a detached VM).
				Config: vmCfg(""),
				Check: func(*terraform.State) error {
					if g := fake.VMGroups(sg.TestVM); len(g) != 0 {
						return fmt.Errorf("VM groups %v, want none", g)
					}
					return nil
				},
			},
			{Config: vmCfg(""), PlanOnly: true},
		},
		CheckDestroy: func(*terraform.State) error {
			if g := fake.VMGroups(sg.TestVM); len(g) != 1 || g[0] != fake.DefaultID() {
				return fmt.Errorf("after destroy VM groups %v, want [default]", g)
			}
			return nil
		},
	})
}

func attachCfg(endpoint string, groups ...string) string {
	b := fmt.Sprintf(`
resource "h3_security_group" "a" {
  project_id = %[1]q
  name       = "a"
  rule {
    direction   = "ingress"
    protocol    = "tcp"
    port_from   = 22
    port_to     = 22
    remote_cidr = "10.0.0.0/8"
  }
}

resource "h3_security_group" "b" {
  project_id = %[1]q
  name       = "b"
  rule {
    direction   = "ingress"
    protocol    = "tcp"
    port_from   = 443
    port_to     = 443
    remote_cidr = "0.0.0.0/0"
  }
}
`, sg.TestProject)
	for _, g := range groups {
		b += fmt.Sprintf(`
resource "h3_vm_security_group_attachment" "%[1]s" {
  vm_id             = %[2]q
  security_group_id = h3_security_group.%[1]s.id
}
`, g, sg.TestVM)
	}
	return cfg(endpoint, b)
}

// Non-exclusive attachments: each adds its group and keeps the others; with
// and without the point API (fallback: read-modify-write).
func TestVMSecurityGroupAttachment(t *testing.T) {
	sg.FastPointRetry()
	for _, point := range []bool{true, false} {
		t.Run(fmt.Sprintf("point=%t", point), func(t *testing.T) {
			fake := sg.NewFakeOVN()
			fake.SetPointAPI(point)
			if point {
				fake.PointConflicts(2) // retried
			}
			srv := httptest.NewServer(fake.Handler())
			defer srv.Close()
			def := fake.DefaultID()
			has := func(want ...string) resource.TestCheckFunc {
				return func(*terraform.State) error {
					got := map[string]bool{}
					for _, id := range fake.VMGroups(sg.TestVM) {
						got[id] = true
					}
					if len(got) != len(want) {
						return fmt.Errorf("VM groups %v, want %d", fake.VMGroups(sg.TestVM), len(want))
					}
					for _, w := range want {
						if !got[w] {
							return fmt.Errorf("VM groups %v miss %s", fake.VMGroups(sg.TestVM), w)
						}
					}
					return nil
				}
			}
			groupID := func(s *terraform.State, n string) string {
				return s.RootModule().Resources["h3_security_group."+n].Primary.ID
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: providers(),
				Steps: []resource.TestStep{
					{
						Config: attachCfg(srv.URL, "a", "b"),
						Check: func(s *terraform.State) error {
							return has(def, groupID(s, "a"), groupID(s, "b"))(s)
						},
					},
					{Config: attachCfg(srv.URL, "a", "b"), PlanOnly: true},
					{
						ResourceName:      "h3_vm_security_group_attachment.a",
						ImportState:       true,
						ImportStateVerify: true,
						ImportStateIdFunc: func(s *terraform.State) (string, error) {
							return sg.TestVM + "/" + groupID(s, "a"), nil
						},
					},
					{
						// Removing one attachment keeps the other and default.
						Config: attachCfg(srv.URL, "a"),
						Check: func(s *terraform.State) error {
							return has(def, groupID(s, "a"))(s)
						},
					},
				},
				CheckDestroy: func(*terraform.State) error {
					if g := fake.VMGroups(sg.TestVM); len(g) != 1 || g[0] != def {
						return fmt.Errorf("after destroy VM groups %v, want [default] only", g)
					}
					return nil
				},
			})
		})
	}
}
