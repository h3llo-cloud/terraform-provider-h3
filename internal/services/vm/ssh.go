package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// sshKeyIDs returns the creator's h3ssh key ids: ssh_key_ids, or the deprecated
// ssh_key_id. ok is false when neither is known. Call it on the config (or on a
// plan at create, where an unconfigured ssh_key_ids is unknown).
func (m VMResourceModel) sshKeyIDs(ctx context.Context) ([]string, bool) {
	if !m.SSHKeyIDs.IsNull() && !m.SSHKeyIDs.IsUnknown() {
		var ids []string
		if d := m.SSHKeyIDs.ElementsAs(ctx, &ids, false); d.HasError() {
			return nil, false
		}
		sort.Strings(ids)
		if ids == nil {
			ids = []string{}
		}
		return ids, true
	}
	if m.SSHKeyID.IsNull() || m.SSHKeyID.IsUnknown() {
		return nil, false
	}
	return []string{m.SSHKeyID.ValueString()}, true
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// UpgradeState: schema 0 (provider <= 0.3) had ssh_key, the public key text,
// which is gone: keys come from h3ssh only. The upgrade drops ssh_key and keeps
// ssh_key_id; ssh_key_ids and ssh_access_managed start empty and are filled by
// the next refresh/apply. No API calls, the VM is not replaced.
func (r *VMResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {StateUpgrader: upgradeVMStateV0},
	}
}

func upgradeVMStateV0(_ context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.RawState == nil || req.RawState.JSON == nil {
		resp.Diagnostics.AddError("Unable to upgrade h3_vm state", "the prior state has no JSON form")
		return
	}
	upgraded, err := upgradeVMStateV0JSON(req.RawState.JSON)
	if err != nil {
		resp.Diagnostics.AddError("Unable to upgrade h3_vm state", err.Error())
		return
	}
	resp.DynamicValue = &tfprotov6.DynamicValue{JSON: upgraded}
}

func upgradeVMStateV0JSON(raw []byte) ([]byte, error) {
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("decode state v0: %w", err)
	}
	delete(st, "ssh_key")
	if _, ok := st["ssh_key_ids"]; !ok {
		st["ssh_key_ids"] = nil
	}
	if _, ok := st["ssh_access_managed"]; !ok {
		st["ssh_access_managed"] = nil
	}
	for _, k := range []string{"allow_restart_on_resize", "user_data", "user_data_encoding", "placement_group_id", "deletion_protection"} {
		if _, ok := st[k]; !ok {
			st[k] = nil
		}
	}
	return json.Marshal(st)
}

// sshCallerKey is the private-state key with the user id of the API key owner
// (the grantee of ssh_key_ids). The h3vm view does not say who the caller is,
// so the provider learns it from the grant its own change produced.
const sshCallerKey = "ssh_caller_user_id"

type privateState interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

func getSSHCaller(ctx context.Context, p privateState) (string, diag.Diagnostics) {
	if p == nil {
		return "", nil
	}
	b, d := p.GetKey(ctx, sshCallerKey)
	if d.HasError() || len(b) == 0 {
		return "", d
	}
	var id string
	_ = json.Unmarshal(b, &id)
	return id, d
}

func setSSHCaller(ctx context.Context, p privateState, id string) diag.Diagnostics {
	if p == nil || id == "" {
		return nil
	}
	b, _ := json.Marshal(id)
	return p.SetKey(ctx, sshCallerKey, b)
}

// isSSHAccessUnavailable: h3vm without the ssh-access API (404 without a code)
// or with VM_SSH_ACCESS_ENABLED off (503 SSH_ACCESS_DISABLED).
func isSSHAccessUnavailable(err error) bool {
	e, ok := client.AsHTTPError(err)
	if !ok {
		return false
	}
	return (e.StatusCode == 404 && !strings.Contains(e.Body, `"code"`)) ||
		(e.StatusCode == 503 && strings.Contains(e.Body, "SSH_ACCESS_DISABLED"))
}

// learnSSHCaller finds the caller's grant after the provider set ssh_key_ids:
// the only self/creator grant in mode "selected" with exactly these keys.
func learnSSHCaller(acc *SSHAccess, sent []string) string {
	if acc == nil || len(sent) == 0 {
		return ""
	}
	found := ""
	for _, g := range acc.Grants {
		if g.Mode != SSHModeSelected || (g.Source != "creator" && g.Source != "self") || !sameIDs(g.selectedKeyIDs(), sent) {
			continue
		}
		if found != "" {
			return "" // ambiguous
		}
		found = g.UserID
	}
	return found
}

// callerGrant: the grant of the known caller, else (import, state from an
// older provider) the only grant with source=creator.
func callerGrant(acc *SSHAccess, caller string) (*SSHAccessGrant, bool) {
	if caller != "" {
		return acc.grant(caller), true
	}
	var found *SSHAccessGrant
	for i := range acc.Grants {
		if acc.Grants[i].Source == "creator" {
			if found != nil {
				return nil, false
			}
			found = &acc.Grants[i]
		}
	}
	return found, found != nil
}

// syncSSH reads the ssh-access view and maps it onto the model:
// ssh_access_managed = managed; ssh_key_ids = key_ids of the caller's grant
// (mode "selected"). When the caller cannot be identified, ssh_key_ids keeps
// the planned/prior value. It returns the caller user id (learned or given).
func (r *VMResource) syncSSH(ctx context.Context, m *VMResourceModel, caller string, sent []string, diags *diag.Diagnostics) string {
	var acc SSHAccess
	err := r.client.Do(ctx, "GET", sshAccessPath(m.ID.ValueString()), nil, nil, &acc)
	if err != nil {
		if !isSSHAccessUnavailable(err) {
			diags.AddWarning("SSH access of the VM not read",
				"ssh_key_ids and ssh_access_managed keep their previous values: "+err.Error())
		}
		m.fillUnknownSSH()
		return caller
	}
	m.SSHAccessManaged = types.BoolValue(acc.Managed)
	if caller == "" {
		caller = learnSSHCaller(&acc, sent)
	}
	if g, ok := callerGrant(&acc, caller); ok {
		switch {
		case g == nil: // the caller's grant was revoked elsewhere
			m.SSHKeyIDs = types.SetValueMust(types.StringType, []attr.Value{})
		case g.Mode == SSHModeSelected:
			m.SSHKeyIDs, _ = types.SetValueFrom(ctx, types.StringType, g.selectedKeyIDs())
		}
	}
	m.fillUnknownSSH()
	return caller
}

// fillUnknownSSH makes computed SSH attributes known after apply when the API
// did not tell their values.
func (m *VMResourceModel) fillUnknownSSH() {
	if m.SSHAccessManaged.IsUnknown() {
		m.SSHAccessManaged = types.BoolNull()
	}
	if m.SSHKeyIDs.IsUnknown() {
		if !m.SSHKeyID.IsNull() && !m.SSHKeyID.IsUnknown() {
			m.SSHKeyIDs = types.SetValueMust(types.StringType, []attr.Value{types.StringValue(m.SSHKeyID.ValueString())})
		} else {
			m.SSHKeyIDs = types.SetNull(types.StringType)
		}
	}
}
