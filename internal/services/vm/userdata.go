package vm

import (
	"encoding/base64"
	"fmt"
	"strings"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// userDataMax is the h3vm limit of the decoded user-data (64 KiB).
const userDataMax = 64 << 10

// validateUserData checks at plan time what h3vm checks on create: size,
// encoding and the first line. The value itself never goes into a message.
func validateUserData(ud, enc types.String) diag.Diagnostics {
	var d diag.Diagnostics
	if ud.IsNull() || ud.IsUnknown() || enc.IsUnknown() {
		return d
	}
	encoding := enc.ValueString()
	if !enc.IsNull() && encoding != "plain" && encoding != "base64" {
		d.AddAttributeError(path.Root("user_data_encoding"), "Invalid user_data_encoding",
			fmt.Sprintf("expected plain or base64, got %q", encoding))
		return d
	}
	text := ud.ValueString()
	if encoding == "base64" {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
		if err != nil {
			d.AddAttributeError(path.Root("user_data"), "Invalid user_data", "user_data is not valid base64 (user_data_encoding = \"base64\").")
			return d
		}
		text = string(b)
	}
	if len(text) == 0 {
		d.AddAttributeError(path.Root("user_data"), "Invalid user_data", "user_data is empty; remove the argument instead.")
		return d
	}
	if len(text) > userDataMax {
		d.AddAttributeError(path.Root("user_data"), "user_data is too large",
			fmt.Sprintf("user_data is %d bytes after decoding, the limit is %d (64 KiB).", len(text), userDataMax))
		return d
	}
	if len(text) >= 2 && text[0] == 0x1f && text[1] == 0x8b {
		d.AddAttributeError(path.Root("user_data"), "Invalid user_data",
			"gzip-compressed user-data is not accepted (e.g. cloudinit_config with gzip = true): send it uncompressed.")
		return d
	}
	first := strings.TrimRight(strings.SplitN(text, "\n", 2)[0], "\r ")
	if !strings.HasPrefix(first, "#cloud-config") && !strings.HasPrefix(first, "#!") && !strings.HasPrefix(first, "#cloud-boothook") {
		d.AddAttributeError(path.Root("user_data"), "Invalid user_data",
			"The first line of user_data must be #cloud-config, #! (a script) or #cloud-boothook. MIME multipart and #include are not accepted.")
	}
	return d
}

// isDeletionProtected: 409 VM_DELETION_PROTECTED on DELETE.
func isDeletionProtected(err error) bool {
	e, ok := client.AsHTTPError(err)
	return ok && e.StatusCode == 409 && (e.Code() == "VM_DELETION_PROTECTED" || strings.Contains(e.Body, "VM_DELETION_PROTECTED"))
}
