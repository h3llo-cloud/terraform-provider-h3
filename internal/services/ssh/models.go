package ssh

// CreateSSHKeyRequest - DTO for creating SSH key (matches h3ssh/internal/publicapi/http/dto.go)
type CreateSSHKeyRequest struct {
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

// UpdateSSHKeyRequest - DTO for updating SSH key
type UpdateSSHKeyRequest struct {
	Name      *string `json:"name,omitempty"`
	PublicKey *string `json:"public_key,omitempty"`
}

// SSHKey - response from API
type SSHKey struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}
