// Package sg implements security groups: h3_security_group,
// h3_security_group_rule, h3_vm_security_groups and the h3_security_group
// data source (h3ovn public API, /api/ovn/v1/security-groups).
package sg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"

	"h3terraform/internal/client"
)

const (
	groupsPath = "/api/ovn/v1/security-groups"

	defaultPriority = 100
	maxPriority     = 170
	maxVMGroups     = 5
)

func groupPath(id string) string      { return groupsPath + "/" + id }
func rulesPath(id string) string      { return groupPath(id) + "/rules" }
func vmGroupsPath(vmID string) string { return "/api/ovn/v1/vm/" + vmID + "/security-groups" }

// Rule is a security group rule as the API returns it.
type Rule struct {
	ID                    string `json:"id,omitempty"`
	Direction             string `json:"direction"`
	Action                string `json:"action,omitempty"`
	Protocol              string `json:"protocol,omitempty"`
	PortFrom              int64  `json:"port_from,omitempty"`
	PortTo                int64  `json:"port_to,omitempty"`
	RemoteType            string `json:"remote_type,omitempty"`
	RemoteCIDR            string `json:"remote_cidr,omitempty"`
	RemoteSecurityGroupID string `json:"remote_security_group_id,omitempty"`
	Priority              int64  `json:"priority,omitempty"`
	Description           string `json:"description,omitempty"`
}

// Group is a security group as the API returns it.
type Group struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	IsDefault     bool   `json:"is_default"`
	Rules         []Rule `json:"rules"`
	Status        string `json:"status"`
	Revision      string `json:"revision"`
	AttachedCount int64  `json:"attached_count"`
	CreatedAt     string `json:"created_at"`
}

