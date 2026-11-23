package dns

import (
	"reflect"
	"testing"
)

func TestCanonicalRecord(t *testing.T) {
	cases := []struct{ typ, in, want string }{
		{"A", " 192.0.2.1 ", "192.0.2.1"},
		{"AAAA", "2001:DB8::1", "2001:db8::1"},
		{"CNAME", "Target.Example.com", "target.example.com."},
		{"MX", "10 Mail.example.com", "10 mail.example.com."},
		{"MX", "0 .", "0 ."},
		{"SRV", "10 5 5060 sip.example.com", "10 5 5060 sip.example.com."},
		{"CAA", `0 ISSUE letsencrypt.org`, `0 issue "letsencrypt.org"`},
		{"TXT", "v=spf1 -all", "v=spf1 -all"},
	}
	for _, c := range cases {
		if got := canonicalRecord(c.typ, c.in); got != c.want {
			t.Errorf("%s %q: got %q want %q", c.typ, c.in, got, c.want)
		}
	}
}

func TestMergeRecordsKeepsSpelling(t *testing.T) {
	got := mergeRecords("MX", []string{"10 mail.example.com"}, []string{"10 mail.example.com.", "20 other.example.com."})
	want := []string{"10 mail.example.com", "20 other.example.com."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestPaths(t *testing.T) {
	if recordSetPath("z", "@", "A") != "/api/dns/v1/zones/z/recordsets/@/A" || ptrPath("p", "e") != "/api/dns/v1/projects/p/eips/e/ptr" {
		t.Fatal("paths")
	}
	if normalizeName("") != "@" || normalizeName(" WWW ") != "www" {
		t.Fatal("normalizeName")
	}
}
