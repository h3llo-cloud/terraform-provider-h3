package database_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"h3terraform/internal/services/database"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// autoscaleDefaults fills the values h3db takes on the first enable.
func autoscaleDefaults(req map[string]any) map[string]any {
	out := map[string]any{"enabled": false, "thresholdPercent": 85, "stepGb": 10, "stepPercent": 20, "maxSizeGb": 0,
		"state": "DISABLED", "atMax": false}
	for k, v := range req {
		out[k] = v
	}
	if out["enabled"] == true {
		out["state"] = "WATCHING"
	} else {
		out["state"] = "DISABLED"
	}
	return out
}

// serveAutoscale: GET/PUT /{id}/storage-autoscale with the h3db validation
// (maxSizeGb >= sizeGb when enabled), 503 STORAGE_AUTOSCALE_DISABLED when off.
func (f *fakeH3DB) serveAutoscale(r *http.Request, id string, db map[string]any, writeJSON func(int, any)) {
	if f.autoscaleOff {
		writeJSON(503, map[string]string{"code": "STORAGE_AUTOSCALE_DISABLED", "message": "storage autoscale is disabled"})
		return
	}
	if f.autoscale == nil {
		f.autoscale = map[string]map[string]any{}
	}
	cur := f.autoscale[id]
	if cur == nil {
		cur = autoscaleDefaults(nil)
		f.autoscale[id] = cur
	}
	if r.Method == "PUT" {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		b, _ := json.Marshal(req)
		f.autoscaleReqs = append(f.autoscaleReqs, "put:"+string(b))
		if req["enabled"] == true {
			max, _ := req["maxSizeGb"].(float64)
			if max == 0 {
				max, _ = cur["maxSizeGb"].(float64)
			}
			if int(max) < gi(db["sizeGb"]) {
				writeJSON(400, map[string]string{"code": "INVALID_AUTOSCALE", "message": "maxSizeGb below sizeGb"})
				return
			}
		}
		for k, v := range req {
			cur[k] = v
		}
		f.autoscale[id] = autoscaleDefaults(cur)
		cur = f.autoscale[id]
	}
	writeJSON(200, cur)
}

