package lb

import (
	"strings"
	"testing"

	"h3terraform/internal/client"
)

func TestValidName(t *testing.T) {
	for _, ok := range []string{"web", "a", "web-1", "1a"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Web", "-a", "a-", "123", "a_b", strings.Repeat("a", 64)} {
		if ValidName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidTarget(t *testing.T) {
	for _, ok := range []string{"10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1", "100.64.0.1"} {
		if err := ValidTarget(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"8.8.8.8", "172.32.0.1", "100.128.0.1", "fd00::1", "web", ""} {
		if ValidTarget(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestExplainAccess(t *testing.T) {
	msg := explain(&client.HTTPError{StatusCode: 401, Method: "POST", URL: "/x", Body: "missing X-User-Id"})
	if !strings.Contains(msg, "console sessions") {
		t.Fatal(msg)
	}
	if msg := explain(&client.HTTPError{StatusCode: 500, Body: "boom"}); strings.Contains(msg, "console") {
		t.Fatal(msg)
	}
}
