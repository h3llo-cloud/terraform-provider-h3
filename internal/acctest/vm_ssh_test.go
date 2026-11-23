package acctest_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"h3terraform/internal/acctest"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func vmSSHConfig(f *acctest.Fake, keys string) string {
	return f.Config(fmt.Sprintf(`
resource "h3_vm" "a" {
  project_id = %q
  name       = "web"
  cpu        = 2
  memory     = "4Gi"
  image      = "ubuntu:24.04"
%s
}
`, acctest.Project, keys))
}

// ssh_key_ids and the deprecated ssh_key_id change in place (PATCH, no restart,
// no replacement); dropping the argument leaves the keys alone.
func TestVM_SSHKeysInPlace(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	const addr = "h3_vm.a"
	k2 := "33333333-0000-4000-8000-000000000002"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config: vmSSHConfig(f, fmt.Sprintf("  ssh_key_id = %q", acctest.SSHKey1)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "ssh_key_ids.#", "1"),
					resource.TestCheckTypeSetElemAttr(addr, "ssh_key_ids.*", acctest.SSHKey1),
					resource.TestCheckResourceAttr(addr, "ssh_access_managed", "true"),
				),
			},
			{
				// The deprecated single id changes in place too (0.3 replaced the VM).
				Config: vmSSHConfig(f, fmt.Sprintf("  ssh_key_id = %q", k2)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckTypeSetElemAttr(addr, "ssh_key_ids.*", k2),
			},
			{
				// Moving the id into ssh_key_ids only rewrites state: no API call.
				Config: vmSSHConfig(f, fmt.Sprintf("  ssh_key_ids = [%q]", k2)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckNoResourceAttr(addr, "ssh_key_id"),
			},
			{
				Config: vmSSHConfig(f, fmt.Sprintf("  ssh_key_ids = [%q, %q]", acctest.SSHKey1, k2)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "ssh_key_ids.#", "2"),
					func(*terraform.State) error {
						vm := f.Get("vms", f.Only("vms"))
						g := vm["_grants"].(acctest.Obj)[acctest.User].(acctest.Obj)
						if n := len(g["key_ids"].([]any)); n != 2 {
							return fmt.Errorf("the caller's grant has %d keys, want 2", n)
						}
						if vm["_restarts"] != nil {
							return fmt.Errorf("a key change restarted the VM")
						}
						return nil
					},
				),
			},
			{
				// Drift: a key removed from your grant in the console is put back.
				PreConfig: func() {
					f.Mutate("vms", f.Only("vms"), func(o acctest.Obj) {
						o["_grants"].(acctest.Obj)[acctest.User].(acctest.Obj)["key_ids"] = []any{acctest.SSHKey1}
					})
				},
				Config: vmSSHConfig(f, fmt.Sprintf("  ssh_key_ids = [%q, %q]", acctest.SSHKey1, k2)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(addr, "ssh_key_ids.#", "2"),
			},
			{
				// Without the argument Terraform leaves the keys as they are.
				Config: vmSSHConfig(f, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
			},
		},
	})
	if c := f.Calls("POST /api/vms/v1"); len(c) != 1 {
		t.Fatalf("VM created %d times, want 1 (key changes must not replace it)", len(c))
	}
	if n := len(f.Calls("PATCH /api/vms/v1/")); n != 3 {
		t.Fatalf("PATCH calls = %d, want 3", n)
	}
}

// ssh_access_managed changes in place; the fake counts the restart it causes.
func TestVM_SSHAccessManagedToggle(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	const addr = "h3_vm.a"
	keys := fmt.Sprintf("  ssh_key_ids = [%q]\n", acctest.SSHKey1)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{Config: vmSSHConfig(f, keys), Check: resource.TestCheckResourceAttr(addr, "ssh_access_managed", "true")},
			{
				Config: vmSSHConfig(f, keys+"  ssh_access_managed = false"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "ssh_access_managed", "false"),
					resource.TestCheckResourceAttr(addr, "status", "RUNNING"),
					func(*terraform.State) error {
						if r := f.Get("vms", f.Only("vms"))["_restarts"]; r != float64(1) {
							return fmt.Errorf("restarts = %v, want 1", r)
						}
						return nil
					},
				),
			},
			{Config: vmSSHConfig(f, keys+"  ssh_access_managed = false"), PlanOnly: true},
		},
	})
}

func grantConfig(f *acctest.Fake, user, body string) string {
	return vmSSHConfig(f, fmt.Sprintf("  ssh_key_ids = [%q]", acctest.SSHKey1)) + fmt.Sprintf(`
resource "h3_vm_ssh_access" "m" {
  vm_id   = h3_vm.a.id
  user_id = %q
%s
}
`, user, body)
}

