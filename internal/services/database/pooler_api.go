package database

// API types, validation and error texts of the PgBouncer poolers of a
// database (h3db multi-pooler: platform/db-pooler-flex-design-2026-11-04.md,
// "API (итог реализации)"). The VPC and public listeners of a pooler are
// fields of the pooler itself.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"h3terraform/internal/client"
)

// Pooler is GET /api/databases/v1/{id}/poolers/{poolerId} (and an item of
// GET /poolers). parameters are the values in effect, userParameters the
// ones set by the user.
type Pooler struct {
	ID             string            `json:"id"`
	K8sName        string            `json:"k8sName"`
	DisplayName    string            `json:"displayName"`
	Role           string            `json:"role"`
	Mode           string            `json:"mode"`
	Instances      int64             `json:"instances"`
	ReadyInstances int64             `json:"readyInstances"`
	Paused         bool              `json:"paused"`
	Phase          string            `json:"phase"`
	Default        bool              `json:"default"`
	Adopted        bool              `json:"adopted"`
	Parameters     map[string]string `json:"parameters"`
	UserParameters map[string]string `json:"userParameters"`
	VPC            *PoolerVPC        `json:"vpc"`
	Public         []PoolerPublic    `json:"public"`
}

// PoolerVPC is the listener of the pooler in the project VPC: mode port (on
// the VPC address of the database, V1) or address (an own VPC address, V2).
type PoolerVPC struct {
	Mode   string `json:"mode"`
	IP     string `json:"ip,omitempty"`
	Port   int64  `json:"port,omitempty"`
	Status string `json:"status,omitempty"` // assigned | pending | unavailable
}

// PoolerPublic is a listener of the pooler on the public EIP of the database.
type PoolerPublic struct {
	IP    string `json:"ip"`
	Port  int64  `json:"port"`
	Via   string `json:"via"` // port-sources | service | external (adopted)
	EIPID string `json:"eipId,omitempty"`
}

// PoolerList is GET /poolers.
type PoolerList struct {
	Poolers []Pooler      `json:"poolers"`
	Budget  *PoolerBudget `json:"budget"`
	Limits  *PoolerLimits `json:"limits"`
}

type PoolerBudget struct {
	MaxConnections int64       `json:"maxConnections"`
	Reserve        int64       `json:"reserve"`
	Primary        *BudgetUsed `json:"primary"`
	Replica        *BudgetUsed `json:"replica"`
}

type BudgetUsed struct {
	Used int64 `json:"used"`
	Max  int64 `json:"max"`
}

type PoolerLimits struct {
	MaxPoolers   int64            `json:"maxPoolers"`
	MaxInstances int64            `json:"maxInstances"`
	PortMin      int64            `json:"portMin"`
	PortMax      int64            `json:"portMax"`
	DefaultPorts map[string]int64 `json:"defaultPorts"`
}

// vpcRequest: mode none removes the VPC listener; port 0 = the default.
type vpcRequest struct {
	Mode string `json:"mode,omitempty"`
	Port int64  `json:"port,omitempty"`
}

// publicRequest: port 0 removes the public listener.
type publicRequest struct {
	Port int64  `json:"port"`
	Via  string `json:"via,omitempty"`
}

// createPoolerRequest is POST /{id}/poolers.
type createPoolerRequest struct {
	ID          string            `json:"id"`
	DisplayName string            `json:"displayName,omitempty"`
	Role        string            `json:"role"`
	Mode        string            `json:"mode,omitempty"`
	Instances   *int64            `json:"instances,omitempty"`
	Parameters  map[string]string `json:"parameters,omitempty"`
	VPC         *vpcRequest       `json:"vpc,omitempty"`
	Public      *publicRequest    `json:"public,omitempty"`
}

// patchPoolerRequest is PATCH /{id}/poolers/{poolerId}: nil fields are not
// changed; a parameter with an empty value returns to the h3db default.
type patchPoolerRequest struct {
	DisplayName *string           `json:"displayName,omitempty"`
	Mode        *string           `json:"mode,omitempty"`
	Instances   *int64            `json:"instances,omitempty"`
	Parameters  map[string]string `json:"parameters,omitempty"`
	Default     *bool             `json:"default,omitempty"`
	VPC         *vpcRequest       `json:"vpc,omitempty"`
	Public      *publicRequest    `json:"public,omitempty"`
}

func (p patchPoolerRequest) empty() bool {
	return p.DisplayName == nil && p.Mode == nil && p.Instances == nil && len(p.Parameters) == 0 &&
		p.Default == nil && p.VPC == nil && p.Public == nil
}

