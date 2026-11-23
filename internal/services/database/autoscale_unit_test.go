package database

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestKeepConfiguredSize(t *testing.T) {
	cases := []struct {
		prior  types.String
		remote string
		grows  bool
		want   string
	}{
		{types.StringValue("20Gi"), "30Gi", true, "20Gi"},  // grown by autoscale: no diff
		{types.StringValue("20Gi"), "20Gi", true, "20Gi"},  // unchanged
		{types.StringValue("20Gi"), "30Gi", false, "30Gi"}, // manual grow without autoscale: as before
		{types.StringValue("40Gi"), "30Gi", true, "30Gi"},  // smaller disk is never hidden
		{types.StringNull(), "30Gi", true, "30Gi"},         // import
		{types.StringUnknown(), "30Gi", true, "30Gi"},
	}
	for _, c := range cases {
		if got := keepConfiguredSize(c.prior, c.remote, c.grows).ValueString(); got != c.want {
			t.Errorf("keepConfiguredSize(%s, %s, %v) = %s, want %s", c.prior, c.remote, c.grows, got, c.want)
		}
	}
}

func TestAutoscaleGrows(t *testing.T) {
	on := &StorageAutoscaleModel{Enabled: types.BoolValue(true)}
	if !autoscaleGrows(&StorageAutoscale{Enabled: true}, nil) {
		t.Error("enabled on the platform (also set in the console)")
	}
	if !autoscaleGrows(&StorageAutoscale{LastEvent: &AutoscaleEvent{Result: "GROWN"}}, nil) {
		t.Error("disabled after an automatic grow")
	}
	if autoscaleGrows(&StorageAutoscale{Events: []AutoscaleEvent{{Result: "DENIED_BALANCE"}}}, on) {
		t.Error("the platform answer wins over the state")
	}
	if !autoscaleGrows(nil, on) || autoscaleGrows(nil, nil) {
		t.Error("unreadable setting: the block in state decides")
	}
}

