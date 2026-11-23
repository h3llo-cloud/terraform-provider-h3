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

	"h3terraform/internal/provider"
	"h3terraform/internal/services/database"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const project = "00000000-0000-0000-0000-0000000000aa"

// fakeH3DB mimics the h3db public API: CREATING -> RUNNING on the next read,
// RESIZING -> READY on the next read, increase-only resize, scale with
// SCALING -> READY on the next read and 409 while a resize/scale runs, delete.
type fakeH3DB struct {
	mu        sync.Mutex
	dbs       map[string]map[string]any
	seq       int
	calls     []string
	unsigned  int
	conflicts int
	scales    []map[string]any
	// EIP emulation: pool EIP ids are leased as eip-lease-N; boundEIP is
	// taken by another resource (409); denyLease answers 403 EIP_DENIED to a
	// new lease; deletes records DELETE query strings.
	leases    int
	boundEIP  string
	denyLease bool
	whiteIPs  []string
	deletes   []string
	// settings: PUT /settings bodies; pending restarts per database (one
	// GET /settings later the restart is done). users per database.
	settings []int
	pending  map[string]bool
	users    map[string][]map[string]any
	patches  []string
	// storage autoscale: settings per database, request bodies (create and
	// PUT), autoscaleOff = feature flag off (503 / 400 on create).
	autoscale     map[string]map[string]any
	autoscaleReqs []string
	autoscaleOff  bool
}

// leaseOrUse returns the eipId to report, or an HTTP error code and body.
func (f *fakeH3DB) leaseOrUse(eipID string) (string, int, map[string]string) {
	if eipID != "" && eipID == f.boundEIP {
		return "", 409, map[string]string{"code": "EIP_ALREADY_BOUND", "message": "EIP is bound to another resource"}
	}
	if eipID != "" {
		return eipID, 0, nil
	}
	if f.denyLease {
		return "", 403, map[string]string{"code": "EIP_DENIED", "message": "EIP quota exceeded"}
	}
	f.leases++
	return fmt.Sprintf("eeeeeeee-0000-0000-0000-%012d", f.leases), 0, nil
}

func gi(s any) int {
	var n int
	fmt.Sscanf(fmt.Sprint(s), "%dGi", &n)
	return n
}

