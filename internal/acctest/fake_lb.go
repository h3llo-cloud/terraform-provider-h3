package acctest

import (
	"net/http"
)

// ---- h3lb (camelCase). Without eipId an LB is a legacy lb-svc one; with eipId it is
// LB Basic: the project EIP is bound as (lb, id), PATCH eipId moves it, DELETE keeps it
// in the project unless releaseEip=true. whiteIp=false is an internal LB Basic (vpcIp, no
// EIP); PATCH whiteIp turns the public IP on (eipId or a new EIP) / off (EIP kept). ----

func init() {
	register("/api/loadbalancers/v1", (*Fake).lbs)
}

func lbValidation(w http.ResponseWriter, field, msg string) {
	WriteJSON(w, 400, Obj{"error": "validation failed", "errors": []Obj{{"field": field, "message": msg}}})
}

// lbEIPError answers like h3lb: 400 EIP_INVALID / 409 EIP_ALREADY_BOUND on field eipId.
func lbEIPError(w http.ResponseWriter, status int, code string) {
	WriteJSON(w, status, Obj{"error": http.StatusText(status), "code": code, "errors": []Obj{{"field": "eipId", "message": code}}})
}

// lbBindEIP binds eid to the LB if it is free (or already this LB's); false = error written.
func (f *Fake) lbBindEIP(w http.ResponseWriter, eid, lbID string) bool {
	e := f.get("eips", eid)
	if e == nil {
		lbEIPError(w, 400, "EIP_INVALID")
		return false
	}
	if rid := str(e["resourceId"]); rid != "" && rid != lbID {
		lbEIPError(w, 409, "EIP_ALREADY_BOUND")
		return false
	}
	e["resourceId"], e["resourceType"], e["status"] = lbID, "lb", "ATTACHED"
	return true
}

func (f *Fake) lbUnbindEIP(eid string, release bool) {
	if eid == "" {
		return
	}
	if release {
		delete(f.store["eips"], eid)
	} else if e := f.get("eips", eid); e != nil {
		e["resourceId"], e["resourceType"], e["status"] = "", "", "DETACHED"
	}
}

