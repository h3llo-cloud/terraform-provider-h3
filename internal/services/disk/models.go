package disk

// Disk - модель диска для Terraform
type Disk struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ProjectID      string `json:"project_id"`
	Size           string `json:"size"`
	StorageClass   string `json:"storage_class"`
	Status         string `json:"status"`
	AttachedToVMID string `json:"attached_to_vm_id"`
	CreatedAt      string `json:"created_at"`
}

// CreateDiskRequest - запрос на создание диска
type CreateDiskRequest struct {
	ProjectID    string `json:"project_id"`
	Name         string `json:"name"`
	Size         string `json:"size"`
	StorageClass string `json:"storage_class"`
}

type ResizeDiskRequest struct {
	DiskID  string `json:"disk_id"`
	NewSize string `json:"new_size"`
}

type AttachDiskRequest struct {
	DiskID string `json:"disk_id"`
	VMID   string `json:"vm_id"`
}

type DetachDiskRequest struct {
	DiskID string `json:"disk_id"`
	VMID   string `json:"vm_id"`
}

type Snapshot struct {
	ID        string `json:"id"`
	DiskID    string `json:"disk_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Size      string `json:"size"`
	CreatedAt string `json:"created_at"`
	ProjectID string `json:"project_id"`
}

type CreateSnapshotRequest struct {
	DiskID    string `json:"disk_id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
}

type RestoreSnapshotRequest struct {
	SnapshotID  string `json:"snapshot_id"`
	NewDiskName string `json:"new_disk_name"`
	ProjectID   string `json:"project_id"`
}

type Backup struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	DiskID     string `json:"disk_id"`
	SnapshotID string `json:"snapshot_id"`
	Status     string `json:"status"`
	Size       string `json:"size"`
	CreatedAt  string `json:"created_at"`
	ProjectID  string `json:"project_id"`
}

type CreateBackupRequest struct {
	SnapshotID string `json:"snapshot_id"`
	ProjectID  string `json:"project_id"`
	Name       string `json:"name"`
}

type RestoreBackupRequest struct {
	BackupID     string `json:"backup_id"`
	DiskName     string `json:"disk_name"`
	StorageClass string `json:"storage_class"`
	ProjectID    string `json:"project_id"`
}

type Restore struct {
	ID          string `json:"id"`
	BackupRef   string `json:"backup_ref"`
	DiskName    string `json:"disk_name"`
	Size        string `json:"size"`
	ProjectID   string `json:"project_id"`
	Status      string `json:"status"`
	DiskID      string `json:"disk_id"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	Message     string `json:"message"`
	CreatedAt   string `json:"created_at"`
}
