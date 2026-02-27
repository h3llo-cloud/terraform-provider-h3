package backup

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
