package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// CPU/memory change (h3vm PATCH /api/vms/v1/{id}).
//
// h3vm with live resize applies an increase within the reserve of the running
// VM without a restart (resize.applied = "live"), restarts the VM otherwise
// ("restarting") unless allow_restart = false (409 RESIZE_RESTART_REQUIRED,
// nothing changed), and answers 409 RESIZE_BUSY during a live migration or
// another hotplug. Older h3vm returns no `resize` and always restarts.
//
// Variables so tests can shorten them.
var (
	liveResizeTimeout  = 5 * time.Minute
	resizeBusyAttempts = 5
	resizeBusyBackoff  = 10 * time.Second // doubles per attempt, capped at resizeBusyMaxWait
	resizeBusyMaxWait  = time.Minute
)

// resizeResult is how a CPU/memory change was applied.
type resizeResult struct {
	// Applied is resize.applied of the PATCH response; "" on backends without live resize.
	Applied string
}

// resize changes CPU and/or memory and, for a live change, waits until the
// running guest has it (resize.state = applied), so the next plan is stable.
func (r *VMResource) resize(ctx context.Context, id string, req UpdateVMRequest, allowRestart bool) (resizeResult, diag.Diagnostics) {
	var diags diag.Diagnostics
	req.AllowRestart = &allowRestart
	if !allowRestart {
		// Backends without live resize ignore allow_restart and restart the
		// VM; they do not report `resize` in GET, so refuse before PATCH.
		cur, err := r.get(ctx, id)
		if err != nil {
			diags.AddError("Error updating VM", "Could not read the VM before changing CPU/memory: "+err.Error())
			return resizeResult{}, diags
		}
		if cur.Resize == nil {
			diags.AddError("Live CPU/memory change is not available",
				"allow_restart_on_resize = false, but the API does not report the live-resize status of VM "+id+
					" (the platform does not support changing CPU/memory without a restart yet, or the status is temporarily unavailable). "+
					"Nothing was changed. Retry later, or set allow_restart_on_resize = true to allow the VM restart.")
			return resizeResult{}, diags
		}
	}

	patched, d := r.patchResources(ctx, id, req)
	diags.Append(d...)
	if diags.HasError() {
		return resizeResult{}, diags
	}
	if patched.Resize == nil {
		tflog.Debug(ctx, "VM resized (backend without live resize)", map[string]any{"id": id})
		return resizeResult{}, diags
	}
	res := resizeResult{Applied: patched.Resize.Applied}
	tflog.Info(ctx, "VM resize accepted", map[string]any{
		"id": id, "applied": res.Applied, "reason": patched.Resize.Reason, "message": patched.Resize.Message,
	})
	if res.Applied == resizeAppliedLive {
		diags.Append(r.waitLiveResize(ctx, id, liveResizeTimeout)...)
	}
	return res, diags
}

// patchResources sends the PATCH, retrying RESIZE_BUSY with a backoff.
func (r *VMResource) patchResources(ctx context.Context, id string, req UpdateVMRequest) (*VM, diag.Diagnostics) {
	var diags diag.Diagnostics
	wait := resizeBusyBackoff
	for attempt := 1; ; attempt++ {
		var out VM
		err := r.client.Do(ctx, "PATCH", "/api/vms/v1/"+id, nil, req, &out)
		if err == nil {
			return &out, diags
		}
		h, ok := client.AsHTTPError(err)
		if !ok || !h.IsConflict() {
			diags.AddError("Error updating VM", "Could not change CPU/memory: "+err.Error())
			return nil, diags
		}
		re := parseResizeError(h)
		switch h.Code() {
		case codeResizeRestartRequired:
			diags.AddError("Changing CPU/memory requires a VM restart",
				fmt.Sprintf("The change cannot be applied to the running VM %s without a restart (reason: %s). Nothing was changed. "+
					"Set allow_restart_on_resize = true to let the platform restart the VM, "+
					"or restart the VM yourself and apply again.", id, re.describe()))
			return nil, diags
		case codeResizeBusy:
			if attempt >= resizeBusyAttempts {
				diags.AddError("VM is busy, CPU/memory not changed",
					fmt.Sprintf("VM %s is being migrated or another CPU/memory change is in progress (reason: %s); "+
						"still busy after %d attempts. Nothing was changed. Apply again in a few minutes.", id, re.describe(), attempt))
				return nil, diags
			}
			tflog.Info(ctx, "VM busy, retrying resize", map[string]any{"id": id, "attempt": attempt, "wait": wait.String()})
			select {
			case <-ctx.Done():
				diags.AddError("Error updating VM", "Could not change CPU/memory: VM busy and "+ctx.Err().Error())
				return nil, diags
			case <-time.After(wait):
			}
			if wait *= 2; wait > resizeBusyMaxWait {
				wait = resizeBusyMaxWait
			}
		default:
			diags.AddError("Error updating VM", "Could not change CPU/memory: "+err.Error())
			return nil, diags
		}
	}
}

func parseResizeError(h *client.HTTPError) resizeError {
	var re resizeError
	_ = json.Unmarshal([]byte(h.Body), &re)
	if re.Message == "" {
		re.Message = h.Detail()
	}
	return re
}

func (e resizeError) describe() string {
	switch {
	case e.Reason != "" && e.Message != "" && e.Message != e.Reason:
		return e.Reason + ": " + e.Message
	case e.Reason != "":
		return e.Reason
	case e.Message != "":
		return e.Message
	}
	return "unknown"
}

// waitLiveResize waits until GET reports resize.state = applied: the running
// guest has the requested CPU/memory. The change is already accepted (the VM
// record has the new size), so a timeout or a restart needed afterwards is a
// warning, not an error.
func (r *VMResource) waitLiveResize(ctx context.Context, id string, timeout time.Duration) diag.Diagnostics {
	var diags diag.Diagnostics
	var last *VMResize
	err := common.WaitFor(ctx, timeout, "live CPU/memory change of VM "+id, func(ctx context.Context) (bool, error) {
		vm, err := r.get(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return false, nil // the deadline cut the GET: WaitFor reports the timeout
			}
			return false, err
		}
		switch vm.Status {
		case "ERROR", "FAILED":
			return false, fmt.Errorf("VM entered %s state", vm.Status)
		}
		last = vm.Resize
		if last == nil {
			return true, nil // status not reported: nothing to wait for
		}
		tflog.Debug(ctx, "waiting for live resize", map[string]any{
			"id": id, "state": last.State, "cpu": last.CPU, "memory": last.Memory,
		})
		switch last.State {
		case resizeStateApplied, resizeStateStopped:
			return true, nil
		case resizeStateRestartRequired:
			diags.AddWarning("VM restart required to finish the CPU/memory change",
				fmt.Sprintf("The change was accepted for VM %s, but the running VM needs a restart to get it (reason: %s). "+
					"It applies at the next restart of the VM.", id, resizeError{Reason: last.Reason, Message: last.Message}.describe()))
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		if errors.Is(err, common.ErrTimeout) {
			state := "unknown"
			if last != nil {
				state = last.State
			}
			diags.AddWarning("Live CPU/memory change still in progress",
				fmt.Sprintf("The change was accepted for VM %s, but the guest has not reported it within %s (resize state: %s). "+
					"It continues in the background.", id, timeout, state))
			return diags
		}
		diags.AddError("Error waiting for the live CPU/memory change", err.Error())
	}
	return diags
}
