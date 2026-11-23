package vm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"h3terraform/internal/client"
	"h3terraform/internal/common"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// h3ovn public API (same API key / HMAC signing as h3vm, routed by the gateway).
const (
	ovnEIPPath       = "/api/ovn/v1/eips/"
	ovnEIPAttachPath = "/api/ovn/v1/eips/attach"
	ovnEIPDetachPath = "/api/ovn/v1/eips/detach"
)

// validateEIPConfig: eip_id selects an existing project EIP as the VM's public
// IP, so it only makes sense together with white_ip = true. Unknown values
// (computed from other resources) are checked again at apply time by the backend.
func validateEIPConfig(eipID types.String, whiteIP types.Bool) diag.Diagnostics {
	var diags diag.Diagnostics
	if eipID.IsNull() || eipID.IsUnknown() || whiteIP.IsUnknown() {
		return diags
	}
	if whiteIP.IsNull() || !whiteIP.ValueBool() {
		diags.AddAttributeError(path.Root("eip_id"), "eip_id requires white_ip = true",
			"eip_id selects an existing Elastic IP as the VM public IP; set white_ip = true or remove eip_id.")
	}
	return diags
}

// buildCreateRequest maps the planned model to the h3vm public create DTO.
// ssh_key_id/ssh_key_ids exclusivity is validated by ValidateConfig.
func buildCreateRequest(plan VMResourceModel) CreateVMRequest {
	req := CreateVMRequest{
		ProjectID: plan.ProjectID.ValueString(),
		Name:      plan.Name.ValueString(),
		CPU:       int(plan.CPU.ValueInt64()),
		Memory:    plan.Memory.ValueString(),
		WhiteIP:   plan.WhiteIP.ValueBool(),
	}
	// One key goes as ssh_key_id: h3vm accepts it also with SSH access
	// management off (ssh_key_ids then answers 503 SSH_ACCESS_DISABLED).
	if ids, ok := plan.sshKeyIDs(context.Background()); ok && len(ids) == 1 {
		req.SSHKeyID = ids[0]
	} else if ok && len(ids) > 1 {
		req.SSHKeyIDs = ids
	}
	if !plan.SSHAccessManaged.IsNull() && !plan.SSHAccessManaged.IsUnknown() {
		managed := plan.SSHAccessManaged.ValueBool()
		req.SSHAccessManaged = &managed
	}
	req.DiskSize = knownString(plan.DiskSize)
	req.Image = knownString(plan.Image)
	req.SubnetName = knownString(plan.SubnetName)
	req.SourceSnapshotID = knownString(plan.SourceSnapshotID)
	req.SourceBackupID = knownString(plan.SourceBackupID)
	req.UserData = knownString(plan.UserData)
	if req.UserData != "" {
		req.UserDataEncoding = knownString(plan.UserDataEncoding)
	}
	req.PlacementGroupID = knownString(plan.PlacementGroupID)
	req.DeletionProtection = plan.DeletionProtection.ValueBool()
	// eip_id is Optional+Computed: unknown in the plan when not configured.
	if req.WhiteIP {
		req.EIPID = knownString(plan.EIPID)
	}
	return req
}

func knownString(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

// deleteQuery builds DELETE /api/vms/v1/{id} query params. h3vm with EIP
// selection keeps the VM's EIP by default and deletes it with release_eip=true;
// the current production h3vm deletes it unless preserve_eip=true. Both are
// sent so the documented default (keep) holds on either backend.
func deleteQuery(releaseEIP bool) map[string]string {
	q := map[string]string{"preserve_disk": "false"}
	if releaseEIP {
		q["release_eip"] = "true"
	} else {
		q["preserve_eip"] = "true"
	}
	return q
}

// eipIDFromResponse resolves the eip_id to store: nil (older backend without
// the field) keeps the prior value, "" means no EIP bound.
func eipIDFromResponse(got *string, prior types.String) types.String {
	if got == nil {
		if prior.IsUnknown() {
			return types.StringNull()
		}
		return prior
	}
	if *got == "" {
		return types.StringNull()
	}
	return types.StringValue(*got)
}

func eipError(action string, err error) (string, string) { return common.EIPErrorDiag(action, err) }

type ovnEIP struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type ovnAttachEIPRequest struct {
	EIPID        string `json:"eip_id"`
	ResourceID   string `json:"resource_id"`
	ResourceType string `json:"resource_type"`
}

type ovnDetachEIPRequest struct {
	EIPID string `json:"eip_id"`
}

// swapEIP moves the VM's public IP from oldID (may be empty) to newID via the
// h3ovn public API: detach old (it stays in the project pool), attach new.
func (r *VMResource) swapEIP(ctx context.Context, vmID, oldID, newID string, timeout time.Duration) (string, string, bool) {
	if oldID != "" {
		err := r.client.Do(ctx, "POST", ovnEIPDetachPath, nil, ovnDetachEIPRequest{EIPID: oldID}, nil)
		var httpErr *client.HTTPError
		if err != nil && !(errors.As(err, &httpErr) && httpErr.IsNotFound()) {
			s, d := eipError("detaching previous EIP "+oldID, err)
			return s, d, false
		}
		if err == nil {
			if err := r.waitEIPStatus(ctx, oldID, "DETACHED", timeout); err != nil {
				return "Error waiting for EIP detach", err.Error(), false
			}
		}
	}
	attach := ovnAttachEIPRequest{EIPID: newID, ResourceID: vmID, ResourceType: "vm"}
	if err := r.client.Do(ctx, "POST", ovnEIPAttachPath, nil, attach, nil); err != nil {
		s, d := eipError("attaching EIP "+newID, err)
		return s, d, false
	}
	if err := r.waitEIPStatus(ctx, newID, "ATTACHED", timeout); err != nil {
		return "Error waiting for EIP attach", err.Error(), false
	}
	return "", "", true
}

func (r *VMResource) waitEIPStatus(ctx context.Context, eipID, want string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		var eip ovnEIP
		if err := r.client.Do(ctx, "GET", ovnEIPPath+eipID, nil, nil, &eip); err != nil {
			return err
		}
		switch eip.Status {
		case want:
			return nil
		case "ERROR":
			return fmt.Errorf("EIP %s entered ERROR state", eipID)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for EIP %s to become %s", eipID, want)
		case <-ticker.C:
		}
	}
}

