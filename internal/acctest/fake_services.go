package acctest

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"h3terraform/internal/common"
)

// Service fakes. Lifecycle convention: a new object starts in a transitional
// status and becomes ready on the next GET, so the provider's wait loops run.

func init() {
	register("/api/vms/v1", (*Fake).vms)
	register("/api/disks/v1", (*Fake).disks)
	register("/api/ovn/v1", (*Fake).ovn)
	register("/api/s3/v1", (*Fake).s3)
	register("/api/ssh/v1", (*Fake).ssh)
}

// ---- h3vm (snake_case) ----

func (f *Fake) vms(w http.ResponseWriter, q *Req) {
	m := q.R.Method
	switch {
	case len(q.Parts) >= 2 && q.Parts[0] == "images" && q.Parts[1] == "custom":
		f.customImages(w, q)
	case len(q.Parts) >= 2 && q.Parts[0] == "placement" && q.Parts[1] == "groups":
		f.placementGroups(w, q)
	case m == "POST" && len(q.Parts) == 0:
		b := q.JSON()
		if _, ok := b["ssh_key"]; ok {
			WriteJSON(w, 400, Obj{"code": "SSH_KEY_TEXT_UNSUPPORTED", "message": "ssh_key text is not accepted, use ssh_key_ids"})
			return
		}
		// h3vm (OpenAPI feature/sshkeys-vm-access): ssh_key_ids, ssh_grants and
		// ssh_access_managed need VM_SSH_ACCESS_ENABLED; ssh_key_id works without it.
		_, hasManaged := b["ssh_access_managed"]
		if f.SSHAccessDisabled && (len(strList(b["ssh_key_ids"])) > 0 || hasManaged) {
			WriteJSON(w, 503, Obj{"error": "SSH_ACCESS_DISABLED: ssh_key_ids, ssh_grants and ssh_access_managed need SSH access management"})
			return
		}
		keyIDs := strList(b["ssh_key_ids"])
		if id := str(b["ssh_key_id"]); id != "" {
			keyIDs = append(keyIDs, id) // appendKeyID
		}
		if len(keyIDs) == 0 {
			WriteJSON(w, 400, Obj{"error": "either ssh_key, ssh_key_id / ssh_key_ids or ssh_grants must be provided"})
			return
		}
		managed := !f.SSHAccessDisabled
		if v, ok := b["ssh_access_managed"].(bool); ok {
			managed = v
		}
		if str(b["disk_size"]) != "" && str(b["image"]) == "" {
			WriteJSON(w, 400, Obj{"error": "disk_size requires image"})
			return
		}
		if code, msg := f.checkVMCreateExtras(b); code != 0 {
			WriteJSON(w, code, Obj{"code": strings.SplitN(msg, ":", 2)[0], "error": msg})
			return
		}
		id := f.newID()
		key := "ssh-ed25519 AAAAresolved " + keyIDs[0]
		subnet := str(b["subnet_name"])
		if subnet == "" {
			subnet = "subnet-default"
		}
		vm := Obj{
			"id": id, "project_id": b["project_id"], "name": b["name"], "k8s_name": "vm-" + str(b["name"]),
			"status": "PENDING", "cpu": b["cpu"], "memory": b["memory"],
			"disk_size": orDefault(str(b["disk_size"]), "20Gi"), "image": orDefault(str(b["image"]), "ubuntu:24.04"),
			"endpoint": "", "ssh_key": key, "white_ip": false,
			"_managed": managed,
			"net_config": Obj{"subnet_name": subnet, "subnet_id": "22222222-0000-4000-8000-000000000001",
				"vpc_id": b["project_id"], "internal_ip": "10.0.0.10"},
			"_want_white_ip": b["white_ip"], "_eip_id": str(b["eip_id"]),
			"_grants": Obj{User: Obj{"user_id": User, "mode": "selected", "key_ids": anyList(keyIDs),
				"granted_by": User, "granted_at": "2026-10-16T10:00:00Z", "source": "creator"}},
		}
		if eid := str(b["eip_id"]); eid != "" {
			eip := f.get("eips", eid)
			if eip == nil {
				WriteJSON(w, 404, Obj{"error": "eip not found"})
				return
			}
			if str(eip["resourceId"]) != "" {
				WriteJSON(w, 409, Obj{"error": Obj{"code": "EIP_ALREADY_BOUND", "message": "eip is bound"}})
				return
			}
		}
		f.applyVMCreateExtras(vm, b)
		f.put("vms", id, vm)
		out := publicVM(vm)
		out["console_password"] = "one-time-password"
		WriteJSON(w, 201, out)
	case len(q.Parts) == 1:
		vm := f.get("vms", q.Parts[0])
		if vm == nil {
			WriteJSON(w, 404, Obj{"error": "VM not found"})
			return
		}
		switch m {
		case "GET":
			f.advanceVM(vm)
			WriteJSON(w, 200, publicVM(vm))
			if vm["_resize_state"] == "applying" {
				vm["_resize_state"] = "applied" // the hotplug finishes after one GET
			}
		case "PATCH":
			f.patchVM(w, vm, q.JSON())
		case "DELETE":
			if vm["deletion_protection"] == true {
				WriteJSON(w, 409, Obj{"type": "about:blank", "title": "Conflict", "status": 409, "code": "VM_DELETION_PROTECTED",
					"detail": "the VM is protected from deletion: set deletion_protection=false first",
					"error":  "VM_DELETION_PROTECTED: the VM is protected from deletion: set deletion_protection=false first"})
				return
			}
			f.leavePlacementGroup(vm)
			if eid := str(vm["_eip_id"]); eid != "" {
				if q.Query("release_eip") == "true" {
					delete(f.store["eips"], eid)
				} else if eip := f.get("eips", eid); eip != nil {
					eip["resourceId"], eip["resourceType"], eip["vmId"], eip["status"] = "", "", "", "DETACHED"
				}
			}
			delete(f.store["vms"], q.Parts[0])
			w.WriteHeader(204)
		}
	case len(q.Parts) >= 2 && q.Parts[1] == "ssh-access":
		f.vmSSHAccess(w, q)
	default:
		WriteJSON(w, 404, Obj{"error": "no route"})
	}
}

