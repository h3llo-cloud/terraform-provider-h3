package database_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"h3terraform/internal/services/database"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const poolerDB = "44444444-0000-0000-0000-000000000001"

var fakePoolerIDRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,18}[a-z0-9])?$`)

// fakePoolerAPI mimics the h3db multi-pooler API ("API (итог реализации)" of
// the design 2026-11-04): PROGRESSING -> READY and VPC pending -> assigned on
// the next GET, pause/resume, the default flag, vpc {mode, port} and public
// {port, via} listeners on the pooler, POOLER_EXISTS, ROLE_NEEDS_REPLICA,
// PUBLIC_NEEDS_EIP, PORT_IN_USE, POOLER_IN_USE (public 5432) and the
// POOLER_MULTI_DISABLED flag.
type fakePoolerAPI struct {
	mu       sync.Mutex
	poolers  map[string]map[string]any
	order    []string
	noEIP    bool // the database has no public address
	disabled bool // H3DB_POOLER_MULTI off
	vpcSeq   int
	bodies   []string
}

func newFakePoolerAPI() *fakePoolerAPI {
	return &fakePoolerAPI{poolers: map[string]map[string]any{}}
}

var defaultPorts = map[string]float64{"rw": 6432, "ro": 6433, "r": 6434}

func (f *fakePoolerAPI) setVPC(p map[string]any, req map[string]any) {
	mode, _ := req["mode"].(string)
	if mode == "" {
		mode = "port"
	}
	port, _ := req["port"].(float64)
	switch mode {
	case "none":
		p["vpc"] = map[string]any{"mode": "none"}
		return
	case "address":
		if port == 0 {
			port = 5432
		}
		f.vpcSeq++
		p["vpc"] = map[string]any{"mode": "address", "ip": fmt.Sprintf("10.0.0.%d", 60+f.vpcSeq), "port": port, "status": "pending"}
	default:
		if port == 0 {
			port = defaultPorts[p["role"].(string)]
		}
		p["vpc"] = map[string]any{"mode": "port", "ip": "10.0.0.50", "port": port, "status": "pending"}
	}
}

// setPublic returns an error code, "" when applied.
func (f *fakePoolerAPI) setPublic(id string, p map[string]any, req map[string]any) string {
	port, _ := req["port"].(float64)
	if port == 0 {
		p["public"] = []any{}
		return ""
	}
	if f.noEIP {
		return "PUBLIC_NEEDS_EIP"
	}
	if port < 1024 || port > 65535 {
		return "PORT_OUT_OF_RANGE"
	}
	for oid, o := range f.poolers {
		for _, l := range o["public"].([]any) {
			if oid != id && l.(map[string]any)["port"] == port {
				return "PORT_IN_USE"
			}
		}
	}
	via, _ := req["via"].(string)
	if via == "" {
		via = "port-sources"
	}
	p["public"] = []any{map[string]any{"ip": "45.0.0.1", "port": port, "via": via, "eipId": "eeeeeeee-0000-0000-0000-000000000001"}}
	return ""
}

func (f *fakePoolerAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	writeJSON := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(code int, c, msg string) { writeJSON(code, map[string]string{"code": c, "message": msg}) }
	if r.Header.Get("X-H3-Signature") == "" {
		w.WriteHeader(401)
		return
	}
	prefix := "/api/databases/v1/" + poolerDB
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeJSON(404, map[string]string{"error": "Database not found"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, prefix)
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	b, _ := json.Marshal(body)
	f.bodies = append(f.bodies, r.Method+" "+path+" "+string(b))
	if f.disabled {
		fail(404, "POOLER_MULTI_DISABLED", "several poolers per database are not enabled")
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")

	switch {
	case r.Method == "GET" && path == "/poolers":
		list := []any{}
		for _, id := range f.order {
			list = append(list, f.poolers[id])
		}
		writeJSON(200, map[string]any{
			"poolers": list,
			"budget":  map[string]any{"maxConnections": 1000, "reserve": 10, "primary": map[string]int{"used": 110, "max": 1000}},
			"limits": map[string]any{"maxPoolers": 4, "maxInstances": 16, "portMin": 1024, "portMax": 65535,
				"defaultPorts": defaultPorts},
		})
	case r.Method == "POST" && path == "/poolers":
		id := fmt.Sprint(body["id"])
		if !fakePoolerIDRe.MatchString(id) {
			fail(400, "INVALID_ID", "bad id")
			return
		}
		if _, ok := f.poolers[id]; ok {
			fail(409, "POOLER_EXISTS", "pooler "+id+" exists")
			return
		}
		if body["role"] != "rw" {
			fail(400, "ROLE_NEEDS_REPLICA", "the database has one instance")
			return
		}
		if len(f.poolers) >= 4 {
			fail(409, "POOLER_LIMIT_REACHED", "4 poolers")
			return
		}
		inst := 2.0
		if n, ok := body["instances"].(float64); ok {
			inst = n
		}
		if inst < 1 || inst > 16 {
			fail(400, "INVALID_INSTANCES", "instances must be 1..16")
			return
		}
		name, mode := body["displayName"], body["mode"]
		if name == nil {
			name = id
		}
		if mode == nil {
			mode = "session"
		}
		params := map[string]any{}
		if p, ok := body["parameters"].(map[string]any); ok {
			params = p
		}
		p := map[string]any{
			"id": id, "k8sName": "db-" + poolerDB[:8] + "-pooler-" + id, "displayName": name, "role": body["role"],
			"mode": mode, "instances": inst, "readyInstances": 0, "paused": false, "phase": "PROGRESSING",
			"default": len(f.poolers) == 0, "adopted": false, "userParameters": params, "public": []any{},
		}
		vpcReq, _ := body["vpc"].(map[string]any)
		f.setVPC(p, vpcReq)
		if pub, ok := body["public"].(map[string]any); ok {
			if c := f.setPublic(id, p, pub); c != "" {
				fail(400, c, "public listener")
				return
			}
		}
		f.poolers[id] = p
		f.order = append(f.order, id)
		writeJSON(201, f.view(id))
	case len(parts) >= 2 && parts[0] == "poolers":
		id := parts[1]
		p, ok := f.poolers[id]
		if !ok {
			fail(404, "POOLER_NOT_FOUND", "no pooler")
			return
		}
		switch {
		case r.Method == "GET" && len(parts) == 2:
			if p["phase"] == "PROGRESSING" {
				p["phase"], p["readyInstances"] = "READY", p["instances"]
			}
			if v := p["vpc"].(map[string]any); v["status"] == "pending" {
				v["status"] = "assigned"
			}
			writeJSON(200, f.view(id))
		case r.Method == "PATCH" && len(parts) == 2:
			for _, k := range []string{"displayName", "mode", "instances"} {
				if v, ok := body[k]; ok {
					p[k] = v
				}
			}
			if v, ok := body["default"].(bool); ok && v {
				for _, o := range f.poolers {
					o["default"] = false
				}
				p["default"] = true
			}
			if ps, ok := body["parameters"].(map[string]any); ok {
				cur := p["userParameters"].(map[string]any)
				for k, v := range ps {
					if v == "" {
						delete(cur, k)
					} else {
						cur[k] = v
					}
				}
			}
			if v, ok := body["vpc"].(map[string]any); ok {
				f.setVPC(p, v)
			}
			if pub, ok := body["public"].(map[string]any); ok {
				if c := f.setPublic(id, p, pub); c != "" {
					fail(409, c, "public listener")
					return
				}
			}
			if p["phase"] != "PAUSED" {
				p["phase"] = "PROGRESSING"
			}
			writeJSON(200, f.view(id))
		case r.Method == "POST" && len(parts) == 3 && parts[2] == "pause":
			p["paused"], p["phase"] = true, "PAUSED"
			writeJSON(200, f.view(id))
		case r.Method == "POST" && len(parts) == 3 && parts[2] == "resume":
			p["paused"], p["phase"] = false, "READY"
			writeJSON(200, f.view(id))
		case r.Method == "DELETE" && len(parts) == 2:
			for _, l := range p["public"].([]any) {
				if l.(map[string]any)["port"] == 5432.0 {
					fail(409, "POOLER_IN_USE", "the public 5432 of the database leads to pooler "+id)
					return
				}
			}
			delete(f.poolers, id)
			for i, o := range f.order {
				if o == id {
					f.order = append(f.order[:i], f.order[i+1:]...)
					break
				}
			}
			w.WriteHeader(204)
		default:
			w.WriteHeader(405)
		}
	default:
		w.WriteHeader(404)
	}
}

// view adds the effective parameters: the user values over defaults.
func (f *fakePoolerAPI) view(id string) map[string]any {
	p := f.poolers[id]
	eff := map[string]any{"default_pool_size": "20", "max_client_conn": "5000", "server_lifetime": "3600"}
	for k, v := range p["userParameters"].(map[string]any) {
		eff[k] = v
	}
	p["parameters"] = eff
	return p
}

func (f *fakePoolerAPI) requests(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, b := range f.bodies {
		if strings.HasPrefix(b, prefix) {
			out = append(out, b)
		}
	}
	return out
}

func poolerConfig(endpoint, body string) string {
	return fmt.Sprintf(`
