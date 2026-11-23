package database

import (
	"fmt"
	"regexp"
	"strconv"
)

// API paths of the h3db public API (API keys via ARS).
const (
	basePath = "/api/databases/v1"
)

// Database is the h3db API object (camelCase, same as the console API).
type Database struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"projectId"`
	Name          string    `json:"name"`
	Engine        string    `json:"engine"`
	Version       string    `json:"version"`
	Status        string    `json:"status"`
	SizeGB        string    `json:"sizeGb"`
	CPU           int64     `json:"cpu"`
	Memory        string    `json:"memory"`
	Endpoint      string    `json:"endpoint"`
	InternalIP    string    `json:"internalIP"`
	WhiteIP       bool      `json:"whiteIp"`
	NetConfig     NetConfig `json:"netConfig"`
	ActualSizeGB  string    `json:"actualSizeGb"`
	StorageStatus string    `json:"storageStatus"`
	// ComputeStatus is READY, or SCALING while a cpu/memory change is being
	// rolled out (the database restarts). Empty on servers without /scale.
	ComputeStatus  string        `json:"computeStatus"`
	PostgresConfig *EngineConfig `json:"postgresConfig,omitempty"`
	MariaDBConfig  *EngineConfig `json:"mariadbConfig,omitempty"`
	// Read replicas (TRD 17): ReplicaOf is the source of a replica (empty
	// otherwise), Replication its short state, PromotedFrom the former
	// source of a promoted replica.
	ReplicaOf    string              `json:"replicaOf,omitempty"`
	Replication  *ReplicationSummary `json:"replication,omitempty"`
	ReplicaCount int64               `json:"replicaCount,omitempty"`
	PromotedFrom string              `json:"promotedFrom,omitempty"`
}

// EngineConfig is the part of postgresConfig / mariadbConfig the provider
// uses.
type EngineConfig struct {
	Instances      int64 `json:"instances,omitempty"`
	MaxConnections int64 `json:"maxConnections,omitempty"`
}

// maxConnections of the database as h3db reports it (0 = unknown).
func (d *Database) maxConnections() int64 {
	switch {
	case d.PostgresConfig != nil:
		return d.PostgresConfig.MaxConnections
	case d.MariaDBConfig != nil:
		return d.MariaDBConfig.MaxConnections
	}
	return 0
}

// SettingsRequest is PUT /api/databases/v1/{id}/settings.
type SettingsRequest struct {
	MaxConnections int64 `json:"maxConnections"`
}

// Settings is GET /api/databases/v1/{id}/settings (the fields the provider
// waits on).
type Settings struct {
	MaxConnections        int64 `json:"maxConnections"`
	RunningMaxConnections int64 `json:"runningMaxConnections"`
	PendingRestart        bool  `json:"pendingRestart"`
	// Read replica settings (TRD 17 §3.3); the delays only on a replica.
	HotStandbyFeedback              bool   `json:"hotStandbyFeedback"`
	MaxStandbyStreamingDelaySeconds *int64 `json:"maxStandbyStreamingDelaySeconds,omitempty"`
	MaxStandbyArchiveDelaySeconds   *int64 `json:"maxStandbyArchiveDelaySeconds,omitempty"`
}

type NetConfig struct {
	SubnetName string `json:"subnetName"`
	// EIPID is the project pool EIP behind the public address; nil when the
	// server does not report it (older h3db, or a non-pool address).
	EIPID *string `json:"eipId,omitempty"`
}

type CreateRequest struct {
	ProjectID  string `json:"projectId"`
	Name       string `json:"name"`
	Engine     string `json:"engine"`
	Version    string `json:"version,omitempty"`
	CPU        int64  `json:"cpu"`
	SizeGB     string `json:"sizeGb"`
	Memory     string `json:"memory"`
	WhiteIP    bool   `json:"whiteIp"`
	SubnetName string `json:"subnetName,omitempty"`
	// EIPID selects an existing free project EIP (requires WhiteIP).
	EIPID string `json:"eipId,omitempty"`
	// PostgresConfig carries max_connections on create when it is within
	// the create bounds of h3db (createMaxConnectionsLimit).
	PostgresConfig *EngineConfig `json:"postgresConfig,omitempty"`
	// StorageAutoscale is sent only for an enabled storage_autoscale block.
	StorageAutoscale *StorageAutoscaleRequest `json:"storageAutoscale,omitempty"`
}

// WhiteIPAttachRequest is the optional body of POST {id}/whiteip/attach.
// Without a body h3db leases a new (billed) project EIP.
type WhiteIPAttachRequest struct {
	EIPID string `json:"eipId"`
}

type ResizeRequest struct {
	SizeGB string `json:"sizeGb"`
}

// ScaleRequest is the body of POST /api/databases/v1/{id}/scale. Both fields
// are sent: the full target shape.
type ScaleRequest struct {
	CPU    int64  `json:"cpu"`
	Memory string `json:"memory"`
}

type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

const (
	StatusRunning   = "RUNNING"
	StatusFailed    = "FAILED"
	StorageResizing = "RESIZING"
	ComputeScaling  = "SCALING"
	maxSizeGi       = 1024
	maxMemoryGi     = 1024
	minCPU          = 1
	maxCPU          = 256
	maxGiPerCPU     = 16

	// max_connections bounds (h3db TRD 14): the API also caps it at
	// RAM GiB × 100. Create accepts up to createMaxConnectionsLimit; a larger
	// value is applied with PUT /settings once the database runs.
	minMaxConnections         = 20
	maxMaxConnections         = 5000
	createMaxConnectionsLimit = 1000
)

// validateMaxConnections checks the static bounds of max_connections.
func validateMaxConnections(n int64, engine string) error {
	if engine != "" && engine != "postgres" {
		return fmt.Errorf("max_connections can be set for PostgreSQL only (engine %q)", engine)
	}
	if n < minMaxConnections || n > maxMaxConnections {
		return fmt.Errorf("max_connections %d must be between %d and %d (h3db also caps it at 100 per Gi of memory)", n, minMaxConnections, maxMaxConnections)
	}
	return nil
}

var giRe = regexp.MustCompile(`^([1-9][0-9]*)Gi$`)

// parseGi parses the "<n>Gi" quantities the h3db API accepts.
func parseGi(s string) (int64, error) {
	m := giRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q must be an integer Gi quantity like \"10Gi\"", s)
	}
	return strconv.ParseInt(m[1], 10, 64)
}

// validateSize checks the size the API will accept (1..1024 Gi).
func validateSize(s string) error {
	n, err := parseGi(s)
	if err != nil {
		return err
	}
	if n > maxSizeGi {
		return fmt.Errorf("%q is above the maximum of %dGi", s, maxSizeGi)
	}
	return nil
}

// checkResize returns an error when the planned size is below the current
// one: database storage can only grow.
func checkResize(current, planned string) error {
	c, err := parseGi(current)
	if err != nil {
		return nil // unknown current size: let the API decide
	}
	p, err := parseGi(planned)
	if err != nil {
		return err
	}
	if p < c {
		return fmt.Errorf("database storage can only be increased (current %s, planned %s); to shrink, create a new database and migrate the data", current, planned)
	}
	return nil
}

// validateShape checks cpu/memory against the bounds h3db applies on create
// and scale: 1-256 vCPU, 1-1024 Gi, 1-16 Gi per vCPU.
func validateShape(cpu int64, memory string) error {
	if cpu < minCPU || cpu > maxCPU {
		return fmt.Errorf("cpu %d must be between %d and %d", cpu, minCPU, maxCPU)
	}
	m, err := parseGi(memory)
	if err != nil {
		return err
	}
	if m > maxMemoryGi {
		return fmt.Errorf("memory %q is above the maximum of %dGi", memory, maxMemoryGi)
	}
	if m < cpu || m > cpu*maxGiPerCPU {
		return fmt.Errorf("memory %q for %d vCPU is outside 1-%d Gi per vCPU (allowed %dGi-%dGi)",
			memory, cpu, maxGiPerCPU, cpu, cpu*maxGiPerCPU)
	}
	return nil
}
