package database

import (
	"context"
	"fmt"
	"strings"

	"h3terraform/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// Storage autoscale (h3db feature flag H3DB_STORAGE_AUTOSCALE_ENABLED):
// GET/PUT /api/databases/v1/{id}/storage-autoscale and the optional
// storageAutoscale field of the create request.
const (
	autoscaleDisabledCode = "STORAGE_AUTOSCALE_DISABLED"

	defaultThresholdPercent = 85
	defaultStepGB           = 10
	defaultStepPercent      = 20
	minThresholdPercent     = 60
	maxThresholdPercent     = 95
	minStepGB               = 5
	maxStepGB               = 1024
	minStepPercent          = 5
	maxStepPercent          = 100
)

// StorageAutoscaleRequest is the PUT body and the storageAutoscale field of
// the create request. Omitted fields keep the current value on PUT.
type StorageAutoscaleRequest struct {
	Enabled          bool   `json:"enabled"`
	ThresholdPercent *int64 `json:"thresholdPercent,omitempty"`
	StepGB           *int64 `json:"stepGb,omitempty"`
	StepPercent      *int64 `json:"stepPercent,omitempty"`
	MaxSizeGB        *int64 `json:"maxSizeGb,omitempty"`
}

// StorageAutoscale is GET /api/databases/v1/{id}/storage-autoscale (the
// fields the provider uses).
type StorageAutoscale struct {
	Enabled          bool             `json:"enabled"`
	ThresholdPercent int64            `json:"thresholdPercent"`
	StepGB           int64            `json:"stepGb"`
	StepPercent      int64            `json:"stepPercent"`
	MaxSizeGB        int64            `json:"maxSizeGb"`
	State            string           `json:"state"`
	AtMax            bool             `json:"atMax"`
	LastEvent        *AutoscaleEvent  `json:"lastEvent,omitempty"`
	Events           []AutoscaleEvent `json:"events,omitempty"`
}

type AutoscaleEvent struct {
	Result     string `json:"result"`
	FromSizeGB int64  `json:"fromSizeGb"`
	ToSizeGB   int64  `json:"toSizeGb"`
}

// grew reports that the platform has grown the disk automatically at least
// once (among the events it still reports).
func (a *StorageAutoscale) grew() bool {
	if a.LastEvent != nil && a.LastEvent.Result == "GROWN" {
		return true
	}
	for _, e := range a.Events {
		if e.Result == "GROWN" {
			return true
		}
	}
	return false
}

// StorageAutoscaleModel is the storage_autoscale attribute.
type StorageAutoscaleModel struct {
	Enabled          types.Bool   `tfsdk:"enabled"`
	ThresholdPercent types.Int64  `tfsdk:"threshold_percent"`
	StepGB           types.Int64  `tfsdk:"step_gb"`
	StepPercent      types.Int64  `tfsdk:"step_percent"`
	MaxSizeGB        types.Int64  `tfsdk:"max_size_gb"`
	AtMax            types.Bool   `tfsdk:"at_max"`
	State            types.String `tfsdk:"state"`
}

// keepAutoscaleValue: an omitted value keeps the current one (h3db PUT keeps
// omitted fields; the first enable takes 85 / 10 / 20).
var keepAutoscaleValue = []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}

var autoscaleAttrTypes = map[string]attr.Type{
	"enabled":           types.BoolType,
	"threshold_percent": types.Int64Type,
	"step_gb":           types.Int64Type,
	"step_percent":      types.Int64Type,
	"max_size_gb":       types.Int64Type,
	"at_max":            types.BoolType,
	"state":             types.StringType,
}

