package vm

// CreateVMRequest is the h3vm public create DTO (publicapi/http/dto.go).
type CreateVMRequest struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	CPU       int    `json:"cpu"`
	Memory    string `json:"memory"`
	DiskSize  string `json:"disk_size,omitempty"`
	Image     string `json:"image,omitempty"`
	// SSHKeyID is the first of SSHKeyIDs, for h3vm versions that predate ssh_key_ids.
	SSHKeyID string `json:"ssh_key_id,omitempty"`
	// SSHKeyIDs are h3ssh keys of the caller (the creator's grant in mode
	// "selected"). Text keys are not accepted: every key lives in h3ssh.
	SSHKeyIDs []string `json:"ssh_key_ids,omitempty"`
	// SSHAccessManaged: qemu-guest-agent keeps ~user/.ssh/authorized_keys in
	// sync with the VM grants. nil = platform default (on when the image has the agent).
	SSHAccessManaged *bool  `json:"ssh_access_managed,omitempty"`
	SubnetName       string `json:"subnet_name,omitempty"`
	WhiteIP          bool   `json:"white_ip"`
	SourceSnapshotID string `json:"source_snapshot_id,omitempty"`
	SourceBackupID   string `json:"source_backup_id,omitempty"`
	// EIPID selects an existing, free project EIP as the public IP (requires
	// WhiteIP). Empty + WhiteIP allocates a new EIP as before.
	EIPID string `json:"eip_id,omitempty"`
	// UserData is the client's cloud-init user-data (write-only: the API never
	// returns it). UserDataEncoding: plain (default) or base64.
	UserData         string `json:"user_data,omitempty"`
	UserDataEncoding string `json:"user_data_encoding,omitempty"`
	// PlacementGroupID puts the VM into a spread placement group (create only).
	PlacementGroupID string `json:"placement_group_id,omitempty"`
	// DeletionProtection: DELETE answers 409 VM_DELETION_PROTECTED until it is turned off.
	DeletionProtection bool `json:"deletion_protection,omitempty"`
}

// UpdateVMRequest is the h3vm PATCH /api/vms/v1/{id} body (OpenAPI h3vm
// feature/sshkeys-vm-access + live resize): CPU and RAM, the caller's own SSH
// grant (ssh_key_ids, mode=selected, [] revokes it; live, no restart) and SSH
// access management (a running VM restarts). AllowEmpty permits leaving the VM
// without SSH keys. AllowRestart (h3vm live resize; default true): restart the
// VM when a CPU/RAM change cannot be applied live; false answers 409
// RESIZE_RESTART_REQUIRED. Backends without live resize ignore it.
type UpdateVMRequest struct {
	CPU              *int      `json:"cpu,omitempty"`
	Memory           *string   `json:"memory,omitempty"`
	AllowRestart     *bool     `json:"allow_restart,omitempty"`
	SSHKeyIDs        *[]string `json:"ssh_key_ids,omitempty"`
	SSHAccessManaged *bool     `json:"ssh_access_managed,omitempty"`
	AllowEmpty       bool      `json:"allow_empty,omitempty"`
	// DeletionProtection is changed live, without a restart.
	DeletionProtection *bool `json:"deletion_protection,omitempty"`
}

func (u UpdateVMRequest) empty() bool {
	return u.CPU == nil && u.Memory == nil && u.SSHKeyIDs == nil && u.SSHAccessManaged == nil
}

// How h3vm applied a CPU/memory change (PATCH resize.applied).
const (
	resizeAppliedLive       = "live"       // hotplug, no restart
	resizeAppliedRestarting = "restarting" // the VM restarts with the new size
	resizeAppliedNextStart  = "next_start" // the VM is stopped: applies at start
)

// Live-resize state of a VM (GET resize.state).
const (
	resizeStateApplied         = "applied"
	resizeStateApplying        = "applying"
	resizeStateRestartRequired = "restart_required"
	resizeStateStopped         = "stopped"
)

// Error codes of a refused PATCH (409).
const (
	codeResizeRestartRequired = "RESIZE_RESTART_REQUIRED"
	codeResizeBusy            = "RESIZE_BUSY"
)

