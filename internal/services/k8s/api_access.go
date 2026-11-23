package k8s

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const maxAllowedCIDRs = 50

// ValidateAllowedCIDRs accepts IPv4 network addresses with a prefix exactly as
// h3k8s returns them (normalized), so the plan never shows a perpetual diff.
func ValidateAllowedCIDRs(cidrs []string) error {
	if len(cidrs) > maxAllowedCIDRs {
		return fmt.Errorf("at most %d entries, got %d", maxAllowedCIDRs, len(cidrs))
	}
	for _, s := range cidrs {
		p, err := netip.ParsePrefix(s)
		if err != nil || !p.Addr().Is4() {
			return fmt.Errorf("%q: expected an IPv4 network with a prefix, e.g. 203.0.113.0/24 or 198.51.100.7/32", s)
		}
		if p.Masked() != p {
			return fmt.Errorf("%q: host bits are set, use %s", s, p.Masked())
		}
		if p.Bits() == 0 {
			return fmt.Errorf("%q: use [] to open the API to everyone", s)
		}
	}
	return nil
}

// knownCIDRs returns the configured list when it is known (null/unknown = not managed).
func knownCIDRs(ctx context.Context, v types.Set) ([]string, bool) {
	if v.IsNull() || v.IsUnknown() {
		return nil, false
	}
	out := []string{}
	if diags := v.ElementsAs(ctx, &out, false); diags.HasError() {
		return nil, false
	}
	return out, true
}

// allowedCIDRsValue is the API value as a set; an h3k8s without the feature
// (no apiAccess in the response) keeps prev, a null prev becomes an empty set.
func allowedCIDRsValue(c *Cluster, prev types.Set) types.Set {
	if c.APIAccess == nil {
		if prev.IsUnknown() {
			return types.SetNull(types.StringType)
		}
		return prev
	}
	elems := make([]attr.Value, 0, len(c.APIAccess.AllowedCIDRs))
	for _, s := range c.APIAccess.AllowedCIDRs {
		elems = append(elems, types.StringValue(s))
	}
	return types.SetValueMust(types.StringType, elems)
}
