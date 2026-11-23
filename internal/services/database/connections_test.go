package database_test

import (
	"fmt"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"h3terraform/internal/services/database"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func configMaxConn(endpoint, memory, maxConn, engine string) string {
	line := ""
	if maxConn != "" {
		line = "max_connections = " + maxConn
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
  engine     = %q
  cpu        = 2
  memory     = %q
  size       = "10Gi"
  %s
}
`, endpoint, project, engine, memory, line)
}

func TestDatabaseMaxConnections(t *testing.T) {
	database.SetPollIntervalForTest(t, 10*time.Millisecond)
	fake := &fakeH3DB{dbs: map[string]map[string]any{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	const addr = "h3_database.pg"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				// Not configured: the service default is read back.
				Config: configMaxConn(srv.URL, "4Gi", "", "postgres"),
				Check:  resource.TestCheckResourceAttr(addr, "max_connections", "100"),
			},
			{
				// In place, waits for the restart.
				Config: configMaxConn(srv.URL, "4Gi", "300", "postgres"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(addr, "max_connections", "300"),
			},
			{
				// Above the memory bound: the API refuses.
				Config:      configMaxConn(srv.URL, "4Gi", "450", "postgres"),
				ExpectError: regexp.MustCompile(`INVALID_MAX_CONNECTIONS`),
			},
			{
				// Static bounds at validate time.
				Config:      configMaxConn(srv.URL, "4Gi", "10", "postgres"),
				ExpectError: regexp.MustCompile(`between 20 and 5000`),
			},
			{
				Config:      configMaxConn(srv.URL, "4Gi", "200", "mariadb"),
				ExpectError: regexp.MustCompile(`PostgreSQL only`),
			},
			{
				// Removing it from the configuration keeps the value.
				Config: configMaxConn(srv.URL, "4Gi", "", "postgres"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
			},
		},
	})
	if fmt.Sprint(fake.settings) != "[300]" {
		t.Fatalf("PUT /settings = %v, want [300]", fake.settings)
	}
}

func TestDatabaseMaxConnectionsOnCreateAboveCreateBound(t *testing.T) {
	database.SetPollIntervalForTest(t, 10*time.Millisecond)
	fake := &fakeH3DB{dbs: map[string]map[string]any{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				// 1500 is above the create bound (1000): created with the
				// default, then PUT /settings.
				Config: configMaxConn(srv.URL, "16Gi", "1500", "postgres"),
				Check:  resource.TestCheckResourceAttr("h3_database.pg", "max_connections", "1500"),
			},
		},
	})
	if fmt.Sprint(fake.settings) != "[1500]" {
		t.Fatalf("PUT /settings = %v", fake.settings)
	}
}

func configUser(endpoint, limit, grants string) string {
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
}

resource "h3_database_user" "checkout" {
  database_id      = h3_database.pg.id
  name             = "s_checkout"
  connection_limit = %s
  %s
}
`, endpoint, project, limit, grants)
}

func TestDatabaseUserConnectionLimit(t *testing.T) {
	database.SetPollIntervalForTest(t, 10*time.Millisecond)
	fake := &fakeH3DB{dbs: map[string]map[string]any{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	const addr = "h3_database_user.checkout"
	grants := `grants = [{ database = "app", preset = "readwrite" }]`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				Config: configUser(srv.URL, "20", grants),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "connection_limit", "20"),
					resource.TestCheckResourceAttr(addr, "password", "pw-s_checkout"),
					resource.TestCheckResourceAttr(addr, "grants.#", "1"),
					resource.TestCheckResourceAttr(addr, "status", "READY"),
				),
			},
			{
				Config:      configUser(srv.URL, "0", grants),
				ExpectError: regexp.MustCompile(`-1 \(unlimited\) or 1..5000`),
			},
			{
				// No restart, in place: PATCH.
				Config: configUser(srv.URL, "50", grants),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("h3_database.pg", plancheck.ResourceActionNoop),
				}},
				Check: resource.TestCheckResourceAttr(addr, "connection_limit", "50"),
			},
			{
				Config: configUser(srv.URL, "-1", `grants = [{ database = "app", preset = "readonly" }]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "connection_limit", "-1"),
					resource.TestCheckResourceAttr(addr, "grants.0.preset", "readonly"),
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) { return s.RootModule().Resources[addr].Primary.ID, nil },
				ImportStateVerify: true,
			},
		},
	})
	if fmt.Sprint(fake.patches) != "[s_checkout=50 s_checkout=-1]" {
		t.Fatalf("PATCH = %v", fake.patches)
	}
}

func TestDatabaseUserSystemRolesAndSettings(t *testing.T) {
	database.SetPollIntervalForTest(t, 10*time.Millisecond)
	fake := &fakeH3DB{dbs: map[string]map[string]any{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	const addr = "h3_database_user.checkout"
	monitor := `grants = [{ database = "app", preset = "monitor" }]
  system_roles = ["pg_monitor"]
  settings = { statement_timeout = "30s", default_transaction_isolation = "repeatable read" }`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				Config: configUser(srv.URL, "-1", monitor),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "system_roles.#", "1"),
					resource.TestCheckTypeSetElemAttr(addr, "system_roles.*", "pg_monitor"),
					resource.TestCheckResourceAttr(addr, "settings.statement_timeout", "30s"),
					resource.TestCheckResourceAttr(addr, "settings.default_transaction_isolation", "repeatable read"),
					resource.TestCheckResourceAttr(addr, "grants.0.preset", "monitor"),
				),
			},
			{
				Config:      configUser(srv.URL, "-1", `settings = { statement_timeout = "30000" }`),
				ExpectError: regexp.MustCompile(`write "30s"`),
			},
			{
				Config:      configUser(srv.URL, "-1", `settings = { search_path = "x" }`),
				ExpectError: regexp.MustCompile(`cannot be set`),
			},
			{
				Config: configUser(srv.URL, "-1", `grants = [{ database = "app", preset = "monitor" }]
  system_roles = ["pg_monitor", "pg_signal_backend"]
  settings = { lock_timeout = "5s" }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "system_roles.#", "2"),
					resource.TestCheckResourceAttr(addr, "settings.%", "1"),
					resource.TestCheckResourceAttr(addr, "settings.lock_timeout", "5s"),
				),
			},
			{
				// Not set = not managed: no change, the roles stay.
				Config: configUser(srv.URL, "-1", `grants = [{ database = "app", preset = "monitor" }]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionNoop),
				}},
				Check: resource.TestCheckResourceAttr(addr, "system_roles.#", "2"),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) { return s.RootModule().Resources[addr].Primary.ID, nil },
				ImportStateVerify: true,
			},
		},
	})
	if fmt.Sprint(fake.patches) != "[s_checkout roles=[pg_monitor pg_signal_backend] s_checkout settings=map[lock_timeout:5s]]" {
		t.Fatalf("PATCH = %v", fake.patches)
	}
}
