package acctest

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// ---- h3k8s v3 (camelCase, ?projectId= on per-cluster calls, h3ttp errors) ----
//
// Mirrors h3k8s origin/main (a3f6462): create/upgrade are 202 and async
// (CREATING/UPGRADING -> READY on the next GET), PATCH workers/control-plane/
// autoscaler/maintenance-window/node-local-dns-cache, versions v1.34.1/v1.35.9/v1.36.5 (b2b), an
// upgrade may go to a newer patch or the next minor only. NodeLocal DNSCache
// rollouts (ENABLING/DISABLING) finish one GET after the cluster is READY.
// Node pools follow h3k8s MR !39 (feat/node-pools): /clusters/{id}/node-pools
// CRUD + upgrade, the cluster shows its workers as pool "default"; a pool
// change (PROVISIONING/ROLLING_OUT/SCALING) is READY on the next pool GET.

func init() { register("/api/k8s/v3", (*Fake).k8s) }

// K8sVersions are the versions of the fake (b2b values, 07.10.2026).
var K8sVersions = []string{"v1.34.1", "v1.35.9", "v1.36.5"}

// FakeKubeconfigCA is the PEM in the fake kubeconfig.
const FakeKubeconfigCA = "-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----\n"

func k8sErr(w http.ResponseWriter, code int, msg string) {
	WriteJSON(w, code, Obj{"error": http.StatusText(code), "message": msg})
}

func k8sVersionParts(v string) (int, int, int, bool) {
	p := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(p) != 3 {
		return 0, 0, 0, false
	}
	a, e1 := strconv.Atoi(p[0])
	b, e2 := strconv.Atoi(p[1])
	c, e3 := strconv.Atoi(p[2])
	return a, b, c, e1 == nil && e2 == nil && e3 == nil
}

