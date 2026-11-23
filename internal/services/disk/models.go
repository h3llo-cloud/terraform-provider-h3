package disk

// Disk - модель диска для Terraform
type Disk struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ProjectID        string `json:"project_id"`
	Size             string `json:"size"`
	StorageClass     string `json:"storage_class"`
	PerformanceClass string `json:"performance_class,omitempty"`
	Status           string `json:"status"`
	AttachedToVMID   string `json:"attached_to_vm_id,omitempty"`
	CreatedAt        string `json:"created_at"`
}

// CreateDiskRequest - запрос на создание диска
type CreateDiskRequest struct {
	ProjectID        string `json:"project_id"`
	Name             string `json:"name"`
	Size             string `json:"size"`
	StorageClass     string `json:"storage_class"`
	PerformanceClass string `json:"performance_class,omitempty"`
}

// PerformanceClassRequest changes the QoS class in place
// (POST /api/disks/v1/{id}/performance-class).
type PerformanceClassRequest struct {
	PerformanceClass string `json:"performance_class"`
}

type ResizeDiskRequest struct {
	DiskID  string `json:"disk_id"`
	NewSize string `json:"new_size"`
}
