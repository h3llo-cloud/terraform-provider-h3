package acctest_test

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"h3terraform/internal/acctest"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func vmConfig(f *acctest.Fake, cpu int, memory, extra string) string {
	return f.Config(fmt.Sprintf(`
resource "h3_vm" "a" {
  project_id = %q
  name       = "web"
  cpu        = %d
  memory     = %q
  image      = "ubuntu:24.04"
  ssh_key_ids = [%q]
%s
}
`, acctest.Project, cpu, memory, acctest.SSHKey1, extra))
}

func noVMs(f *acctest.Fake) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if n := f.Count("vms"); n != 0 {
			return fmt.Errorf("%d VMs left after destroy", n)
		}
		return nil
	}
}

// R2: a VM without disk_size used to fail with "unknown value for disk_size"
// and was replaced on the next apply. Also checks the empty plan after create,
// in-place CPU/memory change, drift detection and a non-destructive import.
func TestVM_NoDiskSize_UpdateInPlace_Drift_Import(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	const addr = "h3_vm.a"
	var vmID string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy:             noVMs(f),
		Steps: []resource.TestStep{
			{
				Config: vmConfig(f, 2, "4Gi", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "disk_size", "20Gi"),
					resource.TestCheckResourceAttr(addr, "status", "RUNNING"),
					resource.TestCheckResourceAttr(addr, "white_ip", "false"),
					resource.TestCheckResourceAttr(addr, "subnet_name", "subnet-default"),
					resource.TestCheckResourceAttr(addr, "internal_ip", "10.0.0.10"),
					resource.TestCheckResourceAttrWith(addr, "id", func(v string) error { vmID = v; return nil }),
				),
			},
			{
				// The same amount written differently is recorded without an API call.
				Config: vmConfig(f, 2, "4096Mi", ""),
				Check:  resource.TestCheckResourceAttr(addr, "memory", "4096Mi"),
			},
			{
				Config: vmConfig(f, 4, "8Gi", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "cpu", "4"),
					resource.TestCheckResourceAttr(addr, "memory", "8Gi"),
				),
			},
			{
				// Drift: CPU changed in the console is detected and reverted in place.
				PreConfig: func() { f.Mutate("vms", vmID, func(o acctest.Obj) { o["cpu"] = 8 }) },
				Config:    vmConfig(f, 4, "8Gi", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(addr, "cpu", "4"),
			},
			{
				// R3: import fills every argument the API returns, ssh_key_ids included.
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
	if n := len(f.Calls("PATCH /api/vms/v1/")); n != 2 {
		t.Fatalf("PATCH calls = %d, want 2 (resize + drift revert)", n)
	}
}

// After `terraform import` the plan must not replace the VM because of
// arguments the API does not return or SSH keys (ssh_key_ids is read back).
func TestVM_ImportBlockIsNotDestructive(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	id := f.SeedVM("web")
	cfg := vmConfig(f, 2, "4Gi", "") + fmt.Sprintf(`
import {
  to = h3_vm.a
  id = %q
}
`, id)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				// ssh_key_ids is read back, so the import leaves nothing to change.
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_vm.a", plancheck.ResourceActionNoop),
				}},
			},
			{Config: cfg, PlanOnly: true},
		},
	})
	if n := len(f.Calls("POST")) + len(f.Calls("PATCH")) + len(f.Calls("DELETE")); n != 1 {
		// the only mutating call is the final destroy
		t.Fatalf("import changed the VM: %v", append(f.Calls("POST"), f.Calls("PATCH")...))
	}
}

// R6: white_ip = true, then a CPU change failed with
// "inconsistent result: white_ip was true, but now false".
func TestVM_WhiteIP_CPUChange(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	const addr = "h3_vm.a"
	// Emulate the h3vm quirk: white_ip reported false although the FIP exists.
	f.Hook("GET /api/vms/v1/", func(w http.ResponseWriter, q *acctest.Req) bool { return false })
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy:             noVMs(f),
		Steps: []resource.TestStep{
			{
				Config: vmConfig(f, 2, "4Gi", "  white_ip = true"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "white_ip", "true"),
					resource.TestCheckResourceAttr(addr, "endpoint", "203.0.113.50"),
				),
			},
			{
				PreConfig: func() {
					f.Mutate("vms", f.Only("vms"), func(o acctest.Obj) { o["white_ip"] = false })
				},
				Config: vmConfig(f, 4, "4Gi", "  white_ip = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "cpu", "4"),
					resource.TestCheckResourceAttr(addr, "white_ip", "true"),
				),
			},
			{
				// Turning the public IP off is in place: the EIP is detached
				// and stays in the project (0.2 replaced the VM).
				Config: vmConfig(f, 4, "4Gi", "  white_ip = false"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "white_ip", "false"),
					resource.TestCheckResourceAttr(addr, "endpoint", ""),
					resource.TestCheckNoResourceAttr(addr, "eip_id"),
					func(*terraform.State) error {
						if n := f.Count("eips"); n != 1 {
							return fmt.Errorf("%d EIPs, want the detached one kept", n)
						}
						if c := f.Calls("DELETE /api/vms"); len(c) != 0 {
							return fmt.Errorf("VM was deleted: %v", c)
						}
						return nil
					},
				),
			},
			{Config: vmConfig(f, 4, "4Gi", "  white_ip = false"), PlanOnly: true},
			{
				// And on again in place: a new EIP is leased and attached.
				Config: vmConfig(f, 4, "4Gi", "  white_ip = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "white_ip", "true"),
					resource.TestCheckResourceAttrSet(addr, "endpoint"),
					resource.TestCheckResourceAttrSet(addr, "eip_id"),
					func(*terraform.State) error {
						if c := f.Calls("POST /api/vms/v1"); len(c) != 1 {
							return fmt.Errorf("VM created %d times, want 1", len(c))
						}
						return nil
					},
				),
			},
			{Config: vmConfig(f, 4, "4Gi", "  white_ip = true"), PlanOnly: true},
		},
	})
}

