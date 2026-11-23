package database_test

import (
	"encoding/json"
	"fmt"
	"io"
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

const (
	seedSourceID  = "22222222-0000-0000-0000-000000000001"
	seedReplicaID = "22222222-0000-0000-0000-000000000002"
)

// fakeReplicaAPI adds the read replica routes of h3db (TRD 17) to fakeH3DB:
// POST /{id}/replicas, POST /{id}/promote (PROMOTING, standalone on the next
// GET), PATCH/GET /settings of a replica, 409 REPLICA_READ_ONLY for users of
// a replica and 409 SOURCE_HAS_REPLICAS on DELETE of a source.
type fakeReplicaAPI struct {
	*fakeH3DB
	createReqs []map[string]any
	patches    []map[string]any
	promotes   []string
	promoting  map[string]bool
	rset       map[string]map[string]any
}

func newFakeReplicaAPI() *fakeReplicaAPI {
	return &fakeReplicaAPI{
		fakeH3DB:  &fakeH3DB{dbs: map[string]map[string]any{}},
		promoting: map[string]bool{},
		rset:      map[string]map[string]any{},
	}
}

// seed adds a RUNNING source (50Gi, max_connections 200) and a replica of it.
func (f *fakeReplicaAPI) seed() {
	src := map[string]any{
		"id": seedSourceID, "projectId": project, "name": "src", "engine": "postgres", "version": "17.5",
		"status": "RUNNING", "sizeGb": "50Gi", "cpu": 2, "memory": "4Gi", "whiteIp": false,
		"internalIP": "10.0.0.4", "netConfig": map[string]any{"subnetName": "subnet-default"},
		"storageStatus": "READY", "computeStatus": "READY",
		"postgresConfig": map[string]any{"instances": 1, "maxConnections": 200},
	}
	rep := map[string]any{}
	for k, v := range src {
		rep[k] = v
	}
	rep["id"], rep["name"], rep["replicaOf"] = seedReplicaID, "src-ro", seedSourceID
	rep["replication"] = map[string]any{"state": "STREAMING", "lagSeconds": 0}
	f.dbs[seedSourceID], f.dbs[seedReplicaID] = src, rep
}

func (f *fakeReplicaAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	handled := r.Header.Get("X-H3-Signature") != "" && f.serveReplica(w, r)
	f.mu.Unlock()
	if !handled {
		f.fakeH3DB.ServeHTTP(w, r)
	}
}

func (f *fakeReplicaAPI) serveReplica(w http.ResponseWriter, r *http.Request) bool {
	path := strings.TrimPrefix(r.URL.Path, "/api/databases/v1")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	writeJSON := func(code int, v any) bool {
		f.calls = append(f.calls, r.Method+" "+path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
		return true
	}
	db, ok := f.dbs[parts[0]]
	if !ok || parts[0] == "" {
		return false
	}
	isReplica := db["replicaOf"] != nil && db["replicaOf"] != ""
	switch {
	case r.Method == "GET" && len(parts) == 1 && f.promoting[parts[0]]:
		// The promotion is done: standalone, then the base GET answers.
		delete(f.promoting, parts[0])
		db["promotedFrom"] = db["replicaOf"]
		delete(db, "replicaOf")
		delete(db, "replication")
		return false
	case r.Method == "POST" && len(parts) == 2 && parts[1] == "replicas":
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.createReqs = append(f.createReqs, req)
		if isReplica {
			return writeJSON(400, map[string]string{"code": "SOURCE_IS_REPLICA", "message": "source is a replica"})
		}
		if gi(req["sizeGb"]) < gi(db["sizeGb"]) {
			return writeJSON(400, map[string]string{"code": "REPLICA_SIZE_TOO_SMALL", "message": "sizeGb below the source"})
		}
		f.seq++
		id := fmt.Sprintf("33333333-0000-0000-0000-%012d", f.seq)
		maxConn := db["postgresConfig"].(map[string]any)["maxConnections"]
		if n, ok := req["maxConnections"].(float64); ok && n > 0 {
			maxConn = n
		}
		rep := map[string]any{
			"id": id, "projectId": db["projectId"], "name": req["name"], "engine": "postgres", "version": db["version"],
			"status": "CREATING", "sizeGb": req["sizeGb"], "cpu": req["cpu"], "memory": req["memory"],
			"whiteIp": req["whiteIp"], "internalIP": "10.0.0.9", "netConfig": map[string]any{"subnetName": "subnet-default"},
			"actualSizeGb": req["sizeGb"], "storageStatus": "READY", "computeStatus": "READY", "endpoint": "",
			"postgresConfig": map[string]any{"instances": 1, "maxConnections": maxConn},
			"replicaOf":      parts[0],
			"replication":    map[string]any{"state": "STREAMING", "lagSeconds": 1, "lagBytes": 8192},
		}
		set := map[string]any{"maxStandbyStreamingDelaySeconds": 900, "maxStandbyArchiveDelaySeconds": 900, "hotStandbyFeedback": false}
		if s, ok := req["settings"].(map[string]any); ok {
			for k, v := range s {
				set[k] = v
			}
		}
		f.rset[id] = set
		f.dbs[id] = rep
		return writeJSON(201, rep)
	case r.Method == "POST" && len(parts) == 2 && parts[1] == "promote":
		b, _ := io.ReadAll(r.Body)
		f.promotes = append(f.promotes, strings.TrimSpace(string(b)))
		if !isReplica {
			return writeJSON(400, map[string]string{"code": "NOT_A_REPLICA", "message": "not a replica"})
		}
		db["replication"] = map[string]any{"state": "PROMOTING", "lagSeconds": 0}
		f.promoting[parts[0]] = true
		return writeJSON(202, db)
	case len(parts) == 2 && parts[1] == "settings" && r.Method == "PATCH":
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.patches = append(f.patches, req)
		if !isReplica {
			return writeJSON(400, map[string]string{"code": "INVALID_PARAMETER", "message": "replica settings on a non-replica"})
		}
		for k, v := range req {
			f.rset[parts[0]][k] = v
		}
		return writeJSON(200, db)
	case len(parts) == 2 && parts[1] == "settings" && r.Method == "GET" && isReplica:
		out := map[string]any{"replica": true, "pendingRestart": false}
		for k, v := range f.rset[parts[0]] {
			out[k] = v
		}
		out["maxConnections"] = db["postgresConfig"].(map[string]any)["maxConnections"]
		return writeJSON(200, out)
	case len(parts) >= 2 && parts[1] == "users" && r.Method != "GET" && isReplica:
		return writeJSON(409, map[string]string{"code": "REPLICA_READ_ONLY", "message": "the database is a read replica"})
	case r.Method == "DELETE" && len(parts) == 1:
		var reps []map[string]string
		for id, d := range f.dbs {
			if d["replicaOf"] == parts[0] {
				reps = append(reps, map[string]string{"id": id, "name": fmt.Sprint(d["name"])})
			}
		}
		if len(reps) > 0 {
			return writeJSON(409, map[string]any{"code": "SOURCE_HAS_REPLICAS", "message": "the database has read replicas", "replicas": reps})
		}
	}
	return false
}

const providerBlock = `
provider "h3" {
  api_endpoint = %q
  key_id       = "test-key"
  secret_key   = "test-secret"
}
`

// replicaConfig: a source and h3_database.ro; replicaOf adds
// replica_of = h3_database.src.id, extra goes into h3_database.ro.
func replicaConfig(endpoint string, replicaOf bool, extra string) string {
	ref := ""
	if replicaOf {
		ref = "replica_of = h3_database.src.id"
	}
	return fmt.Sprintf(providerBlock+`
resource "h3_database" "src" {
  project_id = %q
  name       = "pg-src"
  engine     = "postgres"
  cpu        = 2
  memory     = "4Gi"
  size       = "20Gi"
}

resource "h3_database" "ro" {
  project_id = %q
  name       = "pg-ro"
  engine     = "postgres"
  cpu        = 2
  memory     = "4Gi"
  size       = "20Gi"
  %s
  %s
}
`, endpoint, project, project, ref, extra)
}

func TestDatabaseReplicaLifecycle(t *testing.T) {
	database.SetPollIntervalForTest(t, 10*time.Millisecond)
	fake := newFakeReplicaAPI()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	const ro = "h3_database.ro"
	settings := func(streaming int, hot bool) string {
		return fmt.Sprintf("replica_settings {\n    max_standby_streaming_delay_seconds = %d\n    hot_standby_feedback = %v\n  }", streaming, hot)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				// Create: POST /{source}/replicas, wait for RUNNING.
				Config: replicaConfig(srv.URL, true, "replica_settings {\n    max_standby_streaming_delay_seconds = 300\n  }"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(ro, "replica_of", "h3_database.src", "id"),
					resource.TestCheckResourceAttr(ro, "status", "RUNNING"),
					resource.TestCheckResourceAttr(ro, "version", "17.5"),
					resource.TestCheckResourceAttr(ro, "replication_state", "STREAMING"),
					resource.TestCheckResourceAttr(ro, "replication_lag_seconds", "1"),
					resource.TestCheckResourceAttr(ro, "replica_settings.max_standby_streaming_delay_seconds", "300"),
					resource.TestCheckResourceAttr(ro, "replica_settings.max_standby_archive_delay_seconds", "900"),
					resource.TestCheckResourceAttr(ro, "replica_settings.hot_standby_feedback", "false"),
					resource.TestCheckNoResourceAttr(ro, "promoted_from"),
					resource.TestCheckNoResourceAttr("h3_database.src", "replica_of"),
					resource.TestCheckNoResourceAttr("h3_database.src", "replication_state"),
				),
			},
			{
				// replica_settings change in place: PATCH /settings, no restart.
				Config: replicaConfig(srv.URL, true, settings(300, true)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(ro, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(ro, "replica_settings.hot_standby_feedback", "true"),
			},
			{
				// Import fills replica_of (replica_settings stays null).
				ResourceName:            ro,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"wait_for_resize", "replica_settings"},
			},
			{
				// Removing replica_of neither promotes nor re-creates.
				Config:      replicaConfig(srv.URL, false, ""),
				ExpectError: regexp.MustCompile(`read replica of [0-9a-f-]+; to\s+make\s+it\s+standalone\s+set\s+promote\s+=\s+true`),
			},
			{
				// promote = true with replica_of kept: in place, POST /promote.
				Config: replicaConfig(srv.URL, true, settings(300, true)+"\n  promote = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(ro, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(ro, "promoted_from", "h3_database.src", "id"),
					resource.TestCheckResourceAttrPair(ro, "replica_of", "h3_database.src", "id"),
					resource.TestCheckNoResourceAttr(ro, "replication_state"),
					resource.TestCheckNoResourceAttr(ro, "replication_lag_seconds"),
				),
			},
			{
				// The same configuration after the promotion: empty plan.
				Config:   replicaConfig(srv.URL, true, settings(300, true)+"\n  promote = true"),
				PlanOnly: true,
			},
			{
				// replica_settings cannot change after the promotion.
				Config:      replicaConfig(srv.URL, true, settings(60, true)+"\n  promote = true"),
				ExpectError: regexp.MustCompile(`this database has\s+been\s+promoted`),
			},
			{
				// Removing replica_of, promote and the block afterwards is an
				// in-place no-op, not a replacement.
				Config: replicaConfig(srv.URL, false, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(ro, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(ro, "replica_of"),
					resource.TestCheckResourceAttrPair(ro, "promoted_from", "h3_database.src", "id"),
				),
			},
		},
	})

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.createReqs) != 1 {
		t.Fatalf("replica create requests = %d, want 1", len(fake.createReqs))
	}
	req := fake.createReqs[0]
	for _, k := range []string{"version", "postgresConfig", "backup", "initialDatabase", "projectId", "engine"} {
		if _, ok := req[k]; ok {
			t.Errorf("replica create request has %q: %v", k, req)
		}
	}
	if req["name"] != "pg-ro" || req["sizeGb"] != "20Gi" || req["memory"] != "4Gi" || req["whiteIp"] != false {
		t.Errorf("replica create request = %v", req)
	}
	want := map[string]any{"maxStandbyStreamingDelaySeconds": float64(300), "maxStandbyArchiveDelaySeconds": float64(900), "hotStandbyFeedback": false}
	if fmt.Sprint(req["settings"]) != fmt.Sprint(want) {
		t.Errorf("settings = %v, want %v", req["settings"], want)
	}
	if len(fake.patches) != 1 || fake.patches[0]["hotStandbyFeedback"] != true ||
		fake.patches[0]["maxStandbyStreamingDelaySeconds"] != float64(300) {
		t.Errorf("settings patches = %v, want one with hotStandbyFeedback=true", fake.patches)
	}
	if len(fake.promotes) != 1 || fake.promotes[0] != `{"backup":{"enabled":true}}` {
		t.Errorf("promote requests = %v, want one {\"backup\":{\"enabled\":true}}", fake.promotes)
	}
	if fake.unsigned != 0 {
		t.Fatalf("%d unsigned requests", fake.unsigned)
	}
}

