package iam_test

import (
	"fmt"
	"regexp"
	"testing"
	"time"

	"h3terraform/internal/acctest"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func iamConfig(f *fake, role, desc string, disabled bool, expires string) string {
	return f.config(fmt.Sprintf(`
resource "h3_service_account" "ci" {
  name        = "gitlab-ci"
  description = %q
  role        = %q
  disabled    = %t
}

resource "h3_service_account_key" "ci" {
  service_account_id = h3_service_account.ci.id
  name               = "deploy"
  scopes             = ["vms:write", "dns:read"]
  expires_at         = %q
}
`, desc, role, disabled, expires))
}

func TestServiceAccount_KeyLifecycleImport(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	// The API returns expires_at in UTC; the configured +03:00 spelling must not cause a diff.
	exp := time.Now().Add(30 * 24 * time.Hour).In(time.FixedZone("MSK", 3*3600)).Truncate(time.Second).Format(time.RFC3339)
	var saID, keyID string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		CheckDestroy: func(*terraform.State) error {
			if n := f.count("iam_sas") + f.count("iam_keys"); n != 0 {
				return fmt.Errorf("%d IAM objects left", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: iamConfig(f, "PROJ-VIEWER", "CI", false, exp),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("h3_service_account.ci", "id", regexp.MustCompile(`^sa-[a-z2-7]{26}$`)),
					resource.TestCheckResourceAttr("h3_service_account.ci", "project_id", project),
					resource.TestCheckResourceAttr("h3_service_account.ci", "role", "PROJ-VIEWER"),
					resource.TestCheckResourceAttr("h3_service_account.ci", "disabled", "false"),
					resource.TestCheckResourceAttrSet("h3_service_account.ci", "principal_id"),
					resource.TestCheckResourceAttr("h3_service_account.ci", "created_by", user),
					resource.TestCheckResourceAttrPair("h3_service_account_key.ci", "service_account_id", "h3_service_account.ci", "id"),
					resource.TestCheckResourceAttr("h3_service_account_key.ci", "scopes.#", "2"),
					resource.TestCheckResourceAttr("h3_service_account_key.ci", "expires_at", exp),
					resource.TestCheckResourceAttr("h3_service_account_key.ci", "enabled", "true"),
					resource.TestCheckResourceAttr("h3_service_account_key.ci", "kind", "static"),
					resource.TestMatchResourceAttr("h3_service_account_key.ci", "secret_key", regexp.MustCompile(`^secret-`)),
					func(s *terraform.State) error {
						saID = s.RootModule().Resources["h3_service_account.ci"].Primary.ID
						keyID = s.RootModule().Resources["h3_service_account_key.ci"].Primary.ID
						return nil
					},
				),
			},
			{Config: iamConfig(f, "PROJ-VIEWER", "CI", false, exp), PlanOnly: true},
			{
				// Role and description change in place; the key and its secret stay.
				Config: iamConfig(f, "PROJ-MEMB", "CI pipeline", false, exp),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_service_account.ci", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("h3_service_account_key.ci", plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_service_account.ci", "role", "PROJ-MEMB"),
					resource.TestCheckResourceAttr("h3_service_account.ci", "description", "CI pipeline"),
					resource.TestMatchResourceAttr("h3_service_account_key.ci", "secret_key", regexp.MustCompile(`^secret-`)),
				),
			},
			{
				// Disabling the account disables its keys (seen on the next refresh).
				Config: iamConfig(f, "PROJ-MEMB", "CI pipeline", true, exp),
				Check:  resource.TestCheckResourceAttr("h3_service_account.ci", "disabled", "true"),
			},
			{
				Config: iamConfig(f, "PROJ-MEMB", "CI pipeline", true, exp),
				Check:  resource.TestCheckResourceAttr("h3_service_account_key.ci", "enabled", "false"),
			},
			{
				ResourceName:      "h3_service_account.ci",
				ImportState:       true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return project + "/" + saID, nil },
				ImportStateVerify: true,
			},
			{
				ResourceName: "h3_service_account_key.ci",
				ImportState:  true,
				ImportStateIdFunc: func(*terraform.State) (string, error) {
					return project + "/" + saID + "/" + keyID, nil
				},
				ImportStateVerify: true,
				// The secret is returned only at creation; the API answers expires_at in UTC.
				ImportStateVerifyIgnore: []string{"secret_key", "expires_at"},
			},
			{
				// Another scope is a new key: the old one is deleted.
				Config: f.config(`
resource "h3_service_account" "ci" {
  name     = "gitlab-ci"
  disabled = false
}

resource "h3_service_account_key" "ci" {
  service_account_id = h3_service_account.ci.id
  name               = "deploy"
  scopes             = ["vms:read"]
}
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_service_account.ci", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("h3_service_account_key.ci", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_service_account.ci", "role", "PROJ-MEMB"),
					resource.TestCheckResourceAttr("h3_service_account.ci", "description", ""),
					resource.TestCheckResourceAttr("h3_service_account_key.ci", "scopes.0", "vms:read"),
					resource.TestCheckResourceAttrSet("h3_service_account_key.ci", "expires_at"),
					func(*terraform.State) error {
						if n := f.count("iam_keys"); n != 1 {
							return fmt.Errorf("%d keys, want 1 (old key must be deleted)", n)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestServiceAccount_RolloutOffKeepsState(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	cfg := f.config(`
resource "h3_service_account" "ci" {
  name = "runner"
}
resource "h3_service_account_key" "ci" {
  service_account_id = h3_service_account.ci.id
  name               = "k"
}
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{Config: cfg, Check: resource.TestCheckResourceAttr("h3_service_account.ci", "role", "PROJ-MEMB")},
			{
				// The API answers 404 for the whole project: an error, not "deleted outside Terraform".
				PreConfig:   func() { f.setOff(true) },
				Config:      cfg,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`недоступно для проекта`),
			},
			{
				PreConfig: func() { f.setOff(false) },
				Config:    cfg,
				PlanOnly:  true,
			},
		},
	})
}