// CreateGroupRequest: Rules nil = server default (egress allow-all), [] = no rules.
type CreateGroupRequest struct {
	ProjectID   string  `json:"project_id"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Rules       *[]Rule `json:"rules,omitempty"`
}

type UpdateGroupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Rules       []Rule `json:"rules"`
	Revision    string `json:"revision,omitempty"`
}

type VMGroups struct {
	ResourceType     string   `json:"resource_type"`
	ResourceID       string   `json:"resource_id"`
	SecurityGroupIDs []string `json:"security_group_ids"`
	SecurityGroups   []Group  `json:"security_groups"`
	ForeignGroups    []string `json:"foreign_groups"`
}

type SetVMGroupsRequest struct {
	SecurityGroupIDs []string `json:"security_group_ids"`
}

// ---- normalisation (mirrors h3ovn so that config and API compare equal) ------

// normalize fills the server defaults and canonicalises CIDRs; it never fails
// (invalid values are left for the API to reject).
func normalize(r Rule) Rule {
	n := Rule{
		Direction:             strings.ToLower(strings.TrimSpace(r.Direction)),
		Action:                strings.ToLower(strings.TrimSpace(r.Action)),
		Protocol:              strings.ToLower(strings.TrimSpace(r.Protocol)),
		PortFrom:              r.PortFrom,
		PortTo:                r.PortTo,
		RemoteCIDR:            strings.TrimSpace(r.RemoteCIDR),
		RemoteSecurityGroupID: r.RemoteSecurityGroupID,
		Priority:              r.Priority,
		Description:           strings.TrimSpace(r.Description),
	}
	if n.Action == "" {
		n.Action = "allow"
	}
	if n.Protocol == "" {
		n.Protocol = "all"
	}
	if n.Priority == 0 {
		n.Priority = defaultPriority
	}
	if n.RemoteSecurityGroupID != "" {
		n.RemoteType = "security_group"
	} else {
		n.RemoteType = "cidr"
		n.RemoteCIDR = canonicalCIDR(n.RemoteCIDR)
	}
	if n.Protocol == "tcp" || n.Protocol == "udp" {
		if n.PortFrom == 0 && n.PortTo == 0 {
			n.PortFrom, n.PortTo = 1, 65535
		} else if n.PortTo == 0 {
			n.PortTo = n.PortFrom
		}
	}
	return n
}

func canonicalCIDR(s string) string {
	if s == "" {
		return s
	}
	if !strings.Contains(s, "/") {
		ip := net.ParseIP(s)
		if ip == nil {
			return s
		}
		if v4 := ip.To4(); v4 != nil {
			return v4.String() + "/32"
		}
		return ip.String() + "/128"
	}
	if _, n, err := net.ParseCIDR(s); err == nil {
		return n.String()
	}
	return s
}

func ruleKey(r Rule) string {
	n := normalize(r)
	return fmt.Sprintf("%s|%s|%s|%d-%d|%s|%s|%s|%d|%s", n.Direction, n.Action, n.Protocol, n.PortFrom, n.PortTo,
		n.RemoteType, n.RemoteCIDR, n.RemoteSecurityGroupID, n.Priority, n.Description)
}

// sameRule compares two rules after normalisation (ids are ignored).
func sameRule(a, b Rule) bool { return ruleKey(a) == ruleKey(b) }

// sameRuleSet compares two rule lists as multisets after normalisation.
func sameRuleSet(a, b []Rule) bool {
	if len(a) != len(b) {
		return false
	}
	ka := make([]string, len(a))
	kb := make([]string, len(b))
	for i := range a {
		ka[i] = ruleKey(a[i])
		kb[i] = ruleKey(b[i])
	}
	sort.Strings(ka)
	sort.Strings(kb)
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

// validateRule catches what can be caught before an API call.
func validateRule(r Rule) error {
	n := normalize(r)
	switch n.Direction {
	case "ingress", "egress":
	default:
		return fmt.Errorf("direction must be ingress or egress, got %q", r.Direction)
	}
	switch n.Action {
	case "allow", "deny":
	default:
		return fmt.Errorf("action must be allow or deny, got %q", r.Action)
	}
	switch n.Protocol {
	case "all", "tcp", "udp", "icmp":
	default:
		return fmt.Errorf("protocol must be all, tcp, udp or icmp, got %q", r.Protocol)
	}
	if n.Priority < 1 || n.Priority > maxPriority {
		return fmt.Errorf("priority must be between 1 and %d, got %d", maxPriority, n.Priority)
	}
	if (n.Protocol == "all" || n.Protocol == "icmp") && (r.PortFrom != 0 || r.PortTo != 0) {
		return fmt.Errorf("ports are only allowed for tcp and udp")
	}
	if n.PortFrom < 0 || n.PortTo > 65535 || n.PortFrom > n.PortTo {
		return fmt.Errorf("invalid port range %d-%d", n.PortFrom, n.PortTo)
	}
	if (r.RemoteCIDR == "") == (r.RemoteSecurityGroupID == "") {
		return fmt.Errorf("exactly one of remote_cidr and remote_security_group_id must be set")
	}
	return nil
}

// ---- API helpers --------------------------------------------------------------

func isNotFound(err error) bool {
	var he *client.HTTPError
	return errors.As(err, &he) && he.IsNotFound()
}

func isRevisionConflict(err error) bool {
	var he *client.HTTPError
	return errors.As(err, &he) && he.StatusCode == 409 && strings.Contains(he.Body, "REVISION_CONFLICT")
}

func getGroup(ctx context.Context, c *client.Client, id string) (*Group, error) {
	var g Group
	if err := c.Do(ctx, "GET", groupPath(id), nil, nil, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

func listGroups(ctx context.Context, c *client.Client, projectID string) ([]Group, error) {
	var gs []Group
	if err := c.Do(ctx, "GET", groupsPath, map[string]string{"project_id": projectID}, nil, &gs); err != nil {
		return nil, err
	}
	return gs, nil
}

// updateGroup reads the current revision, applies mutate and PUTs the result;
// it retries once on REVISION_CONFLICT (someone else changed the group).
func updateGroup(ctx context.Context, c *client.Client, id string, mutate func(g *Group) UpdateGroupRequest) (*Group, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		cur, err := getGroup(ctx, c, id)
		if err != nil {
			return nil, err
		}
		req := mutate(cur)
		req.Revision = cur.Revision
		if req.Rules == nil {
			req.Rules = []Rule{}
		}
		var out Group
		lastErr = c.Do(ctx, "PUT", groupPath(id), nil, req, &out)
		if lastErr == nil {
			return &out, nil
		}
		if !isRevisionConflict(lastErr) {
			return nil, lastErr
		}
	}
	return nil, lastErr
}

// keepIDs gives desired rules the ids of equal current rules, so that an
// update does not churn rule ids (h3_security_group_rule tracks them).
func keepIDs(desired, current []Rule) []Rule {
	used := make([]bool, len(current))
	out := make([]Rule, len(desired))
	for i, d := range desired {
		d.ID = ""
		for j, c := range current {
			if !used[j] && sameRule(d, c) {
				d.ID = c.ID
				used[j] = true
				break
			}
		}
		out[i] = d
	}
	return out
}
