// Package k8s implements managed Kubernetes (h3k8s, /api/k8s/v3): the
// h3_k8s_cluster and h3_k8s_node_pool resources and the h3_k8s_versions,
// h3_k8s_cluster, h3_k8s_kubeconfig and h3_k8s_node_pools data sources.
package k8s

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"h3terraform/internal/client"
)

const apiBase = "/api/k8s/v3"

// AccessNote is appended to every description: API-key access and scopes.
const AccessNote = " Uses API-key (HMAC) access to h3k8s (`api.h3llo.cloud/api/k8s/v3`): the key must list the project; " +
	"scope `k8s:read` is enough to read, changes and the kubeconfig (admin credentials) need `k8s:write`."

// flexInt decodes a JSON number or a numeric string (h3k8s parses cpu,
// memoryGi and diskGi as json.Number and may echo them either way).
type flexInt int64

func (n *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("not a number: %s", b)
	}
	*n = flexInt(v)
	return nil
}

type WorkersDTO struct {
	Replicas flexInt `json:"replicas"`
	CPU      flexInt `json:"cpu"`
	MemoryGi flexInt `json:"memoryGi"`
	DiskGi   flexInt `json:"diskGi"`
}

type AutoscalerDTO struct {
	Enabled                bool    `json:"enabled"`
	MaxSize                flexInt `json:"maxSize,omitempty"`
	ScaleDownDelayAfterAdd flexInt `json:"scaleDownDelayAfterAdd,omitempty"`
	ScaleDownUnneededTime  flexInt `json:"scaleDownUnneededTime,omitempty"`
	// Cluster-wide autoscaler flags (PATCH /autoscaler only; h3k8s with node
	// pools). Not echoed by GET, sent only when set.
	Expander                 string `json:"expander,omitempty"`
	BalanceSimilarNodeGroups *bool  `json:"balanceSimilarNodeGroups,omitempty"`
}

type MaintenanceDTO struct {
	Enabled   bool    `json:"enabled"`
	DayOfWeek string  `json:"dayOfWeek,omitempty"`
	StartHour flexInt `json:"startHour"`
	// AutoUpgrade: minor (default) | patch | none (h3k8s !33).
	AutoUpgrade string `json:"autoUpgrade,omitempty"`
	// Exclusions: periods without automatic upgrades. Sent as the full list
	// ([] clears); a nil slice is omitted (older h3k8s / create without it).
	Exclusions *[]MaintenanceExclusionDTO `json:"exclusions,omitempty"`
}

type MaintenanceExclusionDTO struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Reason    string `json:"reason,omitempty"`
}

// NodeLocalDNSCacheDTO is the nodeLocalDNSCache block of the h3k8s cluster:
// in requests only enabled is sent; responses add the rollout state
// (DISABLED, ENABLING, READY, DISABLING, ERROR) and an optional message.
type NodeLocalDNSCacheDTO struct {
	Enabled bool   `json:"enabled"`
	State   string `json:"state,omitempty"`
	Message string `json:"message,omitempty"`
}

// Node-local DNS cache states of the h3k8s API.
const (
	NodeLocalDNSDisabled  = "DISABLED"
	NodeLocalDNSEnabling  = "ENABLING"
	NodeLocalDNSReady     = "READY"
	NodeLocalDNSDisabling = "DISABLING"
	NodeLocalDNSError     = "ERROR"
)

