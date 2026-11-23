package sg

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"h3terraform/internal/client"
)

// ---- pure helpers -------------------------------------------------------------

func TestNormalizeMatchesServerDefaults(t *testing.T) {
	cfg := Rule{Direction: "ingress", Protocol: "tcp", PortFrom: 22, RemoteCIDR: "10.0.0.5"}
	api := Rule{ID: "x", Direction: "ingress", Action: "allow", Protocol: "tcp", PortFrom: 22, PortTo: 22,
		RemoteType: "cidr", RemoteCIDR: "10.0.0.5/32", Priority: 100}
	if !sameRule(cfg, api) {
		t.Fatalf("config %v and API %v must compare equal", ruleKey(cfg), ruleKey(api))
	}
	if !sameRule(Rule{Direction: "egress", Protocol: "udp", RemoteCIDR: "2001:db8::1/64"},
		Rule{Direction: "egress", Protocol: "udp", PortFrom: 1, PortTo: 65535, RemoteCIDR: "2001:db8::/64"}) {
		t.Fatal("udp without ports = all ports; IPv6 CIDR canonical")
	}
	if sameRule(cfg, Rule{Direction: "ingress", Protocol: "tcp", PortFrom: 23, RemoteCIDR: "10.0.0.5"}) {
		t.Fatal("different ports must differ")
	}
}

func TestSameRuleSetIsOrderInsensitive(t *testing.T) {
	a := []Rule{{Direction: "ingress", RemoteCIDR: "0.0.0.0/0"}, {Direction: "egress", RemoteCIDR: "::/0"}}
	b := []Rule{{Direction: "egress", RemoteCIDR: "::/0"}, {Direction: "ingress", RemoteCIDR: "0.0.0.0/0"}}
	if !sameRuleSet(a, b) || sameRuleSet(a, b[:1]) {
		t.Fatal("multiset comparison broken")
	}
}

func TestValidateRule(t *testing.T) {
	bad := []Rule{
		{Direction: "both", RemoteCIDR: "0.0.0.0/0"},
		{Direction: "ingress", Action: "reject", RemoteCIDR: "0.0.0.0/0"},
		{Direction: "ingress", Protocol: "sctp", RemoteCIDR: "0.0.0.0/0"},
		{Direction: "ingress", Priority: 171, RemoteCIDR: "0.0.0.0/0"},
		{Direction: "ingress", Protocol: "icmp", PortFrom: 1, RemoteCIDR: "0.0.0.0/0"},
		{Direction: "ingress", Protocol: "tcp", PortFrom: 100, PortTo: 10, RemoteCIDR: "0.0.0.0/0"},
		{Direction: "ingress"},
		{Direction: "ingress", RemoteCIDR: "0.0.0.0/0", RemoteSecurityGroupID: "sg"},
	}
	for i, r := range bad {
		if validateRule(r) == nil {
			t.Errorf("case %d must fail: %+v", i, r)
		}
	}
	if err := validateRule(Rule{Direction: "ingress", Protocol: "tcp", PortFrom: 443, RemoteSecurityGroupID: "sg"}); err != nil {
		t.Fatal(err)
	}
}

func TestKeepIDsAndFindNewRule(t *testing.T) {
	cur := []Rule{{ID: "a", Direction: "ingress", Protocol: "tcp", PortFrom: 22, PortTo: 22, RemoteCIDR: "0.0.0.0/0", Priority: 100, Action: "allow"}}
	got := keepIDs([]Rule{{Direction: "ingress", Protocol: "tcp", PortFrom: 22, RemoteCIDR: "0.0.0.0/0"}, {Direction: "egress", RemoteCIDR: "0.0.0.0/0"}}, cur)
	if got[0].ID != "a" || got[1].ID != "" {
		t.Fatalf("ids: %+v", got)
	}
	want := Rule{Direction: "egress", RemoteCIDR: "::/0"}
	after := append(cur, Rule{ID: "b", Direction: "ingress", RemoteCIDR: "1.1.1.1/32"}, Rule{ID: "c", Direction: "egress", RemoteCIDR: "::/0"})
	if r, ok := findNewRule(cur, after, want); !ok || r.ID != "c" {
		t.Fatalf("must pick the matching new rule, not a parallel one: %+v %v", r, ok)
	}
}

// ---- fake h3ovn public API ----------------------------------------------------

const (
	project = "00000000-0000-0000-0000-0000000000aa"
	vmID    = "00000000-0000-0000-0000-0000000000b1"
)

