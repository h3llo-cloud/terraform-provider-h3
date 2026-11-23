package vm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"h3terraform/internal/client"
)

const catalogJSON = `{
 "revision": "2026-10-r2", "generatedAt": "2026-10-06T12:00:00Z",
 "default": "ubuntu:24.04", "defaultFallback": false,
 "families": [
  {"id": "ubuntu", "name": "Ubuntu", "osType": "linux", "iconKey": "ubuntu", "versions": [
   {"id": "ubuntu:24.04", "version": "24.04", "name": "24.04 LTS", "arch": "amd64", "status": "supported",
    "eol": "2029-04-30", "usage": ["vm", "capsule"], "min": {"diskGi": 15, "memoryGi": 2, "cpu": 1},
    "cloudInit": {"supported": true, "sshKey": true, "password": true}, "tags": ["lts"]},
   {"id": "ubuntu:22.04", "version": "22.04", "name": "22.04 LTS", "status": "deprecated",
    "eol": "2027-04-30", "usage": ["vm", "capsule"], "min": {"diskGi": 15, "memoryGi": 2, "cpu": 1}, "tags": ["lts"]}
  ]},
  {"id": "windows", "name": "Windows", "osType": "windows", "versions": [
   {"id": "windows:server2025", "version": "server2025", "name": "Server 2025", "status": "supported",
    "usage": ["vm"], "min": {"diskGi": 64, "memoryGi": 4, "cpu": 2}, "license": "evaluation"}
  ]}
 ],
 "labels": {}
}`

func TestImagesRequestSignedWithQuery(t *testing.T) {
	var gotPath, gotQuery, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotKey = r.URL.Path, r.URL.RawQuery, r.Header.Get("X-H3-Signature")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(catalogJSON))
	}))
	defer srv.Close()

	c, err := client.NewClient(client.Config{BaseURL: srv.URL, KeyID: "k", SecretKey: "s", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var out ImagesResponse
	if err := c.Do(context.Background(), "GET", ImagesPath, map[string]string{"usage": "capsule", "lang": "en"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/vms/v1/images" || gotQuery != "lang=en&usage=capsule" || gotKey == "" {
		t.Fatalf("path=%q query=%q signed=%v", gotPath, gotQuery, gotKey != "")
	}
	if out.Default != "ubuntu:24.04" || len(out.Families) != 2 {
		t.Fatalf("decoded %+v", out)
	}
}

func TestFlattenImagesOrderAndFilters(t *testing.T) {
	var c ImagesResponse
	if err := json.Unmarshal([]byte(catalogJSON), &c); err != nil {
		t.Fatal(err)
	}

	all := flattenImages(c, imageFilter{})
	if len(all) != 3 {
		t.Fatalf("want 3 images, got %d", len(all))
	}
	if all[0].ID.ValueString() != "ubuntu:24.04" || all[2].ID.ValueString() != "windows:server2025" {
		t.Fatalf("order: %s, %s", all[0].ID.ValueString(), all[2].ID.ValueString())
	}
	win := all[2]
	if win.OSType.ValueString() != "windows" || win.MinDiskGi.ValueInt64() != 64 || win.MinMemoryGi.ValueInt64() != 4 ||
		win.MinCPU.ValueInt64() != 2 || win.EOL.ValueString() != "" || win.Tags == nil {
		t.Fatalf("windows entry: %+v", win)
	}
	if all[0].FamilyName.ValueString() != "Ubuntu" || all[0].Name.ValueString() != "24.04 LTS" {
		t.Fatalf("names: %+v", all[0])
	}

	if got := flattenImages(c, imageFilter{Family: "ubuntu"}); len(got) != 2 {
		t.Fatalf("family filter: %d", len(got))
	}
	if got := flattenImages(c, imageFilter{OSType: "windows"}); len(got) != 1 {
		t.Fatalf("os_type filter: %d", len(got))
	}
	if got := flattenImages(c, imageFilter{Status: "deprecated"}); len(got) != 1 || got[0].ID.ValueString() != "ubuntu:22.04" {
		t.Fatalf("status filter: %+v", got)
	}
	if got := flattenImages(c, imageFilter{Family: "nope"}); got == nil || len(got) != 0 {
		t.Fatalf("empty filter must be an empty list, got %#v", got)
	}
}