// Cluster is the h3k8s ClusterResponse.
type Cluster struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProjectID    string `json:"projectId"`
	Namespace    string `json:"namespace"`
	Status       string `json:"status"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
	ClusterType  string `json:"clusterType"`
	PodCIDR      string `json:"podCidr"`
	ServiceCIDR  string `json:"serviceCidr"`
	ControlPlane struct {
		Replicas flexInt `json:"replicas"`
	} `json:"controlPlane"`
	// Workers mirrors the node pool "default" on h3k8s with node pools.
	Workers           WorkersDTO      `json:"workers"`
	NodePools         []NodePool      `json:"nodePools"`
	KubernetesVersion string          `json:"kubernetesVersion"`
	Autoscaler        *AutoscalerDTO  `json:"autoscaler"`
	MaintenanceWindow *MaintenanceDTO `json:"maintenanceWindow"`
	// NodeLocalDNSCache is absent on backends without the feature.
	NodeLocalDNSCache *NodeLocalDNSCacheDTO `json:"nodeLocalDNSCache"`
	// APIAccess is absent on h3k8s without the allowlist feature.
	APIAccess *APIAccessDTO `json:"apiAccess"`
	// Network is where the workers live: mode "vpc" (networkId of the project VPC) or "shared" (legacy
	// shared platform network). Absent on h3k8s without workers in the tenant VPC.
	Network *ClusterNetworkDTO `json:"network"`
}

// ClusterNetworkDTO is the worker network of a cluster (h3k8s ClusterResponse.network).
type ClusterNetworkDTO struct {
	Mode      string `json:"mode"`
	NetworkID string `json:"networkId"`
}

// APIAccessDTO is the Kubernetes API allowlist (PUT /clusters/{id}/api-access).
type APIAccessDTO struct {
	AllowedCIDRs []string `json:"allowedCidrs"`
}

// CreateClusterRequest is the h3k8s CreateCluster body (camelCase).
type CreateClusterRequest struct {
	ProjectID            string          `json:"projectId"`
	Name                 string          `json:"name"`
	ControlPlaneReplicas int64           `json:"controlPlaneReplicas"`
	ClusterType          string          `json:"clusterType"`
	Workers              WorkersBody     `json:"workers"`
	KubernetesVersion    string          `json:"kubernetesVersion"`
	PodCIDR              string          `json:"podCidr,omitempty"`
	ServiceCIDR          string          `json:"serviceCidr,omitempty"`
	Autoscaler           *AutoscalerDTO  `json:"autoscaler,omitempty"`
	MaintenanceWindow    *MaintenanceDTO `json:"maintenanceWindow,omitempty"`
	// NodeLocalDNSCache is sent only when enabled (older backends reject unknown fields).
	NodeLocalDNSCache *NodeLocalDNSCacheDTO `json:"nodeLocalDNSCache,omitempty"`
	APIAccess         *APIAccessDTO         `json:"apiAccess,omitempty"`
	// NetworkID places the workers in that h3ovn network of the project VPC; omitted = platform default.
	NetworkID string `json:"networkId,omitempty"`
}

type WorkersBody struct {
	Replicas int64 `json:"replicas"`
	CPU      int64 `json:"cpu"`
	MemoryGi int64 `json:"memoryGi"`
	DiskGi   int64 `json:"diskGi"`
}

// PatchWorkersRequest: at least one field; cpu and memoryGi together.
type PatchWorkersRequest struct {
	Replicas *int64 `json:"replicas,omitempty"`
	CPU      *int64 `json:"cpu,omitempty"`
	MemoryGi *int64 `json:"memoryGi,omitempty"`
}

type VersionsResponse struct {
	Versions []struct {
		Version     string `json:"version"`
		NodeVMImage string `json:"nodeVMImage"`
	} `json:"versions"`
	Latest         string   `json:"latest"`
	From           string   `json:"from"`
	UpgradeTargets []string `json:"upgradeTargets"`
	NextUpgrade    string   `json:"nextUpgrade"`
}

type KubeconfigResponse struct {
	Kubeconfig string `json:"kubeconfig"`
}

// NormalizeVersion returns "v1.35.9" for "1.35.9" or "v1.35.9".
func NormalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

// SameVersion compares versions ignoring the "v" prefix.
func SameVersion(a, b string) bool { return NormalizeVersion(a) == NormalizeVersion(b) }

// CompareVersions compares two "vMAJOR.MINOR.PATCH" versions; ok is false
// when one does not parse.
func CompareVersions(a, b string) (int, bool) {
	pa, ok1 := parseVersion(a)
	pb, ok2 := parseVersion(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(NormalizeVersion(v), "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// KubeconfigEndpoint extracts the API server URL and the decoded CA
// certificate (PEM) of the first cluster of a kubeconfig. Both are empty when
// the kubeconfig has no such entries.
func KubeconfigEndpoint(kubeconfig string) (host, caPEM string) {
	for _, line := range strings.Split(kubeconfig, "\n") {
		l := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		switch {
		case host == "" && strings.HasPrefix(l, "server:"):
			host = strings.Trim(strings.TrimSpace(strings.TrimPrefix(l, "server:")), `"'`)
		case caPEM == "" && strings.HasPrefix(l, "certificate-authority-data:"):
			raw := strings.Trim(strings.TrimSpace(strings.TrimPrefix(l, "certificate-authority-data:")), `"'`)
			if b, err := base64.StdEncoding.DecodeString(raw); err == nil {
				caPEM = string(b)
			}
		}
	}
	return host, caPEM
}