// Promote together with removing replica_of in one apply: no replacement.
func TestDatabaseReplicaPromoteRemovingReplicaOf(t *testing.T) {
	database.SetPollIntervalForTest(t, 10*time.Millisecond)
	fake := newFakeReplicaAPI()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	const ro = "h3_database.ro"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{Config: replicaConfig(srv.URL, true, "")},
			{
				Config: replicaConfig(srv.URL, false, "promote = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(ro, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(ro, "replica_of"),
					resource.TestCheckResourceAttrPair(ro, "promoted_from", "h3_database.src", "id"),
				),
			},
			{
				// promote on a database that never was a replica.
				Config: strings.Replace(replicaConfig(srv.URL, false, "promote = true"),
					"size       = \"20Gi\"\n}", "size       = \"20Gi\"\n  promote    = true\n}", 1),
				ExpectError: regexp.MustCompile(`Not a read replica`),
			},
		},
	})
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.promotes) != 1 {
		t.Fatalf("promote requests = %v, want 1", fake.promotes)
	}
}

func seededConfig(endpoint, body string) string {
	return fmt.Sprintf(providerBlock+`
resource "h3_database" "ro" {
  project_id = %q
  name       = "pg-ro"
  engine     = "postgres"
  cpu        = 2
  memory     = "4Gi"
  size       = "50Gi"
  %s
}
`, endpoint, project, body)
}

