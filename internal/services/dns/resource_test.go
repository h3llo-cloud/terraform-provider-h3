package dns_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"h3terraform/internal/provider"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const project = "00000000-0000-0000-0000-0000000000aa"

// fakeH3DNS mimics the h3dns tenant API used by the provider: zone CRUD,
// RRset PUT/GET/DELETE with server-side canonical host names (trailing dot),
// EIP PTR. Every request must be HMAC-signed.
type fakeH3DNS struct {
	mu       sync.Mutex
	seq      int
	zones    map[string]map[string]any
	rrsets   map[string]map[string]any // zone|name|type
	ptr      map[string]string         // eip -> hostname
	unsigned int
	// concurrent: before the next conditional PUT/DELETE, another writer
	// changes the RRset (between Terraform's refresh and apply).
	concurrent bool
	patches    []map[string]any
	condDelete int
}

func (f *fakeH3DNS) bump(k string) {
	f.seq++
	f.rrsets[k]["etag"] = fmt.Sprintf(`"%d"`, f.seq)
	f.rrsets[k]["records"] = []string{"203.0.113.99"}
}

func newFake() *fakeH3DNS {
	return &fakeH3DNS{zones: map[string]map[string]any{}, rrsets: map[string]map[string]any{}, ptr: map[string]string{}}
}

func canon(typ, v string) string {
	dot := func(h string) string {
		if strings.HasSuffix(h, ".") || h == "." {
			return strings.ToLower(h)
		}
		return strings.ToLower(h) + "."
	}
	f := strings.Fields(v)
	switch typ {
	case "CNAME", "NS", "ALIAS":
		return dot(v)
	case "MX":
		return f[0] + " " + dot(f[1])
	}
	return v
}