func TestServiceAccount_DeletedOutside(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	cfg := f.config(`
resource "h3_service_account" "ci" {
  name = "runner"
}
resource "h3_service_account_key" "ci" {
  service_account_id = h3_service_account.ci.id
  name               = "k"
}
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				// The account (and with it its keys) was deleted in the console.
				PreConfig: func() {
					id := f.only("iam_sas")
					f.remove("iam_keys", f.only("iam_keys"))
					f.remove("iam_sas", id)
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("h3_service_account.ci", plancheck.ResourceActionCreate),
					plancheck.ExpectResourceAction("h3_service_account_key.ci", plancheck.ResourceActionCreate),
				}},
			},
		},
	})
}

func TestServiceAccount_Validation(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	for _, tc := range []struct{ body, err string }{
		{`resource "h3_service_account" "x" { name = "CI" }`, `Недопустимое имя`},
		{`resource "h3_service_account" "x" {
  name = "ci-x"
  role = "OWNER"
}`, `Недопустимая роль`},
		{`resource "h3_service_account_key" "x" {
  service_account_id = "sa-aaaaaaaaaaaaaaaaaaaaaaaaaa"
  name               = "k"
  scopes             = ["vms"]
}`, `Неверный scope`},
		{`resource "h3_service_account_key" "x" {
  service_account_id = "sa-aaaaaaaaaaaaaaaaaaaaaaaaaa"
  name               = "k"
  expires_at         = "2027-01-01"
}`, `Неверная дата`},
	} {
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: acctest.Providers(),
			Steps: []resource.TestStep{{
				Config: f.config(tc.body), PlanOnly: true, ExpectError: regexp.MustCompile(tc.err),
			}},
		})
	}
	if n := f.callCount(); n != 0 {
		t.Fatalf("validation errors must not reach the API, got %d calls", n)
	}
}

func TestServiceAccounts_DataSources(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.Providers(),
		Steps: []resource.TestStep{
			{
				Config: f.config(`
resource "h3_service_account" "b" {
  name = "beta"
}
resource "h3_service_account" "a" {
  name = "alpha"
  role = "PROJ-ADM"
}
resource "h3_service_account_key" "a" {
  service_account_id = h3_service_account.a.id
  name               = "k"
}
`),
			},
			{
				Config: f.config(`
resource "h3_service_account" "b" {
  name = "beta"
}
resource "h3_service_account" "a" {
  name = "alpha"
  role = "PROJ-ADM"
}
resource "h3_service_account_key" "a" {
  service_account_id = h3_service_account.a.id
  name               = "k"
}
data "h3_service_accounts" "all" {}
data "h3_service_accounts" "alpha" {
  name = "alpha"
}
data "h3_iam_audit_events" "last" {
  limit = 2
}
data "h3_iam_audit_events" "all" {}
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.h3_service_accounts.all", "project_id", project),
					resource.TestCheckResourceAttr("data.h3_service_accounts.all", "service_accounts.#", "2"),
					resource.TestCheckResourceAttr("data.h3_service_accounts.all", "service_accounts.0.name", "alpha"),
					resource.TestCheckResourceAttr("data.h3_service_accounts.all", "service_accounts.1.name", "beta"),
					resource.TestCheckResourceAttr("data.h3_service_accounts.alpha", "service_accounts.#", "1"),
					resource.TestCheckResourceAttrPair("data.h3_service_accounts.alpha", "service_accounts.0.id", "h3_service_account.a", "id"),
					resource.TestCheckResourceAttr("data.h3_service_accounts.alpha", "service_accounts.0.role", "PROJ-ADM"),
					resource.TestCheckResourceAttr("data.h3_iam_audit_events.last", "events.#", "2"),
					resource.TestCheckResourceAttr("data.h3_iam_audit_events.all", "events.#", "3"),
					resource.TestCheckResourceAttr("data.h3_iam_audit_events.all", "retention_days", "90"),
					resource.TestCheckResourceAttr("data.h3_iam_audit_events.all", "events.0.action", "service_account.key_create"),
					resource.TestCheckResourceAttrPair("data.h3_iam_audit_events.all", "events.0.resource_id", "h3_service_account.a", "id"),
					resource.TestCheckResourceAttr("data.h3_iam_audit_events.all", "events.0.resource_type", "service_account"),
					resource.TestCheckResourceAttr("data.h3_iam_audit_events.all", "events.0.actor_id", user),
					resource.TestMatchResourceAttr("data.h3_iam_audit_events.all", "events.0.details", regexp.MustCompile(`"key_id"`)),
					resource.TestCheckResourceAttr("data.h3_iam_audit_events.all", "events.2.action", "service_account.create"),
				),
			},
		},
	})
}
