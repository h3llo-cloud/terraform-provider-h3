package s3

type CreateBucketRequest struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
}

type CreateBucketResponse struct {
	Message     string      `json:"message"`
	Credentials Credentials `json:"credentials"`
}

type Credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

type ListBucketsResponse struct {
	Buckets []Bucket `json:"buckets"`
}

type Bucket struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Region      string `json:"region"`
	IsPublic    bool   `json:"isPublic"`
	Versioning  bool   `json:"versioning"`
	SizeBytes   int64  `json:"sizeBytes"`
	ObjectCount int64  `json:"objectCount"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

type GetBucketResponse struct {
	Bucket  Bucket      `json:"bucket"`
	Objects *BucketTree `json:"objects,omitempty"`
}

type BucketTree struct {
	CurrentPath string       `json:"currentPath"`
	Items       []BucketItem `json:"items"`
}

type BucketItem struct {
	Name         string  `json:"name"`
	Type         string  `json:"type"`
	Size         *int64  `json:"size,omitempty"`
	LastModified *string `json:"lastModified,omitempty"`
}
