package acctest_test

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"h3terraform/internal/acctest"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// h3vm TRD 08/09: user_data, deletion_protection, placement groups, custom images.

const cloudConfig = "#cloud-config\npackages: [nginx]\n"

func computeVM(f *acctest.Fake, extra string) string {
	return f.Config(fmt.Sprintf(`
resource "h3_vm_placement_group" "db" {
  project_id  = %[1]q
  name        = "db"
  description = "replicas"
}

resource "h3_vm" "a" {
  project_id  = %[1]q
  name        = "db-1"
  cpu         = 2
  memory      = "4Gi"
  image       = "ubuntu:24.04"
  ssh_key_ids = [%[2]q]
  placement_group_id = h3_vm_placement_group.db.id
%[3]s
}
`, acctest.Project, acctest.SSHKey1, extra))
}

func noComputeLeft(f *acctest.Fake) resource.TestCheckFunc {
	return func(*terraform.State) error {
		for _, c := range []string{"vms", "placement_groups", "custom_images"} {
			if n := f.Count(c); n != 0 {
				return fmt.Errorf("%d %s left", n, c)
			}
		}
		return nil
	}
}

func TestVM_UserDataPlacementDeletionProtection(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	const addr = "h3_vm.a"
	var vmID, groupID string
	ud := fmt.Sprintf("  user_data = %q\n", cloudConfig)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy:             noComputeLeft(f),
		Steps: []resource.TestStep{
			{
				Config: computeVM(f, ud+"  deletion_protection = true\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "user_data", cloudConfig),
					resource.TestCheckNoResourceAttr(addr, "user_data_encoding"),
					resource.TestCheckResourceAttr(addr, "deletion_protection", "true"),
					resource.TestCheckResourceAttrPair(addr, "placement_group_id", "h3_vm_placement_group.db", "id"),
					resource.TestCheckResourceAttr("h3_vm_placement_group.db", "strategy", "spread"),
					resource.TestCheckResourceAttr("h3_vm_placement_group.db", "max_vms", "2"),
					resource.TestCheckResourceAttrWith(addr, "id", func(v string) error { vmID = v; return nil }),
					resource.TestCheckResourceAttrWith("h3_vm_placement_group.db", "id", func(v string) error { groupID = v; return nil }),
					func(*terraform.State) error {
						if got, enc := f.UserData(vmID); got != cloudConfig || enc != "" {
							return fmt.Errorf("API got user_data %q (encoding %q)", got, enc)
						}
						if v := f.Get("vms", vmID)["deletion_protection"]; v != true {
							return fmt.Errorf("deletion_protection in API = %v", v)
						}
						return nil
					},
				),
			},
			// Write-only user_data is kept from state: no diff after refresh; the
			// group now lists the VM.
			{
				Config:   computeVM(f, ud+"  deletion_protection = true\n"),
				PlanOnly: true,
			},
			{
				Config: computeVM(f, ud+"  deletion_protection = true\n"),
				Check:  resource.TestCheckResourceAttr("h3_vm_placement_group.db", "vm_ids.#", "1"),
			},
			// Removing the VM while it is protected fails, nothing is deleted.
			{
				Config: f.Config(fmt.Sprintf(`
resource "h3_vm_placement_group" "db" {
  project_id  = %q
  name        = "db"
  description = "replicas"
}
`, acctest.Project)),
				ExpectError: regexp.MustCompile(`protected from deletion`),
			},
			// Off in place, no restart, no replacement.
			{
				Config: computeVM(f, ud+"  deletion_protection = false\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "deletion_protection", "false"),
					func(*terraform.State) error {
						if v := f.Get("vms", vmID); v["deletion_protection"] != false || v["status"] != "RUNNING" {
							return fmt.Errorf("VM after PATCH: protection %v, status %v", v["deletion_protection"], v["status"])
						}
						return nil
					},
				),
			},
			// A changed user_data replaces the VM (cloud-init runs it only once).
			{
				Config: computeVM(f, fmt.Sprintf("  user_data = %q\n  user_data_encoding = \"base64\"\n",
					base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\necho hi\n")))),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionReplace),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "user_data_encoding", "base64"),
					resource.TestCheckResourceAttrWith(addr, "id", func(v string) error {
						if v == vmID {
							return fmt.Errorf("VM was not replaced")
						}
						vmID = v
						if _, enc := f.UserData(v); enc != "base64" {
							return fmt.Errorf("encoding sent = %q", enc)
						}
						return nil
					}),
				),
			},
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"user_data", "user_data_encoding"},
			},
			{
				ResourceName:      "h3_vm_placement_group.db",
				ImportState:       true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return acctest.Project + "/" + groupID, nil },
				ImportStateVerify: true,
				// vm_ids in state is from before the VM was replaced (refreshed by the next plan).
				ImportStateVerifyIgnore: []string{"vm_ids"},
			},
		},
	})
}