// storageAutoscaleAttribute: Optional + Computed. Not set: h3db applies its
// default on create (enabled, max_size_gb = 2 × size, at most 1024; owner
// decision 2026-10-20) and the state shows what the platform has; the
// provider never changes it. Set: managed, changed in place with PUT.
// Removing it from the configuration keeps the last value (it does not turn
// autoscale off): to turn it off, set enabled = false.
func storageAutoscaleAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Optional: true, Computed: true,
		MarkdownDescription: "Automatic growth of `size`. When the used space reaches `threshold_percent`, h3db grows the disk " +
			"by max(`step_gb`, `step_percent` of the size, 5 GiB) up to `max_size_gb` (at most once per 6 hours; each step is " +
			"billed as a normal resize). **Not set:** a new database gets the platform default (enabled, `max_size_gb` = 2 × " +
			"`size`, at most 1024) and the state shows the values h3db reports; the provider does not change them " +
			"(a value set in the console is kept). **Set:** the provider manages it, changed in place " +
			"(`PUT /storage-autoscale`, no restart). Removing it from the configuration keeps the current setting; " +
			"to turn autoscale off set `enabled = false`. While autoscale is on, a disk that has grown beyond `size` is not " +
			"a diff: `size` stays the configured value and `current_size` shows the real one.",
		PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
		Attributes: map[string]schema.Attribute{
			// Optional + Computed (required by ValidateConfig): Terraform
			// keeps the prior value of an omitted Optional + Computed object
			// only when every nested attribute is computed.
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Turn automatic storage growth on or off (required when `storage_autoscale` is set)",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"threshold_percent": schema.Int64Attribute{
				Optional: true, Computed: true, PlanModifiers: keepAutoscaleValue,
				MarkdownDescription: "Used space, % of the disk, that triggers a grow (60-95; not set: the current value, 85 on the first enable)",
			},
			"step_gb": schema.Int64Attribute{
				Optional: true, Computed: true, PlanModifiers: keepAutoscaleValue,
				MarkdownDescription: "Minimum growth per step, GiB (5-1024; not set: the current value, 10 on the first enable)",
			},
			"step_percent": schema.Int64Attribute{
				Optional: true, Computed: true, PlanModifiers: keepAutoscaleValue,
				MarkdownDescription: "Growth per step, % of the current size, when larger than `step_gb` (0 = off, or 5-100; not set: the current value, 20 on the first enable)",
			},
			"max_size_gb": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Upper bound of automatic growth, GiB: at least the current size and at most 1024. " +
					"Not set: the current value, or 2 × the size (at most 1024) when there is none.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"at_max": schema.BoolAttribute{
				Computed: true,
				MarkdownDescription: "True when the disk is at `max_size_gb` and still above the threshold " +
					"(autoscale cannot grow it any more)",
			},
			"state": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`DISABLED`, `WATCHING`, `GROWING`, `AT_MAX` or `BLOCKED` (empty when the platform does not report it)",
			},
		},
	}
}

// autoscaleFromObject converts the attribute; nil when null or unknown.
func autoscaleFromObject(ctx context.Context, obj types.Object) (*StorageAutoscaleModel, diag.Diagnostics) {
	if obj.IsNull() || obj.IsUnknown() {
		return nil, nil
	}
	var m StorageAutoscaleModel
	diags := obj.As(ctx, &m, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return nil, diags
	}
	return &m, nil
}

// autoscaleObject converts back; nil is null.
func autoscaleObject(ctx context.Context, m *StorageAutoscaleModel) (types.Object, diag.Diagnostics) {
	if m == nil {
		return types.ObjectNull(autoscaleAttrTypes), nil
	}
	return types.ObjectValueFrom(ctx, autoscaleAttrTypes, m)
}

// defaultMaxSize is the max_size_gb h3db takes when none is given: 2 × size,
// at most the platform maximum.
func defaultMaxSize(sizeGi int64) int64 {
	if 2*sizeGi > maxSizeGi {
		return maxSizeGi
	}
	return 2 * sizeGi
}

// fieldError is a validation error on a storage_autoscale attribute.
type fieldError struct {
	attr, msg string
}

// validateAutoscale checks the block against the h3db bounds. sizeGi is the
// configured size (0 when unknown).
func validateAutoscale(m StorageAutoscaleModel, sizeGi int64) []fieldError {
	var errs []fieldError
	if m.Enabled.IsNull() {
		errs = append(errs, fieldError{"enabled", "enabled is required when storage_autoscale is set"})
	}
	if v := m.ThresholdPercent; known(v) && (v.ValueInt64() < minThresholdPercent || v.ValueInt64() > maxThresholdPercent) {
		errs = append(errs, fieldError{"threshold_percent", fmt.Sprintf("threshold_percent %d must be between %d and %d", v.ValueInt64(), minThresholdPercent, maxThresholdPercent)})
	}
	if v := m.StepGB; known(v) && (v.ValueInt64() < minStepGB || v.ValueInt64() > maxStepGB) {
		errs = append(errs, fieldError{"step_gb", fmt.Sprintf("step_gb %d must be between %d and %d", v.ValueInt64(), minStepGB, maxStepGB)})
	}
	if v := m.StepPercent; known(v) && v.ValueInt64() != 0 && (v.ValueInt64() < minStepPercent || v.ValueInt64() > maxStepPercent) {
		errs = append(errs, fieldError{"step_percent", fmt.Sprintf("step_percent %d must be 0 (off) or between %d and %d", v.ValueInt64(), minStepPercent, maxStepPercent)})
	}
	enabled := known(m.Enabled) && m.Enabled.ValueBool()
	if v := m.MaxSizeGB; known(v) {
		switch n := v.ValueInt64(); {
		case n > maxSizeGi:
			errs = append(errs, fieldError{"max_size_gb", fmt.Sprintf("max_size_gb %d is above the maximum of %d", n, maxSizeGi)})
		case n < 1:
			errs = append(errs, fieldError{"max_size_gb", "max_size_gb must be positive"})
		case enabled && sizeGi > 0 && n < sizeGi:
			errs = append(errs, fieldError{"max_size_gb", fmt.Sprintf("max_size_gb %d is below size (%dGi): autoscale only grows the disk", n, sizeGi)})
		}
	}
	return errs
}