func knownK8sVersion(v string) bool {
	for _, k := range K8sVersions {
		if k == v {
			return true
		}
	}
	return false
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

func (f *Fake) k8s(w http.ResponseWriter, q *Req) {
	m, p := q.R.Method, q.Parts
	if len(p) == 0 || p[0] != "clusters" {
		k8sErr(w, 404, "not found")
		return
	}
	p = p[1:]
	switch {
	case m == "GET" && len(p) == 1 && p[0] == "versions":
		vs := []Obj{}
		for _, v := range K8sVersions {
			vs = append(vs, Obj{"version": v, "nodeVMImage": "h3-k8s-node-" + v})
		}
		out := Obj{"versions": vs, "latest": K8sVersions[len(K8sVersions)-1]}
		if from := q.Query("from"); from != "" {
			targets := []string{}
			for _, v := range K8sVersions {
				if upgradeAllowed(from, v) == "" {
					targets = append(targets, v)
				}
			}
			out["from"], out["upgradeOnly"], out["upgradeTargets"] = from, true, targets
			if len(targets) > 0 {
				out["nextUpgrade"] = targets[0]
			}
		}
		WriteJSON(w, 200, out)
	case m == "POST" && len(p) == 0:
		b := q.JSON()
		v := str(b["kubernetesVersion"])
		if !knownK8sVersion(v) {
			k8sErr(w, 400, "unsupported kubernetesVersion "+v)
			return
		}
		if str(b["projectId"]) == "" || str(b["name"]) == "" {
			k8sErr(w, 400, "projectId and name are required")
			return
		}
		id := f.newID()
		wk, _ := b["workers"].(map[string]any)
		cp := b["controlPlaneReplicas"]
		if num(cp) == 0 {
			cp = 1.0
		}
		c := Obj{
			"id": id, "name": b["name"], "projectId": b["projectId"], "namespace": "k8s-" + str(b["name"]),
			"status": "CREATING", "createdAt": "2026-10-08T10:00:00Z", "updatedAt": "2026-10-08T10:00:00Z",
			"clusterType": orDefault(str(b["clusterType"]), "DEV"),
			"podCidr":     orDefault(str(b["podCidr"]), "10.244.0.0/16"), "serviceCidr": orDefault(str(b["serviceCidr"]), "10.96.0.0/12"),
			"controlPlane": Obj{"replicas": num(cp)},
			"workers": Obj{"replicas": num(wk["replicas"]), "cpu": num(wk["cpu"]), "memoryGi": num(wk["memoryGi"]),
				"diskGi": num(wk["diskGi"])},
			"kubernetesVersion": v,
			"autoscaler":        Obj{"enabled": false},
			"maintenanceWindow": Obj{"enabled": false, "startHour": 0},
		}
		if a, ok := b["autoscaler"].(map[string]any); ok {
			c["autoscaler"] = a
		}
		if mw, ok := b["maintenanceWindow"].(map[string]any); ok {
			c["maintenanceWindow"] = mw
		}
		if !f.K8sNoNodeLocalDNS {
			c["nodeLocalDNSCache"] = Obj{"enabled": false, "state": "DISABLED"}
			if n, ok := b["nodeLocalDNSCache"].(map[string]any); ok && n["enabled"] == true {
				c["nodeLocalDNSCache"] = Obj{"enabled": true, "state": "ENABLING"}
			}
		}
		// Worker network (h3k8s with workers in the tenant VPC): networkId → mode vpc, otherwise the shared network.
		if !f.K8sNoNetwork {
			c["network"] = Obj{"mode": "shared"}
			if n := str(b["networkId"]); n != "" {
				c["network"] = Obj{"mode": "vpc", "networkId": n}
			}
		}
		f.put("k8s", id, c)
		WriteJSON(w, 202, publicCluster(c))
	case len(p) >= 1:
		c := f.get("k8s", p[0])
		if c == nil || q.Query("projectId") != str(c["projectId"]) {
			k8sErr(w, 404, "cluster not found")
			return
		}
		sub := strings.Join(p[1:], "/")
		switch {
		case m == "GET" && sub == "":
			f.advanceCluster(c)
			WriteJSON(w, 200, publicCluster(c))
		case m == "DELETE" && sub == "":
			// Gone on the next GET (deletion is async in h3k8s).
			delete(f.store["k8s"], p[0])
			WriteJSON(w, 200, Obj{"message": "Cluster deletion initiated"})
		case m == "GET" && sub == "kubeconfig":
			if str(c["status"]) == "CREATING" {
				k8sErr(w, 409, "cluster is not ready")
				return
			}
			ca := base64.StdEncoding.EncodeToString([]byte(FakeKubeconfigCA))
			WriteJSON(w, 200, Obj{"kubeconfig": fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    certificate-authority-data: %s\n    server: https://%s.k8s.h3llo.cloud:6443\n  name: %s\nusers:\n- name: admin\n  user:\n    token: secret-token\n", ca, str(c["name"]), str(c["name"]))})
		case str(c["status"]) != "READY" && m != "GET":
			k8sErr(w, 409, "cluster is "+str(c["status"])+", wait for READY")
		case sub == "node-pools" || strings.HasPrefix(sub, "node-pools/"):
			f.k8sNodePools(w, q, c, p[2:])
		case m == "POST" && sub == "upgrade":
			target := str(q.JSON()["kubernetesVersion"])
			if msg := upgradeAllowed(str(c["kubernetesVersion"]), target); msg != "" {
				k8sErr(w, 400, msg)
				return
			}
			c["status"], c["_target"] = "UPGRADING", target
			WriteJSON(w, 202, Obj{"message": "Cluster upgrade initiated"})
		case m == "PATCH" && sub == "workers":
			b := q.JSON()
			if len(b) == 0 {
				k8sErr(w, 400, "at least one field is required")
				return
			}
			if (b["cpu"] == nil) != (b["memoryGi"] == nil) {
				k8sErr(w, 400, "cpu and memoryGi must be changed together")
				return
			}
			wk := c["workers"].(map[string]any)
			for _, k := range []string{"replicas", "cpu", "memoryGi"} {
				if b[k] != nil {
					wk[k] = num(b[k])
				}
			}
			c["status"] = "UPDATING"
			WriteJSON(w, 200, publicCluster(c))
		case m == "PATCH" && sub == "control-plane":
			n := num(q.JSON()["replicas"])
			if n != 1 && n != 3 && n != 5 {
				k8sErr(w, 400, "replicas must be 1, 3 or 5")
				return
			}
			c["controlPlane"] = Obj{"replicas": n}
			c["status"] = "UPDATING"
			WriteJSON(w, 200, publicCluster(c))
		case m == "PATCH" && sub == "autoscaler":
			c["autoscaler"] = q.JSON()
			WriteJSON(w, 200, publicCluster(c))
		case m == "PATCH" && sub == "maintenance-window":
			c["maintenanceWindow"] = q.JSON()
			WriteJSON(w, 200, publicCluster(c))
		case m == "PATCH" && sub == "node-local-dns-cache" && !f.K8sNoNodeLocalDNS:
			if e, ok := c["_nldReject"].(Obj); ok {
				WriteJSON(w, int(num(e["code"])), e)
				return
			}
			n := c["nodeLocalDNSCache"].(Obj)
			if st := str(n["state"]); st == "ENABLING" || st == "DISABLING" {
				WriteJSON(w, 409, Obj{"code": 409, "reason": "NODE_LOCAL_DNS_BUSY", "message": "NodeLocal DNSCache is " + st})
				return
			}
			en, ok := q.JSON()["enabled"].(bool)
			if !ok {
				k8sErr(w, 400, "enabled is required")
				return
			}
			if en != n["enabled"] || str(n["state"]) == "ERROR" {
				state := "DISABLING"
				if en {
					state = "ENABLING"
				}
				c["nodeLocalDNSCache"] = Obj{"enabled": en, "state": state}
			}
			WriteJSON(w, 202, publicCluster(c))
		default:
			k8sErr(w, 404, "no route")
		}
	default:
		k8sErr(w, 404, "no route")
	}
}

// upgradeAllowed returns "" when from -> to is a valid single upgrade step.
func upgradeAllowed(from, to string) string {
	if !knownK8sVersion(to) {
		return "unsupported kubernetesVersion " + to
	}
	a1, b1, c1, ok1 := k8sVersionParts(from)
	a2, b2, c2, ok2 := k8sVersionParts(to)
	switch {
	case !ok1 || !ok2:
		return "invalid version"
	case a1 != a2 || b2 < b1 || (b2 == b1 && c2 <= c1):
		return "downgrade or same version is not allowed"
	case b2 > b1+1:
		return "upgrade one minor version at a time"
	}
	return ""
}

func (f *Fake) advanceCluster(c Obj) {
	if n, ok := c["nodeLocalDNSCache"].(Obj); ok && str(c["status"]) == "READY" {
		switch str(n["state"]) {
		case "ENABLING":
			if msg, failed := c["_nldFail"].(string); failed {
				delete(c, "_nldFail")
				c["nodeLocalDNSCache"] = Obj{"enabled": true, "state": "ERROR", "message": msg}
			} else {
				c["nodeLocalDNSCache"] = Obj{"enabled": true, "state": "READY"}
			}
		case "DISABLING":
			c["nodeLocalDNSCache"] = Obj{"enabled": false, "state": "DISABLED"}
		}
	}
	switch str(c["status"]) {
	case "CREATING", "UPDATING":
		c["status"] = "READY"
	case "UPGRADING":
		c["kubernetesVersion"] = c["_target"]
		for _, p := range clusterPools(c)[1:] {
			p["kubernetesVersion"] = c["_target"]
		}
		delete(c, "_target")
		c["status"] = "READY"
	}
}

func publicCluster(c Obj) Obj {
	out := Obj{}
	for k, v := range c {
		if !strings.HasPrefix(k, "_") {
			out[k] = v
		}
	}
	pools := []any{}
	for _, p := range clusterPools(c) {
		pp := publicPool(c, p)
		for _, k := range []string{"status", "desiredReplicas", "currentReplicas", "readyReplicas"} {
			delete(pp, k)
		}
		pools = append(pools, pp)
	}
	out["nodePools"] = pools
	return clone(out)
}

// ---- node pools ----

var fakePoolNameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,18}[a-z0-9])?$`)

// defaultPool is the pool "default" derived from the workers of the cluster.
func defaultPool(c Obj) Obj {
	wk, _ := c["workers"].(map[string]any)
	p := Obj{"name": "default", "cpu": num(wk["cpu"]), "memoryGi": num(wk["memoryGi"]), "diskGi": num(wk["diskGi"]),
		"diskProfile": "replicated", "replicas": num(wk["replicas"]), "autoscaling": Obj{"enabled": false},
		"kubernetesVersion": c["kubernetesVersion"], "_status": "READY"}
	if a, ok := c["autoscaler"].(map[string]any); ok && a["enabled"] == true {
		min := num(wk["replicas"])
		p["autoscaling"] = Obj{"enabled": true, "minSize": min, "maxSize": num(a["maxSize"])}
	}
	return p
}

// clusterPools: "default" (from workers) and the pools added via /node-pools.
func clusterPools(c Obj) []Obj {
	out := []Obj{defaultPool(c)}
	if extra, ok := c["_pools"].([]Obj); ok {
		out = append(out, extra...)
	}
	return out
}

func findPool(c Obj, name string) (Obj, int) {
	extra, _ := c["_pools"].([]Obj)
	for i, p := range extra {
		if str(p["name"]) == name {
			return p, i
		}
	}
	return nil, -1
}

func publicPool(c Obj, p Obj) Obj {
	out := Obj{}
	for k, v := range p {
		if !strings.HasPrefix(k, "_") {
			out[k] = v
		}
	}
	if str(out["kubernetesVersion"]) == "" {
		out["kubernetesVersion"] = c["kubernetesVersion"]
	}
	desired := num(p["replicas"])
	if cur, ok := p["_current"].(float64); ok {
		desired = cur
	}
	out["status"] = orDefault(str(p["_status"]), "READY")
	out["desiredReplicas"], out["currentReplicas"], out["readyReplicas"] = desired, desired, desired
	out["billedReplicas"] = desired
	return clone(out)
}

// advancePool finishes a pending pool change (one GET later).
func advancePool(p Obj) {
	switch str(p["_status"]) {
	case "PROVISIONING", "ROLLING_OUT", "SCALING":
		if t, ok := p["_target"].(string); ok {
			p["kubernetesVersion"] = t
			delete(p, "_target")
		}
		p["_status"] = "READY"
	}
}

func poolAutoscaling(p Obj, a map[string]any) {
	if a["enabled"] != true {
		if as, _ := p["autoscaling"].(Obj); as != nil && as["enabled"] == true {
			if cur, ok := p["_current"].(float64); ok {
				p["replicas"] = cur // keeps the current size
			}
		}
		delete(p, "_current")
		p["autoscaling"] = Obj{"enabled": false}
		return
	}
	as := Obj{"enabled": true}
	for _, k := range []string{"maxSize", "scaleDownUnneededTime", "maxNodeProvisionTime"} {
		if a[k] != nil {
			as[k] = num(a[k])
		}
	}
	if t := str(a["scaleDownUtilizationThreshold"]); t != "" {
		as["scaleDownUtilizationThreshold"] = t
	}
	if a["minSize"] != nil {
		p["replicas"] = num(a["minSize"])
	}
	as["minSize"] = p["replicas"]
	p["autoscaling"] = as
}

func (f *Fake) k8sNodePools(w http.ResponseWriter, q *Req, c Obj, p []string) {
	m := q.R.Method
	switch {
	case m == "GET" && len(p) == 0:
		list := []any{}
		for _, pool := range clusterPools(c) {
			advancePool(pool)
			list = append(list, publicPool(c, pool))
		}
		WriteJSON(w, 200, Obj{"nodePools": list})
		return
	case m == "POST" && len(p) == 0:
		b := q.JSON()
		name := str(b["name"])
		if !fakePoolNameRe.MatchString(name) || num(b["cpu"]) == 0 || num(b["memoryGi"]) == 0 {
			k8sErr(w, 400, "invalid node pool: name, cpu and memoryGi are required")
			return
		}
		if existing, _ := findPool(c, name); existing != nil || name == "default" {
			k8sErr(w, 409, "node pool conflict: pool \""+name+"\" already exists")
			return
		}
		a, _ := b["autoscaling"].(map[string]any)
		if b["replicas"] == nil && (a == nil || a["enabled"] != true) {
			k8sErr(w, 400, "invalid node pool: replicas is required without autoscaling")
			return
		}
		pool := Obj{"name": name, "cpu": num(b["cpu"]), "memoryGi": num(b["memoryGi"]),
			"diskGi": 50.0, "diskProfile": orDefault(str(b["diskProfile"]), "replicated"), "replicas": num(b["replicas"]),
			"autoscaling": Obj{"enabled": false}, "kubernetesVersion": c["kubernetesVersion"], "_status": "PROVISIONING"}
		if b["diskGi"] != nil {
			pool["diskGi"] = num(b["diskGi"])
		}
		for _, k := range []string{"labels", "taints", "kubelet"} {
			if b[k] != nil {
				pool[k] = b[k]
			}
		}
		if b["maxPods"] != nil {
			pool["maxPods"] = num(b["maxPods"])
		}
		if a != nil {
			poolAutoscaling(pool, a)
		}
		extra, _ := c["_pools"].([]Obj)
		c["_pools"] = append(extra, pool)
		WriteJSON(w, 201, publicPool(c, pool))
		return
	}
	name := p[0]
	if name == "default" {
		// The pool of the workers: read-only in the fake (h3_k8s_cluster manages it).
		if m == "GET" && len(p) == 1 {
			WriteJSON(w, 200, publicPool(c, defaultPool(c)))
			return
		}
		k8sErr(w, 400, "the fake manages pool default through /workers")
		return
	}
	pool, idx := findPool(c, name)
	if pool == nil {
		k8sErr(w, 404, "node pool not found")
		return
	}
	switch {
	case m == "GET" && len(p) == 1:
		advancePool(pool)
		WriteJSON(w, 200, publicPool(c, pool))
	case m == "DELETE" && len(p) == 1:
		extra := c["_pools"].([]Obj)
		c["_pools"] = append(extra[:idx:idx], extra[idx+1:]...)
		w.WriteHeader(202)
	case m == "PATCH" && len(p) == 1:
		b := q.JSON()
		rolling := false
		for _, k := range []string{"cpu", "memoryGi", "diskGi", "maxPods", "kubelet"} {
			if b[k] != nil {
				rolling = true
			}
		}
		if rolling && str(pool["_status"]) != "READY" {
			k8sErr(w, 409, "node pool conflict: pool "+name+" is busy (rolling out/scaling), retry when it is READY")
			return
		}
		// Autoscaling first: turning it off keeps the current size unless replicas is given.
		if a, ok := b["autoscaling"].(map[string]any); ok {
			poolAutoscaling(pool, a)
		}
		for _, k := range []string{"cpu", "memoryGi", "diskGi", "maxPods", "replicas"} {
			if b[k] != nil {
				pool[k] = num(b[k])
			}
		}
		for _, k := range []string{"labels", "taints", "kubelet"} {
			if v, ok := b[k]; ok {
				pool[k] = v
				if mm, ok := v.(map[string]any); ok && len(mm) == 0 {
					delete(pool, k)
				}
				if l, ok := v.([]any); ok && len(l) == 0 {
					delete(pool, k)
				}
			}
		}
		if rolling {
			pool["_status"] = "ROLLING_OUT"
		} else if b["replicas"] != nil {
			pool["_status"] = "SCALING"
		}
		WriteJSON(w, 200, publicPool(c, pool))
	case m == "POST" && len(p) == 2 && p[1] == "upgrade":
		target := str(q.JSON()["kubernetesVersion"])
		from := orDefault(str(pool["kubernetesVersion"]), str(c["kubernetesVersion"]))
		if target != str(c["kubernetesVersion"]) {
			k8sErr(w, 400, "invalid node pool: pool version must be the control plane version "+str(c["kubernetesVersion"]))
			return
		}
		if msg := upgradeAllowed(from, target); msg != "" {
			k8sErr(w, 400, "invalid node pool: "+msg)
			return
		}
		pool["_status"], pool["_target"] = "ROLLING_OUT", target
		WriteJSON(w, 200, publicPool(c, pool))
	default:
		k8sErr(w, 404, "no route")
	}
}

// K8sPoolAutoscale sets the node count of a pool as cluster-autoscaler would.
func (f *Fake) K8sPoolAutoscale(clusterID, pool string, n int) {
	f.Mutate("k8s", clusterID, func(c Obj) {
		if p, _ := findPool(c, pool); p != nil {
			p["_current"] = float64(n)
		}
	})
}

// K8sSetPoolVersion moves a pool to the cluster version (as a cluster upgrade does).
func (f *Fake) K8sSetPoolVersion(clusterID, pool, version string) {
	f.Mutate("k8s", clusterID, func(c Obj) {
		if p, _ := findPool(c, pool); p != nil {
			p["kubernetesVersion"] = version
		}
	})
}

// K8sAutoscale changes the worker count as the cluster autoscaler would.
func (f *Fake) K8sAutoscale(id string, replicas int) {
	f.Mutate("k8s", id, func(c Obj) { c["workers"].(map[string]any)["replicas"] = float64(replicas) })
}

// K8sNodeLocalDNSReject makes PATCH /node-local-dns-cache of a cluster answer
// an h3k8s error ({"code","reason","message"}); reason "" clears it.
func (f *Fake) K8sNodeLocalDNSReject(id string, code int, reason, message string) {
	f.Mutate("k8s", id, func(c Obj) {
		if reason == "" {
			delete(c, "_nldReject")
			return
		}
		c["_nldReject"] = Obj{"code": float64(code), "reason": reason, "message": message}
	})
}

// K8sNodeLocalDNSFail makes the next enable rollout of a cluster end in ERROR
// with message.
func (f *Fake) K8sNodeLocalDNSFail(id, message string) {
	f.Mutate("k8s", id, func(c Obj) { c["_nldFail"] = message })
}