provider "h3" {
  api_endpoint = %q
  key_id       = "test-key"
  secret_key   = "test-secret"
}

locals {
  db = %q
}
%s
`, endpoint, poolerDB, body)
}

func mainPooler(extra string) string {
	return `
resource "h3_database_pooler" "main" {
  database_id = local.db
  pooler_id   = "main"
  role        = "rw"
` + extra + `
}
`
}

func TestDatabasePoolerLifecycle(t *testing.T) {
	database.SetPollIntervalForTest(t, 10*time.Millisecond)
	fake := newFakePoolerAPI()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	const pa = "h3_database_pooler.main"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		CheckDestroy: func(*terraform.State) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.poolers) != 0 {
				return fmt.Errorf("poolers left: %v", fake.order)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				// Defaults of h3db: VPC listener on 6432, no public one.
				Config: poolerConfig(srv.URL, mainPooler(`
  mode       = "transaction"
  parameters = { default_pool_size = "50" }`)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(pa, "id", poolerDB+"/main"),
					resource.TestCheckResourceAttr(pa, "phase", "READY"),
					resource.TestCheckResourceAttr(pa, "instances", "2"),
					resource.TestCheckResourceAttr(pa, "ready_instances", "2"),
					resource.TestCheckResourceAttr(pa, "default", "true"),
					resource.TestCheckResourceAttr(pa, "paused", "false"),
					resource.TestCheckResourceAttr(pa, "display_name", "main"),
					resource.TestCheckResourceAttr(pa, "k8s_name", "db-44444444-pooler-main"),
					resource.TestCheckResourceAttr(pa, "parameters.%", "1"),
					resource.TestCheckResourceAttr(pa, "effective_parameters.default_pool_size", "50"),
					resource.TestCheckResourceAttr(pa, "effective_parameters.server_lifetime", "3600"),
					resource.TestCheckResourceAttr(pa, "vpc_mode", "port"),
					resource.TestCheckResourceAttr(pa, "vpc_port", "6432"),
					resource.TestCheckResourceAttr(pa, "vpc_status", "assigned"),
					resource.TestCheckResourceAttr(pa, "vpc_endpoint", "10.0.0.50:6432"),
					resource.TestCheckResourceAttr(pa, "public_port", "0"),
					resource.TestCheckNoResourceAttr(pa, "public_via"),
					resource.TestCheckResourceAttr(pa, "public_endpoints.#", "0"),
					func(*terraform.State) error {
						b := fake.requests("POST /poolers ")
						if len(b) != 1 || strings.Contains(b[0], `"vpc"`) || strings.Contains(b[0], `"public"`) ||
							!strings.Contains(b[0], `"mode":"transaction"`) || strings.Contains(b[0], `"instances"`) {
							return fmt.Errorf("POST: %v", b)
						}
						return nil
					},
				),
			},
			{
				// mode, instances, parameters, a public listener in place, then pause.
				Config: poolerConfig(srv.URL, mainPooler(`
  mode        = "session"
  instances   = 3
  paused      = true
  public_port = 6432
  parameters  = { max_client_conn = "3000" }`)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(pa, plancheck.ResourceActionUpdate),
					plancheck.ExpectUnknownValue(pa, tfjsonpath.New("public_via")),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(pa, "mode", "session"),
					resource.TestCheckResourceAttr(pa, "instances", "3"),
					resource.TestCheckResourceAttr(pa, "paused", "true"),
					resource.TestCheckResourceAttr(pa, "phase", "PAUSED"),
					resource.TestCheckResourceAttr(pa, "parameters.%", "1"),
					resource.TestCheckResourceAttr(pa, "parameters.max_client_conn", "3000"),
					resource.TestCheckResourceAttr(pa, "effective_parameters.default_pool_size", "20"),
					resource.TestCheckResourceAttr(pa, "public_via", "port-sources"),
					resource.TestCheckResourceAttr(pa, "public_endpoints.0", "45.0.0.1:6432"),
					resource.TestCheckResourceAttr(pa, "public_addresses.0.eip_id", "eeeeeeee-0000-0000-0000-000000000001"),
					func(*terraform.State) error {
						// The removed key is sent empty (back to the default).
						b := fake.requests("PATCH /poolers/main")
						if len(b) != 1 || !strings.Contains(b[0], `"default_pool_size":""`) ||
							!strings.Contains(b[0], `"max_client_conn":"3000"`) || !strings.Contains(b[0], `"mode":"session"`) ||
							!strings.Contains(b[0], `"public":{"port":6432}`) || strings.Contains(b[0], `"vpc"`) {
							return fmt.Errorf("PATCH: %v", b)
						}
						if p := fake.requests("POST /poolers/main/pause"); len(p) != 1 {
							return fmt.Errorf("pause: %v", p)
						}
						return nil
					},
				),
			},
			{
				// Resume; an own VPC address (the port follows the mode);
				// public_port removed from the configuration keeps the listener.
				Config: poolerConfig(srv.URL, mainPooler(`
  mode       = "session"
  instances  = 3
  vpc_mode   = "address"
  parameters = { max_client_conn = "3000" }`)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectUnknownValue(pa, tfjsonpath.New("vpc_port")),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(pa, "phase", "READY"),
					resource.TestCheckResourceAttr(pa, "vpc_mode", "address"),
					resource.TestCheckResourceAttr(pa, "vpc_port", "5432"),
					resource.TestCheckResourceAttr(pa, "vpc_endpoint", "10.0.0.61:5432"),
					resource.TestCheckResourceAttr(pa, "public_port", "6432"),
					func(*terraform.State) error {
						if p := fake.requests("POST /poolers/main/resume"); len(p) != 1 {
							return fmt.Errorf("resume: %v", p)
						}
						return nil
					},
				),
			},
			{
				ResourceName:      pa,
				ImportState:       true,
				ImportStateId:     poolerDB + "/main",
				ImportStateVerify: true,
			},
			{
				// E1: the pooler on the main public port 5432.
				Config: poolerConfig(srv.URL, mainPooler(`
  mode        = "session"
  instances   = 3
  vpc_mode    = "none"
  public_port = 5432
  parameters  = { max_client_conn = "3000" }`)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(pa, "vpc_mode", "none"),
					resource.TestCheckNoResourceAttr(pa, "vpc_port"),
					resource.TestCheckNoResourceAttr(pa, "vpc_endpoint"),
					resource.TestCheckResourceAttr(pa, "public_endpoints.0", "45.0.0.1:5432"),
				),
			},
			{
				// A ro pooler without replicas: the h3db code is shown.
				Config: poolerConfig(srv.URL, mainPooler(`
  mode        = "session"
  instances   = 3
  vpc_mode    = "none"
  public_port = 5432
  parameters  = { max_client_conn = "3000" }`)+`