const (
	poolerPhaseReady   = "READY"
	poolerPhasePaused  = "PAUSED"
	poolerPhaseStopped = "STOPPED"

	vpcModeNone    = "none"
	vpcStatusWait  = "pending"
	vpcStatusNoVIP = "unavailable"

	listenerPortMin = 1024
	listenerPortMax = 65535
	displayNameMax  = 64
)

var (
	poolerIDRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,18}[a-z0-9])?$`)
	poolerRoles   = []string{"rw", "ro", "r"}
	poolerModes   = []string{"session", "transaction"}
	vpcModes      = []string{vpcModeNone, "port", "address"}
	publicVias    = []string{"port-sources", "service"}
	unsignedIntRe = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
)

// poolerParamEnums: the allowlist parameters with string values; every other
// allowlist key takes a non-negative integer. Ranges and the connection
// budget are checked by h3db (they depend on the database and the other
// poolers; GET returns them as limits).
var poolerParamEnums = map[string][]string{
	"client_tls_sslmode": {"require", "prefer"},
}

// poolerParamKeys is the h3db PgBouncer allowlist (crds.PoolerParamAllowlist).
var poolerParamKeys = []string{
	"client_idle_timeout",
	"client_tls_sslmode",
	"default_pool_size",
	"idle_transaction_timeout",
	"max_client_conn",
	"max_db_client_connections",
	"max_db_connections",
	"max_prepared_statements",
	"max_user_client_connections",
	"max_user_connections",
	"min_pool_size",
	"query_timeout",
	"query_wait_timeout",
	"reserve_pool_size",
	"reserve_pool_timeout",
	"server_idle_timeout",
	"server_lifetime",
}

func oneOf(v string, allowed []string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func validatePoolerID(id string) error {
	if !poolerIDRe.MatchString(id) {
		return fmt.Errorf("pooler_id %q must be 1-20 of a-z, 0-9 and -, starting and ending with a letter or digit", id)
	}
	return nil
}

func validateDisplayName(s string) error {
	if n := len([]rune(s)); n > displayNameMax {
		return fmt.Errorf("display_name is %d characters, at most %d are allowed", n, displayNameMax)
	}
	if strings.ContainsAny(s, "\n\r\t") {
		return fmt.Errorf("display_name must be one line")
	}
	return nil
}

func validateListenerPort(attr string, port int64, zeroAllowed bool) error {
	if zeroAllowed && port == 0 {
		return nil
	}
	if port < listenerPortMin || port > listenerPortMax {
		zero := ""
		if zeroAllowed {
			zero = " (0 = no listener)"
		}
		return fmt.Errorf("%s %d is out of %d..%d%s (PORT_OUT_OF_RANGE)", attr, port, listenerPortMin, listenerPortMax, zero)
	}
	return nil
}

// validatePoolerParameter checks the key against the allowlist and the value
// format (integer or enum). Empty values are refused: removing the key from
// the map returns the parameter to its default.
func validatePoolerParameter(key, value string) error {
	if !oneOf(key, poolerParamKeys) {
		return fmt.Errorf("parameter %q is not allowed; allowed: %s", key, strings.Join(poolerParamKeys, ", "))
	}
	if value == "" {
		return fmt.Errorf("parameter %s: empty value; remove the key to return it to the default", key)
	}
	if enum, ok := poolerParamEnums[key]; ok {
		if !oneOf(value, enum) {
			return fmt.Errorf("parameter %s: %q is not one of %s", key, value, strings.Join(enum, ", "))
		}
		return nil
	}
	if !unsignedIntRe.MatchString(value) {
		return fmt.Errorf("parameter %s: %q is not a non-negative integer", key, value)
	}
	if _, err := strconv.ParseInt(value, 10, 32); err != nil {
		return fmt.Errorf("parameter %s: %q is too large", key, value)
	}
	return nil
}

func poolersPath(dbID string) string { return basePath + "/" + dbID + "/poolers" }
func poolerPath(dbID, poolerID string) string {
	return poolersPath(dbID) + "/" + url.PathEscape(poolerID)
}

// decodePoolerList reads GET /poolers: {"poolers":[...],"budget":...,
// "limits":...}, or a bare array.
func decodePoolerList(raw []byte) (*PoolerList, error) {
	raw = bytes.TrimSpace(raw)
	var out PoolerList
	if len(raw) == 0 || string(raw) == "null" {
		return &out, nil
	}
	if raw[0] == '[' {
		return &out, json.Unmarshal(raw, &out.Poolers)
	}
	return &out, json.Unmarshal(raw, &out)
}

// isPoolerGone: the pooler (or its database) does not exist. A 404
// POOLER_MULTI_DISABLED is the feature flag of h3db, not a missing pooler:
// dropping the state on it would lose the pooler.
func isPoolerGone(err error) bool {
	he, ok := client.AsHTTPError(err)
	return ok && he.IsNotFound() && he.Code() != "POOLER_MULTI_DISABLED"
}

// poolerErrorText explains the h3db pooler error codes.
var poolerErrorText = map[string]string{
	"POOLER_MULTI_DISABLED": "Several poolers per database (the /poolers API) are not enabled for this project on the " +
		"platform (h3db H3DB_POOLER_MULTI). Nothing was changed.",
	"POOLER_NOT_FOUND": "The pooler does not exist.",
	"POOLER_EXISTS": "A pooler with this pooler_id already exists in the database. Choose another pooler_id or import " +
		"the existing one: terraform import <address> <database_id>/<pooler_id>.",
	"POOLER_LIMIT_REACHED": "The database already has the maximum number of poolers. Delete an unused pooler first.",
	"POOLER_IN_USE": "The main public port 5432 of the database leads to this pooler. Point it to another pooler " +
		"(public_port = 5432 there) or move this pooler to another public_port first, then delete it or change default.",
	"ROLE_NEEDS_REPLICA": "role = \"ro\" or \"r\" needs at least two instances of the database (a read replica or an HA " +
		"standby). Add one, or use role = \"rw\".",
	"INVALID_ID":        "pooler_id must be 1-20 of a-z, 0-9 and -, starting and ending with a letter or digit.",
	"INVALID_ROLE":      "role must be rw, ro or r.",
	"INVALID_MODE":      "mode must be session or transaction.",
	"INVALID_INSTANCES": "instances is out of the range h3db allows (see limits.max_instances of data source h3_database_poolers).",
	"INVALID_PARAMETER": "A pooler parameter was refused (not in the allowlist or out of its range for this database).",
	"INVALID_LIMITS": "The poolers would use more server connections than the database allows: the sum of instances × " +
		"max_db_connections over the poolers of an instance plus 10 must stay within max_connections. Lower instances " +
		"or max_db_connections, or raise max_connections of the database.",
	"PORT_OUT_OF_RANGE": "Listener ports must be 1024-65535.",
	"PORT_IN_USE":       "The port is already used on this address (by the database or another pooler). Choose another port.",
	"PUBLIC_NEEDS_EIP":  "public_port needs a public address of the database: set white_ip = true (and eip_id) on h3_database.",
	"VPC_ADDRESS_UNAVAILABLE": "vpc_mode = \"port\" needs the VPC address of the database, which it does not have " +
		"(the VPC address feature is off for the project). Use vpc_mode = \"address\" or \"none\".",
	"POOLER_DISABLED":      "The connection pooler is not enabled on this platform.",
	"UNSUPPORTED_ENGINE":   "Connection poolers are available for PostgreSQL only.",
	"DATABASE_NOT_RUNNING": "The database is not RUNNING. Wait until it runs and apply again.",
	"CHANGE_IN_PROGRESS":   "Another change of the database or the pooler is in progress. Wait for it to finish and apply again.",
}

// poolerErrorDiag is the diagnostic of a pooler call: a readable text for the
// known codes (INVALID_LIMITS with its numbers when h3db sends them), the
// EIP and replica errors, or the raw error.
func poolerErrorDiag(action string, err error) (string, string) {
	he, ok := client.AsHTTPError(err)
	if !ok {
		return "Error " + action, err.Error()
	}
	code := he.Code()
	text, ok := poolerErrorText[code]
	if !ok {
		return apiErrorDiag(action, err)
	}
	if code == "INVALID_LIMITS" {
		var b struct {
			Budget    *int64 `json:"budget"`
			Used      *int64 `json:"used"`
			Requested *int64 `json:"requested"`
		}
		if json.Unmarshal([]byte(he.Body), &b) == nil && b.Budget != nil {
			text += fmt.Sprintf(" Budget %d, used %d, requested %d.", *b.Budget, deref(b.Used), deref(b.Requested))
		}
	}
	if d := he.Detail(); d != "" && d != strings.TrimSpace(he.Body) {
		text += "\nh3db: " + d
	}
	return "Error " + action + ": " + code, text + "\n\n" + err.Error()
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
