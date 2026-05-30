package vm

// CreateVMRequest - DTO для создания VM (соответствует h3vm/internal/publicapi/http/dto.go)
type CreateVMRequest struct {
	ProjectID        string `json:"project_id"`
	Name             string `json:"name"`
	CPU              int    `json:"cpu"`
	Memory           string `json:"memory"`
	DiskSize         string `json:"disk_size,omitempty"`
	Image            string `json:"image,omitempty"`
	SSHKey           string `json:"ssh_key,omitempty"`
	SSHKeyID         string `json:"ssh_key_id,omitempty"`
	SubnetName       string `json:"subnet_name,omitempty"`
	WhiteIP          bool   `json:"white_ip"`
	SourceSnapshotID string `json:"source_snapshot_id,omitempty"`
	SourceBackupID   string `json:"source_backup_id,omitempty"`
}

// UpdateVMRequest - DTO для обновления VM (CPU/RAM)
type UpdateVMRequest struct {
	CPU    *int    `json:"cpu,omitempty"`
	Memory *string `json:"memory,omitempty"`
}

// VM - ответ от API
type VM struct {
	ID         string `json:"id"`
	ProjectID  string `json:"project_id"`
	Name       string `json:"name"`
	CPU        int    `json:"cpu"`
	Memory     string `json:"memory"`
	DiskSize   string `json:"disk_size"`
	Image      string `json:"image"`
	Status     string `json:"status"`
	Endpoint   string `json:"endpoint"`
	WhiteIP    bool   `json:"white_ip"`
	SubnetName string `json:"subnet_name,omitempty"`
}