type fakeOVN struct {
	mu       sync.Mutex
	groups   map[string]*Group
	vmGroups map[string][]string
	seq      int
	conflict int // answer the next N PUTs with REVISION_CONFLICT
	calls    []string
	// noPoint: the point attach/detach routes are not deployed (public API
	// before h3ovn MR !14): plain-text 404 like Go's mux.
	noPoint       bool
	pointConflict int // answer the next N point calls with REVISION_CONFLICT
}

func newFakeOVN() *fakeOVN {
	f := &fakeOVN{groups: map[string]*Group{}, vmGroups: map[string][]string{}}
	def := f.add(project, "default", true, []Rule{
		{Direction: "ingress", RemoteCIDR: "0.0.0.0/0"}, {Direction: "ingress", RemoteCIDR: "::/0"},
		{Direction: "egress", RemoteCIDR: "0.0.0.0/0"}, {Direction: "egress", RemoteCIDR: "::/0"},
	})
	f.groups[def].Description = "Allows all inbound and outbound traffic."
	f.vmGroups[vmID] = []string{def}
	return f
}

func (f *fakeOVN) id(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%04d", prefix, f.seq)
}

func (f *fakeOVN) canon(rs []Rule) []Rule {
	out := make([]Rule, 0, len(rs))
	for _, r := range rs {
		n := normalize(r)
		n.ID = r.ID
		if n.ID == "" {
			n.ID = f.id("rule")
		}
		out = append(out, n)
	}
	return out
}

func (f *fakeOVN) add(projectID, name string, isDefault bool, rules []Rule) string {
	id := f.id("sg")
	f.groups[id] = &Group{ID: id, ProjectID: projectID, Name: name, IsDefault: isDefault, Rules: f.canon(rules),
		Status: "ACTIVE", Revision: "1"}
	return id
}

func (f *fakeOVN) bump(g *Group) {
	f.seq++
	g.Revision = fmt.Sprint(f.seq)
}

func (f *fakeOVN) view(g *Group) Group {
	c := *g
	c.Rules = append([]Rule{}, g.Rules...)
	for _, ids := range f.vmGroups {
		for _, id := range ids {
			if id == g.ID {
				c.AttachedCount++
			}
		}
	}
	return c
}