func (f *Fake) lbs(w http.ResponseWriter, q *Req) {
	m, p := q.R.Method, q.Parts
	switch {
	case m == "POST" && len(p) == 0:
		b := q.JSON()
		if str(b["projectId"]) != Project {
			lbValidation(w, "projectId", "invalid project")
			return
		}
		ports, _ := b["ports"].([]any)
		if len(ports) == 0 || len(ports) > 32 {
			lbValidation(w, "ports", "1..32 ports required")
			return
		}
		seen := map[string]bool{}
		for _, pt := range ports {
			fe := str(pt.(map[string]any)["frontend"])
			if seen[fe] {
				lbValidation(w, "ports", "duplicate frontend port")
				return
			}
			seen[fe] = true
		}
		backends, _ := b["backends"].([]any)
		if len(backends) == 0 || len(backends) > 64 {
			lbValidation(w, "backends", "1..64 backends required")
			return
		}
		if c, ok := b["config"].(map[string]any); ok && c["algorithm"] != nil && str(c["algorithm"]) != "round_robin" {
			lbValidation(w, "config.algorithm", "only round_robin")
			return
		}
		for _, o := range f.store["lbs"] {
			if o["name"] == b["name"] {
				lbValidation(w, "name", "name already used")
				return
			}
		}
		id := f.newID()
		o := Obj{"id": id, "projectId": Project, "userId": User, "name": b["name"], "status": "PENDING",
			"ports": ports, "backends": backends, "endpoint": "", "k8sUid": "uid-" + id, "config": Obj{"algorithm": "round_robin"},
			"dataplane": "lb-svc"}
		if wi, ok := b["whiteIp"].(bool); ok && !wi {
			if str(b["eipId"]) != "" {
				lbValidation(w, "eipId", "eipId requires whiteIp=true")
				return
			}
			o["internal"], o["dataplane"], o["vpcIp"] = true, "ovn", "10.0.0.253"
		}
		if eid := str(b["eipId"]); eid != "" {
			if !f.lbBindEIP(w, eid, id) {
				return
			}
			o["eipId"], o["dataplane"] = eid, "ovn"
		}
		f.put("lbs", id, o)
		WriteJSON(w, 201, o)
	case m == "GET" && len(p) == 0:
		out := []Obj{}
		for _, o := range f.store["lbs"] {
			if str(o["projectId"]) == q.Query("projectId") {
				out = append(out, o)
			}
		}
		WriteJSON(w, 200, out)
	case len(p) == 1:
		o := f.get("lbs", p[0])
		if o == nil {
			WriteJSON(w, 404, Obj{"error": "load balancer not found"})
			return
		}
		switch m {
		case "GET":
			switch o["status"] {
			case "PENDING":
				o["status"] = "CREATING"
			case "CREATING":
				o["status"] = "RUNNING"
				if str(o["name"]) == "fail" { // test hook: provisioning fails
					o["status"] = "FAILED"
					break
				}
				o["externalIp"] = "203.0.113.10"
				o["endpoint"] = "203.0.113.10"
				o["subnetName"] = "subnet-default"
				if e := f.get("eips", str(o["eipId"])); e != nil {
					o["externalIp"], o["endpoint"], o["subnetName"] = e["ipAddress"], e["ipAddress"], ""
				}
				if o["internal"] == true {
					o["externalIp"], o["endpoint"], o["subnetName"] = "", o["vpcIp"], ""
				}
			case "DELETING":
				delete(f.store["lbs"], p[0])
				WriteJSON(w, 404, Obj{"error": "load balancer not found"})
				return
			}
			WriteJSON(w, 200, o)
		case "PATCH":
			b := q.JSON()
			if wi, ok := b["whiteIp"].(bool); ok {
				f.lbPatchWhiteIP(w, o, p[0], wi, str(b["eipId"]))
				return
			}
			eid := str(b["eipId"])
			if eid == "" || len(b) != 1 {
				lbValidation(w, "eipId", "this fake only moves the EIP")
				return
			}
			if str(o["dataplane"]) != "ovn" {
				lbEIPError(w, 400, "EIP_SELECTION_UNAVAILABLE")
				return
			}
			if !f.lbBindEIP(w, eid, p[0]) {
				return
			}
			if old := str(o["eipId"]); old != eid {
				f.lbUnbindEIP(old, false)
			}
			o["eipId"], o["status"] = eid, "CREATING"
			WriteJSON(w, 200, o)
		case "DELETE":
			o["status"] = "DELETING"
			f.lbUnbindEIP(str(o["eipId"]), q.Query("releaseEip") == "true")
			w.WriteHeader(204)
		default:
			WriteJSON(w, 405, Obj{"error": "method not allowed"})
		}
	default:
		WriteJSON(w, 404, Obj{"error": "no route"})
	}
}

// lbPatchWhiteIP: off detaches the EIP (kept in the project) and keeps/assigns vpcIp; on binds
// eipId or allocates a new EIP in the project.
func (f *Fake) lbPatchWhiteIP(w http.ResponseWriter, o Obj, id string, on bool, eid string) {
	if str(o["dataplane"]) != "ovn" {
		WriteJSON(w, 400, Obj{"error": "Validation failed", "code": "INTERNAL_LB_UNAVAILABLE", "errors": []Obj{{"field": "whiteIp", "message": "not LB Basic"}}})
		return
	}
	if !on {
		if o["internal"] != true {
			f.lbUnbindEIP(str(o["eipId"]), false)
			delete(o, "eipId")
			if str(o["vpcIp"]) == "" {
				o["vpcIp"] = "10.0.0.252"
			}
			o["internal"], o["externalIp"], o["endpoint"] = true, "", o["vpcIp"]
		}
		WriteJSON(w, 200, o)
		return
	}
	if o["internal"] != true {
		WriteJSON(w, 200, o)
		return
	}
	if eid == "" {
		eid = f.newID()
		f.put("eips", eid, Obj{"id": eid, "projectId": Project, "name": "eip-" + eid, "ipAddress": "203.0.113.77", "status": "DETACHED", "resourceId": "", "resourceType": ""})
	}
	if !f.lbBindEIP(w, eid, id) {
		return
	}
	o["eipId"], o["internal"], o["status"] = eid, false, "CREATING"
	WriteJSON(w, 200, o)
}
