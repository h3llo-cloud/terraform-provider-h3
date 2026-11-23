package vm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// fakeH3VM answers PATCH with the queued responses (the last one repeats)
// and GET with the queued resize states (the last one repeats).
type fakeH3VM struct {
	mu      sync.Mutex
	patches []fakeResp
	gets    []string // GET bodies
	calls   []string
	bodies  []string // PATCH bodies
}

type fakeResp struct {
	code int
	body string
}

func (f *fakeH3VM) serve(t *testing.T) *VMResource {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method)
		switch r.Method {
		case http.MethodPatch:
			b, _ := io.ReadAll(r.Body)
			f.bodies = append(f.bodies, string(b))
			resp := f.patches[0]
			if len(f.patches) > 1 {
				f.patches = f.patches[1:]
			}
			w.WriteHeader(resp.code)
			_, _ = w.Write([]byte(resp.body))
		case http.MethodGet:
			body := f.gets[0]
			if len(f.gets) > 1 {
				f.gets = f.gets[1:]
			}
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(srv.Close)
	old := common.PollInterval
	common.PollInterval = time.Millisecond
	t.Cleanup(func() { common.PollInterval = old })
	c, err := client.NewClient(client.Config{BaseURL: srv.URL, KeyID: "k", SecretKey: "s", Timeout: 5 * time.Second, MaxRetries: 0})
	if err != nil {
		t.Fatal(err)
	}
	return &VMResource{client: c}
}

func (f *fakeH3VM) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == method {
			n++
		}
	}
	return n
}

func vmWithResize(state string) string {
	return `{"id":"vm-1","status":"RUNNING","cpu":4,"memory":"8Gi","resize":{"state":"` + state + `","cpu":2,"memory":"4Gi","live_cpu":true,"live_memory":true,"live_max_cpu":8,"live_max_memory":"16Gi","live_cpu_step":1}}`
}

const (
	vmOld         = `{"id":"vm-1","status":"RUNNING","cpu":4,"memory":"8Gi"}`
	patchLive     = `{"id":"vm-1","status":"RUNNING","cpu":4,"memory":"8Gi","resize":{"applied":"live"}}`
	patchRestart  = `{"id":"vm-1","status":"RUNNING","cpu":4,"memory":"8Gi","resize":{"applied":"restarting","reason":"decrease"}}`
	conflictRR    = `{"error":"reducing CPU or memory needs a restart","code":"RESIZE_RESTART_REQUIRED","applied":"restart_required","reason":"decrease","message":"reducing CPU or memory needs a restart"}`
	conflictBusy  = `{"error":"live migration in progress","code":"RESIZE_BUSY","reason":"busy","message":"live migration in progress"}`
	conflictOther = `{"error":"something else"}`
)

func resizeReq() UpdateVMRequest {
	cpu, mem := 4, "8Gi"
	return UpdateVMRequest{CPU: &cpu, Memory: &mem}
}

func noDiags(t *testing.T, d diag.Diagnostics) {
	t.Helper()
	if d.HasError() || d.WarningsCount() > 0 {
		t.Fatalf("unexpected diagnostics: %v", d)
	}
}

func TestResizeLiveWaitsForApplied(t *testing.T) {
	f := &fakeH3VM{
		patches: []fakeResp{{200, patchLive}},
		gets:    []string{vmWithResize("applying"), vmWithResize("applying"), vmWithResize("applied")},
	}
	r := f.serve(t)
	res, d := r.resize(context.Background(), "vm-1", resizeReq(), true)
	noDiags(t, d)
	if res.Applied != resizeAppliedLive {
		t.Fatalf("applied = %q", res.Applied)
	}
	if n := f.count("GET"); n != 3 {
		t.Fatalf("GET calls = %d, want 3 (until state applied)", n)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(f.bodies[0]), &sent)
	if sent["allow_restart"] != true || sent["cpu"] != float64(4) || sent["memory"] != "8Gi" {
		t.Fatalf("PATCH body: %s", f.bodies[0])
	}
}

func TestResizeLiveTimeoutIsWarning(t *testing.T) {
	old := liveResizeTimeout
	liveResizeTimeout = 30 * time.Millisecond
	t.Cleanup(func() { liveResizeTimeout = old })
	f := &fakeH3VM{patches: []fakeResp{{200, patchLive}}, gets: []string{vmWithResize("applying")}}
	r := f.serve(t)
	_, d := r.resize(context.Background(), "vm-1", resizeReq(), true)
	if d.HasError() || d.WarningsCount() != 1 || !strings.Contains(d.Warnings()[0].Detail(), "applying") {
		t.Fatalf("want one warning with the state, got %v", d)
	}
}

func TestResizeLiveRestartRequiredAfterwardsIsWarning(t *testing.T) {
	f := &fakeH3VM{patches: []fakeResp{{200, patchLive}}, gets: []string{vmWithResize("restart_required")}}
	r := f.serve(t)
	_, d := r.resize(context.Background(), "vm-1", resizeReq(), true)
	if d.HasError() || d.WarningsCount() != 1 {
		t.Fatalf("want one warning, got %v", d)
	}
}

func TestResizeRestartingDoesNotWaitForLive(t *testing.T) {
	f := &fakeH3VM{patches: []fakeResp{{200, patchRestart}}, gets: []string{vmWithResize("applying")}}
	r := f.serve(t)
	res, d := r.resize(context.Background(), "vm-1", resizeReq(), true)
	noDiags(t, d)
	if res.Applied != resizeAppliedRestarting || f.count("GET") != 0 {
		t.Fatalf("applied=%q GETs=%d; the restart is waited for by waitForVMReady", res.Applied, f.count("GET"))
	}
}