// disk_size and image changes replace the VM explicitly (no "Update not
// supported" error during apply).
func TestVM_DiskSizeChangeReplaces(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{Config: vmConfig(f, 2, "4Gi", `  disk_size = "25Gi"`)},
			{
				Config: vmConfig(f, 2, "4Gi", `  disk_size = "25600Mi"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_vm.a", plancheck.ResourceActionUpdate),
				}},
			},
			{
				Config: vmConfig(f, 2, "4Gi", `  disk_size = "30Gi"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_vm.a", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
		},
	})
}

func TestVM_ValidateConfig(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{Config: vmConfig(f, 2, "4Gi", `  ssh_key_id = "k1"`), ExpectError: regexp.MustCompile(`mutually exclusive`)},
			{Config: vmConfig(f, 2, "4Gi", `  ssh_key = "ssh-ed25519 AAAA user@host"`), ExpectError: regexp.MustCompile(`Unsupported argument`)},
			{Config: vmConfig(f, 2, "four", ""), ExpectError: regexp.MustCompile(`Invalid quantity`)},
		},
	})
	if n := len(f.Calls("POST")); n != 0 {
		t.Fatalf("invalid configuration reached the API: %d POSTs", n)
	}
}

// A VM that fails to become ready stays in state (tainted), not orphaned.
func TestVM_FailedCreateIsTracked(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	f.Hook("GET /api/vms/v1/", func(w http.ResponseWriter, q *acctest.Req) bool {
		acctest.WriteJSON(w, 200, acctest.Obj{"id": q.Parts, "status": "FAILED"})
		return true
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{Config: vmConfig(f, 2, "4Gi", ""), ExpectError: regexp.MustCompile(`FAILED`)},
		},
	})
}

// Live resize (h3vm feature/vm-live-resize): an increase of a running VM is
// applied without a restart and waited for (resize.state = applied); with
// allow_restart_on_resize = false a change that needs a restart fails with
// nothing changed; on a backend without live resize false is refused before
// any PATCH (it would restart the VM).
func TestVM_LiveResize(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	const addr = "h3_vm.a"
	noRestart := "  allow_restart_on_resize = false"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy:             noVMs(f),
		Steps: []resource.TestStep{
			{
				Config: vmConfig(f, 2, "4Gi", ""),
				Check:  resource.TestCheckResourceAttr(addr, "allow_restart_on_resize", "true"),
			},
			{
				// Only the flag changes: recorded without a PATCH.
				Config: vmConfig(f, 2, "4Gi", noRestart),
				Check:  resource.TestCheckResourceAttr(addr, "allow_restart_on_resize", "false"),
			},
			{
				Config: vmConfig(f, 4, "8Gi", noRestart),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "cpu", "4"),
					resource.TestCheckResourceAttr(addr, "memory", "8Gi"),
					resource.TestCheckResourceAttr(addr, "status", "RUNNING"),
					func(*terraform.State) error {
						vm := f.Get("vms", f.Only("vms"))
						if vm["_resize_state"] != "applied" {
							return fmt.Errorf("live resize not waited for: state %v", vm["_resize_state"])
						}
						return nil
					},
				),
			},
			{Config: vmConfig(f, 4, "8Gi", noRestart), PlanOnly: true},
			{
				Config:      vmConfig(f, 2, "8Gi", noRestart),
				ExpectError: regexp.MustCompile(`(?s)requires a VM restart.*reason: decrease.*allow_restart_on_resize`),
			},
			{
				Config: vmConfig(f, 2, "8Gi", ""),
				Check:  resource.TestCheckResourceAttr(addr, "cpu", "2"),
			},
			{
				PreConfig:   func() { f.Mutate("vms", f.Only("vms"), func(o acctest.Obj) { o["_no_live_resize"] = true }) },
				Config:      vmConfig(f, 4, "8Gi", noRestart),
				ExpectError: regexp.MustCompile(`Live CPU/memory change is not available`),
			},
		},
	})
	patches := f.Calls("PATCH /api/vms/v1/")
	// increase (live) + refused decrease + decrease with restart; none for the
	// flag-only change and none on the old backend.
	if len(patches) != 3 {
		t.Fatalf("PATCH calls = %d, want 3: %v", len(patches), patches)
	}
}