func (f *fakeH3DB) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-H3-Signature") == "" || r.Header.Get("X-H3-Key-Id") == "" {
		f.unsigned++
		w.WriteHeader(401)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/databases/v1")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	f.calls = append(f.calls, r.Method+" "+path)
	writeJSON := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}

	if r.Method == "POST" && path == "" {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		netConfig := map[string]any{"subnetName": "subnet-default"}
		if req["whiteIp"] == true {
			eip, code, body := f.leaseOrUse(fmt.Sprint(req["eipId"]))
			if req["eipId"] == nil {
				eip, code, body = f.leaseOrUse("")
			}
			if code != 0 {
				writeJSON(code, body)
				return
			}
			netConfig["eipId"] = eip
		}
		var as map[string]any
		if a, ok := req["storageAutoscale"].(map[string]any); ok {
			b, _ := json.Marshal(a)
			f.autoscaleReqs = append(f.autoscaleReqs, "create:"+string(b))
			if a["enabled"] == true && f.autoscaleOff {
				writeJSON(400, map[string]string{"field": "storageAutoscale", "message": "storage autoscale is disabled"})
				return
			}
			as = a
		}
		f.seq++
		id := fmt.Sprintf("11111111-0000-0000-0000-%012d", f.seq)
		// h3db default since 2026-10-20: a new database without the field
		// gets autoscale enabled with maxSizeGb = 2 × size (at most 1024);
		// enabled without maxSizeGb takes the same.
		if !f.autoscaleOff {
			if as == nil {
				as = map[string]any{"enabled": true}
			}
			if as["enabled"] == true && as["maxSizeGb"] == nil {
				as["maxSizeGb"] = min(2*gi(req["sizeGb"]), 1024)
			}
		} else if as != nil && as["enabled"] != true {
			as = nil // ignored on a platform without the feature
		}
		if as != nil {
			if f.autoscale == nil {
				f.autoscale = map[string]map[string]any{}
			}
			f.autoscale[id] = autoscaleDefaults(as)
		}
		maxConn := 100
		if pc, ok := req["postgresConfig"].(map[string]any); ok {
			if n, ok := pc["maxConnections"].(float64); ok && n > 0 {
				if n > 1000 {
					writeJSON(400, map[string]string{"code": "bad_request", "message": "postgresConfig.maxConnections must be between 10 and 1000"})
					return
				}
				maxConn = int(n)
			}
		}
		version := req["version"]
		if version == nil || version == "" {
			version = "17.5"
		}
		db := map[string]any{
			"id": id, "projectId": req["projectId"], "name": req["name"], "engine": req["engine"],
			"version": version, "status": "CREATING", "sizeGb": req["sizeGb"], "cpu": req["cpu"],
			"memory": req["memory"], "whiteIp": req["whiteIp"], "internalIP": "10.0.0.5",
			"netConfig":    netConfig,
			"actualSizeGb": req["sizeGb"], "storageStatus": "READY", "computeStatus": "READY", "endpoint": "",
		}
		if req["engine"] == "postgres" {
			db["postgresConfig"] = map[string]any{"instances": 1, "maxConnections": maxConn}
		} else {
			db["mariadbConfig"] = map[string]any{"replicas": 1, "maxConnections": 200}
		}
		f.dbs[id] = db
		writeJSON(201, db)
		return
	}
	db, ok := f.dbs[parts[0]]
	if !ok {
		writeJSON(404, map[string]string{"code": "not_found", "message": "Database not found"})
		return
	}
	switch {
	case r.Method == "GET" && len(parts) == 1:
		if db["status"] == "CREATING" {
			db["status"] = "RUNNING"
		} else if db["storageStatus"] == "RESIZING" {
			db["storageStatus"] = "READY"
			db["actualSizeGb"] = db["sizeGb"]
		} else if db["computeStatus"] == "SCALING" {
			db["computeStatus"] = "READY"
		}
		writeJSON(200, db)
	case r.Method == "GET" && parts[1] == "credentials":
		writeJSON(200, map[string]string{"username": "app", "password": "s3cret-" + parts[0][len(parts[0])-2:]})
	case r.Method == "POST" && parts[1] == "resize":
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if gi(req["sizeGb"]) < gi(db["sizeGb"]) {
			writeJSON(400, map[string]string{"code": "bad_request", "message": "can only be increased"})
			return
		}
		if gi(req["sizeGb"]) > gi(db["sizeGb"]) {
			db["sizeGb"] = req["sizeGb"]
			db["storageStatus"] = "RESIZING"
		}
		writeJSON(200, db)
	case r.Method == "POST" && parts[1] == "scale":
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		same := fmt.Sprint(req["cpu"]) == fmt.Sprint(db["cpu"]) && req["memory"] == db["memory"]
		if !same && (db["storageStatus"] == "RESIZING" || db["computeStatus"] == "SCALING") {
			f.conflicts++
			writeJSON(409, map[string]string{"code": "conflict", "message": "resize or scale in progress"})
			return
		}
		if !same {
			f.scales = append(f.scales, req)
			db["cpu"], db["memory"], db["computeStatus"] = req["cpu"], req["memory"], "SCALING"
		}
		out := map[string]any{"restartRequired": !same, "expectedDowntimeSeconds": 60}
		for k, v := range db {
			out[k] = v
		}
		writeJSON(200, out)
	case r.Method == "POST" && parts[1] == "whiteip":
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.whiteIPs = append(f.whiteIPs, parts[2]+":"+req["eipId"])
		nc := db["netConfig"].(map[string]any)
		if parts[2] == "attach" {
			eip, code, body := f.leaseOrUse(req["eipId"])
			if code != 0 {
				writeJSON(code, body)
				return
			}
			nc["eipId"] = eip
			db["whiteIp"] = true
			db["endpoint"] = "203.0.113.10:5432"
		} else {
			delete(nc, "eipId")
			db["whiteIp"] = false
			db["endpoint"] = ""
		}
		writeJSON(200, map[string]string{"message": "ok"})
	case len(parts) > 1 && parts[1] == "storage-autoscale":
		f.serveAutoscale(r, parts[0], db, writeJSON)
	case len(parts) > 1 && parts[1] == "settings":
		f.serveSettings(w, r, parts[0], db, writeJSON)
	case len(parts) > 1 && parts[1] == "users":
		f.serveUsers(w, r, parts, writeJSON)
	case r.Method == "DELETE" && len(parts) == 1:
		f.deletes = append(f.deletes, r.URL.RawQuery)
		delete(f.dbs, parts[0])
		writeJSON(200, nil)
	default:
		writeJSON(404, nil)
	}
}

func config(endpoint, size string, cpu int, whiteIP bool) string {
	return configShape(endpoint, size, cpu, "4Gi", whiteIP, true)
}