func (f *fakeH3DNS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-H3-Signature") == "" || r.Header.Get("X-H3-Key-Id") == "" {
		f.unsigned++
		w.WriteHeader(401)
		return
	}
	out := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/dns/v1/")
	parts := strings.Split(p, "/")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	problem := func(code int, kind, c string) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "https://docs.h3llo.cloud/dns/errors/" + kind, "title": kind, "status": code, "code": c})
	}
	switch {
	case p == "nameservers" && r.Method == "GET":
		out(200, map[string]any{"nameservers": []string{"ns1.h3llo.cloud", "ns2.h3llo.cloud", "ns3.h3llo.cloud", "ns4.h3llo.cloud"},
			"verification_txt_label": "_h3dns-verification"})
	case len(parts) == 3 && parts[0] == "projects" && parts[2] == "zones" && r.Method == "POST":
		f.seq++
		id := fmt.Sprintf("0192aaaa-0000-7000-8000-%012d", f.seq)
		z := map[string]any{"id": id, "project_id": parts[1], "name": body["name"], "kind": "public", "status": "pending_verification",
			"nameservers": []string{"ns1.h3llo.cloud", "ns2.h3llo.cloud", "ns3.h3llo.cloud", "ns4.h3llo.cloud"},
			"default_ttl": body["default_ttl"], "verification": map[string]any{"method": "ns"}, "labels": body["labels"],
			"verification_txt": map[string]any{"name": "_h3dns-verification." + fmt.Sprint(body["name"]), "value": "h3dns-verify=abc"},
			"dnssec":           map[string]any{"enabled": body["dnssec"] == true, "state": "unsigned"}}
		f.zones[id] = z
		out(201, z)
	case len(parts) == 3 && parts[0] == "projects" && parts[2] == "zones" && r.Method == "GET":
		var items []any
		for _, z := range f.zones {
			if z["project_id"] == parts[1] {
				items = append(items, z)
			}
		}
		out(200, map[string]any{"items": items, "total": len(items)})
	case len(parts) == 2 && parts[0] == "zones":
		z, ok := f.zones[parts[1]]
		if !ok {
			out(404, map[string]any{"title": "Not found"})
			return
		}
		switch r.Method {
		case "GET":
			z["status"] = "active" // verified meanwhile
			out(200, z)
		case "PATCH":
			f.patches = append(f.patches, body)
			if v, ok := body["dnssec"].(bool); ok {
				d := map[string]any{"enabled": v, "state": "unsigned"}
				if v {
					d["state"], d["ds"] = "waiting_ds", []string{"12345 13 2 abcdef"}
				}
				z["dnssec"] = d
			}
			if v, ok := body["default_ttl"]; ok {
				z["default_ttl"] = v
			}
			if v, ok := body["labels"].(map[string]any); ok {
				if len(v) > 0 {
					z["labels"] = v
				} else {
					delete(z, "labels")
				}
			}
			out(200, z)
		case "DELETE":
			for k := range f.rrsets {
				if strings.HasPrefix(k, parts[1]+"|") {
					delete(f.rrsets, k)
				}
			}
			delete(f.zones, parts[1])
			w.WriteHeader(202)
		}
	case len(parts) == 5 && parts[0] == "zones" && parts[2] == "recordsets":
		z, ok := f.zones[parts[1]]
		if !ok {
			out(404, nil)
			return
		}
		name, typ := parts[3], parts[4]
		k := parts[1] + "|" + name + "|" + typ
		switch r.Method {
		case "GET":
			rs, ok := f.rrsets[k]
			if !ok {
				out(404, nil)
				return
			}
			w.Header().Set("ETag", fmt.Sprint(rs["etag"]))
			out(200, rs)
		case "PUT":
			cur, existed := f.rrsets[k]
			if r.Header.Get("If-None-Match") == "*" && existed {
				problem(412, "precondition-failed", "RECORDSET_EXISTS")
				return
			}
			if im := r.Header.Get("If-Match"); im != "" {
				if f.concurrent && existed {
					f.concurrent = false
					f.bump(k)
					cur = f.rrsets[k]
				}
				if !existed || (im != "*" && im != fmt.Sprint(cur["etag"])) {
					problem(412, "precondition-failed", "ETAG_MISMATCH")
					return
				}
			}
			if existed && cur["managed_by"] != "user" && cur["managed_by"] != nil {
				problem(409, "managed-recordset", "MANAGED_RECORDSET")
				return
			}
			var recs []string
			for _, v := range body["records"].([]any) {
				recs = append(recs, canon(typ, v.(string)))
			}
			sort.Strings(recs)
			ttl := body["ttl"]
			if ttl == nil {
				ttl = z["default_ttl"]
			}
			fq := fmt.Sprint(z["name"])
			if name != "@" {
				fq = name + "." + fq
			}
			f.seq++
			rs := map[string]any{"id": k, "zone_id": parts[1], "name": name, "type": typ, "ttl": ttl, "records": recs,
				"managed_by": "user", "fqdn": fq, "etag": fmt.Sprintf(`"%d"`, f.seq)}
			f.rrsets[k] = rs
			w.Header().Set("ETag", fmt.Sprint(rs["etag"]))
			if existed {
				out(200, rs)
			} else {
				out(201, rs)
			}
		case "DELETE":
			cur, existed := f.rrsets[k]
			if !existed {
				out(404, nil)
				return
			}
			if im := r.Header.Get("If-Match"); im != "" {
				f.condDelete++
				if f.concurrent {
					f.concurrent = false
					f.bump(k)
				}
				if im != fmt.Sprint(f.rrsets[k]["etag"]) {
					problem(412, "precondition-failed", "ETAG_MISMATCH")
					return
				}
			}
			_ = cur
			delete(f.rrsets, k)
			w.WriteHeader(204)
		}
	case len(parts) == 5 && parts[0] == "projects" && parts[2] == "eips" && parts[4] == "ptr":
		eip := parts[3]
		view := func(h, src string) map[string]any {
			return map[string]any{"eip_id": eip, "ip": "198.51.100.10", "hostname": h, "source": src, "forward_check": "ok"}
		}
		switch r.Method {
		case "PUT":
			f.ptr[eip] = fmt.Sprint(body["hostname"])
			out(200, view(f.ptr[eip], "tenant"))
		case "GET":
			if h, ok := f.ptr[eip]; ok {
				out(200, view(h, "tenant"))
				return
			}
			out(200, view("ip-198-51-100-10.msk1.ip.h3svc.test", "default"))
		case "DELETE":
			delete(f.ptr, eip)
			out(200, view("ip-198-51-100-10.msk1.ip.h3svc.test", "default"))
		}
	default:
		out(404, nil)
	}
}

