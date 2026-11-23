package database

import "testing"

func TestValidateSize(t *testing.T) {
	for _, ok := range []string{"1Gi", "10Gi", "1024Gi"} {
		if err := validateSize(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "0Gi", "10G", "10", "1025Gi", "1.5Gi", "10Mi"} {
		if err := validateSize(bad); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
}

func TestCheckResize(t *testing.T) {
	if err := checkResize("20Gi", "10Gi"); err == nil {
		t.Fatal("shrink must be refused")
	}
	for _, p := range []string{"20Gi", "21Gi", "1024Gi"} {
		if err := checkResize("20Gi", p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if err := checkResize("9Gi", "10Gi"); err != nil {
		t.Fatal("numeric, not string, comparison")
	}
	if err := checkResize("", "10Gi"); err != nil {
		t.Fatal("unknown current size is left to the API")
	}
}

func TestValidateShape(t *testing.T) {
	for _, ok := range []struct {
		cpu int64
		mem string
	}{{1, "1Gi"}, {1, "16Gi"}, {2, "4Gi"}, {4, "8Gi"}, {256, "1024Gi"}} {
		if err := validateShape(ok.cpu, ok.mem); err != nil {
			t.Errorf("%d/%s: %v", ok.cpu, ok.mem, err)
		}
	}
	for _, bad := range []struct {
		cpu int64
		mem string
	}{{0, "1Gi"}, {257, "1024Gi"}, {2, "1Gi"}, {1, "17Gi"}, {2, "4G"}, {128, "2048Gi"}} {
		if err := validateShape(bad.cpu, bad.mem); err == nil {
			t.Errorf("%d/%s must be rejected", bad.cpu, bad.mem)
		}
	}
}
