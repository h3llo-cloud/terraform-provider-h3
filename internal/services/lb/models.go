// Package lb implements h3_lb and its data sources on the h3lb API
// (/api/loadbalancers/v1, camelCase). The resource schema is shaped so that it
// also maps to the OVN-native LB Basic (loadBalancerClass h3.io/ovn, EIP bind):
// listeners with a protocol, a target set and eip_id.
package lb

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"

	"h3terraform/internal/client"
)

const apiBase = "/api/loadbalancers/v1"

// Port is one frontend -> backend port mapping.
type Port struct {
	Frontend int64 `json:"frontend"`
	Backend  int64 `json:"backend"`
}

// Config is the balancing configuration.
type Config struct {
	Algorithm string   `json:"algorithm,omitempty"`
	Whitelist []string `json:"whitelist,omitempty"`
}

// CreateRequest is CreateLBDTO.
type CreateRequest struct {
	ProjectID string   `json:"projectId"`
	Name      string   `json:"name"`
	Ports     []Port   `json:"ports"`
	Backends  []string `json:"backends"`
	Config    *Config  `json:"config,omitempty"`
	// EipID (LB Basic): a free project EIP for the VIP; empty = h3ovn allocates one.
	EipID string `json:"eipId,omitempty"`
	// WhiteIP false = internal LB (address inside the VPC only, no EIP); nil = public (h3lb default).
	WhiteIP *bool `json:"whiteIp,omitempty"`
}

// UpdateEIPRequest is the UpdateLBDTO subset that moves an LB Basic to another EIP in place.
type UpdateEIPRequest struct {
	EipID string `json:"eipId"`
}

// UpdatePublicIPRequest is the UpdateLBDTO subset that turns the public IP on (eipId or a new
// EIP) or off (the EIP is detached and stays in the project).
type UpdatePublicIPRequest struct {
	WhiteIP bool   `json:"whiteIp"`
	EipID   string `json:"eipId,omitempty"`
}

// LoadBalancer is the h3lb response.
type LoadBalancer struct {
	ID         string   `json:"id"`
	ProjectID  string   `json:"projectId"`
	UserID     string   `json:"userId"`
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	ExternalIP string   `json:"externalIp"`
	Ports      []Port   `json:"ports"`
	Backends   []string `json:"backends"`
	Endpoint   string   `json:"endpoint"`
	SubnetName string   `json:"subnetName"`
	Config     *Config  `json:"config,omitempty"`
	// Dataplane: "ovn" (LB Basic, VIP = project EIP) or "lb-svc" (legacy); empty on older h3lb.
	Dataplane string `json:"dataplane,omitempty"`
	// EipID: the project EIP of an LB Basic; empty for lb-svc LBs and older h3lb.
	EipID string `json:"eipId,omitempty"`
	// Internal: no public IP (whiteIp=false); absent on older h3lb = public.
	Internal bool `json:"internal,omitempty"`
	// VpcIP: address of the LB inside its VPC (internal LBs; kept when a public IP is attached).
	VpcIP string `json:"vpcIp,omitempty"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
var digitsRe = regexp.MustCompile(`^[0-9]+$`)

// ValidName checks the h3lb name rule (k8s DNS label, not all digits).
func ValidName(s string) error {
	if !nameRe.MatchString(s) || digitsRe.MatchString(s) {
		return fmt.Errorf("must be a DNS label: 1-63 lowercase letters, digits and '-', starting and ending with a letter or digit, not all digits; got %q", s)
	}
	return nil
}

var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
}

// ValidTarget checks that a target is a private IPv4 address, as h3lb requires.
func ValidTarget(s string) error {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() {
		return fmt.Errorf("must be an IPv4 address, got %q", s)
	}
	for _, p := range privateRanges {
		if p.Contains(a) {
			return nil
		}
	}
	return fmt.Errorf("%s is not in a private range (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10)", s)
}

// explain turns access errors into an actionable message: h3lb has no API-key
// access on api.h3llo.cloud yet.
func explain(err error) string {
	var h *client.HTTPError
	if errors.As(err, &h) {
		switch h.StatusCode {
		case 401, 403:
			return fmt.Sprintf("the load balancer API rejected the API key (HTTP %d). As of provider 0.3.0 h3lb accepts only console sessions; "+
				"API-key (HMAC) access on api.h3llo.cloud is pending on the platform side. Details: %s", h.StatusCode, err)
		}
	}
	return err.Error()
}