type knowable interface {
	IsNull() bool
	IsUnknown() bool
}

func known(v knowable) bool { return !v.IsNull() && !v.IsUnknown() }

func optInt(v types.Int64) *int64 {
	if !known(v) {
		return nil
	}
	n := v.ValueInt64()
	return &n
}

// autoscaleRequest builds the PUT/create body from the attribute.
func autoscaleRequest(m *StorageAutoscaleModel) StorageAutoscaleRequest {
	return StorageAutoscaleRequest{
		Enabled:          m.Enabled.ValueBool(),
		ThresholdPercent: optInt(m.ThresholdPercent),
		StepGB:           optInt(m.StepGB),
		StepPercent:      optInt(m.StepPercent),
		MaxSizeGB:        optInt(m.MaxSizeGB),
	}
}

// createAutoscale is the storageAutoscale field of the create request: sent
// only when the attribute is set (enabled = false too, so h3db does not apply
// its default). Not set: no field, h3db applies the default.
func createAutoscale(m *StorageAutoscaleModel) *StorageAutoscaleRequest {
	if m == nil {
		return nil
	}
	req := autoscaleRequest(m)
	return &req
}

// autoscaleFromAPI maps the API answer to the attribute.
func autoscaleFromAPI(a *StorageAutoscale) *StorageAutoscaleModel {
	m := &StorageAutoscaleModel{
		Enabled:          types.BoolValue(a.Enabled),
		ThresholdPercent: types.Int64Value(a.ThresholdPercent),
		StepGB:           types.Int64Value(a.StepGB),
		StepPercent:      types.Int64Value(a.StepPercent),
		MaxSizeGB:        types.Int64Null(),
		AtMax:            types.BoolValue(a.AtMax),
		State:            types.StringValue(a.State),
	}
	if a.MaxSizeGB > 0 {
		m.MaxSizeGB = types.Int64Value(a.MaxSizeGB)
	}
	return m
}

// fillAutoscaleComputed resolves unknown computed values of a configured
// attribute that could not be read back (feature disabled), so the state has
// no unknowns.
func fillAutoscaleComputed(m *StorageAutoscaleModel) *StorageAutoscaleModel {
	if m == nil {
		return nil
	}
	if m.AtMax.IsUnknown() {
		m.AtMax = types.BoolValue(false)
	}
	if m.State.IsUnknown() {
		m.State = types.StringValue("")
	}
	for _, v := range []*types.Int64{&m.MaxSizeGB, &m.ThresholdPercent, &m.StepGB, &m.StepPercent} {
		if v.IsUnknown() {
			*v = types.Int64Null()
		}
	}
	if m.Enabled.IsUnknown() {
		m.Enabled = types.BoolNull()
	}
	return m
}

// keepConfiguredSize decides the size to store in state. prior is the size
// in state (or the planned size on create/update), remote the API sizeGb.
// The configured value is kept when the disk is larger than it and storage
// autoscale is (or was) growing it, so an automatic grow is neither a diff
// nor a shrink attempt. Without autoscale the real size is stored and a
// larger disk shows up as a plan error (storage cannot be decreased), as
// before.
func keepConfiguredSize(prior types.String, remote string, autoscaleGrows bool) types.String {
	if !known(prior) || !autoscaleGrows {
		return types.StringValue(remote)
	}
	p, err1 := parseGi(prior.ValueString())
	r, err2 := parseGi(remote)
	if err1 != nil || err2 != nil || r < p {
		return types.StringValue(remote)
	}
	return prior
}

// autoscaleGrows reports whether a disk larger than the configured size is
// expected: autoscale is on, has grown the disk before, or (the setting could
// not be read) the value in state enables it.
func autoscaleGrows(a *StorageAutoscale, m *StorageAutoscaleModel) bool {
	if a != nil {
		return a.Enabled || a.grew()
	}
	return m != nil && m.Enabled.ValueBool()
}

func isAutoscaleDisabled(err error) bool {
	he, ok := client.AsHTTPError(err)
	return ok && (he.Code() == autoscaleDisabledCode ||
		(he.StatusCode == 400 && containsField(he.Body, "storageAutoscale")))
}

