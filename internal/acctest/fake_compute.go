package acctest

import (
	"net/http"
	"sort"
	"strings"

	"h3terraform/internal/common"
)

// h3vm custom images, user-data, placement groups and deletion protection
// (h3vm TRD 08/09, public API snake_case). Errors use the h3vm problem body:
// {"code": "...", "error": "CODE: text"}.

func problem(w http.ResponseWriter, status int, code, msg string) {
	WriteJSON(w, status, Obj{"status": status, "code": code, "detail": msg, "error": code + ": " + msg})
}

// UserData returns the user-data a VM was created with (write-only in the API).
func (f *Fake) UserData(vmID string) (data, encoding string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm := f.store["vms"][vmID]
	return str(vm["_user_data"]), str(vm["_user_data_encoding"])
}

// checkVMCreateExtras validates the new create fields like h3vm does.
func (f *Fake) checkVMCreateExtras(b Obj) (int, string) {
	if ud := str(b["user_data"]); ud != "" {
		if str(b["source_snapshot_id"]) != "" || str(b["source_backup_id"]) != "" {
			return 400, "USER_DATA_NOT_SUPPORTED: user-data needs a new VM from an image"
		}
		if len(ud) > 64<<10*4/3+4 {
			return 413, "USER_DATA_TOO_LARGE: user-data is larger than 64 KiB"
		}
	}
	if b["deletion_protection"] == true && f.DeletionProtectionDisabled {
		return 400, "DELETION_PROTECTION_DISABLED: deletion protection is not enabled for this project"
	}
	if gid := str(b["placement_group_id"]); gid != "" {
		g := f.get("placement_groups", gid)
		if g == nil || f.PlacementGroupsDisabled || str(g["project_id"]) != str(b["project_id"]) {
			return 400, "PLACEMENT_GROUP_NOT_FOUND: placement group not found"
		}
		if len(strList(g["vm_ids"])) >= intOf(g["max_vms"]) {
			return 409, "PLACEMENT_GROUP_FULL: the placement group is full"
		}
	}
	if img := str(b["image"]); strings.HasPrefix(img, "custom:") {
		ci := f.get("custom_images", strings.TrimPrefix(img, "custom:"))
		if ci == nil || str(ci["project_id"]) != str(b["project_id"]) {
			return 400, "IMAGE_NOT_FOUND: custom image not found"
		}
		if ci["status"] != "READY" {
			return 409, "IMAGE_NOT_READY: the image is not ready"
		}
		size := str(b["disk_size"])
		if size == "" {
			return 400, "DISK_TOO_SMALL: disk_size is required for a custom image"
		}
		if c, ok := common.QuantityCmp(size, str(intOf(ci["size_gi"]))+"Gi"); !ok || c < 0 {
			return 400, "DISK_TOO_SMALL: disk_size is below the image size"
		}
	}
	return 0, ""
}

// applyVMCreateExtras records the new create fields on the stored VM.
func (f *Fake) applyVMCreateExtras(vm, b Obj) {
	vm["deletion_protection"] = b["deletion_protection"] == true
	if ud := str(b["user_data"]); ud != "" {
		vm["_user_data"] = ud
		vm["_user_data_encoding"] = str(b["user_data_encoding"])
	}
	if gid := str(b["placement_group_id"]); gid != "" {
		vm["placement_group_id"] = gid
		g := f.get("placement_groups", gid)
		g["vm_ids"] = anyList(append(strList(g["vm_ids"]), str(vm["id"])))
	}
}

func (f *Fake) leavePlacementGroup(vm Obj) {
	g := f.get("placement_groups", str(vm["placement_group_id"]))
	if g == nil {
		return
	}
	var rest []string
	for _, id := range strList(g["vm_ids"]) {
		if id != str(vm["id"]) {
			rest = append(rest, id)
		}
	}
	g["vm_ids"] = anyList(rest)
}

// placementGroups: /api/vms/v1/placement/groups[/{id}]?project_id=.
func (f *Fake) placementGroups(w http.ResponseWriter, q *Req) {
	if f.PlacementGroupsDisabled {
		problem(w, 404, "PLACEMENT_GROUPS_DISABLED", "placement groups are not enabled for this project")
		return
	}
	m, p := q.R.Method, q.Parts[2:]
	switch {
	case len(p) == 0 && m == "GET":
		project := q.Query("project_id")
		out := []Obj{}
		for _, g := range f.store["placement_groups"] {
			if str(g["project_id"]) == project {
				out = append(out, clone(g))
			}
		}
		sort.Slice(out, func(i, j int) bool { return str(out[i]["id"]) < str(out[j]["id"]) })
		WriteJSON(w, 200, out)
	case len(p) == 0 && m == "POST":
		b := q.JSON()
		name := strings.TrimSpace(str(b["name"]))
		if name == "" || len(name) > 63 {
			problem(w, 400, "PLACEMENT_GROUP_INVALID", "name: 1..63 characters")
			return
		}
		strategy := str(b["strategy"])
		if strategy == "" {
			strategy = "spread"
		}
		if strategy != "spread" {
			problem(w, 400, "PLACEMENT_GROUP_INVALID", "strategy: only spread")
			return
		}
		id := f.newID()
		g := Obj{"id": id, "project_id": b["project_id"], "name": name, "description": str(b["description"]),
			"strategy": strategy, "vm_ids": []any{}, "max_vms": 2, "created_at": "2026-11-04T10:00:00Z"}
		f.put("placement_groups", id, g)
		WriteJSON(w, 201, clone(g))
	case len(p) == 1:
		g := f.get("placement_groups", p[0])
		if g == nil || str(g["project_id"]) != q.Query("project_id") {
			problem(w, 404, "PLACEMENT_GROUP_NOT_FOUND", "placement group not found")
			return
		}
		switch m {
		case "GET":
			WriteJSON(w, 200, clone(g))
		case "PATCH":
			b := q.JSON()
			if v, ok := b["name"]; ok {
				g["name"] = str(v)
			}
			if v, ok := b["description"]; ok {
				g["description"] = str(v)
			}
			WriteJSON(w, 200, clone(g))
		case "DELETE":
			if len(strList(g["vm_ids"])) > 0 {
				problem(w, 409, "PLACEMENT_GROUP_NOT_EMPTY", "the placement group has VMs")
				return
			}
			delete(f.store["placement_groups"], p[0])
			w.WriteHeader(204)
		}
	default:
		WriteJSON(w, 404, Obj{"error": "no route"})
	}
}