// Users of the fake project besides User: Member may get access, Viewer (PROJ-VIEWER) may not.
const (
	Member = "00000000-0000-4000-8000-0000000000cc"
	Viewer = "00000000-0000-4000-8000-0000000000dd"
)

// SeedSSHKey adds an h3ssh key of any user (keys of other users cannot be made through the API).
func (f *Fake) SeedSSHKey(userID, name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.newID()
	f.put("sshkeys", id, Obj{"id": id, "user_id": userID, "name": name, "public_key": "ssh-ed25519 AAAA" + name,
		"created_at": "2026-10-07T10:00:00Z", "updated_at": "2026-10-07T10:00:00Z"})
	return id
}

// vmSSHAccess serves /api/vms/v1/{id}/ssh-access[/grants/{user_id}] as h3vm
// feature/sshkeys-vm-access does: snake_case view, PUT/DELETE answer the view.
func (f *Fake) vmSSHAccess(w http.ResponseWriter, q *Req) {
	if f.SSHAccessDisabled {
		WriteJSON(w, 503, Obj{"error": "SSH access management is disabled", "code": "SSH_ACCESS_DISABLED"})
		return
	}
	vm := f.get("vms", q.Parts[0])
	if vm == nil {
		WriteJSON(w, 404, Obj{"error": "VM not found"})
		return
	}
	m, p := q.R.Method, q.Parts
	switch {
	case m == "GET" && len(p) == 2:
		WriteJSON(w, 200, f.sshView(vm))
	case len(p) == 4 && p[2] == "grants":
		uid := p[3]
		switch m {
		case "PUT":
			b := q.JSON()
			mode := str(b["mode"])
			if mode != "all" && mode != "selected" {
				WriteJSON(w, 400, Obj{"code": "bad_request", "error": "mode must be all or selected"})
				return
			}
			if code, msg := f.setGrant(vm, uid, mode, strList(b["key_ids"]), b["allow_empty"] == true); code != 0 {
				WriteJSON(w, code, Obj{"code": msg, "error": msg})
				return
			}
			WriteJSON(w, 200, f.sshView(vm))
		case "DELETE":
			grants := vmGrants(vm)
			if _, ok := grants[uid]; !ok {
				WriteJSON(w, 404, Obj{"code": "SSH_GRANT_NOT_FOUND", "error": "no grant"})
				return
			}
			if len(grants) == 1 && q.Query("allow_empty") != "true" {
				WriteJSON(w, 409, Obj{"code": "SSH_KEYS_LAST_KEY", "error": "last key"})
				return
			}
			delete(grants, uid)
			WriteJSON(w, 200, f.sshView(vm))
		default:
			WriteJSON(w, 405, Obj{"error": "method not allowed"})
		}
	default:
		WriteJSON(w, 404, Obj{"error": "no route"})
	}
}