func providers() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"h3": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
}

func config(url string, wwwRecords string, ttl int, dnssec bool) string {
	return fmt.Sprintf(`
provider "h3" {
  api_endpoint = %q
  key_id       = "test-key"
  secret_key   = "test-secret"
}

resource "h3_dns_zone" "main" {
  project_id  = %q
  name        = "example.com"
  dnssec      = %t
  default_ttl = 300
}

resource "h3_dns_recordset" "www" {
  zone_id = h3_dns_zone.main.id
  name    = "www"
  type    = "A"
  ttl     = %d
  records = [%s]
}

resource "h3_dns_recordset" "mx" {
  zone_id = h3_dns_zone.main.id
  name    = "@"
  type    = "MX"
  records = ["10 mail.example.com", "20 Backup.Example.com."]
}

resource "h3_dns_ptr" "mail" {
  project_id = %q
  eip_id     = "eip-1"
  hostname   = "mail.example.com"
}

data "h3_dns_zone" "existing" {
  project_id = %q
  name       = h3_dns_zone.main.name
}
`, url, project, dnssec, ttl, wwwRecords, project, project)
}

func TestDNSResourcesLifecycle(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				Config: config(srv.URL, `"192.0.2.1", "192.0.2.2"`, 300, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_dns_zone.main", "status", "pending_verification"),
					resource.TestCheckResourceAttr("h3_dns_zone.main", "nameservers.#", "4"),
					resource.TestCheckResourceAttr("h3_dns_zone.main", "verification_txt_name", "_h3dns-verification.example.com"),
					resource.TestCheckResourceAttr("h3_dns_recordset.www", "records.#", "2"),
					resource.TestCheckResourceAttr("h3_dns_recordset.www", "fqdn", "www.example.com"),
					resource.TestCheckResourceAttr("h3_dns_recordset.mx", "name", "@"),
					// the user's spelling is kept although the server returns canonical names (no perpetual diff)
					resource.TestCheckTypeSetElemAttr("h3_dns_recordset.mx", "records.*", "10 mail.example.com"),
					resource.TestCheckResourceAttr("h3_dns_ptr.mail", "ip_address", "198.51.100.10"),
					resource.TestCheckResourceAttrPair("data.h3_dns_zone.existing", "id", "h3_dns_zone.main", "id"),
				),
			},
			{
				// replace the whole RRset, change TTL, sign the zone
				Config: config(srv.URL, `"192.0.2.3"`, 600, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("h3_dns_recordset.www", "records.#", "1"),
					resource.TestCheckTypeSetElemAttr("h3_dns_recordset.www", "records.*", "192.0.2.3"),
					resource.TestCheckResourceAttr("h3_dns_recordset.www", "ttl", "600"),
					resource.TestCheckResourceAttr("h3_dns_zone.main", "status", "active"),
					resource.TestCheckResourceAttr("h3_dns_zone.main", "dnssec_state", "waiting_ds"),
					resource.TestCheckResourceAttr("h3_dns_zone.main", "ds_records.0", "12345 13 2 abcdef"),
				),
			},
			{
				ResourceName:      "h3_dns_recordset.www",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources["h3_dns_zone.main"].Primary.ID + "/www/A", nil
				},
			},
			{
				ResourceName:            "h3_dns_zone.main",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"force_destroy"},
			},
			{
				ResourceName:      "h3_dns_ptr.mail",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateId:     project + "/eip-1",
			},
		},
		CheckDestroy: func(*terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.zones)+len(f.rrsets)+len(f.ptr) != 0 {
				return fmt.Errorf("left over: zones=%d rrsets=%d ptr=%d", len(f.zones), len(f.rrsets), len(f.ptr))
			}
			if f.unsigned != 0 {
				return fmt.Errorf("%d unsigned requests", f.unsigned)
			}
			return nil
		},
	})
}