func TestVM_PlacementGroupInPlaceDataSourceAndFull(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	group := func(name, desc string) string {
		return fmt.Sprintf(`
resource "h3_vm_placement_group" "g" {
  name        = %q
  description = %q
}
`, name, desc)
	}
	vm := func(n int) string {
		return fmt.Sprintf(`
resource "h3_vm" "v%[1]d" {
  name        = "app-%[1]d"
  cpu         = 1
  memory      = "2Gi"
  ssh_key_ids = [%[2]q]
  placement_group_id = h3_vm_placement_group.g.id
}
`, n, acctest.SSHKey1)
	}
	var groupID string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy:             noComputeLeft(f),
		Steps: []resource.TestStep{
			{
				Config: f.ConfigWithProject(acctest.Project, group("app", "")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_vm_placement_group.g", "project_id", acctest.Project),
					resource.TestCheckResourceAttr("h3_vm_placement_group.g", "description", ""),
					resource.TestCheckResourceAttr("h3_vm_placement_group.g", "vm_ids.#", "0"),
					resource.TestCheckResourceAttrWith("h3_vm_placement_group.g", "id", func(v string) error { groupID = v; return nil }),
				),
			},
			{
				Config: f.ConfigWithProject(acctest.Project, group("app-spread", "web tier")+`
data "h3_vm_placement_group" "by_name" {
  name = h3_vm_placement_group.g.name
}
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_vm_placement_group.g", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_vm_placement_group.g", "name", "app-spread"),
					resource.TestCheckResourceAttr("h3_vm_placement_group.g", "description", "web tier"),
					resource.TestCheckResourceAttrPair("data.h3_vm_placement_group.by_name", "id", "h3_vm_placement_group.g", "id"),
					resource.TestCheckResourceAttr("data.h3_vm_placement_group.by_name", "strategy", "spread"),
					func(*terraform.State) error {
						if g := f.Get("placement_groups", groupID); g["name"] != "app-spread" {
							return fmt.Errorf("group was not renamed in place: %v", g)
						}
						return nil
					},
				),
			},
			// Bare id import with the provider default project.
			{
				Config:            f.ConfigWithProject(acctest.Project, group("app-spread", "web tier")),
				ResourceName:      "h3_vm_placement_group.g",
				ImportState:       true,
				ImportStateVerify: true,
			},
			// The fake group holds 2 VMs: the third answers PLACEMENT_GROUP_FULL.
			{
				Config:      f.ConfigWithProject(acctest.Project, group("app-spread", "web tier")+vm(1)+vm(2)+vm(3)),
				ExpectError: regexp.MustCompile(`PLACEMENT_GROUP_FULL`),
			},
			{
				Config: f.ConfigWithProject(acctest.Project, group("app-spread", "web tier")+vm(1)+vm(2)),
				Check: func(*terraform.State) error {
					if n := len(f.Get("placement_groups", groupID)["vm_ids"].([]any)); n != 2 {
						return fmt.Errorf("group has %d VMs, want 2", n)
					}
					return nil
				},
			},
		},
	})
}

func imageConfig(f *acctest.Fake, body string) string {
	return f.ConfigWithProject(acctest.Project, body)
}

func TestVMImage_FromURL_VMFromImage_DataSource_Import(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	const img = "h3_vm_image.alma"
	base := `
resource "h3_vm_image" "alma" {
  name     = "alma-9"
  url      = "https://repo.example.org/AlmaLinux-9-GenericCloud.qcow2?X-Amz-Signature=secret"
  size_gi  = 10
  firmware = "efi"
}
`
	withVM := base + fmt.Sprintf(`
resource "h3_vm" "a" {
  name        = "alma"
  cpu         = 2
  memory      = "4Gi"
  image       = h3_vm_image.alma.image
  disk_size   = "%%s"
  ssh_key_ids = [%q]
}

data "h3_vm_image" "by_name" {
  name       = "alma-9"
  depends_on = [h3_vm_image.alma]
}
`, acctest.SSHKey1)
	var imageID string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy:             noComputeLeft(f),
		Steps: []resource.TestStep{
			{
				Config: imageConfig(f, base),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(img, "status", "READY"),
					resource.TestCheckResourceAttr(img, "source_type", "url"),
					resource.TestCheckResourceAttr(img, "size_gi", "10"),
					resource.TestCheckResourceAttr(img, "firmware", "efi"),
					resource.TestCheckResourceAttr(img, "os_type", "linux"),
					// The configured URL with its token stays in state; the API hides the query.
					resource.TestCheckResourceAttr(img, "url", "https://repo.example.org/AlmaLinux-9-GenericCloud.qcow2?X-Amz-Signature=secret"),
					resource.TestCheckResourceAttrWith(img, "image", func(v string) error {
						if !strings.HasPrefix(v, "custom:") {
							return fmt.Errorf("image = %q", v)
						}
						imageID = strings.TrimPrefix(v, "custom:")
						return nil
					}),
				),
			},
			{Config: imageConfig(f, base), PlanOnly: true},
			// disk_size below the image size is refused by the API.
			{
				Config:      imageConfig(f, fmt.Sprintf(withVM, "5Gi")),
				ExpectError: regexp.MustCompile(`DISK_TOO_SMALL`),
			},
			{
				Config: imageConfig(f, fmt.Sprintf(withVM, "10Gi")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("h3_vm.a", "image", img, "image"),
					resource.TestCheckResourceAttrPair("data.h3_vm_image.by_name", "id", img, "id"),
					resource.TestCheckResourceAttr("data.h3_vm_image.by_name", "source_url", "https://repo.example.org/AlmaLinux-9-GenericCloud.qcow2"),
					resource.TestCheckResourceAttr("data.h3_vm_image.by_name", "size_gi", "10"),
				),
			},
			{
				Config:                  imageConfig(f, fmt.Sprintf(withVM, "10Gi")),
				ResourceName:            img,
				ImportState:             true,
				ImportStateIdFunc:       func(*terraform.State) (string, error) { return acctest.Project + "/" + imageID, nil },
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"url", "timeouts"},
			},
			// Any argument replaces the image.
			{
				Config: imageConfig(f, strings.Replace(base, `"alma-9"`, `"alma-9-2"`, 1)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(img, plancheck.ResourceActionReplace),
				}},
			},
		},
	})
}

func TestVMImage_FromStoppedVM(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	vmID := f.SeedVM("golden")
	cfg := imageConfig(f, fmt.Sprintf(`
resource "h3_vm_image" "golden" {
  name         = "golden"
  source_vm_id = %q
}
`, vmID))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy: func(*terraform.State) error {
			if n := f.Count("custom_images"); n != 0 {
				return fmt.Errorf("%d images left", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: cfg, ExpectError: regexp.MustCompile(`VM_NOT_STOPPED`)},
			{
				PreConfig: func() { f.Mutate("vms", vmID, func(o acctest.Obj) { o["status"] = "STOPPED" }) },
				Config:    cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_vm_image.golden", "status", "READY"),
					resource.TestCheckResourceAttr("h3_vm_image.golden", "source_type", "disk"),
					resource.TestCheckResourceAttr("h3_vm_image.golden", "size_gi", "20"),
					resource.TestCheckResourceAttr("h3_vm_image.golden", "firmware", "bios"),
				),
			},
		},
	})
}

func TestVMImage_FailedImportTaintsAndReplaces(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	cfg := func(url string) string {
		return imageConfig(f, fmt.Sprintf(`
resource "h3_vm_image" "x" {
  name = "x"
  url  = %q
}
`, url))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy:             noComputeLeft(f),
		Steps: []resource.TestStep{
			{Config: cfg("https://example.org/broken.qcow2"), ExpectError: regexp.MustCompile(`import failed:\s+HTTP\s+404`)},
			{
				Config: cfg("https://example.org/broken.qcow2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_vm_image.x", plancheck.ResourceActionReplace),
				}},
				ExpectError: regexp.MustCompile(`import failed`),
			},
			{Config: cfg("https://example.org/ok.qcow2.xz"), Check: resource.TestCheckResourceAttr("h3_vm_image.x", "status", "READY")},
		},
	})
}

func TestVMImage_Disabled(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	f.CustomImagesDisabled = true
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{{
			Config: imageConfig(f, `
resource "h3_vm_image" "x" {
  name = "x"
  url  = "https://example.org/a.qcow2"
}
`),
			ExpectError: regexp.MustCompile(`CUSTOM_IMAGES_DISABLED`),
		}},
	})
}

func TestVM_DeletionProtectionDisabled(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	f.DeletionProtectionDisabled = true
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy:             noVMs(f),
		Steps: []resource.TestStep{
			{Config: vmConfig(f, 2, "4Gi", "  deletion_protection = true"), ExpectError: regexp.MustCompile(`DELETION_PROTECTION_DISABLED`)},
			// Without the argument nothing is sent: the VM is created as before.
			{Config: vmConfig(f, 2, "4Gi", ""), Check: resource.TestCheckResourceAttr("h3_vm.a", "deletion_protection", "false")},
		},
	})
	if n := len(f.Calls("PATCH /api/vms/v1/")); n != 0 {
		t.Fatalf("PATCH calls = %d, want 0", n)
	}
}

func TestVMComputeValidation(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	cases := []struct{ cfg, err string }{
		{`resource "h3_vm" "a" {
  name = "a"
  cpu = 1
  memory = "1Gi"
  user_data_encoding = "base64"
}`, `user_data_encoding requires user_data`},
		{`resource "h3_vm" "a" {
  name = "a"
  cpu = 1
  memory = "1Gi"
  user_data = "packages: [nginx]"
}`, `first line of user_data must be`},
		{`resource "h3_vm" "a" {
  name = "a"
  cpu = 1
  memory = "1Gi"
  user_data = "not base64!"
  user_data_encoding = "base64"
}`, `not valid base64`},
		{`resource "h3_vm" "a" {
  name = "a"
  cpu = 1
  memory = "1Gi"
  user_data = "#cloud-config\n"
  user_data_encoding = "gzip"
}`, `expected plain or base64`},
		{`resource "h3_vm" "a" {
  name = "a"
  cpu = 1
  memory = "1Gi"
  source_snapshot_id = "11111111-0000-4000-8000-000000000001"
  user_data = "#cloud-config\n"
}`, `user_data needs a VM from an image`},
		{fmt.Sprintf(`resource "h3_vm" "a" {
  name = "a"
  cpu = 1
  memory = "1Gi"
  image = "ubuntu:24.04"
  user_data = %q
}`, "#!/bin/sh\n"+strings.Repeat("x", 64<<10)), `user_data is too large`},
		{`resource "h3_vm_image" "x" {
  name = "x"
}`, `Set exactly one image source`},
		{`resource "h3_vm_image" "x" {
  name = "x"
  url = "https://example.org/a.img"
  source_vm_id = "11111111-0000-4000-8000-000000000001"
}`, `Set exactly one image source`},
		{`resource "h3_vm_image" "x" {
  name = "x"
  url = "http://example.org/a.img"
}`, `url must be https`},
		{`resource "h3_vm_image" "x" {
  name = "x"
  source_vm_id = "11111111-0000-4000-8000-000000000001"
  size_gi = 30
}`, `size_gi is for url`},
		{`resource "h3_vm_image" "x" {
  name = "x"
  url = "https://example.org/a.img"
  size_gi = 300
}`, `size_gi must be between 1 and 256`},
		{`resource "h3_vm_image" "x" {
  name = "x"
  url = "https://example.org/a.img"
  firmware = "uefi"
}`, `expected bios or efi`},
	}
	t.Cleanup(func() {
		if n := len(f.Calls("")); n != 0 {
			t.Errorf("validation made %d API calls", n)
		}
	})
	for i, c := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: acctest.Providers(),
				Steps: []resource.TestStep{{
					Config: f.ConfigWithProject(acctest.Project, c.cfg), PlanOnly: true, ExpectError: regexp.MustCompile(c.err),
				}},
			})
		})
	}
}
