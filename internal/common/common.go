// Package common holds helpers shared by all resources: provider data, mapping
// API values to state, quantity normalization and polling.
package common

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ClientFrom extracts the API client from provider data. It returns nil
// (without diagnostics) before the provider is configured.
func ClientFrom(providerData any, diags *diag.Diagnostics) *client.Client {
	if providerData == nil {
		return nil
	}
	c, ok := providerData.(*client.Client)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T; please report this issue to the provider developers", providerData))
		return nil
	}
	return c
}

// OptString maps "" to null, any other value to a known string.
func OptString(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}

// KnownString returns the value, or "" when null or unknown.
func KnownString(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

// KnownInt64 returns the value, or 0 when null or unknown.
func KnownInt64(v types.Int64) int64 {
	if v.IsNull() || v.IsUnknown() {
		return 0
	}
	return v.ValueInt64()
}

var quantityRe = regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)?)\s*(Ki|Mi|Gi|Ti|Pi|K|M|G|T|P)?\s*$`)

var quantityUnits = map[string]int64{
	"": 1, "Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40, "Pi": 1 << 50,
	"K": 1e3, "M": 1e6, "G": 1e9, "T": 1e12, "P": 1e15,
}

// QuantityBytes parses a Kubernetes-style quantity ("4Gi", "4096Mi", "20G").
func QuantityBytes(s string) (*big.Rat, bool) {
	m := quantityRe.FindStringSubmatch(s)
	if m == nil {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(m[1])
	if !ok {
		return nil, false
	}
	return r.Mul(r, new(big.Rat).SetInt64(quantityUnits[m[2]])), true
}

// QuantityEqual reports whether two quantities denote the same amount.
func QuantityEqual(a, b string) bool {
	if strings.TrimSpace(a) == strings.TrimSpace(b) {
		return true
	}
	x, ok1 := QuantityBytes(a)
	y, ok2 := QuantityBytes(b)
	return ok1 && ok2 && x.Cmp(y) == 0
}

// QuantityCmp compares two quantities; ok is false when one does not parse.
func QuantityCmp(a, b string) (int, bool) {
	x, ok1 := QuantityBytes(a)
	y, ok2 := QuantityBytes(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	return x.Cmp(y), true
}

// Quantity maps an API quantity to state, keeping the prior (configured)
// spelling when it denotes the same amount, so "4Gi" in the configuration
// and "4096Mi" from the API do not show up as drift.
func Quantity(prior types.String, api string) types.String {
	if api == "" {
		if prior.IsUnknown() {
			return types.StringNull()
		}
		return prior
	}
	if !prior.IsNull() && !prior.IsUnknown() && QuantityEqual(prior.ValueString(), api) {
		return prior
	}
	return types.StringValue(api)
}

// PollInterval is the delay between readiness checks; tests shorten it.
var PollInterval = 5 * time.Second

// ErrTimeout is returned by WaitFor when the timeout expires.
var ErrTimeout = errors.New("timeout")

// WaitFor calls check immediately and then every PollInterval until it
// reports done, returns an error, or the timeout expires. A failed GET is
// retried by the API client itself, so check errors are final.
func WaitFor(ctx context.Context, timeout time.Duration, what string, check func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		done, err := check(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%w after %s waiting for %s", ErrTimeout, timeout, what)
			}
			return fmt.Errorf("waiting for %s: %w", what, ctx.Err())
		case <-time.After(PollInterval):
		}
	}
}

// ImportProjectScoped parses "<project_id>/<id>" import ids, used where the API
// needs project_id to read an object (snapshots, backups, buckets).
func ImportProjectScoped(raw, example string) (projectID, id string, err error) {
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected import id \"<project_id>/%s\", got %q", example, raw)
	}
	return parts[0], parts[1], nil
}
