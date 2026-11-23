package common

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// Renamed resources (0.3.0): the implementation-specific "ovn" was dropped from
// the type names. The old name stays registered as a deprecated alias with the
// same schema, and the new type accepts `moved {}` from the old one
// (Terraform >= 1.8), so state migrates without destroy/create.

// Rename is a resource type rename, as type name suffixes after "h3_".
type Rename struct {
	New string // e.g. "vpc"
	Old string // e.g. "ovn_vpc"
}

// Renames is the list documented in docs/guides/migration-0.3.md.
var (
	RenameVPC     = Rename{New: "vpc", Old: "ovn_vpc"}
	RenameNetwork = Rename{New: "network", Old: "ovn_network"}
	RenameEIP     = Rename{New: "eip", Old: "ovn_eip"}
)

// TypeName returns the resource type name for the new name or the alias.
func (r Rename) TypeName(provider string, alias bool) string {
	if alias {
		return provider + "_" + r.Old
	}
	return provider + "_" + r.New
}

// Deprecate marks the schema of the alias as deprecated.
func (r Rename) Deprecate(s *schema.Schema, alias bool) {
	if !alias {
		return
	}
	s.DeprecationMessage = fmt.Sprintf(
		"h3_%[1]s is renamed to h3_%[2]s and will be removed in 1.0. Rename the resource in the configuration and add "+
			"`moved { from = h3_%[1]s.<name>  to = h3_%[2]s.<name> }` (Terraform >= 1.8); see the 0.3 migration guide.",
		r.Old, r.New)
	s.MarkdownDescription = fmt.Sprintf("**Deprecated:** use `h3_%s`; this alias is removed in 1.0. ", r.New) + s.MarkdownDescription
}

// MoveFromOld returns the state mover that accepts `moved {}` from the old
// type name of this provider. Both types share one schema, so the state is
// taken over as is. sourceSchema is the schema of the alias.
func (r Rename) MoveFromOld(sourceSchema schema.Schema) []resource.StateMover {
	return []resource.StateMover{{
		SourceSchema: &sourceSchema,
		StateMover: func(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
			if req.SourceTypeName != "h3_"+r.Old || !IsThisProvider(req.SourceProviderAddress) {
				return // not ours: the framework tries the next mover or reports it unsupported
			}
			if req.SourceSchemaVersion != sourceSchema.Version {
				resp.Diagnostics.AddError("Unsupported state version",
					fmt.Sprintf("h3_%s state version %d cannot be moved; upgrade the provider and run `terraform apply` first", r.Old, req.SourceSchemaVersion))
				return
			}
			if req.SourceState == nil {
				resp.Diagnostics.AddError("Cannot move state", "source state could not be decoded")
				return
			}
			resp.TargetState.Raw = req.SourceState.Raw.Copy()
		},
	}}
}

// IsThisProvider reports whether a provider source address
// ("registry.terraform.io/h3llo-cloud/h3", a mirror, a dev override) is this provider.
func IsThisProvider(addr string) bool {
	return addr == "h3" || strings.HasSuffix(addr, "/h3")
}