func containsField(body, field string) bool {
	return strings.Contains(body, `"field":"`+field+`"`) || strings.Contains(body, `"field": "`+field+`"`)
}

// getAutoscale reads the setting; (nil, nil) when the platform does not
// provide it (503 STORAGE_AUTOSCALE_DISABLED, or 404 from an older h3db).
func (r *DatabaseResource) getAutoscale(ctx context.Context, id string) (*StorageAutoscale, error) {
	var a StorageAutoscale
	err := r.client.Do(ctx, "GET", basePath+"/"+id+"/storage-autoscale", nil, nil, &a)
	if err == nil {
		return &a, nil
	}
	if isAutoscaleDisabled(err) || isNotFound(err) {
		return nil, nil
	}
	return nil, err
}

// putAutoscale applies the attribute.
func (r *DatabaseResource) putAutoscale(ctx context.Context, id string, m *StorageAutoscaleModel) (*StorageAutoscale, error) {
	var a StorageAutoscale
	err := r.client.Do(ctx, "PUT", basePath+"/"+id+"/storage-autoscale", nil, autoscaleRequest(m), &a)
	if err == nil {
		return &a, nil
	}
	if isAutoscaleDisabled(err) || isNotFound(err) {
		if !m.Enabled.ValueBool() {
			return nil, nil // turning off what the platform does not have
		}
		return nil, autoscaleUnavailable(err)
	}
	return nil, err
}

func autoscaleUnavailable(err error) error {
	return fmt.Errorf("storage autoscale is not available on this platform (h3db answers %w); "+
		"leave storage_autoscale unset or set enabled = false until it is rolled out", err)
}

// readAutoscale sets the attribute from the API, whether it is configured or
// not (computed: an omitted attribute shows the platform value, and its plan
// keeps the state, so a change in the console is no diff). A platform without
// the feature keeps a configured value and leaves an omitted one null.
// Returns the setting (nil when unavailable) for the size drift decision.
func (r *DatabaseResource) readAutoscale(ctx context.Context, m *DatabaseResourceModel, warn func(string, string)) (*StorageAutoscale, diag.Diagnostics) {
	cur, diags := autoscaleFromObject(ctx, m.StorageAutoscale)
	if diags.HasError() {
		return nil, diags
	}
	a, err := r.getAutoscale(ctx, m.ID.ValueString())
	if err != nil {
		if cur != nil {
			warn("Could not read storage autoscale", err.Error())
		}
	}
	next := fillAutoscaleComputed(cur)
	if a != nil {
		next = autoscaleFromAPI(a)
	}
	m.StorageAutoscale, diags = autoscaleObject(ctx, next)
	return a, diags
}

// planMaxSize is the max_size_gb to plan for a configured, enabled attribute
// without one and without a value in state: 2 × the larger of the configured
// and the current size, at most 1024 (what h3db takes on create).
func planMaxSize(size, current types.String) types.Int64 {
	var gi int64
	for _, v := range []types.String{size, current} {
		if known(v) {
			if n, err := parseGi(v.ValueString()); err == nil && n > gi {
				gi = n
			}
		}
	}
	if gi == 0 {
		return types.Int64Unknown()
	}
	return types.Int64Value(defaultMaxSize(gi))
}

// autoscaleChanged reports a change of the configurable values (computed
// at_max/state are ignored). An attribute removed from the configuration keeps
// the state (UseStateForUnknown), so it is no change and sends nothing.
func autoscaleChanged(plan, state *StorageAutoscaleModel) bool {
	if plan == nil {
		return false
	}
	if state == nil {
		return true
	}
	return !plan.Enabled.Equal(state.Enabled) || !plan.ThresholdPercent.Equal(state.ThresholdPercent) ||
		!plan.StepGB.Equal(state.StepGB) || !plan.StepPercent.Equal(state.StepPercent) ||
		(known(plan.MaxSizeGB) && !plan.MaxSizeGB.Equal(state.MaxSizeGB))
}

// checkMaxSize refuses at plan time a max_size_gb below the size the disk
// already has (h3db would answer 400 INVALID_AUTOSCALE).
func checkMaxSize(max types.Int64, current types.String) error {
	if !known(max) || !known(current) {
		return nil
	}
	cur, err := parseGi(current.ValueString())
	if err != nil {
		return nil
	}
	if max.ValueInt64() < cur {
		return fmt.Errorf("max_size_gb %d is below the current size of the disk (%s); autoscale never shrinks it", max.ValueInt64(), current.ValueString())
	}
	return nil
}

// autoscalePath is the path of a storage_autoscale nested attribute for
// diagnostics and plan changes.
func autoscalePath(attr string) path.Path {
	return path.Root("storage_autoscale").AtName(attr)
}
