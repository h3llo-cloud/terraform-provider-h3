package ssh

// CreateSSHKeyRequest is the h3ssh public create DTO. The public API has no
// update: a key is created and deleted only.
type CreateSSHKeyRequest struct {
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

// SSHKey is the h3ssh public API object.
type SSHKey struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}