func vmGrants(vm Obj) Obj {
	grants, _ := vm["_grants"].(Obj)
	if grants == nil {
		grants = Obj{}
		vm["_grants"] = grants
	}
	return grants
}

// setGrant puts a grant of uid (the API key owner is User); it returns an
// HTTP status and code on error. selected with no keys revokes the grant.
func (f *Fake) setGrant(vm Obj, uid, mode string, keyIDs []string, allowEmpty bool) (int, string) {
	if vm["_managed"] != true {
		return 409, "SSH_ACCESS_NOT_MANAGED"
	}
	if uid != User && uid != Member {
		return 422, "SSH_GRANTEE_NOT_MEMBER"
	}
	for _, k := range keyIDs {
		if key := f.get("sshkeys", k); key != nil && str(key["user_id"]) != uid {
			return 422, "SSH_KEY_NOT_OWNED"
		}
	}
	grants := vmGrants(vm)
	if mode == "selected" && len(keyIDs) == 0 {
		if len(grants) <= 1 && !allowEmpty {
			return 409, "SSH_KEYS_LAST_KEY"
		}
		delete(grants, uid)
		return 0, ""
	}
	source := "member"
	if uid == User {
		source = "self"
	}
	g := Obj{"user_id": uid, "mode": mode, "key_ids": anyList(keyIDs), "granted_by": User,
		"granted_at": "2026-10-16T12:00:00Z", "source": source}
	if mode == "all" {
		delete(g, "key_ids")
	}
	if old, ok := grants[uid].(Obj); ok {
		g["granted_at"], g["source"] = old["granted_at"], old["source"]
	}
	grants[uid] = g
	return 0, ""
}

// sshView is the snake_case ssh-access view. Keys deleted in h3ssh leave the
// grants, as h3vm does on every rebuild; keys unknown to the fake h3ssh (VM
// configs use made-up ids) are kept.
func (f *Fake) sshView(vm Obj) Obj {
	grants := vmGrants(vm)
	ids := make([]string, 0, len(grants))
	for id := range grants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	list := []any{}
	for _, id := range ids {
		g := clone(grants[id].(Obj))
		if g["mode"] == "selected" {
			kept, keys := []string{}, []any{}
			for _, k := range strList(g["key_ids"]) {
				key := f.get("sshkeys", k)
				if key == nil && f.deletedKeys[k] {
					continue
				}
				kept = append(kept, k)
				keys = append(keys, Obj{"key_id": k, "name": str(key["name"]), "type": "ssh-ed25519", "fingerprint": "SHA256:" + k})
			}
			g["key_ids"], g["keys"] = anyList(kept), keys
		}
		g["active"], g["display_name"] = true, "user "+id[len(id)-2:]
		list = append(list, g)
	}
	status := "applied"
	if vm["_managed"] != true {
		status = "unmanaged"
	}
	return Obj{"managed": vm["_managed"] == true, "toggle_available": true, "guest_agent_image": true, "openssh": "9.6",
		"status": status, "username": "user", "key_count": len(list), "revision": "1", "grants": list, "skipped": []any{}}
}

func strList(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, e := range l {
		out = append(out, str(e))
	}
	return out
}

func anyList(l []string) []any {
	out := make([]any, 0, len(l))
	for _, s := range l {
		out = append(out, s)
	}
	return out
}

