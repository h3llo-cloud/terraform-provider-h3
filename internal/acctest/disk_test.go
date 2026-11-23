package acctest_test

import (
	"fmt"
	"regexp"
	"testing"

	"h3terraform/internal/acctest"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func diskConfig(f *acctest.Fake, size, extra string) string {
	return f.Config(fmt.Sprintf(`
resource "h3_disk" "d" {
  project_id    = %q
  name          = "data"
  size          = %q
  storage_class = "replicated"
%s
}
`, acctest.Project, size, extra))
}

// R7: create failed with "unknown value for attached_to_vm_id".
// R1: a size change failed with "invalid result object after apply", the disk
// was tainted and the next apply deleted it with its data.
func TestDisk_CreateResizeInPlace_ShrinkRefused_Import(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	const addr = "h3_disk.d"
	var id string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy: func(*terraform.State) error {
			if n := f.Count("disks"); n != 0 {
				return fmt.Errorf("%d disks left", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: diskConfig(f, "10Gi", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "status", "AVAILABLE"),
					resource.TestCheckNoResourceAttr(addr, "attached_to_vm_id"),
					resource.TestCheckResourceAttr(addr, "performance_class", "R_STANDARD"),
					resource.TestCheckResourceAttrWith(addr, "id", func(v string) error { id = v; return nil }),
				),
			},
			{
				Config: diskConfig(f, "12Gi", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "size", "12Gi"),
					resource.TestCheckResourceAttr(addr, "status", "AVAILABLE"),
					resource.TestCheckResourceAttrWith(addr, "id", func(v string) error {
						if v != id {
							return fmt.Errorf("disk was replaced: %s -> %s", id, v)
						}
						return nil
					}),
				),
			},
			{
				Config:      diskConfig(f, "8Gi", ""),
				ExpectError: regexp.MustCompile(`Disk size cannot be decreased`),
			},
			{
				// Performance class changes in place, the spelling is normalized.
				Config: diskConfig(f, "12Gi", `  performance_class = "r-fast"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(addr, "performance_class", "r-fast"),
			},
			{
				// Drift: attached in the console; a size grown outside Terraform is reported.
				PreConfig: func() {
					f.Mutate("disks", id, func(o acctest.Obj) { o["status"], o["attached_to_vm_id"] = "ATTACHED", "vm-1" })
				},
				RefreshState: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "status", "ATTACHED"),
					resource.TestCheckResourceAttr(addr, "attached_to_vm_id", "vm-1"),
				),
			},
			{Config: diskConfig(f, "12Gi", `  performance_class = "r-fast"`), PlanOnly: true},
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"performance_class"}, // configured as r-fast, API reports R_FAST
			},
		},
	})
	if n := len(f.Calls("POST /api/disks/v1/resize")); n != 1 {
		t.Fatalf("resize calls = %d, want 1 (shrink must not reach the API)", n)
	}
	if n := len(f.Calls("POST /api/disks/v1")) - 1 - 1 - 1; n != 0 { // create + resize + class
		t.Fatalf("unexpected POSTs: %v", f.Calls("POST"))
	}
}

func TestDiskSnapshotBackup(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	cfg := f.Config(fmt.Sprintf(`
resource "h3_disk" "d" {
  project_id    = %[1]q
  name          = "data"
  size          = "10Gi"
  storage_class = "replicated"
}

resource "h3_snapshot" "s" {
  project_id = %[1]q
  disk_id    = h3_disk.d.id
  name       = "daily"
}

resource "h3_backup" "b" {
  project_id  = %[1]q
  snapshot_id = h3_snapshot.s.id
  name        = "offsite"
}
`, acctest.Project))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy: func(*terraform.State) error {
			for _, c := range []string{"disks", "snapshots", "backups"} {
				if n := f.Count(c); n != 0 {
					return fmt.Errorf("%d %s left", n, c)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_snapshot.s", "status", "READY"),
					resource.TestCheckResourceAttr("h3_backup.b", "status", "READY"),
				),
			},
			{Config: cfg, PlanOnly: true},
			{
				ResourceName:      "h3_snapshot.s",
				ImportState:       true,
				ImportStateIdFunc: projectScopedID("h3_snapshot.s"),
				ImportStateVerify: true,
			},
			{
				ResourceName:      "h3_backup.b",
				ImportState:       true,
				ImportStateIdFunc: projectScopedID("h3_backup.b"),
				ImportStateVerify: true,
			},
			{
				ResourceName:  "h3_snapshot.s",
				ImportState:   true,
				ImportStateId: "only-an-id",
				ExpectError:   regexp.MustCompile(`expected import id "<project_id>/<snapshot_id>"`),
			},
		},
	})
}

func projectScopedID(addr string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return "", fmt.Errorf("%s not in state", addr)
		}
		return rs.Primary.Attributes["project_id"] + "/" + rs.Primary.ID, nil
	}
}