func TestDatabaseReplicaValidation(t *testing.T) {
	fake := newFakeReplicaAPI()
	fake.seed()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	src := fmt.Sprintf("replica_of = %q\n", seedSourceID)
	cases := []struct {
		name, config, want string
	}{
		{"engine", strings.Replace(seededConfig(srv.URL, src), `"postgres"`, `"mariadb"`, 1), `PostgreSQL only`},
		{"version", seededConfig(srv.URL, src+`version = "16.4"`), `version cannot be set on a read replica`},
		{"subnet", seededConfig(srv.URL, src+`subnet_name = "other"`), `subnet_name cannot be set on a read replica`},
		{"settings without replica_of", seededConfig(srv.URL, "replica_settings {\n hot_standby_feedback = true\n}"), `replica_settings requires replica_of`},
		{"delay range", seededConfig(srv.URL, src+"replica_settings {\n max_standby_archive_delay_seconds = 4000\n}"), `0\.\.3600`},
		{"negative delay", seededConfig(srv.URL, src+"replica_settings {\n max_standby_streaming_delay_seconds = -1\n}"), `0\.\.3600`},
		{"size below source", strings.Replace(seededConfig(srv.URL, src), `"50Gi"`, `"20Gi"`, 1), `REPLICA_SIZE_TOO_SMALL`},
		{"max_connections below source", seededConfig(srv.URL, src+"max_connections = 100"), `REPLICA_MAX_CONNECTIONS_TOO_LOW`},
		{"source is a replica", seededConfig(srv.URL, fmt.Sprintf("replica_of = %q", seedReplicaID)), `SOURCE_IS_REPLICA`},
		{"source not found", seededConfig(srv.URL, `replica_of = "99999999-0000-0000-0000-000000000000"`), `Source database not found`},
		{"promote on create", seededConfig(srv.URL, src+"promote = true"), `promote cannot be set on create`},
		{"user on a replica", fmt.Sprintf(providerBlock+`
resource "h3_database_user" "u" {
  database_id = %q
  name        = "analyst"
}
`, srv.URL, seedReplicaID), `REPLICA_READ_ONLY`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: providers(),
				Steps: []resource.TestStep{{
					Config:      tc.config,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
		})
	}
}

// A valid replica of the seeded source plans; a user on the source too.
func TestDatabaseReplicaPlanOK(t *testing.T) {
	fake := newFakeReplicaAPI()
	fake.seed()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{{
			Config: seededConfig(srv.URL, fmt.Sprintf("replica_of = %q\n max_connections = 300", seedSourceID)) +
				fmt.Sprintf(`
resource "h3_database_user" "u" {
  database_id = %q
  name        = "analyst"
}
`, seedSourceID),
			PlanOnly:           true,
			ExpectNonEmptyPlan: true,
		}},
	})
}