// patchVM is h3vm PATCH /api/vms/v1/{id}: CPU/memory with live resize, the
// caller's own SSH grant (ssh_key_ids) and ssh_access_managed.
//
// Live resize: an increase of a RUNNING VM is applied live (resize.state
// "applying" for one GET), anything else restarts the VM unless allow_restart
// is false (409 RESIZE_RESTART_REQUIRED, nothing changed). A VM with
// "_no_live_resize" behaves like h3vm before live resize: always restart, no
// `resize` in responses, allow_restart ignored. Changing ssh_access_managed
// restarts the VM; ssh_key_ids is applied live.
func (f *Fake) patchVM(w http.ResponseWriter, vm Obj, b Obj) {
	for k := range b {
		switch k {
		case "cpu", "memory", "allow_restart", "ssh_key_ids", "ssh_access_managed", "allow_empty", "deletion_protection":
		default:
			WriteJSON(w, 400, Obj{"error": "unknown field " + k})
			return
		}
	}
	if v, ok := b["deletion_protection"]; ok {
		if f.DeletionProtectionDisabled {
			WriteJSON(w, 400, Obj{"code": "DELETION_PROTECTION_DISABLED", "error": "DELETION_PROTECTION_DISABLED: deletion protection is not enabled for this project"})
			return
		}
		vm["deletion_protection"] = v == true
		vm["_protect_patches"] = num(vm["_protect_patches"]) + 1
	}
	_, keysSet := b["ssh_key_ids"]
	_, managedSet := b["ssh_access_managed"]
	if (keysSet || managedSet) && f.SSHAccessDisabled {
		WriteJSON(w, 503, Obj{"error": "SSH access management is disabled", "code": "SSH_ACCESS_DISABLED"})
		return
	}
	_, cpuSet := b["cpu"]
	_, memSet := b["memory"]
	resize := cpuSet || memSet
	cpu, mem := intOf(vm["cpu"]), str(vm["memory"])
	if cpuSet {
		cpu = intOf(b["cpu"])
	}
	if memSet {
		mem = str(b["memory"])
	}
	memCmp, _ := common.QuantityCmp(mem, str(vm["memory"]))
	decrease := cpu < intOf(vm["cpu"]) || memCmp < 0
	live := vm["_no_live_resize"] != true && !decrease && vm["status"] == "RUNNING"
	if resize && vm["_no_live_resize"] != true && !live && b["allow_restart"] == false {
		WriteJSON(w, 409, Obj{"error": "reducing CPU or memory needs a restart", "code": "RESIZE_RESTART_REQUIRED",
			"applied": "restart_required", "reason": "decrease", "message": "reducing CPU or memory needs a restart"})
		return
	}
	restart := false
	if v, ok := b["ssh_access_managed"].(bool); ok && v != vm["_managed"] {
		vm["_managed"], restart = v, true
		vm["_restarts"] = num(vm["_restarts"]) + 1
	}
	if v, ok := b["ssh_key_ids"]; ok {
		// The caller's own grant (mode=selected); [] revokes it.
		if code, msg := f.setGrant(vm, User, "selected", strList(v), b["allow_empty"] == true); code != 0 {
			WriteJSON(w, code, Obj{"code": msg, "error": msg})
			return
		}
	}
	applied := ""
	if resize {
		vm["cpu"], vm["memory"] = cpu, mem
		if live && !restart {
			vm["_resize_state"] = "applying"
			applied = "live"
		} else {
			restart = true
			applied = "restarting"
		}
	}
	if restart {
		vm["status"] = "UPDATING"
	}
	out := publicVM(vm)
	if resize && vm["_no_live_resize"] != true {
		out["resize"] = Obj{"applied": applied}
	}
	WriteJSON(w, 201, out)
}

func intOf(v any) int {
	n, _ := strconv.Atoi(str(v))
	return n
}

func (f *Fake) advanceVM(vm Obj) {
	switch vm["status"] {
	case "PENDING", "UPDATING":
		vm["status"] = "RUNNING"
		return
	}
	if vm["_want_white_ip"] == true && str(vm["endpoint"]) == "" {
		eid := str(vm["_eip_id"])
		if eid == "" {
			eid = f.newID()
			f.put("eips", eid, Obj{"id": eid, "name": "vm-" + str(vm["name"]) + "-eip", "k8sName": "eip-" + eid[len(eid)-4:],
				"projectId": vm["project_id"], "gatewayName": "gw-1", "ipAddress": "203.0.113.50", "status": "DETACHED"})
			vm["_eip_id"] = eid
		}
		eip := f.get("eips", eid)
		eip["resourceId"], eip["resourceType"], eip["vmId"], eip["status"] = vm["id"], "vm", vm["id"], "ATTACHED"
		vm["endpoint"] = eip["ipAddress"]
		vm["white_ip"] = true
	}
}