func TestVMSSHAccess_GrantModes_Drift_Import(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	const addr = "h3_vm_ssh_access.m"
	k1 := f.SeedSSHKey(acctest.Member, "laptop")
	k2 := f.SeedSSHKey(acctest.Member, "yubikey")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy: func(*terraform.State) error {
			if n := f.Count("vms"); n != 0 {
				return fmt.Errorf("%d VMs left", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: grantConfig(f, acctest.Member, `  mode = "all"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "mode", "all"),
					resource.TestCheckNoResourceAttr(addr, "key_ids"),
					resource.TestCheckResourceAttr(addr, "active", "true"),
					resource.TestCheckResourceAttr(addr, "granted_by", acctest.User),
					resource.TestCheckResourceAttrPair(addr, "vm_id", "h3_vm.a", "id"),
				),
			},
			{
				Config: grantConfig(f, acctest.Member, fmt.Sprintf("  mode = \"selected\"\n  key_ids = [%q, %q]", k1, k2)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(addr, "key_ids.#", "2"),
			},
			{
				// A key deleted in h3ssh leaves the grant: the plan puts it back (and h3vm refuses it).
				PreConfig: func() { f.Remove("sshkeys", k2) },
				Config:    grantConfig(f, acctest.Member, fmt.Sprintf("  mode = \"selected\"\n  key_ids = [%q]", k1)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) { return s.RootModule().Resources[addr].Primary.ID, nil },
				ImportStateVerify: true,
			},
		},
	})
	if c := f.Calls("DELETE /api/vms/v1/"); len(c) < 2 || !strings.Contains(strings.Join(c, " "), "/ssh-access/grants/"+acctest.Member) {
		t.Fatalf("grant was not revoked on destroy: %v", c)
	}
}

func TestVMSSHAccess_Validation(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{Config: grantConfig(f, acctest.Member, "  mode = \"all\"\n  key_ids = [\"k\"]"), ExpectError: regexp.MustCompile(`not allowed with mode`)},
			{Config: grantConfig(f, acctest.Member, `  mode = "selected"`), ExpectError: regexp.MustCompile(`key_ids required`)},
			{Config: grantConfig(f, acctest.Member, `  mode = "some"`), ExpectError: regexp.MustCompile(`is not one of`)},
			{Config: grantConfig(f, acctest.Viewer, `  mode = "all"`), ExpectError: regexp.MustCompile(`read-only role`)},
		},
	})
}

func TestVMSSHAccess_ImportBadID(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config:        grantConfig(f, acctest.Member, `  mode = "all"`),
				ResourceName:  "h3_vm_ssh_access.m",
				ImportState:   true,
				ImportStateId: "no-slash",
				ExpectError:   regexp.MustCompile(`Expected <vm_id>/<user_id>`),
			},
		},
	})
}

// h3vm with VM_SSH_ACCESS_ENABLED off: one key goes as ssh_key_id and works,
// Read survives 503 SSH_ACCESS_DISABLED, ssh_access_managed stays null; two keys
// need the feature and fail with a clear error.
func TestVM_SSHAccessDisabledOnPlatform(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	f.SSHAccessDisabled = true
	const addr = "h3_vm.a"
	one := fmt.Sprintf("  ssh_key_ids = [%q]", acctest.SSHKey1)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config: vmSSHConfig(f, one),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "ssh_key_ids.#", "1"),
					resource.TestCheckNoResourceAttr(addr, "ssh_access_managed"),
					func(*terraform.State) error {
						if c := f.Calls("GET /api/vms/v1/"); len(c) == 0 {
							return fmt.Errorf("no GET calls")
						}
						return nil
					},
				),
			},
			{Config: vmSSHConfig(f, one), PlanOnly: true},
			{
				Config:      vmSSHConfig(f, fmt.Sprintf("  ssh_key_ids = [%q, \"33333333-0000-4000-8000-000000000009\"]", acctest.SSHKey1)),
				ExpectError: regexp.MustCompile(`SSH_ACCESS_DISABLED`),
			},
		},
	})
}

// A live CPU/memory increase together with an ssh_key_ids change goes in one
// PATCH: the keys change and the VM is resized without a restart.
func TestVM_LiveResizeWithSSHKeys(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	const addr = "h3_vm.a"
	k2 := "33333333-0000-4000-8000-000000000002"
	cfg := func(cpu int, mem, keys string) string {
		return f.Config(fmt.Sprintf(`
resource "h3_vm" "a" {
  project_id              = %q
  name                    = "web"
  cpu                     = %d
  memory                  = %q
  image                   = "ubuntu:24.04"
  ssh_key_ids             = [%s]
  allow_restart_on_resize = false
}
`, acctest.Project, cpu, mem, keys))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{Config: cfg(2, "4Gi", fmt.Sprintf("%q", acctest.SSHKey1))},
			{
				Config: cfg(4, "8Gi", fmt.Sprintf("%q, %q", acctest.SSHKey1, k2)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "cpu", "4"),
					resource.TestCheckResourceAttr(addr, "memory", "8Gi"),
					resource.TestCheckResourceAttr(addr, "ssh_key_ids.#", "2"),
					func(*terraform.State) error {
						vm := f.Get("vms", f.Only("vms"))
						if vm["_resize_state"] != "applied" {
							return fmt.Errorf("live resize not waited for: state %v", vm["_resize_state"])
						}
						if vm["_restarts"] != nil {
							return fmt.Errorf("the change restarted the VM")
						}
						return nil
					},
				),
			},
			{Config: cfg(4, "8Gi", fmt.Sprintf("%q, %q", acctest.SSHKey1, k2)), PlanOnly: true},
		},
	})
	if n := len(f.Calls("PATCH /api/vms/v1/")); n != 1 {
		t.Fatalf("PATCH calls = %d, want 1", n)
	}
}