// VMResize is the h3vm live-resize view (publicapi VMResizeResponse): in the
// PATCH response Applied/Reason/Message, in GET of one VM the state and what
// the running guest has. nil when the backend predates live resize.
type VMResize struct {
	Applied       string `json:"applied,omitempty"`
	State         string `json:"state,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Message       string `json:"message,omitempty"`
	CPU           int    `json:"cpu,omitempty"`
	Memory        string `json:"memory,omitempty"`
	LiveCPU       bool   `json:"live_cpu"`
	LiveMemory    bool   `json:"live_memory"`
	LiveMaxCPU    int    `json:"live_max_cpu,omitempty"`
	LiveMaxMemory string `json:"live_max_memory,omitempty"`
	LiveCPUStep   int    `json:"live_cpu_step,omitempty"`
}

// resizeError is the 409 body of a refused PATCH (publicapi ResizeErrorResponse).
type resizeError struct {
	Error   string `json:"error"`
	Code    string `json:"code"`
	Applied string `json:"applied,omitempty"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// VM is the h3vm public API object (VirtualMachineResponse). The API does not
// return ssh_key_id, source_snapshot_id or source_backup_id; SSH keys and
// ssh_access_managed come from GET /api/vms/v1/{id}/ssh-access.
type VM struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	CPU       int       `json:"cpu"`
	Memory    string    `json:"memory"`
	DiskSize  string    `json:"disk_size"`
	Image     string    `json:"image"`
	Status    string    `json:"status"`
	Endpoint  string    `json:"endpoint"`
	WhiteIP   bool      `json:"white_ip"`
	NetConfig NetConfig `json:"net_config"`
	// SubnetName is a legacy top-level field; current h3vm returns net_config.subnet_name.
	SubnetName string `json:"subnet_name,omitempty"`
	// EIPID is the bound pool EIP; nil when the backend predates the field.
	EIPID *string `json:"eip_id,omitempty"`
	// RestartRequired: changes are pending until the VM restarts (live-resize backends).
	RestartRequired bool `json:"restart_required,omitempty"`
	// Resize: live-resize view (GET) or how a PATCH was applied; nil on old backends.
	Resize *VMResize `json:"resize,omitempty"`
	// DeletionProtection: DELETE answers 409 VM_DELETION_PROTECTED (false on backends without it).
	DeletionProtection bool `json:"deletion_protection"`
	// PlacementGroupID is the spread placement group of the VM ("" when none).
	PlacementGroupID string `json:"placement_group_id,omitempty"`
}

// NetConfig is the VM network placement.
type NetConfig struct {
	SubnetName string `json:"subnet_name"`
	SubnetID   string `json:"subnet_id"`
	VPCID      string `json:"vpc_id"`
	InternalIP string `json:"internal_ip,omitempty"`
}

func (v VM) subnetName() string {
	if v.NetConfig.SubnetName != "" {
		return v.NetConfig.SubnetName
	}
	return v.SubnetName
}

// SSHAccess is the h3vm ssh-access view (GET /api/vms/v1/{id}/ssh-access, and
// the answer of PUT/DELETE on grants): who may log in to the VM. Only the
// fields the provider uses.
type SSHAccess struct {
	Managed         bool             `json:"managed"`
	ToggleAvailable bool             `json:"toggle_available"`
	Status          string           `json:"status"`
	Grants          []SSHAccessGrant `json:"grants"`
}

func (a *SSHAccess) grant(userID string) *SSHAccessGrant {
	for i := range a.Grants {
		if a.Grants[i].UserID == userID {
			return &a.Grants[i]
		}
	}
	return nil
}

// SSHAccessGrant is the access of one project member to the VM.
type SSHAccessGrant struct {
	UserID string         `json:"user_id"`
	Mode   string         `json:"mode"`
	KeyIDs []string       `json:"key_ids,omitempty"`
	Keys   []SSHAccessKey `json:"keys,omitempty"`
	Active *bool          `json:"active,omitempty"`
	// InactiveReason: NOT_MEMBER | ROLE_NOT_ALLOWED.
	InactiveReason string `json:"inactive_reason,omitempty"`
	GrantedBy      string `json:"granted_by,omitempty"`
	GrantedAt      string `json:"granted_at,omitempty"`
	// Source: creator | member | self.
	Source string `json:"source,omitempty"`
}

// SSHAccessKey is one h3ssh key of a grant.
type SSHAccessKey struct {
	KeyID       string `json:"key_id"`
	Name        string `json:"name,omitempty"`
	Type        string `json:"type,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// SetSSHGrantRequest is the PUT body of /api/vms/v1/{id}/ssh-access/grants/{user_id}.
type SetSSHGrantRequest struct {
	Mode       string   `json:"mode"`
	KeyIDs     []string `json:"key_ids,omitempty"`
	AllowEmpty bool     `json:"allow_empty,omitempty"`
}

// selectedKeyIDs: key_ids, or the ids of keys when the API returns only those.
func (g SSHAccessGrant) selectedKeyIDs() []string {
	if g.KeyIDs != nil {
		return g.KeyIDs
	}
	ids := make([]string, 0, len(g.Keys))
	for _, k := range g.Keys {
		if k.KeyID != "" {
			ids = append(ids, k.KeyID)
		}
	}
	return ids
}