func configShape(endpoint, size string, cpu int, memory string, whiteIP, waitResize bool) string {
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
  cpu        = %d
  memory     = %q
  size       = %q
  white_ip   = %v
  wait_for_resize = %v
}
`, endpoint, project, cpu, memory, size, whiteIP, waitResize)
}

func providers() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"h3": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
}

func TestDatabaseResourceLifecycle(t *testing.T) {
	database.SetPollIntervalForTest(t, 10*time.Millisecond)
	fake := &fakeH3DB{dbs: map[string]map[string]any{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	const addr = "h3_database.pg"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				Config: config(srv.URL, "10Gi", 2, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "status", "RUNNING"),
					resource.TestCheckResourceAttr(addr, "size", "10Gi"),
					resource.TestCheckResourceAttr(addr, "version", "17.5"),
					resource.TestCheckResourceAttr(addr, "subnet_name", "subnet-default"),
					resource.TestCheckResourceAttr(addr, "username", "app"),
					resource.TestCheckResourceAttrSet(addr, "password"),
					resource.TestCheckResourceAttr(addr, "white_ip", "false"),
				),
			},
			{
				// Size increase is an in-place update and waits for READY.
				Config: config(srv.URL, "20Gi", 2, false),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "size", "20Gi"),
					resource.TestCheckResourceAttr(addr, "actual_size", "20Gi"),
					resource.TestCheckResourceAttr(addr, "storage_status", "READY"),
				),
			},
			{
				// Shrinking is refused at plan time.
				Config:      config(srv.URL, "15Gi", 2, false),
				ExpectError: regexp.MustCompile(`can only be increased`),
			},
			{
				// Above the API ceiling is refused at validate time.
				Config:      config(srv.URL, "2000Gi", 2, false),
				ExpectError: regexp.MustCompile(`maximum of 1024Gi`),
			},
			{
				// white_ip changes in place.
				Config: config(srv.URL, "20Gi", 2, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "white_ip", "true"),
					resource.TestCheckResourceAttr(addr, "endpoint", "203.0.113.10:5432"),
				),
			},
			{
				// CPU up is an in-place update and waits for computeStatus READY.
				Config: config(srv.URL, "20Gi", 4, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "cpu", "4"),
					resource.TestCheckResourceAttr(addr, "memory", "4Gi"),
					resource.TestCheckResourceAttr(addr, "compute_status", "READY"),
					resource.TestCheckResourceAttr(addr, "status", "RUNNING"),
				),
			},
			{
				// Memory down is in place too.
				Config: configShape(srv.URL, "20Gi", 4, "8Gi", true, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(addr, "memory", "8Gi"),
			},
			{
				// Size and shape together without waiting for the resize: the
				// scale gets 409 while RESIZING, waits for READY and retries.
				Config: configShape(srv.URL, "30Gi", 2, "4Gi", true, false),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "size", "30Gi"),
					resource.TestCheckResourceAttr(addr, "cpu", "2"),
					resource.TestCheckResourceAttr(addr, "memory", "4Gi"),
					resource.TestCheckResourceAttr(addr, "compute_status", "READY"),
				),
			},
			{
				// Shape outside 1-16 Gi per vCPU is refused at validate time.
				Config:      configShape(srv.URL, "30Gi", 1, "32Gi", true, false),
				ExpectError: regexp.MustCompile(`per vCPU`),
			},
			{
				Config:      configShape(srv.URL, "30Gi", 300, "512Gi", true, false),
				ExpectError: regexp.MustCompile(`between 1 and 256`),
			},
			{
				// engine still forces a new database.
				Config: strings.Replace(configShape(srv.URL, "30Gi", 2, "4Gi", true, false), `"postgres"`, `"mariadb"`, 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.TestCheckResourceAttr(addr, "engine", "mariadb"),
			},
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"wait_for_resize"},
			},
		},
		CheckDestroy: func(_ *terraform.State) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.dbs) != 0 {
				return fmt.Errorf("databases left after destroy: %d", len(fake.dbs))
			}
			return nil
		},
	})

	if n := countSuffix(fake, "/resize"); n != 2 {
		t.Fatalf("resize calls = %d, want 2", n)
	}
	if len(fake.scales) != 3 {
		t.Fatalf("applied scales = %v, want 3 (4/4Gi, 4/8Gi, 2/4Gi)", fake.scales)
	}
	if fake.conflicts == 0 {
		t.Fatal("the scale issued during a resize must have hit 409 and been retried")
	}
	if fake.unsigned != 0 {
		t.Fatalf("%d unsigned requests", fake.unsigned)
	}
}

func countSuffix(f *fakeH3DB, suffix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasSuffix(c, suffix) {
			n++
		}
	}
	return n
}

func (f *fakeH3DB) serveSettings(_ http.ResponseWriter, r *http.Request, id string, db map[string]any, writeJSON func(int, any)) {
	pc, _ := db["postgresConfig"].(map[string]any)
	if pc == nil {
		writeJSON(400, map[string]string{"code": "UNSUPPORTED_ENGINE"})
		return
	}
	if f.pending == nil {
		f.pending = map[string]bool{}
	}
	cur := 0
	fmt.Sscan(fmt.Sprint(pc["maxConnections"]), &cur)
	if r.Method == "PUT" {
		var req map[string]int
		_ = json.NewDecoder(r.Body).Decode(&req)
		n := req["maxConnections"]
		if n < 20 || n > gi(db["memory"])*100 {
			writeJSON(400, map[string]string{"code": "INVALID_MAX_CONNECTIONS", "message": "out of bounds"})
			return
		}
		if n != cur {
			f.settings = append(f.settings, n)
			pc["maxConnections"] = n
			f.pending[id] = true
		}
		writeJSON(200, map[string]any{"restartRequired": n != cur})
		return
	}
	running := cur
	pending := f.pending[id]
	if pending {
		running = 100
		f.pending[id] = false
	}
	writeJSON(200, map[string]any{"maxConnections": cur, "runningMaxConnections": running, "pendingRestart": pending})
}

func (f *fakeH3DB) serveUsers(_ http.ResponseWriter, r *http.Request, parts []string, writeJSON func(int, any)) {
	if f.users == nil {
		f.users = map[string][]map[string]any{}
	}
	id := parts[0]
	find := func(name string) map[string]any {
		for _, u := range f.users[id] {
			if u["name"] == name {
				return u
			}
		}
		return nil
	}
	switch {
	case r.Method == "GET" && len(parts) == 2:
		out := []map[string]any{{"name": "app", "builtIn": true, "connectionLimit": -1, "grants": []any{}, "status": "READY"}}
		out = append(out, f.users[id]...)
		writeJSON(200, out)
	case r.Method == "POST" && len(parts) == 2:
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		limit := req["connectionLimit"]
		if limit == nil || limit == float64(0) {
			limit = float64(-1)
		}
		grants := req["grants"]
		if grants == nil {
			grants = []any{}
		}
		roles := req["systemRoles"]
		if roles == nil {
			roles = []any{}
		}
		settings := req["settings"]
		if settings == nil {
			settings = map[string]any{}
		}
		f.users[id] = append(f.users[id], map[string]any{"name": req["name"], "builtIn": false, "connectionLimit": limit, "grants": grants,
			"systemRoles": roles, "settings": settings, "status": "READY"})
		writeJSON(201, map[string]string{"username": fmt.Sprint(req["name"]), "password": "pw-" + fmt.Sprint(req["name"])})
	case len(parts) >= 3 && find(parts[2]) == nil:
		writeJSON(404, map[string]string{"code": "NOT_FOUND"})
	case r.Method == "PATCH" && len(parts) == 3:
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		if roles, ok := raw["systemRoles"]; ok {
			f.patches = append(f.patches, fmt.Sprintf("%s roles=%v", parts[2], roles))
			find(parts[2])["systemRoles"] = roles
			writeJSON(200, map[string]string{"message": "user updated"})
			return
		}
		if settings, ok := raw["settings"]; ok {
			f.patches = append(f.patches, fmt.Sprintf("%s settings=%v", parts[2], settings))
			find(parts[2])["settings"] = settings
			writeJSON(200, map[string]string{"message": "user updated"})
			return
		}
		n, _ := raw["connectionLimit"].(float64)
		if !(n == -1 || (n >= 1 && n <= 5000)) {
			writeJSON(400, map[string]string{"code": "INVALID_CONNECTION_LIMIT"})
			return
		}
		f.patches = append(f.patches, fmt.Sprintf("%s=%v", parts[2], n))
		find(parts[2])["connectionLimit"] = n
		writeJSON(200, map[string]string{"message": "user updated"})
	case r.Method == "PUT" && len(parts) == 4 && parts[3] == "grants":
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		find(parts[2])["grants"] = req["grants"]
		writeJSON(200, map[string]string{"message": "grants applied"})
	case r.Method == "GET" && len(parts) == 4 && parts[3] == "credentials":
		writeJSON(200, map[string]string{"username": parts[2], "password": "pw-" + parts[2]})
	case r.Method == "DELETE" && len(parts) == 3:
		kept := f.users[id][:0]
		for _, u := range f.users[id] {
			if u["name"] != parts[2] {
				kept = append(kept, u)
			}
		}
		f.users[id] = kept
		writeJSON(200, map[string]string{"message": "user deleted"})
	default:
		writeJSON(404, nil)
	}
}