func TestAutoscaleRequest(t *testing.T) {
	blk := &StorageAutoscaleModel{
		Enabled: types.BoolValue(true), ThresholdPercent: types.Int64Value(85), StepGB: types.Int64Value(10),
		StepPercent: types.Int64Value(0), MaxSizeGB: types.Int64Value(200),
	}
	b, _ := json.Marshal(autoscaleRequest(blk))
	if want := `{"enabled":true,"thresholdPercent":85,"stepGb":10,"stepPercent":0,"maxSizeGb":200}`; string(b) != want {
		t.Fatalf("PUT body %s, want %s", b, want)
	}
	b, _ = json.Marshal(autoscaleRequest(&StorageAutoscaleModel{Enabled: types.BoolValue(false)}))
	if string(b) != `{"enabled":false}` {
		t.Fatalf("disabled: %s", b)
	}
	blk.MaxSizeGB = types.Int64Unknown()
	b, _ = json.Marshal(autoscaleRequest(blk))
	if want := `{"enabled":true,"thresholdPercent":85,"stepGb":10,"stepPercent":0}`; string(b) != want {
		t.Fatalf("unknown max_size_gb is omitted (keeps the current value): %s", b)
	}
	if createAutoscale(nil) != nil {
		t.Fatal("not set: no field, h3db applies its default")
	}
	b, _ = json.Marshal(CreateRequest{Name: "x", StorageAutoscale: createAutoscale(&StorageAutoscaleModel{Enabled: types.BoolValue(false)})})
	if !contains(string(b), `"storageAutoscale":{"enabled":false}`) {
		t.Fatalf("enabled = false is sent on create (overrides the default): %s", b)
	}
	b, _ = json.Marshal(CreateRequest{Name: "x", StorageAutoscale: createAutoscale(blk)})
	if !json.Valid(b) || !contains(string(b), `"storageAutoscale":{"enabled":true`) {
		t.Fatalf("create body: %s", b)
	}
	b, _ = json.Marshal(CreateRequest{Name: "x"})
	if contains(string(b), "storageAutoscale") {
		t.Fatalf("not set, no field: %s", b)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestAutoscaleBlockFromAPI(t *testing.T) {
	var a StorageAutoscale
	_ = json.Unmarshal([]byte(`{"enabled":true,"thresholdPercent":90,"stepGb":5,"stepPercent":0,"maxSizeGb":100,
		"state":"AT_MAX","atMax":true,"lastEvent":{"result":"AT_MAX","fromSizeGb":100,"toSizeGb":100},
		"limits":{"maxSizeGb":1024}}`), &a)
	m := autoscaleFromAPI(&a)
	if !m.Enabled.ValueBool() || m.ThresholdPercent.ValueInt64() != 90 || m.StepGB.ValueInt64() != 5 ||
		m.StepPercent.ValueInt64() != 0 || m.MaxSizeGB.ValueInt64() != 100 || !m.AtMax.ValueBool() ||
		m.State.ValueString() != "AT_MAX" {
		t.Fatalf("mapped %+v", m)
	}
	if a.grew() {
		t.Fatal("AT_MAX is not a grow")
	}
	if m := autoscaleFromAPI(&StorageAutoscale{}); !m.MaxSizeGB.IsNull() {
		t.Fatal("maxSizeGb 0 (never enabled) maps to null")
	}
	filled := fillAutoscaleComputed(&StorageAutoscaleModel{AtMax: types.BoolUnknown(), State: types.StringUnknown(), MaxSizeGB: types.Int64Unknown()})
	if filled.AtMax.IsUnknown() || filled.State.IsUnknown() || filled.MaxSizeGB.IsUnknown() {
		t.Fatal("unknowns must be resolved when the setting cannot be read")
	}
}

func TestValidateAutoscale(t *testing.T) {
	ok := StorageAutoscaleModel{Enabled: types.BoolValue(true), ThresholdPercent: types.Int64Value(60),
		StepGB: types.Int64Value(1024), StepPercent: types.Int64Value(0), MaxSizeGB: types.Int64Value(20)}
	if errs := validateAutoscale(ok, 20); len(errs) != 0 {
		t.Fatalf("valid block: %v", errs)
	}
	off := StorageAutoscaleModel{Enabled: types.BoolValue(false), MaxSizeGB: types.Int64Null(),
		ThresholdPercent: types.Int64Value(85), StepGB: types.Int64Value(10), StepPercent: types.Int64Value(20)}
	if errs := validateAutoscale(off, 20); len(errs) != 0 {
		t.Fatalf("disabled block needs no max_size_gb: %v", errs)
	}
	bad := map[string]StorageAutoscaleModel{
		"threshold_percent": {Enabled: types.BoolValue(true), ThresholdPercent: types.Int64Value(96), MaxSizeGB: types.Int64Value(50)},
		"step_gb":           {Enabled: types.BoolValue(true), StepGB: types.Int64Value(4), MaxSizeGB: types.Int64Value(50)},
		"step_percent":      {Enabled: types.BoolValue(true), StepPercent: types.Int64Value(101), MaxSizeGB: types.Int64Value(50)},
		"max_size_gb":       {Enabled: types.BoolValue(true), MaxSizeGB: types.Int64Value(1025)},
	}
	for attr, m := range bad {
		errs := validateAutoscale(m, 20)
		if len(errs) != 1 || errs[0].attr != attr {
			t.Errorf("%s: %v", attr, errs)
		}
	}
	below := StorageAutoscaleModel{Enabled: types.BoolValue(true), MaxSizeGB: types.Int64Value(19)}
	if errs := validateAutoscale(below, 20); len(errs) != 1 {
		t.Errorf("max below size: %v", errs)
	}
	if errs := validateAutoscale(below, 0); len(errs) != 0 {
		t.Errorf("unknown size is left to the API: %v", errs)
	}
	noMax := StorageAutoscaleModel{Enabled: types.BoolValue(true), MaxSizeGB: types.Int64Null(),
		ThresholdPercent: types.Int64Value(85), StepGB: types.Int64Value(10), StepPercent: types.Int64Value(20)}
	if errs := validateAutoscale(noMax, 20); len(errs) != 0 {
		t.Errorf("enabled without max_size_gb takes 2 x size: %v", errs)
	}
	if err := checkMaxSize(types.Int64Value(30), types.StringValue("40Gi")); err == nil {
		t.Error("max_size_gb below the grown disk must be refused")
	}
	if err := checkMaxSize(types.Int64Value(40), types.StringValue("40Gi")); err != nil {
		t.Error(err)
	}
}

func TestDefaultMaxSize(t *testing.T) {
	for in, want := range map[int64]int64{10: 20, 20: 40, 512: 1024, 600: 1024, 1024: 1024} {
		if got := defaultMaxSize(in); got != want {
			t.Errorf("defaultMaxSize(%d) = %d, want %d", in, got, want)
		}
	}
	if got := planMaxSize(types.StringValue("20Gi"), types.StringValue("35Gi")); got.ValueInt64() != 70 {
		t.Errorf("larger of size and current_size: %v", got)
	}
	if got := planMaxSize(types.StringUnknown(), types.StringNull()); !got.IsUnknown() {
		t.Errorf("unknown size: %v", got)
	}
}

func TestAutoscaleChanged(t *testing.T) {
	on := &StorageAutoscaleModel{Enabled: types.BoolValue(true), ThresholdPercent: types.Int64Value(85),
		StepGB: types.Int64Value(10), StepPercent: types.Int64Value(20), MaxSizeGB: types.Int64Value(40)}
	if autoscaleChanged(nil, on) {
		t.Error("not configured: nothing is sent (removing it does not turn autoscale off)")
	}
	if !autoscaleChanged(on, nil) {
		t.Error("configured on a platform value that was null")
	}
	off := *on
	off.Enabled = types.BoolValue(false)
	if !autoscaleChanged(&off, on) || autoscaleChanged(on, on) {
		t.Error("enabled change")
	}
}