// errorStatuses are terminal failure statuses of a cluster.
func isErrorStatus(s string) bool {
	return s == "ERROR" || strings.HasSuffix(s, "_ERROR")
}

// Error reasons of the node-local DNS cache endpoints.
const (
	ReasonNodeLocalDNSFeatureDisabled = "NODE_LOCAL_DNS_FEATURE_DISABLED"
	ReasonNodeLocalDNSUnsupported     = "NODE_LOCAL_DNS_UNSUPPORTED"
	ReasonNodeLocalDNSConflict        = "NODE_LOCAL_DNS_CONFLICT"
	ReasonNodeLocalDNSBusy            = "NODE_LOCAL_DNS_BUSY"
)

// reasonHints explain what to do about a known error reason.
var reasonHints = map[string]string{
	ReasonNodeLocalDNSFeatureDisabled: "NodeLocal DNSCache is not enabled on this platform yet; keep node_local_dns_cache = false.",
	ReasonNodeLocalDNSUnsupported: "The Kubernetes/Cilium version of the cluster does not support NodeLocal DNSCache; " +
		"upgrade kubernetes_version first.",
	ReasonNodeLocalDNSConflict: "The cluster already runs its own node-local-dns or a CiliumLocalRedirectPolicy for kube-dns; " +
		"remove it before enabling node_local_dns_cache.",
	ReasonNodeLocalDNSBusy: "A NodeLocal DNSCache change is already in progress; apply again once it has finished.",
}

// errorReason returns the machine-readable `reason` of an h3k8s error body
// (`{"code": <http>, "reason": "...", "message": "..."}`), "" when absent.
func errorReason(err error) string {
	h, ok := client.AsHTTPError(err)
	if !ok {
		return ""
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal([]byte(h.Body), &body) != nil {
		return ""
	}
	return body.Reason
}

// apiError turns an API error into a diagnostic detail, explaining the
// backend gap on 401/403 and on 404 for collection routes; an error with a
// `reason` is shown as "REASON (HTTP code): message" plus a hint.
func apiError(err error, collection bool) string {
	if h, ok := client.AsHTTPError(err); ok {
		msg := h.Body
		var body struct {
			Error   any    `json:"error"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(h.Body), &body) == nil && body.Message != "" {
			msg = body.Message
		}
		if body.Reason != "" {
			out := fmt.Sprintf("%s (HTTP %d from %s %s): %s", body.Reason, h.StatusCode, h.Method, h.URL, msg)
			if hint := reasonHints[body.Reason]; hint != "" {
				out += "\n\n" + hint
			}
			return out
		}
		switch {
		case h.StatusCode == 401 || h.StatusCode == 403 || (h.StatusCode == 404 && collection):
			return fmt.Sprintf("HTTP %d from %s %s: %s\n\nThe managed Kubernetes API (h3k8s) does not accept API keys yet: as of "+
				"provider 0.3.0 it is reachable only from the console. The h3_k8s_* resources start working once h3k8s is "+
				"published on api.h3llo.cloud with API-key (HMAC) authentication. A 403 can also mean the key has no access "+
				"to the project or the project quota is exhausted.", h.StatusCode, h.Method, h.URL, msg)
		}
	}
	return err.Error()
}