func (f *fakeOVN) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	out := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if v != nil {
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	fail := func(code int, c string) {
		out(code, map[string]any{"error": map[string]string{"code": c, "message": c}})
	}
	if r.Header.Get("X-H3-Signature") == "" {
		fail(401, "UNSIGNED")
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/ovn/v1/")
	parts := strings.Split(p, "/")
	switch {
	case p == "security-groups" && r.Method == "GET":
		list := []Group{}
		for _, g := range f.groups {
			if g.ProjectID == r.URL.Query().Get("project_id") {
				list = append(list, f.view(g))
			}
		}
		out(200, list)
	case p == "security-groups" && r.Method == "POST":
		var req struct {
			ProjectID   string  `json:"project_id"`
			Name        string  `json:"name"`
			Description string  `json:"description"`
			Rules       *[]Rule `json:"rules"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		rules := []Rule{{Direction: "egress", RemoteCIDR: "0.0.0.0/0"}, {Direction: "egress", RemoteCIDR: "::/0"}}
		if req.Rules != nil {
			rules = *req.Rules
		}
		id := f.add(req.ProjectID, req.Name, false, rules)
		f.groups[id].Description = req.Description
		out(201, f.view(f.groups[id]))
	case parts[0] == "security-groups" && len(parts) >= 2:
		g, ok := f.groups[parts[1]]
		if !ok {
			fail(404, "NOT_FOUND")
			return
		}
		switch {
		case len(parts) == 2 && r.Method == "GET":
			out(200, f.view(g))
		case len(parts) == 2 && r.Method == "PUT":
			var req UpdateGroupRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if f.conflict > 0 || (req.Revision != "" && req.Revision != g.Revision) {
				if f.conflict > 0 {
					f.conflict--
					f.bump(g)
				}
				fail(409, "REVISION_CONFLICT")
				return
			}
			g.Name, g.Description, g.Rules = req.Name, req.Description, f.canon(req.Rules)
			f.bump(g)
			out(200, f.view(g))
		case len(parts) == 2 && r.Method == "DELETE":
			if g.IsDefault {
				fail(409, "SG_DEFAULT_UNDELETABLE")
				return
			}
			if f.view(g).AttachedCount > 0 {
				fail(409, "SG_IN_USE")
				return
			}
			delete(f.groups, g.ID)
			out(204, nil)
		case len(parts) == 3 && r.Method == "POST":
			var rule Rule
			_ = json.NewDecoder(r.Body).Decode(&rule)
			g.Rules = append(g.Rules, f.canon([]Rule{rule})...)
			f.bump(g)
			out(201, f.view(g))
		case len(parts) == 4 && r.Method == "DELETE":
			kept := []Rule{}
			for _, x := range g.Rules {
				if x.ID != parts[3] {
					kept = append(kept, x)
				}
			}
			if len(kept) == len(g.Rules) {
				fail(404, "NOT_FOUND")
				return
			}
			g.Rules = kept
			f.bump(g)
			out(200, f.view(g))
		default:
			fail(405, "METHOD")
		}
	case parts[0] == "vm" && len(parts) == 4 && (r.Method == "POST" || r.Method == "DELETE"):
		if f.noPoint {
			http.Error(w, "404 page not found", 404)
			return
		}
		ids, ok := f.vmGroups[parts[1]]
		if !ok {
			fail(404, "NOT_FOUND")
			return
		}
		if f.pointConflict > 0 {
			f.pointConflict--
			fail(409, "REVISION_CONFLICT")
			return
		}
		sgID := parts[3]
		var next []string
		has := false
		for _, id := range ids {
			if id == sgID {
				has = true
				if r.Method == "DELETE" {
					continue
				}
			}
			next = append(next, id)
		}
		if r.Method == "POST" && !has {
			if _, ok := f.groups[sgID]; !ok || len(ids) >= 5 {
				fail(400, "VALIDATION_FAILED")
				return
			}
			next = append(next, sgID)
		}
		if next == nil {
			next = []string{}
		}
		f.vmGroups[parts[1]] = next
		resp := VMGroups{ResourceType: "vm", ResourceID: parts[1], SecurityGroupIDs: next}
		out(200, resp)
	case parts[0] == "vm" && len(parts) == 3:
		ids, ok := f.vmGroups[parts[1]]
		if !ok {
			fail(404, "NOT_FOUND")
			return
		}
		if r.Method == "PUT" {
			var req struct {
				SecurityGroupIDs *[]string `json:"security_group_ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			// h3ovn 5a00bf8: [] detaches every group; null is an error.
			if req.SecurityGroupIDs == nil || len(*req.SecurityGroupIDs) > 5 {
				fail(400, "VALIDATION_FAILED")
				return
			}
			f.vmGroups[parts[1]] = *req.SecurityGroupIDs
			ids = *req.SecurityGroupIDs
		}
		resp := VMGroups{ResourceType: "vm", ResourceID: parts[1], SecurityGroupIDs: ids, ForeignGroups: []string{"sg-project-x"}}
		for _, id := range ids {
			if g, ok := f.groups[id]; ok {
				resp.SecurityGroups = append(resp.SecurityGroups, f.view(g))
			}
		}
		out(200, resp)
	default:
		fail(404, "NOT_FOUND")
	}
}

func newTestClient(t *testing.T, f *fakeOVN) *client.Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := client.NewClient(client.Config{BaseURL: srv.URL, KeyID: "k", SecretKey: "s", Timeout: 5 * time.Second, MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUpdateGroupRetriesOnRevisionConflict(t *testing.T) {
	f := newFakeOVN()
	c := newTestClient(t, f)
	id := f.add(project, "web", false, nil)
	f.conflict = 2
	g, err := updateGroup(context.Background(), c, id, func(cur *Group) UpdateGroupRequest {
		return UpdateGroupRequest{Name: "web2", Rules: cur.Rules}
	})
	if err != nil || g.Name != "web2" {
		t.Fatalf("update after 2 conflicts: %v %+v", err, g)
	}
	f.conflict = 5
	if _, err := updateGroup(context.Background(), c, id, func(cur *Group) UpdateGroupRequest {
		return UpdateGroupRequest{Name: "web3", Rules: cur.Rules}
	}); !isRevisionConflict(err) {
		t.Fatalf("persistent conflict must surface: %v", err)
	}
}

func TestFindGroupAndDefaultLookup(t *testing.T) {
	f := newFakeOVN()
	c := newTestClient(t, f)
	g, err := findGroup(context.Background(), c, "", project, "DEFAULT")
	if err != nil || !g.IsDefault {
		t.Fatalf("lookup by name: %v %+v", err, g)
	}
	if _, err := findGroup(context.Background(), c, "", project, "nope"); err == nil {
		t.Fatal("unknown name must fail")
	}
	if _, err := findGroup(context.Background(), c, "", "", ""); err == nil {
		t.Fatal("missing arguments must fail")
	}
	vm := VMGroups{ResourceID: vmID, SecurityGroups: []Group{{ID: "x", ProjectID: project}}}
	def, err := defaultGroupFor(context.Background(), c, &vm, nil)
	if err != nil || def != g.ID {
		t.Fatalf("default for VM: %v %s", err, def)
	}
}