func TestResizeOldBackend(t *testing.T) {
	f := &fakeH3VM{patches: []fakeResp{{200, vmOld}}, gets: []string{vmOld}}
	r := f.serve(t)
	res, d := r.resize(context.Background(), "vm-1", resizeReq(), true)
	noDiags(t, d)
	if res.Applied != "" || f.count("GET") != 0 {
		t.Fatalf("old backend: applied=%q GETs=%d, want previous behaviour", res.Applied, f.count("GET"))
	}
	// An empty PATCH response (old backend answering 204) is the same.
	f = &fakeH3VM{patches: []fakeResp{{204, ""}}, gets: []string{vmOld}}
	r = f.serve(t)
	if _, d := r.resize(context.Background(), "vm-1", resizeReq(), true); d.HasError() {
		t.Fatal(d)
	}
}

func TestResizeRestartRequiredDiagnostic(t *testing.T) {
	f := &fakeH3VM{patches: []fakeResp{{409, conflictRR}}, gets: []string{vmWithResize("applied")}}
	r := f.serve(t)
	_, d := r.resize(context.Background(), "vm-1", resizeReq(), false)
	if !d.HasError() {
		t.Fatal("want an error")
	}
	e := d.Errors()[0]
	if e.Summary() != "Changing CPU/memory requires a VM restart" ||
		!strings.Contains(e.Detail(), "reason: decrease: reducing CPU or memory needs a restart") ||
		!strings.Contains(e.Detail(), "allow_restart_on_resize = true") {
		t.Fatalf("diagnostic: %s: %s", e.Summary(), e.Detail())
	}
	if f.count("PATCH") != 1 || !strings.Contains(f.bodies[0], `"allow_restart":false`) {
		t.Fatalf("PATCH bodies: %v", f.bodies)
	}
}

func TestResizeNoRestartOnOldBackendRefusedBeforePatch(t *testing.T) {
	f := &fakeH3VM{patches: []fakeResp{{200, vmOld}}, gets: []string{vmOld}}
	r := f.serve(t)
	_, d := r.resize(context.Background(), "vm-1", resizeReq(), false)
	if !d.HasError() || d.Errors()[0].Summary() != "Live CPU/memory change is not available" {
		t.Fatalf("want refusal, got %v", d)
	}
	if f.count("PATCH") != 0 {
		t.Fatal("old backend would restart the VM: no PATCH expected")
	}
}

func TestResizeBusyRetried(t *testing.T) {
	oldB, oldA := resizeBusyBackoff, resizeBusyAttempts
	resizeBusyBackoff, resizeBusyAttempts = time.Millisecond, 3
	t.Cleanup(func() { resizeBusyBackoff, resizeBusyAttempts = oldB, oldA })

	f := &fakeH3VM{
		patches: []fakeResp{{409, conflictBusy}, {409, conflictBusy}, {200, patchLive}},
		gets:    []string{vmWithResize("applied")},
	}
	r := f.serve(t)
	res, d := r.resize(context.Background(), "vm-1", resizeReq(), true)
	noDiags(t, d)
	if res.Applied != resizeAppliedLive || f.count("PATCH") != 3 {
		t.Fatalf("applied=%q PATCHes=%d", res.Applied, f.count("PATCH"))
	}

	f = &fakeH3VM{patches: []fakeResp{{409, conflictBusy}}, gets: []string{vmWithResize("applied")}}
	r = f.serve(t)
	_, d = r.resize(context.Background(), "vm-1", resizeReq(), true)
	if !d.HasError() || d.Errors()[0].Summary() != "VM is busy, CPU/memory not changed" ||
		!strings.Contains(d.Errors()[0].Detail(), "busy: live migration in progress") {
		t.Fatalf("want busy error, got %v", d)
	}
	if f.count("PATCH") != 3 {
		t.Fatalf("PATCHes = %d, want 3 attempts", f.count("PATCH"))
	}
}

func TestResizeOtherConflictNotRetried(t *testing.T) {
	f := &fakeH3VM{patches: []fakeResp{{409, conflictOther}}, gets: []string{vmOld}}
	r := f.serve(t)
	_, d := r.resize(context.Background(), "vm-1", resizeReq(), true)
	if !d.HasError() || d.Errors()[0].Summary() != "Error updating VM" || f.count("PATCH") != 1 {
		t.Fatalf("got %v, PATCHes %d", d, f.count("PATCH"))
	}
}

func TestSchemaAllowRestartOnResize(t *testing.T) {
	resp := &resource.SchemaResponse{}
	(&VMResource{}).Schema(context.Background(), resource.SchemaRequest{}, resp)
	a, ok := resp.Schema.Attributes["allow_restart_on_resize"].(schema.BoolAttribute)
	if !ok || !a.IsOptional() || !a.IsComputed() || a.Default == nil {
		t.Fatalf("allow_restart_on_resize must be optional+computed with a default: %+v", a)
	}
	// cpu and memory change in place: no replace plan modifiers.
	if c := resp.Schema.Attributes["cpu"].(schema.Int64Attribute); len(c.PlanModifiers) != 0 || !c.IsRequired() {
		t.Fatalf("cpu: %+v", c)
	}
	if m := resp.Schema.Attributes["memory"].(schema.StringAttribute); len(m.PlanModifiers) != 0 || !m.IsRequired() {
		t.Fatalf("memory: %+v", m)
	}
}
