package iam_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"h3terraform/internal/acctest"
)

// A fake h3iam /api/iam/v1 (origin/main 12.11.2026: internal/presentation/rest/server/service_accounts.go,
// audit.go; api/openapi/v1/service-accounts.yaml). Every request must be HMAC-signed (checked as h3ars does).
// Errors are {"error": "..."}; a project outside the cpm rollout answers 404 on every route (rolloutGate).

const (
	project = acctest.Project
	user    = acctest.User
)

type obj = map[string]any

type fake struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	seq    int
	store  map[string]map[string]obj
	calls  int
	iamOff map[string]bool
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, store: map[string]map[string]obj{}, iamOff: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) config(body string) string {
	return fmt.Sprintf(`
provider "h3" {
  api_endpoint = %q
  key_id       = %q
  secret_key   = %q
  max_retries  = 0
  project_id   = %q
}
`, f.srv.URL, acctest.KeyID, acctest.SecretKey, project) + body
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if r.Header.Get("X-H3-Key-Id") != acctest.KeyID ||
		!acctest.VerifyHMAC(acctest.SecretKey, acctest.BuildCanonicalRequest(r, body), r.Header.Get("X-H3-Signature")) {
		writeJSON(w, 401, obj{"error": "Authentication failed: invalid signature"})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	const prefix = "/api/iam/v1/projects/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeJSON(w, 404, obj{"error": "no route"})
		return
	}
	f.iam(w, &req{r: r, body: body, parts: strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/"), "/")})
}

type req struct {
	r     *http.Request
	body  []byte
	parts []string
}

func (q *req) JSON() obj {
	var o obj
	_ = json.Unmarshal(q.body, &o)
	if o == nil {
		o = obj{}
	}
	return o
}

func (q *req) Query(k string) string { return q.r.URL.Query().Get(k) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func (f *fake) newID() string {
	f.seq++
	return fmt.Sprintf("11111111-0000-4000-8000-%012d", f.seq)
}

func (f *fake) put(coll, id string, o obj) {
	if f.store[coll] == nil {
		f.store[coll] = map[string]obj{}
	}
	f.store[coll][id] = o
}

func (f *fake) get(coll, id string) obj { return f.store[coll][id] }

func (f *fake) count(coll string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.store[coll])
}

func (f *fake) remove(coll, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.store[coll], id)
}

func (f *fake) only(coll string) string {
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

func (f *fake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fake) setOff(off bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.iamOff[project] = off
}

var fakeSANameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,61}[a-z0-9]$`)

func (f *fake) saID() string {
	f.seq++
	const abc = "abcdefghijklmnopqrstuvwxyz234567"
	b := []byte(strings.Repeat("a", 26))
	for i, n := 25, f.seq; n > 0 && i >= 0; i, n = i-1, n/32 {
		b[i] = abc[n%32]
	}
	return "sa-" + string(b)
}

func iamErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, obj{"error": msg})
}

func (f *fake) iamAudit(project, action, sa string, after obj) {
	f.seq++
	id := f.newID()
	ev := obj{"event_id": id, "occurred_at": time.Now().UTC().Add(time.Duration(f.seq) * time.Millisecond).Format(time.RFC3339Nano),
		"source": "h3iam", "project_id": project, "actor": obj{"type": "TYPE_USER", "id": user}, "action": action,
		"result": "RESULT_OK", "ip": "203.0.113.7", "request_id": "req-" + id, "user_agent": "terraform", "_seq": f.seq}
	if sa != "" {
		ev["resource"] = obj{"type": "service_account", "id": sa}
	}
	if len(after) > 0 {
		ev["after"] = after
	}
	f.put("iam_audit", id, ev)
}

func publicIAM(o obj) obj {
	out := obj{}
	for k, v := range o {
		if !strings.HasPrefix(k, "_") {
			out[k] = v
		}
	}
	return out
}

// iam serves /api/iam/v1/projects/{project}/service-accounts[/{sa}[/keys[/{key}]]] and /{project}/audit.
func (f *fake) iam(w http.ResponseWriter, q *req) {
	p := q.parts
	if len(p) < 2 {
		iamErr(w, 404, "not found")
		return
	}
	project := p[0]
	if f.iamOff[project] {
		iamErr(w, 404, "not found")
		return
	}
	m := q.r.Method
	switch {
	case len(p) == 2 && p[1] == "audit" && m == "GET":
		f.iamAuditList(w, q, project)
	case p[1] != "service-accounts":
		iamErr(w, 404, "not found")
	case len(p) == 2 && m == "GET":
		list := []obj{}
		for _, sa := range f.store["iam_sas"] {
			if sa["project_id"] == project {
				list = append(list, publicIAM(sa))
			}
		}
		sort.Slice(list, func(i, j int) bool { return str(list[i]["created_at"]) < str(list[j]["created_at"]) })
		writeJSON(w, 200, obj{"service_accounts": list})
	case len(p) == 2 && m == "POST":
		b := q.JSON()
		name, role := str(b["name"]), str(b["role"])
		if !fakeSANameRe.MatchString(name) {
			iamErr(w, 400, "name must match ^[a-z][a-z0-9-]{1,61}[a-z0-9]$")
			return
		}
		if role == "" {
			role = "PROJ-MEMB"
		}
		if role != "PROJ-ADM" && role != "PROJ-MEMB" && role != "PROJ-VIEWER" {
			iamErr(w, 400, "role must be one of PROJ-ADM, PROJ-MEMB, PROJ-VIEWER")
			return
		}
		for _, sa := range f.store["iam_sas"] {
			if sa["project_id"] == project && sa["name"] == name {
				iamErr(w, 409, "service account "+name+" already exists")
				return
			}
		}
		now := time.Now().UTC().Add(time.Duration(f.seq) * time.Millisecond).Format(time.RFC3339Nano)
		id := f.saID()
		sa := obj{"id": id, "principal_id": f.newID(), "project_id": project, "name": name, "description": str(b["description"]),
			"role": role, "disabled": false, "created_by": user, "created_at": now, "updated_at": now}
		f.put("iam_sas", id, sa)
		f.iamAudit(project, "service_account.create", id, obj{"name": name, "role": role})
		writeJSON(w, 201, publicIAM(sa))
	default:
		sa := f.get("iam_sas", p[2])
		if sa == nil || sa["project_id"] != project {
			iamErr(w, 404, "service account not found")
			return
		}
		f.iamSA(w, q, project, sa)
	}
}

func (f *fake) iamSA(w http.ResponseWriter, q *req, project string, sa obj) {
	p, m, id := q.parts, q.r.Method, str(sa["id"])
	if len(p) == 3 {
		switch m {
		case "GET":
			writeJSON(w, 200, publicIAM(sa))
		case "PATCH":
			b := q.JSON()
			if v, ok := b["description"]; ok {
				sa["description"] = str(v)
			}
			if v, ok := b["role"]; ok {
				r := str(v)
				if r != "PROJ-ADM" && r != "PROJ-MEMB" && r != "PROJ-VIEWER" {
					iamErr(w, 400, "role must be one of PROJ-ADM, PROJ-MEMB, PROJ-VIEWER")
					return
				}
				sa["role"] = r
			}
			action := "service_account.update"
			if v, ok := b["disabled"].(bool); ok && v != sa["disabled"] {
				sa["disabled"] = v
				action = "service_account.enable"
				if v {
					action = "service_account.disable"
					for _, k := range f.store["iam_keys"] {
						if k["_sa"] == id {
							k["enabled"] = false
						}
					}
				}
			}
			sa["updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
			f.iamAudit(project, action, id, b)
			writeJSON(w, 200, publicIAM(sa))
		case "DELETE":
			for kid, k := range f.store["iam_keys"] {
				if k["_sa"] == id {
					delete(f.store["iam_keys"], kid)
				}
			}
			delete(f.store["iam_sas"], id)
			f.iamAudit(project, "service_account.delete", id, nil)
			w.WriteHeader(204)
		default:
			iamErr(w, 405, "method not allowed")
		}
		return
	}
	if p[3] != "keys" {
		iamErr(w, 404, "not found")
		return
	}
	switch {
	case len(p) == 4 && m == "GET":
		list := []obj{}
		for _, k := range f.store["iam_keys"] {
			if k["_sa"] == id {
				list = append(list, publicIAM(k))
			}
		}
		sort.Slice(list, func(i, j int) bool { return str(list[i]["created_at"]) < str(list[j]["created_at"]) })
		writeJSON(w, 200, obj{"keys": list})
	case len(p) == 4 && m == "POST":
		if sa["disabled"] == true {
			iamErr(w, 409, "service account is disabled")
			return
		}
		b := q.JSON()
		if str(b["name"]) == "" {
			iamErr(w, 400, "name is required")
			return
		}
		exp := time.Now().UTC().Add(90 * 24 * time.Hour).Truncate(time.Second)
		if v := str(b["expires_at"]); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil || t.Before(time.Now()) || t.After(time.Now().Add(366*24*time.Hour)) {
				iamErr(w, 400, "expires_at: RFC3339 in the future, at most 366 days")
				return
			}
			exp = t.UTC()
		}
		scopes := []any{}
		if l, ok := b["scopes"].([]any); ok {
			scopes = l
		}
		kid := f.newID()
		k := obj{"key_id": kid, "name": str(b["name"]), "description": str(b["description"]), "scopes": scopes, "enabled": true,
			"created_at": time.Now().UTC().Format(time.RFC3339Nano), "expires_at": exp.Format(time.RFC3339), "kind": "static", "_sa": id}
		f.put("iam_keys", kid, k)
		f.iamAudit(project, "service_account.key_create", id, obj{"key_id": kid})
		out := publicIAM(k)
		out["secret_key"] = "secret-" + kid
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 201, out)
	case len(p) == 5 && m == "DELETE":
		k := f.get("iam_keys", p[4])
		if k == nil || k["_sa"] != id {
			iamErr(w, 404, "key not found")
			return
		}
		delete(f.store["iam_keys"], p[4])
		f.iamAudit(project, "service_account.key_delete", id, obj{"key_id": p[4]})
		w.WriteHeader(204)
	default:
		iamErr(w, 404, "not found")
	}
}

func (f *fake) iamAuditList(w http.ResponseWriter, q *req, project string) {
	limit := 100
	if v := q.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			iamErr(w, 400, "limit: 1..500")
			return
		}
		limit = n
	}
	var from, to time.Time
	for k, t := range map[string]*time.Time{"from": &from, "to": &to} {
		if v := q.Query(k); v != "" {
			p, err := time.Parse(time.RFC3339, v)
			if err != nil {
				iamErr(w, 400, k+": RFC 3339 expected")
				return
			}
			*t = p
		}
	}
	var evs []obj
	for _, e := range f.store["iam_audit"] {
		if e["project_id"] != project {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, str(e["occurred_at"]))
		if (!from.IsZero() && at.Before(from)) || (!to.IsZero() && !at.Before(to)) {
			continue
		}
		evs = append(evs, e)
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i]["_seq"].(int) > evs[j]["_seq"].(int) })
	if len(evs) > limit {
		evs = evs[:limit]
	}
	out := []obj{}
	for _, e := range evs {
		out = append(out, publicIAM(e))
	}
	writeJSON(w, 200, obj{"events": out, "retention_days": 90, "export_available": false})
}