func configAutoscale(endpoint, size, block string) string {
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
  size       = %q
%s
}
`, endpoint, project, size, block)
}

func block(enabled bool, extra string) string {
	return fmt.Sprintf("  storage_autoscale = {\n    enabled = %v\n%s  }\n", enabled, extra)
}

// grow emulates an automatic grow by h3db.
func (f *fakeH3DB) grow(to string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, db := range f.dbs {
		from := gi(db["sizeGb"])
		db["sizeGb"], db["actualSizeGb"] = to, to
		if a := f.autoscale[id]; a != nil {
			ev := map[string]any{"result": "GROWN", "trigger": "THRESHOLD", "fromSizeGb": from, "toSizeGb": gi(to)}
			a["lastEvent"] = ev
			a["events"] = []any{ev}
		}
	}
}

func TestDatabaseStorageAutoscale(t *testing.T) {
	database.SetPollIntervalForTest(t, 10*time.Millisecond)
	fake := &fakeH3DB{dbs: map[string]map[string]any{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	const addr = "h3_database.pg"
	on50 := block(true, "    max_size_gb = 50\n")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				// Create sends storageAutoscale; defaults come back.
				Config: configAutoscale(srv.URL, "20Gi", on50),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "storage_autoscale.enabled", "true"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.threshold_percent", "85"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.step_gb", "10"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.step_percent", "20"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.max_size_gb", "50"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.state", "WATCHING"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.at_max", "false"),
					resource.TestCheckResourceAttr(addr, "current_size", "20Gi"),
				),
			},
			{
				// h3db grew the disk: no diff, size keeps the configured value.
				PreConfig: func() { fake.grow("30Gi") },
				Config:    configAutoscale(srv.URL, "20Gi", on50),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "size", "20Gi"),
					resource.TestCheckResourceAttr(addr, "current_size", "30Gi"),
				),
			},
			{
				// Settings change in place with PUT, no resize.
				Config: configAutoscale(srv.URL, "20Gi", block(true, "    max_size_gb = 60\n    threshold_percent = 90\n")),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "storage_autoscale.threshold_percent", "90"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.max_size_gb", "60"),
				),
			},
			{
				// A size below the grown disk is not sent (h3db would refuse it).
				Config: configAutoscale(srv.URL, "25Gi", block(true, "    max_size_gb = 60\n    threshold_percent = 90\n")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "size", "25Gi"),
					resource.TestCheckResourceAttr(addr, "current_size", "30Gi"),
				),
			},
			{
				// A size above it is a normal resize.
				Config: configAutoscale(srv.URL, "40Gi", block(true, "    max_size_gb = 60\n    threshold_percent = 90\n")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "size", "40Gi"),
					resource.TestCheckResourceAttr(addr, "current_size", "40Gi"),
				),
			},
			{
				// Import reads an enabled setting into the block.
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"wait_for_resize"},
			},
			{
				// max_size_gb below the disk is refused at plan time.
				Config:      configAutoscale(srv.URL, "40Gi", block(true, "    max_size_gb = 35\n")),
				ExpectError: regexp.MustCompile(`below size|below the current size`),
			},
			{
				// Another automatic grow, then storage_autoscale is removed
				// from the configuration: nothing is sent, the setting stays
				// on (removing it does not turn autoscale off).
				PreConfig: func() { fake.grow("50Gi") },
				Config:    configAutoscale(srv.URL, "40Gi", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "storage_autoscale.enabled", "true"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.max_size_gb", "60"),
					resource.TestCheckResourceAttr(addr, "size", "40Gi"),
					resource.TestCheckResourceAttr(addr, "current_size", "50Gi"),
				),
			},
			{
				// Turning it off is explicit: enabled = false, PUT.
				Config: configAutoscale(srv.URL, "40Gi", block(false, "")),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "storage_autoscale.enabled", "false"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.state", "DISABLED"),
					resource.TestCheckResourceAttr(addr, "current_size", "50Gi"),
				),
			},
			{
				Config:             configAutoscale(srv.URL, "40Gi", ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})

	if n := countSuffix(fake, "/resize"); n != 1 {
		t.Fatalf("resize calls = %d, want 1 (only 25Gi -> 40Gi above the grown 30Gi)", n)
	}
	want := []string{
		`create:{"enabled":true,"maxSizeGb":50}`,
		`put:{"enabled":true,"maxSizeGb":60,"stepGb":10,"stepPercent":20,"thresholdPercent":90}`,
		`put:{"enabled":false,"maxSizeGb":60,"stepGb":10,"stepPercent":20,"thresholdPercent":90}`,
	}
	if strings.Join(fake.autoscaleReqs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("autoscale requests:\n%s\nwant:\n%s", strings.Join(fake.autoscaleReqs, "\n"), strings.Join(want, "\n"))
	}
}

func TestDatabaseStorageAutoscaleFeatureOff(t *testing.T) {
	database.SetPollIntervalForTest(t, 10*time.Millisecond)
	fake := &fakeH3DB{dbs: map[string]map[string]any{}, autoscaleOff: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	const addr = "h3_database.pg"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				// enabled = true on a platform without the feature: clear error.
				Config:      configAutoscale(srv.URL, "20Gi", block(true, "    max_size_gb = 50\n")),
				ExpectError: regexp.MustCompile(`storage autoscale is not available`),
			},
			{
				// enabled = false is accepted; reads keep the configured value.
				Config: configAutoscale(srv.URL, "20Gi", block(false, "")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "storage_autoscale.enabled", "false"),
					resource.TestCheckNoResourceAttr(addr, "storage_autoscale.threshold_percent"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.state", ""),
				),
			},
			{
				// Enabling it later fails on apply with the same error.
				Config:      configAutoscale(srv.URL, "20Gi", block(true, "    max_size_gb = 50\n")),
				ExpectError: regexp.MustCompile(`storage autoscale is not available`),
			},
			{
				Config: configAutoscale(srv.URL, "20Gi", block(false, "")),
			},
		},
	})
}

func TestDatabaseStorageAutoscaleValidation(t *testing.T) {
	cases := map[string]string{
		"  storage_autoscale = {\n    max_size_gb = 50\n  }\n":            `enabled is required`,
		block(true, "    max_size_gb = 10\n"):                             `below size`,
		block(true, "    max_size_gb = 2000\n"):                           `above the maximum`,
		block(true, "    max_size_gb = 50\n    threshold_percent = 50\n"): `between 60 and 95`,
		block(true, "    max_size_gb = 50\n    step_gb = 2\n"):            `between 5 and 1024`,
		block(true, "    max_size_gb = 50\n    step_percent = 3\n"):       `0 \(off\) or between 5 and 100`,
	}
	for blk, re := range cases {
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: providers(),
			Steps: []resource.TestStep{{
				Config:      configAutoscale("http://127.0.0.1:1", "20Gi", blk),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(re),
			}},
		})
	}
}

// Not configured: h3db applies its default on create (enabled, maxSizeGb =
// 2 × size) and the state shows it; nothing is sent and no diff follows,
// also after a change in the console. Enabled without max_size_gb plans
// 2 × size.
func TestDatabaseStorageAutoscaleDefault(t *testing.T) {
	database.SetPollIntervalForTest(t, 10*time.Millisecond)
	fake := &fakeH3DB{dbs: map[string]map[string]any{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	const addr = "h3_database.pg"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				Config: configAutoscale(srv.URL, "20Gi", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "storage_autoscale.enabled", "true"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.max_size_gb", "40"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.threshold_percent", "85"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.state", "WATCHING"),
				),
			},
			{
				// Changed in the console: refreshed into state, no diff.
				PreConfig: func() {
					fake.mu.Lock()
					for _, a := range fake.autoscale {
						a["maxSizeGb"] = 80
					}
					fake.mu.Unlock()
				},
				Config: configAutoscale(srv.URL, "20Gi", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
				Check: resource.TestCheckResourceAttr(addr, "storage_autoscale.max_size_gb", "80"),
			},
			{
				// Turned off, then on without max_size_gb: the current
				// value is kept.
				Config: configAutoscale(srv.URL, "30Gi", block(false, "")),
				Check:  resource.TestCheckResourceAttr(addr, "storage_autoscale.state", "DISABLED"),
			},
			{
				Config: configAutoscale(srv.URL, "30Gi", block(true, "")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "storage_autoscale.enabled", "true"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.max_size_gb", "80"),
				),
			},
		},
	})
	want := []string{
		`put:{"enabled":false,"maxSizeGb":80,"stepGb":10,"stepPercent":20,"thresholdPercent":85}`,
		`put:{"enabled":true,"maxSizeGb":80,"stepGb":10,"stepPercent":20,"thresholdPercent":85}`,
	}
	if strings.Join(fake.autoscaleReqs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("autoscale requests:\n%s\nwant:\n%s", strings.Join(fake.autoscaleReqs, "\n"), strings.Join(want, "\n"))
	}

	// Explicit enabled = false on create overrides the default; enabling it
	// later without max_size_gb (none on the platform) plans 2 × size.
	fake = &fakeH3DB{dbs: map[string]map[string]any{}}
	srv2 := httptest.NewServer(fake)
	defer srv2.Close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				Config: configAutoscale(srv2.URL, "30Gi", block(false, "")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "storage_autoscale.enabled", "false"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.state", "DISABLED"),
				),
			},
			{
				Config: configAutoscale(srv2.URL, "30Gi", block(true, "")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "storage_autoscale.enabled", "true"),
					resource.TestCheckResourceAttr(addr, "storage_autoscale.max_size_gb", "60"),
				),
			},
		},
	})
	want = []string{
		`create:{"enabled":false}`,
		`put:{"enabled":true,"maxSizeGb":60,"stepGb":10,"stepPercent":20,"thresholdPercent":85}`,
	}
	if strings.Join(fake.autoscaleReqs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("autoscale requests:\n%s\nwant:\n%s", strings.Join(fake.autoscaleReqs, "\n"), strings.Join(want, "\n"))
	}
}
