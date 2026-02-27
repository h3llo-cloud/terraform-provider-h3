package snapshot

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