func publicVM(vm Obj) Obj {
	out := Obj{}
	for k, v := range clone(vm) {
		if !strings.HasPrefix(k, "_") {
			out[k] = v
		}
	}
	if vm["_no_live_resize"] != true {
		state := orDefault(str(vm["_resize_state"]), "applied")
		if vm["status"] != "RUNNING" {
			state = "stopped"
		}
		out["resize"] = Obj{"state": state, "cpu": vm["cpu"], "memory": vm["memory"], "live_cpu": true, "live_memory": true,
			"live_max_cpu": 4 * intOf(vm["cpu"]), "live_cpu_step": 1}
	}
	return out
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// ---- h3disks (snake_case) ----

func (f *Fake) disks(w http.ResponseWriter, q *Req) {
	m, p := q.R.Method, q.Parts
	switch {
	case m == "POST" && len(p) == 0:
		b := q.JSON()
		id := f.newID()
		d := Obj{"id": id, "project_id": b["project_id"], "name": b["name"], "size": b["size"],
			"storage_class": b["storage_class"], "status": "PENDING", "created_at": "2026-10-07T10:00:00Z",
			"performance_class": orDefault(strings.ToUpper(strings.ReplaceAll(str(b["performance_class"]), "-", "_")), "R_STANDARD"), "is_root_disk": false}
		f.put("disks", id, d)
		WriteJSON(w, 201, d)
	case m == "POST" && len(p) == 1 && p[0] == "resize":
		b := q.JSON()
		d := f.get("disks", str(b["disk_id"]))
		if d == nil {
			WriteJSON(w, 404, Obj{"code": "not_found", "message": "disk not found"})
			return
		}
		if c, ok := common.QuantityCmp(str(b["new_size"]), str(d["size"])); !ok || c < 0 {
			// production answers 500 on a shrink
			WriteJSON(w, 500, Obj{"code": "internal_error", "message": "Failed to resize disk"})
			return
		}
		d["size"], d["_status_after"], d["status"] = b["new_size"], d["status"], "RESIZING"
		w.WriteHeader(204)
	case m == "POST" && len(p) == 2 && p[1] == "performance-class":
		d := f.get("disks", p[0])
		if d == nil {
			notFound(w, "disk")
			return
		}
		d["performance_class"] = strings.ToUpper(strings.ReplaceAll(str(q.JSON()["performance_class"]), "-", "_"))
		WriteJSON(w, 200, d)
	case len(p) == 1 && p[0] != "snapshots" && p[0] != "backups":
		d := f.get("disks", p[0])
		if d == nil {
			notFound(w, "disk")
			return
		}
		switch m {
		case "GET":
			switch d["status"] {
			case "PENDING":
				d["status"] = "AVAILABLE"
			case "RESIZING":
				d["status"] = d["_status_after"]
			}
			WriteJSON(w, 200, publicVM(d))
		case "DELETE":
			delete(f.store["disks"], p[0])
			w.WriteHeader(204)
		}
	case len(p) >= 1 && (p[0] == "snapshots" || p[0] == "backups"):
		f.diskChildren(w, q)
	default:
		notFound(w, "route")
	}
}

func (f *Fake) diskChildren(w http.ResponseWriter, q *Req) {
	m, p := q.R.Method, q.Parts
	coll := p[0]
	if m == "POST" && len(p) == 1 {
		b := q.JSON()
		id := f.newID()
		o := Obj{"id": id, "project_id": b["project_id"], "name": b["name"], "size": "10Gi", "created_at": "2026-10-07T10:00:00Z"}
		if coll == "snapshots" {
			if f.get("disks", str(b["disk_id"])) == nil {
				notFound(w, "disk")
				return
			}
			o["disk_id"], o["status"] = b["disk_id"], "CREATING"
		} else {
			s := f.get("snapshots", str(b["snapshot_id"]))
			if s == nil || s["status"] != "READY" {
				WriteJSON(w, 409, Obj{"code": "conflict", "message": "snapshot is not ready"})
				return
			}
			o["snapshot_id"], o["status"] = b["snapshot_id"], "PENDING"
		}
		f.put(coll, id, o)
		WriteJSON(w, 201, o)
		return
	}
	if len(p) != 2 {
		notFound(w, "route")
		return
	}
	if q.Query("project_id") == "" {
		WriteJSON(w, 400, Obj{"code": "bad_request", "message": "project_id is required"})
		return
	}
	o := f.get(coll, p[1])
	if o == nil || str(o["project_id"]) != q.Query("project_id") {
		notFound(w, coll)
		return
	}
	switch m {
	case "GET":
		if o["status"] == "CREATING" || o["status"] == "PENDING" {
			o["status"] = "READY"
		}
		WriteJSON(w, 200, o)
	case "DELETE":
		delete(f.store[coll], p[1])
		w.WriteHeader(204)
	}
}

// ---- h3ovn (snake_case requests, camelCase responses, plain-text errors) ----

func (f *Fake) ovn(w http.ResponseWriter, q *Req) {
	m, p := q.R.Method, q.Parts
	if len(p) == 0 {
		http.Error(w, "not found", 404)
		return
	}
	if m == "GET" && len(p) == 1 && (p[0] == "vpcs" || p[0] == "networks" || p[0] == "eips") {
		f.ovnList(w, q, p[0])
		return
	}
	switch p[0] {
	case "vpcs":
		switch {
		case m == "POST" && len(p) == 1:
			b := q.JSON()
			id := str(b["project_id"]) // production: the VPC id is the project id
			if f.get("vpcs", id) != nil {
				http.Error(w, "vpc already exists", http.StatusConflict)
				return
			}
			var routes []Obj
			if rs, ok := b["static_routes"].([]any); ok {
				for _, r := range rs {
					ro := r.(map[string]any)
					routes = append(routes, Obj{"cidr": ro["cidr"], "nextHopIp": ro["next_hop_ip"], "policy": orDefault(str(ro["policy"]), "policyDst")})
				}
			}
			v := Obj{"id": id, "name": b["name"], "projectId": b["project_id"], "namespaces": []any{"ns-" + id},
				"status": "CREATING", "_routes": routes}
			f.put("vpcs", id, v)
			out := publicVM(v)
			out["staticRoutes"] = routes
			WriteJSON(w, 201, out)
		case len(p) >= 3 && p[2] == "routes":
			f.vpcRoutes(w, q)
		case len(p) == 2:
			v := f.get("vpcs", p[1])
			if v == nil {
				http.Error(w, "vpc not found", 404)
				return
			}
			if m == "DELETE" {
				delete(f.store["vpcs"], p[1])
				w.WriteHeader(204)
				return
			}
			v["status"] = "ACTIVE"
			WriteJSON(w, 200, publicVM(v)) // GET never returns staticRoutes
		}
	case "networks":
		switch {
		case m == "POST" && len(p) == 1:
			b := q.JSON()
			id := f.newID()
			n := Obj{"id": id, "name": b["name"], "projectId": b["project_id"], "vpcId": orDefault(str(b["vpc_id"]), str(b["project_id"])),
				"vpcName": "vpc-1", "cidrBlock": b["cidr_block"], "protocol": orDefault(str(b["protocol"]), "IPv4"),
				"gatewayId": f.newID(), "gatewayName": "gw-" + str(b["name"]), "subnetName": "subnet-" + str(b["name"]),
				"gatewayIp": "10.1.0.1", "status": "CREATING"}
			f.put("networks", id, n)
			WriteJSON(w, 201, n)
		case len(p) == 2:
			n := f.get("networks", p[1])
			if n == nil {
				http.Error(w, "network not found", 404)
				return
			}
			if m == "DELETE" {
				delete(f.store["networks"], p[1])
				w.WriteHeader(204)
				return
			}
			n["status"] = "ACTIVE"
			WriteJSON(w, 200, n)
		}
	case "eips":
		f.eips(w, q)
	default:
		http.Error(w, "not found", 404)
	}
}

// ovnList: GET /vpcs|/networks|/eips?project_id= returns a bare JSON array
// (production h3ovn); eips also take bound=true|false.
func (f *Fake) ovnList(w http.ResponseWriter, q *Req, coll string) {
	pid := q.Query("project_id")
	if pid == "" {
		http.Error(w, "project_id is required", 400)
		return
	}
	ids := make([]string, 0, len(f.store[coll]))
	for id := range f.store[coll] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := []Obj{}
	for _, id := range ids {
		o := f.store[coll][id]
		if str(o["projectId"]) != pid {
			continue
		}
		if coll == "eips" && q.Query("bound") != "" {
			bound := str(o["resourceId"]) != ""
			if (q.Query("bound") == "true") != bound {
				continue
			}
		}
		if coll == "vpcs" {
			o["status"] = "ACTIVE"
			o = publicVM(o)
		}
		out = append(out, o)
	}
	WriteJSON(w, 200, out)
}

func (f *Fake) eips(w http.ResponseWriter, q *Req) {
	m, p := q.R.Method, q.Parts
	switch {
	case m == "POST" && len(p) == 1:
		b := q.JSON()
		id := f.newID()
		e := Obj{"id": id, "name": b["name"], "description": b["description"], "k8sName": "eip-" + id[len(id)-4:], "projectId": b["project_id"],
			"gatewayName": "gw-1", "ipAddress": fmt.Sprintf("203.0.113.%d", f.seq), "vmId": "", "resourceType": "", "resourceId": "",
			"status": "DETACHED"}
		f.put("eips", id, e)
		WriteJSON(w, 201, e)
	case m == "POST" && len(p) == 2 && (p[1] == "attach" || p[1] == "detach"):
		b := q.JSON()
		e := f.get("eips", str(b["eip_id"]))
		if e == nil {
			http.Error(w, "eip not found", 404)
			return
		}
		if p[1] == "attach" {
			if rid := str(e["resourceId"]); rid != "" && rid != str(b["resource_id"]) {
				WriteJSON(w, 409, Obj{"error": Obj{"code": "EIP_ALREADY_BOUND", "message": "eip is bound"}})
				return
			}
			e["resourceId"], e["resourceType"], e["vmId"], e["status"] = b["resource_id"], b["resource_type"], b["resource_id"], "ATTACHED"
			// h3vm FIP watcher: the VM reports the public IP.
			if vm := f.get("vms", str(b["resource_id"])); vm != nil && str(b["resource_type"]) == "vm" {
				vm["endpoint"], vm["white_ip"], vm["_eip_id"], vm["_want_white_ip"] = e["ipAddress"], true, e["id"], true
			}
		} else {
			if vm := f.get("vms", str(e["resourceId"])); vm != nil && str(e["resourceType"]) == "vm" {
				vm["endpoint"], vm["white_ip"], vm["_eip_id"], vm["_want_white_ip"] = "", false, "", false
			}
			e["resourceId"], e["resourceType"], e["vmId"], e["status"] = "", "", "", "DETACHED"
		}
		WriteJSON(w, 200, Obj{"message": "ok"})
	case len(p) == 2:
		e := f.get("eips", p[1])
		if e == nil {
			http.Error(w, "eip not found", 404)
			return
		}
		if m == "DELETE" {
			delete(f.store["eips"], p[1])
			w.WriteHeader(204)
			return
		}
		if m == "PATCH" {
			// h3ovn PATCH /eips/{id}: name/description, "" removes the description.
			b := q.JSON()
			if v, ok := b["description"]; ok {
				d := strings.TrimSpace(str(v))
				if strings.ContainsAny(d, "\n\t") || len([]rune(d)) > 255 {
					http.Error(w, "EIP_DESCRIPTION_INVALID: bad description", 400)
					return
				}
				e["description"] = d
			}
			if v, ok := b["name"]; ok {
				e["name"] = v
			}
		}
		WriteJSON(w, 200, e)
	default:
		http.Error(w, "not found", 404)
	}
}

// ---- h3s3 (camelCase, plain-text errors) ----

func (f *Fake) s3(w http.ResponseWriter, q *Req) {
	m, p := q.R.Method, q.Parts
	if len(p) == 0 || p[0] != "buckets" {
		http.Error(w, "not found", 404)
		return
	}
	project := q.Query("project_id")
	switch {
	case m == "POST" && len(p) == 1:
		b := q.JSON()
		key := str(b["project_id"]) + "/" + str(b["name"])
		if f.get("buckets", key) != nil {
			http.Error(w, "bucket already exists", http.StatusConflict)
			return
		}
		f.put("buckets", key, Obj{"id": "", "name": b["name"], "slug": "h3-" + str(b["name"]), "projectId": b["project_id"],
			"region": "", "isPublic": false, "versioning": false, "createdAt": "2026-10-07T10:00:00Z"})
		WriteJSON(w, 201, Obj{"message": "bucket created successfully",
			"credentials": Obj{"access_key_id": "AKIAFAKE", "secret_access_key": "fake-secret"}})
	case m == "GET" && len(p) == 1:
		var list []Obj
		for k, v := range f.store["buckets"] {
			if strings.HasPrefix(k, project+"/") {
				list = append(list, v)
			}
		}
		WriteJSON(w, 200, Obj{"buckets": list})
	case len(p) == 2:
		bk := f.get("buckets", project+"/"+p[1])
		if bk == nil {
			http.Error(w, "failed to get bucket", 500) // production: 500, not 404
			return
		}
		if m == "DELETE" {
			delete(f.store["buckets"], project+"/"+p[1])
			w.WriteHeader(204)
			return
		}
		WriteJSON(w, 200, Obj{"bucket": bk, "objects": Obj{"currentPath": "", "items": []any{}}})
	default:
		http.Error(w, "not found", 404)
	}
}

// ---- h3ssh (snake_case, no PATCH) ----

func (f *Fake) ssh(w http.ResponseWriter, q *Req) {
	m, p := q.R.Method, q.Parts
	switch {
	case m == "POST" && len(p) == 1 && p[0] == "keys":
		b := q.JSON()
		if str(b["user_id"]) != User {
			WriteJSON(w, 403, Obj{"error": "user_id must match the API key owner"})
			return
		}
		id := f.newID()
		k := Obj{"id": id, "user_id": b["user_id"], "name": b["name"], "public_key": strings.TrimSpace(str(b["public_key"])),
			"created_at": "2026-10-07T10:00:00Z", "updated_at": "2026-10-07T10:00:00Z"}
		f.put("sshkeys", id, k)
		WriteJSON(w, 201, k)
	case len(p) == 2 && p[0] == "keys":
		k := f.get("sshkeys", p[1])
		if k == nil {
			WriteJSON(w, 404, Obj{"error": "ssh key not found"})
			return
		}
		switch m {
		case "GET":
			WriteJSON(w, 200, k)
		case "DELETE":
			f.remove("sshkeys", p[1])
			w.WriteHeader(204)
		default:
			WriteJSON(w, 405, Obj{"error": "method not allowed"})
		}
	default:
		WriteJSON(w, 404, Obj{"error": "no route"})
	}
}

// vpcRoutes: h3ovn route table (snake_case both ways, problem codes in {"error":{code,message}}).
func (f *Fake) vpcRoutes(w http.ResponseWriter, q *Req) {
	m, p := q.R.Method, q.Parts
	vpc := f.get("vpcs", p[1])
	if vpc == nil {
		http.Error(w, `{"error":{"code":"NOT_FOUND","message":"VPC not found"}}`, 404)
		return
	}
	key := func(id string) string { return p[1] + "/" + id }
	switch {
	case m == "GET" && len(p) == 3:
		ids := []string{}
		for id, o := range f.store["vpc_routes"] {
			if strings.HasPrefix(id, p[1]+"/") {
				ids = append(ids, str(o["id"]))
			}
		}
		sort.Strings(ids)
		out := []Obj{}
		for _, id := range ids {
			out = append(out, f.get("vpc_routes", key(id)))
		}
		WriteJSON(w, 200, Obj{"vpc_id": p[1], "routes": out, "system_routes": []Obj{}, "limit": 50})
	case m == "POST" && len(p) == 3:
		b := q.JSON()
		r := Obj{"id": "rt-" + f.newID(), "destination": b["destination"], "target_type": b["target_type"], "status": "active"}
		switch str(b["target_type"]) {
		case "ip":
			r["next_hop_ip"] = b["next_hop_ip"]
		case "vm":
			r["vm_id"], r["next_hop_ip"] = b["vm_id"], "10.1.0.50"
		}
		if d := str(b["description"]); d != "" {
			r["description"] = d
		}
		for id, o := range f.store["vpc_routes"] {
			if strings.HasPrefix(id, p[1]+"/") && str(o["destination"]) == str(b["destination"]) {
				http.Error(w, `{"error":{"code":"ROUTE_DUPLICATE","message":"a route already exists"}}`, http.StatusConflict)
				return
			}
		}
		f.put("vpc_routes", key(str(r["id"])), r)
		WriteJSON(w, 201, r)
	case m == "DELETE" && len(p) == 4:
		if f.get("vpc_routes", key(p[3])) == nil {
			http.Error(w, `{"error":{"code":"ROUTE_NOT_FOUND","message":"route not found"}}`, 404)
			return
		}
		delete(f.store["vpc_routes"], key(p[3]))
		w.WriteHeader(204)
	default:
		http.Error(w, "not found", 404)
	}
}
