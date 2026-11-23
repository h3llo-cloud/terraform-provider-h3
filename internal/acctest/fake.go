package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"h3terraform/internal/common"
	"h3terraform/internal/provider"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// Test credentials and ids accepted by the fake API.
const (
	KeyID     = "test-key-id"
	SecretKey = "test-secret"
	Project   = "00000000-0000-4000-8000-0000000000aa"
	User      = "00000000-0000-4000-8000-0000000000bb"
	// SSHKey1 is an h3ssh key id of User used by VM configs (VM create does not look it up).
	SSHKey1 = "33333333-0000-4000-8000-000000000001"
)

// Obj is a JSON object as the API returns it.
type Obj = map[string]any

// Fake is an in-memory h3llo cloud public API. Every request must carry a
// valid HMAC signature, checked exactly as h3ars does. JSON field names and
// casing follow the production services (origin/main, 07.10.2026):
// snake_case for h3vm, h3disks and h3ssh; camelCase responses for h3ovn and h3s3.
type Fake struct {
	t   *testing.T
	Srv *httptest.Server

	mu    sync.Mutex
	seq   int
	store map[string]map[string]Obj
	calls []string
	hooks map[string]func(w http.ResponseWriter, q *Req) bool

	// SSHAccessDisabled emulates h3vm with VM_SSH_ACCESS_ENABLED off.
	SSHAccessDisabled bool
	// K8sNoNodeLocalDNS emulates an h3k8s without NodeLocal DNSCache: no
	// nodeLocalDNSCache in responses, no PATCH /node-local-dns-cache route.
	K8sNoNodeLocalDNS bool
	// K8sNoNetwork emulates an h3k8s without workers in the tenant VPC: no `network` in the cluster.
	K8sNoNetwork bool
	// DeletionProtectionDisabled emulates h3vm with VM_DELETION_PROTECTION_ENABLED off.
	DeletionProtectionDisabled bool
	// CustomImagesDisabled and PlacementGroupsDisabled: the h3vm flags off (404 *_DISABLED).
	CustomImagesDisabled    bool
	PlacementGroupsDisabled bool
	deletedKeys             map[string]bool
}

var pollOnce sync.Once

// NewFake starts the fake API for the duration of the test and shortens polling.
func NewFake(t *testing.T) *Fake {
	t.Helper()
	// One write for the whole test binary: the tests run in parallel (t.Parallel) and must
	// not write this global concurrently. No test here needs the 5 s default back.
	pollOnce.Do(func() { common.PollInterval = 5 * time.Millisecond })

	f := &Fake{t: t, store: map[string]map[string]Obj{}, hooks: map[string]func(http.ResponseWriter, *Req) bool{}}
	f.Srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Srv.Close)
	return f
}

// Providers returns the provider factories for resource.UnitTest.
func Providers() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"h3": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
}

// ConfigWithProject is Config with a provider default project.
func (f *Fake) ConfigWithProject(project, body string) string {
	return fmt.Sprintf(`
provider "h3" {
  api_endpoint = %q
  key_id       = %q
  secret_key   = %q
  max_retries  = 0
  project_id   = %q
}
`, f.Srv.URL, KeyID, SecretKey, project) + body
}

// Config prefixes a configuration with the provider block pointing at the fake.
func (f *Fake) Config(body string) string {
	return fmt.Sprintf(`
provider "h3" {
  api_endpoint = %q
  key_id       = %q
  secret_key   = %q
  max_retries  = 0
}
`, f.Srv.URL, KeyID, SecretKey) + body
}

func (f *Fake) newID() string {
	f.seq++
	return fmt.Sprintf("11111111-0000-4000-8000-%012d", f.seq)
}

func (f *Fake) put(coll, id string, o Obj) {
	if f.store[coll] == nil {
		f.store[coll] = map[string]Obj{}
	}
	f.store[coll][id] = o
}

func (f *Fake) get(coll, id string) Obj { return f.store[coll][id] }

// Get returns a copy of a stored object, nil if absent.
func (f *Fake) Get(coll, id string) Obj {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.store[coll][id]
	if o == nil {
		return nil
	}
	out := Obj{}
	for k, v := range o {
		out[k] = v
	}
	return out
}