type ovnCreateEIPRequest struct {
	Name      string `json:"name"`
	ProjectID string `json:"project_id"`
}

type ovnEIPFull struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	ResourceID   string `json:"resourceId"`
	ResourceType string `json:"resourceType"`
	VMID         string `json:"vmId"`
}

// publicIPOn (white_ip false -> true in place): attach eipID, or lease a new
// project EIP first. The h3vm FIP watcher then reports white_ip and endpoint.
func (r *VMResource) publicIPOn(ctx context.Context, state VMResourceModel, eipID string) (string, error) {
	if eipID == "" {
		var created ovnEIP
		req := ovnCreateEIPRequest{Name: state.Name.ValueString() + "-ip", ProjectID: state.ProjectID.ValueString()}
		if err := r.client.Do(ctx, "POST", strings.TrimSuffix(ovnEIPPath, "/"), nil, req, &created); err != nil {
			return "", fmt.Errorf("leasing an Elastic IP: %w", err)
		}
		if created.ID == "" {
			return "", fmt.Errorf("leasing an Elastic IP: the API response has no id")
		}
		if err := r.waitEIPStatus(ctx, created.ID, "DETACHED", eipSwapTimeout); err != nil {
			return created.ID, err
		}
		eipID = created.ID
	}
	if s, d, ok := r.swapEIP(ctx, state.ID.ValueString(), "", eipID, eipSwapTimeout); !ok {
		return eipID, fmt.Errorf("%s: %s", s, d)
	}
	return eipID, nil
}

// publicIPOff (white_ip true -> false in place): detach the VM's EIP; it stays
// in the project unless release is set. Without a known eip_id (older backends)
// the EIP bound to the VM is looked up in the project list.
func (r *VMResource) publicIPOff(ctx context.Context, state VMResourceModel, eipID string, release bool) error {
	vmID := state.ID.ValueString()
	if eipID == "" {
		var list []ovnEIPFull
		q := map[string]string{"project_id": state.ProjectID.ValueString(), "bound": "true"}
		if err := r.client.Do(ctx, "GET", strings.TrimSuffix(ovnEIPPath, "/"), q, nil, &list); err != nil {
			return fmt.Errorf("finding the VM's Elastic IP: %w", err)
		}
		for _, e := range list {
			if e.ResourceID == vmID || e.VMID == vmID {
				eipID = e.ID
				break
			}
		}
		if eipID == "" {
			return nil // nothing attached: already off
		}
	}
	err := r.client.Do(ctx, "POST", ovnEIPDetachPath, nil, ovnDetachEIPRequest{EIPID: eipID}, nil)
	if err != nil && !client.IsNotFound(err) {
		return fmt.Errorf("detaching EIP %s: %w", eipID, err)
	}
	if err == nil {
		if err := r.waitEIPStatus(ctx, eipID, "DETACHED", eipSwapTimeout); err != nil {
			return err
		}
	}
	if release {
		if err := r.client.Do(ctx, "DELETE", ovnEIPPath+eipID, nil, nil, nil); err != nil && !client.IsNotFound(err) {
			return fmt.Errorf("releasing EIP %s: %w", eipID, err)
		}
	}
	return nil
}