func TestRecordSetRefusesToOverwrite(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	f.zones["z1"] = map[string]any{"id": "z1", "project_id": project, "name": "example.com", "default_ttl": 300,
		"dnssec": map[string]any{"enabled": false}, "verification": map[string]any{"method": "ns"}}
	f.rrsets["z1|www|A"] = map[string]any{"id": "x", "name": "www", "type": "A", "ttl": 300, "records": []string{"192.0.2.9"}, "managed_by": "h3lb"}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
provider "h3" {
  api_endpoint = %q
  key_id       = "k"
  secret_key   = "s"
}
resource "h3_dns_recordset" "www" {
  zone_id = "z1"
  name    = "www"
  type    = "A"
  records = ["192.0.2.1"]
}`, srv.URL),
			ExpectError: regexpMust(`already exists \(managed_by=h3lb\)`),
		}},
	})
}

func regexpMust(s string) *regexp.Regexp { return regexp.MustCompile(s) }

func rrConfig(url, records string) string {
	return fmt.Sprintf(`
provider "h3" {
  api_endpoint = %q
  key_id       = "k"
  secret_key   = "s"
  project_id   = %q
}

resource "h3_dns_zone" "main" {
  name   = "example.org"
  labels = { env = "test" }
}

resource "h3_dns_recordset" "www" {
  zone_id = h3_dns_zone.main.id
  name    = "www"
  type    = "A"
  records = [%s]
}

data "h3_dns_nameservers" "h3" {}
`, url, project, records)
}

// ETag/If-Match: a change made between refresh and apply is not overwritten
// and not deleted; the zone PATCH carries only what changed.
func TestRecordSetConditionalWrites(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providers(),
		Steps: []resource.TestStep{
			{
				Config: rrConfig(srv.URL, `"192.0.2.1"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("h3_dns_recordset.www", "etag"),
					resource.TestCheckResourceAttr("data.h3_dns_nameservers.h3", "nameservers.#", "4"),
					resource.TestCheckResourceAttr("data.h3_dns_nameservers.h3", "nameservers.0", "ns1.h3llo.cloud"),
					resource.TestCheckResourceAttr("h3_dns_zone.main", "project_id", project),
				),
			},
			{
				PreConfig:   func() { f.mu.Lock(); f.concurrent = true; f.mu.Unlock() },
				Config:      rrConfig(srv.URL, `"192.0.2.2"`),
				ExpectError: regexpMust(`changed outside Terraform`),
			},
			{
				// After a refresh the change is visible and applied normally.
				Config: rrConfig(srv.URL, `"192.0.2.2"`),
				Check:  resource.TestCheckTypeSetElemAttr("h3_dns_recordset.www", "records.*", "192.0.2.2"),
			},
			{
				// Labels removed: PATCH carries labels={} and nothing else.
				Config: strings.Replace(rrConfig(srv.URL, `"192.0.2.2"`), `labels = { env = "test" }`, "", 1),
				Check: func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					last := f.patches[len(f.patches)-1]
					if _, ok := last["dnssec"]; ok || len(last) != 1 {
						return fmt.Errorf("PATCH sent %v, want only labels", last)
					}
					if l, ok := last["labels"].(map[string]any); !ok || len(l) != 0 {
						return fmt.Errorf("PATCH labels %v, want {}", last["labels"])
					}
					return nil
				},
			},
			{
				// Delete is conditional too: a concurrent change stops it.
				PreConfig:   func() { f.mu.Lock(); f.concurrent = true; f.mu.Unlock() },
				Config:      noRR(srv.URL),
				ExpectError: regexpMust(`changed outside Terraform`),
			},
			// Terraform hands Delete the state of the last apply, not the
			// refreshed one: persist the refresh first, as the error says.
			{RefreshState: true, ExpectNonEmptyPlan: true},
			{
				Config: noRR(srv.URL),
				Check: func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					if len(f.rrsets) != 0 || f.condDelete < 2 {
						return fmt.Errorf("rrsets=%d conditional deletes=%d", len(f.rrsets), f.condDelete)
					}
					return nil
				},
			},
		},
	})
}

func noRR(url string) string {
	c := rrConfig(url, `"192.0.2.2"`)
	i := strings.Index(c, `resource "h3_dns_recordset"`)
	j := strings.Index(c, `data "h3_dns_nameservers"`)
	return c[:i] + c[j:]
}