// Count returns the number of objects in a collection.
func (f *Fake) Count(coll string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.store[coll])
}

// Mutate changes a stored object, as a change made in the console would (drift).
func (f *Fake) Mutate(coll, id string, fn func(Obj)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.store[coll][id])
}

// Remove deletes an object behind Terraform's back.
func (f *Fake) Remove(coll, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remove(coll, id)
}

func (f *Fake) remove(coll, id string) {
	if coll == "sshkeys" {
		if f.deletedKeys == nil {
			f.deletedKeys = map[string]bool{}
		}
		f.deletedKeys[id] = true
	}
	delete(f.store[coll], id)
}

// Only returns the id of the single object of a collection.
func (f *Fake) Only(coll string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.store[coll]) != 1 {
		f.t.Fatalf("collection %s has %d objects, want 1", coll, len(f.store[coll]))
	}
	for id := range f.store[coll] {
		return id
	}
	return ""
}

// Calls returns the "METHOD path" log of requests starting with prefix.
func (f *Fake) Calls(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// ResetCalls forgets the request log.
func (f *Fake) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

// Hook runs h before the service handler for requests whose "METHOD path"
// starts with key; h returns true when it wrote the response.
func (f *Fake) Hook(key string, h func(w http.ResponseWriter, q *Req) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hooks[key] = h
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if r.Header.Get("X-H3-Key-Id") != KeyID ||
		!VerifyHMAC(SecretKey, BuildCanonicalRequest(r, body), r.Header.Get("X-H3-Signature")) {
		WriteJSON(w, 401, Obj{"error": "Authentication failed: invalid signature"})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	call := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, call)

	q := &Req{R: r, Body: body}
	keys := make([]string, 0, len(f.hooks))
	for k := range f.hooks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.HasPrefix(call, k) && f.hooks[k](w, q) {
			return
		}
	}
	for _, s := range services {
		if strings.HasPrefix(r.URL.Path, s.prefix) {
			q.Parts = splitPath(strings.TrimPrefix(r.URL.Path, s.prefix))
			s.handle(f, w, q)
			return
		}
	}
	WriteJSON(w, 404, Obj{"error": "no route " + call})
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// Req is a parsed request for service handlers.
type Req struct {
	R     *http.Request
	Body  []byte
	Parts []string // path segments below the service prefix
}

// JSON decodes the request body.
func (q *Req) JSON() Obj {
	var o Obj
	_ = json.Unmarshal(q.Body, &o)
	if o == nil {
		o = Obj{}
	}
	return o
}

// Query returns a query parameter.
func (q *Req) Query(k string) string { return q.R.URL.Query().Get(k) }

type service struct {
	prefix string
	handle func(f *Fake, w http.ResponseWriter, q *Req)
}

var services []service

func register(prefix string, h func(f *Fake, w http.ResponseWriter, q *Req)) {
	services = append(services, service{prefix: prefix, handle: h})
	sort.SliceStable(services, func(i, j int) bool { return len(services[i].prefix) > len(services[j].prefix) })
}

// WriteJSON writes a JSON response.
func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func notFound(w http.ResponseWriter, what string) {
	WriteJSON(w, 404, Obj{"code": "not_found", "message": what + " not found"})
}

func clone(o Obj) Obj {
	b, _ := json.Marshal(o)
	var c Obj
	_ = json.Unmarshal(b, &c)
	return c
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// SeedVM creates a running VM directly in the fake (made outside Terraform).
func (f *Fake) SeedVM(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.newID()
	f.put("vms", id, Obj{"id": id, "project_id": Project, "name": name, "status": "RUNNING", "cpu": 2, "memory": "4Gi",
		"disk_size": "20Gi", "image": "ubuntu:24.04", "endpoint": "", "ssh_key": "ssh-ed25519 AAAAtest user@host", "white_ip": false,
		"_managed": false,
		"_grants": Obj{User: Obj{"user_id": User, "mode": "selected", "key_ids": []any{SSHKey1}, "granted_by": User,
			"granted_at": "2026-10-01T10:00:00Z", "source": "creator"}},
		"net_config": Obj{"subnet_name": "subnet-default", "subnet_id": "22222222-0000-4000-8000-000000000001", "vpc_id": Project, "internal_ip": "10.0.0.11"}})
	return id
}