resource "h3_database_pooler" "ro" {
  database_id = local.db
  pooler_id   = "ro"
  role        = "ro"
}
`),
				ExpectError: regexp.MustCompile(`ROLE_NEEDS_REPLICA[\s\S]*at\s+least\s+two\s+instances`),
			},
			{
				// The pooler behind the public 5432 cannot be deleted.
				Config:      poolerConfig(srv.URL, ""),
				ExpectError: regexp.MustCompile(`POOLER_IN_USE[\s\S]*main\s+public\s+port\s+5432`),
			},
			{
				// Move it off 5432, then destroy works.
				Config: poolerConfig(srv.URL, mainPooler(`
  mode        = "session"
  instances   = 3
  vpc_mode    = "none"
  public_port = 0
  parameters  = { max_client_conn = "3000" }`)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(pa, "public_port", "0"),
					resource.TestCheckResourceAttr(pa, "public_endpoints.#", "0"),
				),
			},
		},
	})
}

func TestDatabasePoolerAPIErrorsAndDataSource(t *testing.T) {
	database.SetPollIntervalForTest(t, 10*time.Millisecond)
	fake := newFakePoolerAPI()
	fake.noEIP = true
	srv := httptest.NewServer(fake)
	defer srv.Close()

	ds := `
data "h3_database_poolers" "all" {
  database_id = local.db
  depends_on  = [h3_database_pooler.main]
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				Config:      poolerConfig(srv.URL, mainPooler(`  public_port = 6432`)),
				ExpectError: regexp.MustCompile(`PUBLIC_NEEDS_EIP[\s\S]*white_ip`),
			},
			{
				Config: poolerConfig(srv.URL, mainPooler(`  vpc_port = 7000`)+ds),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_database_pooler.main", "vpc_endpoint", "10.0.0.50:7000"),
					resource.TestCheckResourceAttr("h3_database_pooler.main", "mode", "session"),
					resource.TestCheckResourceAttr("data.h3_database_poolers.all", "poolers.#", "1"),
					resource.TestCheckResourceAttr("data.h3_database_poolers.all", "poolers.0.pooler_id", "main"),
					resource.TestCheckResourceAttr("data.h3_database_poolers.all", "poolers.0.vpc_port", "7000"),
					resource.TestCheckResourceAttr("data.h3_database_poolers.all", "poolers.0.effective_parameters.max_client_conn", "5000"),
					resource.TestCheckResourceAttr("data.h3_database_poolers.all", "budget.primary.used", "110"),
					resource.TestCheckNoResourceAttr("data.h3_database_poolers.all", "budget.replica.used"),
					resource.TestCheckResourceAttr("data.h3_database_poolers.all", "limits.max_instances", "16"),
					resource.TestCheckResourceAttr("data.h3_database_poolers.all", "limits.default_ports.ro", "6433"),
					func(*terraform.State) error {
						b := fake.requests("POST /poolers ")
						if len(b) != 2 || !strings.Contains(b[1], `"vpc":{"port":7000}`) {
							return fmt.Errorf("POST: %v", b)
						}
						return nil
					},
				),
			},
			{
				// The feature flag off: a clear error, the pooler stays in state.
				PreConfig:   func() { fake.mu.Lock(); fake.disabled = true; fake.mu.Unlock() },
				Config:      poolerConfig(srv.URL, mainPooler(`  vpc_port = 7000`)+ds),
				ExpectError: regexp.MustCompile(`POOLER_MULTI_DISABLED`),
			},
			{
				PreConfig: func() { fake.mu.Lock(); fake.disabled = false; fake.mu.Unlock() },
				Config:    poolerConfig(srv.URL, mainPooler(`  vpc_port = 7000`)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
			},
		},
	})
}

