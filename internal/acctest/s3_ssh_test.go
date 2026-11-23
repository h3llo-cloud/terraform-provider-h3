package acctest_test

import (
	"fmt"
	"testing"

	"h3terraform/internal/acctest"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestS3Bucket_CreateImportGone(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	cfg := f.Config(fmt.Sprintf(`
resource "h3_s3_bucket" "b" {
  project_id = %q
  name       = "assets"
}
`, acctest.Project))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy: func(*terraform.State) error {
			if n := f.Count("buckets"); n != 0 {
				return fmt.Errorf("%d buckets left", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_s3_bucket.b", "id", acctest.Project+"/assets"),
					resource.TestCheckResourceAttr("h3_s3_bucket.b", "access_key_id", "AKIAFAKE"),
					resource.TestCheckResourceAttr("h3_s3_bucket.b", "slug", "h3-assets"),
				),
			},
			{Config: cfg, PlanOnly: true},
			{
				ResourceName:            "h3_s3_bucket.b",
				ImportState:             true,
				ImportStateId:           acctest.Project + "/assets",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"access_key_id", "secret_access_key"},
			},
			{
				// Deleted outside Terraform (GET answers 500): planned for re-creation, not an error.
				PreConfig: func() { f.Remove("buckets", acctest.Project+"/assets") },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_s3_bucket.b", plancheck.ResourceActionCreate),
				}},
			},
		},
	})
}

func TestSSHKey_RenameReplaces_NoPatch(t *testing.T) {
	t.Parallel()
	f := acctest.NewFake(t)
	cfg := func(name string) string {
		return f.Config(fmt.Sprintf(`
resource "h3_ssh_key" "k" {
  user_id    = %q
  name       = %q
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItest user@host\n"
}
`, acctest.User, name))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config: cfg("deploy"),
				// public_key is not sensitive: the diff stays readable.
				Check: resource.TestCheckResourceAttr("h3_ssh_key.k", "public_key", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItest user@host\n"),
			},
			{Config: cfg("deploy"), PlanOnly: true},
			{
				Config: cfg("deploy-2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_ssh_key.k", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
			{ResourceName: "h3_ssh_key.k", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"public_key"}},
		},
	})
	if n := len(f.Calls("PATCH")); n != 0 {
		t.Fatalf("PATCH is not in the h3ssh public API, got %d calls", n)
	}
}
