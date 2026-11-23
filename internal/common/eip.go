package common

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// UUIDValidator rejects values that are not a canonical UUID.
func UUIDValidator() validator.String { return uuidValidator{} }

type uuidValidator struct{}

func (uuidValidator) Description(context.Context) string { return "value must be a UUID" }
func (v uuidValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (uuidValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !uuidRe.MatchString(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid UUID",
			fmt.Sprintf("%q is not a valid UUID", req.ConfigValue.ValueString()))
	}
}

// EIPErrorDiag maps platform Elastic IP errors to a diagnostic summary and
// detail: 409 EIP_ALREADY_BOUND (selected EIP taken) and 403 EIP_DENIED
// (no new EIP allowed: quota or balance). Anything else is "Error <action>".
func EIPErrorDiag(action string, err error) (string, string) {
	if h, ok := client.AsHTTPError(err); ok {
		switch {
		case h.IsEIPAlreadyBound():
			return "Elastic IP already in use",
				"This IP address is already in use (attached to or reserved by another resource), choose another eip_id. " + err.Error()
		case h.IsEIPDenied():
			return "Elastic IP not allowed",
				"The project cannot lease a new Elastic IP (EIP quota reached or insufficient balance). " +
					"Select a free project EIP with eip_id, release unused EIPs, or top up the balance. " + err.Error()
		}
	}
	return "Error " + action, err.Error()
}

// EIPDescriptionValidator mirrors the h3ovn rules for an EIP comment: at most
// 255 characters, one line without control characters. Leading or trailing
// spaces are rejected too: h3ovn trims them, which would show as a diff on
// every plan.
func EIPDescriptionValidator() validator.String { return eipDescriptionValidator{} }

type eipDescriptionValidator struct{}

func (eipDescriptionValidator) Description(context.Context) string {
	return "up to 255 characters, one line without control characters, no leading or trailing spaces"
}
func (v eipDescriptionValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (eipDescriptionValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if msg := CheckEIPDescription(req.ConfigValue.ValueString()); msg != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid EIP description", msg)
	}
}

// CheckEIPDescription returns why s is not a valid EIP description ("" if valid).
func CheckEIPDescription(s string) string {
	if !utf8.ValidString(s) {
		return "must be valid UTF-8"
	}
	if utf8.RuneCountInString(s) > 255 {
		return "must be at most 255 characters"
	}
	if strings.TrimSpace(s) != s {
		return "must not start or end with spaces"
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "must be one line without control characters (newline, tab)"
		}
	}
	return ""
}
