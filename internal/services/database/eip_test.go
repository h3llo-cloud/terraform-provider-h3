package database_test

import (
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"h3terraform/internal/services/database"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const (
	eipA     = "aaaaaaaa-0000-0000-0000-000000000001"
	eipB     = "aaaaaaaa-0000-0000-0000-000000000002"
	eipTaken = "aaaaaaaa-0000-0000-0000-0000000000ff"
)

func eipConfig(endpoint string, whiteIP bool, eipID string, release bool) string {
	extra := ""
	if eipID != "" {
		extra += fmt.Sprintf("  eip_id = %q\n", eipID)
	}
	if release {
		extra += "  release_eip_on_destroy = true\n"
	}
	return fmt.Sprintf(`
provider "h3" {
  api_endpoint = %q
  key_id       = "test-key"
  secret_key   = "test-secret"
}

resource "h3_database" "pg" {
  project_id = %q
  name       = "pg-main"
  engine     = "postgres"
  cpu        = 2
  memory     = "4Gi"
  size       = "10Gi"
  white_ip   = %v
%s}
`, endpoint, project, whiteIP, extra)
}

func TestDatabaseEIPLifecycle(t *testing.T) {
	database.SetPollIntervalForTest(t, 10*time.Millisecond)
	fake := &fakeH3DB{dbs: map[string]map[string]any{}, boundEIP: eipTaken}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	const addr = "h3_database.pg"
	update := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
		plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
	}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				// eip_id without white_ip is refused at validate time.
				Config:      eipConfig(srv.URL, false, eipA, false),
				ExpectError: regexp.MustCompile(`eip_id requires white_ip = true`),
			},
			{
				Config:      eipConfig(srv.URL, true, "not-a-uuid", false),
				ExpectError: regexp.MustCompile(`not a valid UUID`),
			},
			{
				// A taken EIP fails create with a clear diagnostic.
				Config:      eipConfig(srv.URL, true, eipTaken, false),
				ExpectError: regexp.MustCompile(`Elastic IP already in use`),
			},
			{
				// Create with a selected EIP: sent as eipId, read back from netConfig.eipId.
				Config: eipConfig(srv.URL, true, eipA, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "eip_id", eipA),
					resource.TestCheckResourceAttr(addr, "release_eip_on_destroy", "false"),
				),
			},
			{
				// Changing eip_id is in place: detach, then attach {eipId}.
				Config:           eipConfig(srv.URL, true, eipB, false),
				ConfigPlanChecks: update,
				Check:            resource.TestCheckResourceAttr(addr, "eip_id", eipB),
			},
			{
				// Removing eip_id from the configuration keeps the current EIP.
				Config: eipConfig(srv.URL, true, "", false),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
			},
			{
				// white_ip off: detach, eip_id becomes null.
				Config:           eipConfig(srv.URL, false, "", false),
				ConfigPlanChecks: update,
				Check:            resource.TestCheckNoResourceAttr(addr, "eip_id"),
			},
			{
				// Leasing a new EIP denied by quota/balance.
				PreConfig:   func() { fake.mu.Lock(); fake.denyLease = true; fake.mu.Unlock() },
				Config:      eipConfig(srv.URL, true, "", false),
				ExpectError: regexp.MustCompile(`Elastic IP not allowed`),
			},
			{
				// white_ip on without eip_id leases a new EIP, exported as eip_id.
				PreConfig: func() { fake.mu.Lock(); fake.denyLease = false; fake.mu.Unlock() },
				Config:    eipConfig(srv.URL, true, "", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "white_ip", "true"),
					resource.TestMatchResourceAttr(addr, "eip_id", regexp.MustCompile(`^eeeeeeee-`)),
					resource.TestCheckResourceAttr(addr, "release_eip_on_destroy", "true"),
				),
			},
		},
		CheckDestroy: func(_ *terraform.State) error { return nil },
	})

	fake.mu.Lock()
	defer fake.mu.Unlock()
	got := strings.Join(fake.whiteIPs, ",")
	want := "detach:,attach:" + eipB + ",detach:,attach:,attach:"
	if got != want {
		t.Fatalf("whiteip calls = %s, want %s", got, want)
	}
	if len(fake.deletes) != 1 || fake.deletes[0] != "releaseEip=true" {
		t.Fatalf("delete queries = %q, want [releaseEip=true]", fake.deletes)
	}
}