// customImages: /api/vms/v1/images/custom[/{id}]?project_id=. A new image is
// IMPORTING and READY on the next GET; a URL containing "broken" ends FAILED.
// DELETE marks it DELETING, the next GET answers 404.
func (f *Fake) customImages(w http.ResponseWriter, q *Req) {
	if f.CustomImagesDisabled {
		problem(w, 404, "CUSTOM_IMAGES_DISABLED", "custom images are not enabled for this project")
		return
	}
	m, p := q.R.Method, q.Parts[2:]
	switch {
	case len(p) == 0 && m == "GET":
		project := q.Query("project_id")
		out := []Obj{}
		for _, img := range f.store["custom_images"] {
			if str(img["project_id"]) == project {
				out = append(out, publicImage(img))
			}
		}
		sort.Slice(out, func(i, j int) bool { return str(out[i]["id"]) > str(out[j]["id"]) })
		WriteJSON(w, 200, out)
	case len(p) == 0 && m == "POST":
		b := q.JSON()
		url, vmID := str(b["url"]), str(b["source_vm_id"])
		if (url == "") == (vmID == "") {
			problem(w, 400, "CUSTOM_IMAGE_INVALID", "exactly one of url and source_vm_id")
			return
		}
		firmware := orDefault(str(b["firmware"]), "bios")
		id := f.newID()
		img := Obj{"id": id, "project_id": b["project_id"], "image": "custom:" + id, "name": b["name"],
			"description": str(b["description"]), "os_type": "linux", "firmware": firmware,
			"status": "IMPORTING", "progress": "0%", "created_at": "2026-11-04T10:00:00Z"}
		if url != "" {
			if !strings.HasPrefix(url, "https://") {
				problem(w, 400, "CUSTOM_IMAGE_URL_NOT_ALLOWED", "only https URLs")
				return
			}
			size := intOf(b["size_gi"])
			if size == 0 {
				size = 20
			}
			img["source_type"], img["source_url"], img["size_gi"] = "url", strings.SplitN(url, "?", 2)[0], size
		} else {
			vm := f.get("vms", vmID)
			if vm == nil || str(vm["project_id"]) != str(b["project_id"]) {
				problem(w, 404, "VM_NOT_FOUND", "VM not found")
				return
			}
			if vm["status"] != "STOPPED" {
				problem(w, 409, "VM_NOT_STOPPED", "stop the VM first")
				return
			}
			size, _ := common.QuantityBytes(str(vm["disk_size"]))
			gi := 20
			if size != nil {
				gi = int(size.Num().Int64() / size.Denom().Int64() >> 30)
			}
			img["source_type"], img["source_vm_id"], img["size_gi"] = "disk", vmID, gi
		}
		f.put("custom_images", id, img)
		WriteJSON(w, 202, publicImage(img))
	case len(p) == 1:
		img := f.get("custom_images", p[0])
		if img == nil || str(img["project_id"]) != q.Query("project_id") {
			problem(w, 404, "IMAGE_NOT_FOUND", "custom image not found")
			return
		}
		switch m {
		case "GET":
			switch img["status"] {
			case "IMPORTING":
				if strings.Contains(str(img["source_url"]), "broken") {
					img["status"], img["message"] = "FAILED", "HTTP 404 from the source"
				} else {
					img["status"], img["progress"] = "READY", "100%"
				}
			case "DELETING":
				delete(f.store["custom_images"], p[0])
				problem(w, 404, "IMAGE_NOT_FOUND", "custom image not found")
				return
			}
			WriteJSON(w, 200, publicImage(img))
		case "DELETE":
			img["status"] = "DELETING"
			w.WriteHeader(204)
		}
	default:
		WriteJSON(w, 404, Obj{"error": "no route"})
	}
}

func publicImage(img Obj) Obj {
	out := clone(img)
	for k := range out {
		if strings.HasPrefix(k, "_") {
			delete(out, k)
		}
	}
	return out
}