func TestDatabasePoolerValidation(t *testing.T) {
	cases := []struct{ extra, want string }{
		{`  instances = 0`, `instances must be at least 1`},
		{`  mode = "statement"`, `must be session or transaction`},
		{`  parameters = { pool_mode = "session" }`, `"pool_mode" is not allowed`},
		{`  parameters = { default_pool_size = "lots" }`, `not a non-negative integer`},
		{`  parameters = { client_tls_sslmode = "disable" }`, `is not one of require, prefer`},
		{`  vpc_mode = "lb"`, `must be port, address or none`},
		{"  vpc_mode = \"none\"\n  vpc_port = 6432", `remove vpc_port`},
		{`  vpc_port = 80`, `PORT_OUT_OF_RANGE`},
		{`  public_port = 70000`, `PORT_OUT_OF_RANGE`},
		{`  public_via = "external"`, `must be port-sources or service`},
		{`  display_name = "` + strings.Repeat("x", 65) + `"`, `at most 64`},
	}
	steps := make([]resource.TestStep, 0, len(cases)+2)
	for _, tc := range cases {
		steps = append(steps, resource.TestStep{
			Config:      poolerConfig("http://127.0.0.1:1", mainPooler(tc.extra)),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(regexp.QuoteMeta(tc.want)),
		})
	}
	steps = append(steps,
		resource.TestStep{
			Config:   poolerConfig("http://127.0.0.1:1", strings.Replace(mainPooler(""), `pooler_id   = "main"`, `pooler_id   = "-main"`, 1)),
			PlanOnly: true, ExpectError: regexp.MustCompile(`starting\s+and\s+ending\s+with\s+a\s+letter\s+or\s+digit`),
		},
		resource.TestStep{
			Config:   poolerConfig("http://127.0.0.1:1", strings.Replace(mainPooler(`  default = true`), `"rw"`, `"r"`, 1)),
			PlanOnly: true, ExpectError: regexp.MustCompile(`role = "rw" only`),
		},
	)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: providers(), Steps: steps})
}
