// Package dns implements h3_dns_zone, h3_dns_recordset, h3_dns_ptr and the
// h3_dns_zone data source on top of the h3dns API
// (platform/dns-service-spec-2026-10-07.md §7.3).
package dns

import (
	"net/netip"
	"strings"
)

const apiBase = "/api/dns/v1"

func projectZonesPath(projectID string) string { return apiBase + "/projects/" + projectID + "/zones" }
func zonePath(zoneID string) string            { return apiBase + "/zones/" + zoneID }
func recordSetPath(zoneID, name, typ string) string {
	return zonePath(zoneID) + "/recordsets/" + name + "/" + typ
}
func ptrPath(projectID, eipID string) string {
	return apiBase + "/projects/" + projectID + "/eips/" + eipID + "/ptr"
}

// Zone is the h3dns zone view.
type Zone struct {
	ID              string            `json:"id"`
	ProjectID       string            `json:"project_id"`
	Name            string            `json:"name"`
	Kind            string            `json:"kind"`
	Status          string            `json:"status"`
	Nameservers     []string          `json:"nameservers"`
	DefaultTTL      int64             `json:"default_ttl"`
	Labels          map[string]string `json:"labels"`
	Verification    Verification      `json:"verification"`
	VerificationTXT *TXTHint          `json:"verification_txt"`
	DNSSEC          DNSSEC            `json:"dnssec"`
}

type Verification struct {
	Method    string `json:"method"`
	Token     string `json:"token"`
	LastError string `json:"last_error"`
}

type TXTHint struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type DNSSEC struct {
	Enabled bool     `json:"enabled"`
	State   string   `json:"state"`
	DS      []string `json:"ds"`
}

type ZoneCreate struct {
	Name               string            `json:"name"`
	DNSSEC             bool              `json:"dnssec"`
	DefaultTTL         int64             `json:"default_ttl,omitempty"`
	Labels             map[string]string `json:"labels,omitempty"`
	VerificationMethod string            `json:"verification_method,omitempty"`
}

// ZonePatch sends only what changed: every PATCH with `dnssec` makes the
// service reconcile signing. Labels is a pointer so that `{}` (clear all
// labels) is sent while nil leaves them unchanged.
type ZonePatch struct {
	DNSSEC     *bool              `json:"dnssec,omitempty"`
	DefaultTTL *int64             `json:"default_ttl,omitempty"`
	Labels     *map[string]string `json:"labels,omitempty"`
}

// Nameservers is GET /api/dns/v1/nameservers.
type Nameservers struct {
	Nameservers          []string `json:"nameservers"`
	VerificationTXTLabel string   `json:"verification_txt_label"`
}

type ZoneList struct {
	Items         []Zone `json:"items"`
	Total         int    `json:"total"`
	NextPageToken string `json:"next_page_token"`
}

type RecordSet struct {
	ID        string   `json:"id"`
	ZoneID    string   `json:"zone_id"`
	Name      string   `json:"name"`
	FQDN      string   `json:"fqdn"`
	Type      string   `json:"type"`
	TTL       int64    `json:"ttl"`
	Records   []string `json:"records"`
	ManagedBy string   `json:"managed_by"`
	Etag      string   `json:"etag"`
}

type RecordSetBody struct {
	TTL     int64    `json:"ttl,omitempty"`
	Records []string `json:"records"`
}

type PTR struct {
	EIPID        string `json:"eip_id"`
	IP           string `json:"ip"`
	Hostname     string `json:"hostname"`
	Source       string `json:"source"`
	ForwardCheck string `json:"forward_check"`
}

// normalizeName mirrors h3dns: "" and "@" are the apex, names are lower-case.
func normalizeName(n string) string {
	n = strings.ToLower(strings.TrimSpace(n))
	if n == "" {
		return "@"
	}
	return n
}

// canonicalRecord approximates the server's canonical form so that a value
// written as "mail.example.com" in HCL and returned as "mail.example.com."
// does not produce a perpetual diff. The server stays the authority: values
// that do not normalize to the same form are shown as changes.
func canonicalRecord(typ, v string) string {
	v = strings.TrimSpace(v)
	host := func(h string) string {
		if h == "." {
			return h
		}
		h = strings.ToLower(h)
		if !strings.HasSuffix(h, ".") {
			h += "."
		}
		return h
	}
	switch strings.ToUpper(typ) {
	case "A", "AAAA":
		if ip, err := netip.ParseAddr(v); err == nil {
			return ip.String()
		}
	case "CNAME", "NS", "PTR", "ALIAS":
		return host(v)
	case "MX":
		if f := strings.Fields(v); len(f) == 2 {
			return f[0] + " " + host(f[1])
		}
	case "SRV":
		if f := strings.Fields(v); len(f) == 4 {
			return f[0] + " " + f[1] + " " + f[2] + " " + host(f[3])
		}
	case "CAA":
		if f := strings.Fields(v); len(f) >= 3 {
			val := strings.Join(f[2:], " ")
			if !strings.HasPrefix(val, `"`) {
				val = `"` + val + `"`
			}
			return f[0] + " " + strings.ToLower(f[1]) + " " + val
		}
	}
	return v
}

// mergeRecords keeps the user's spelling for records the server returned
// in an equivalent canonical form.
func mergeRecords(typ string, prior, server []string) []string {
	byCanon := map[string]string{}
	for _, p := range prior {
		byCanon[canonicalRecord(typ, p)] = p
	}
	out := make([]string, 0, len(server))
	for _, s := range server {
		if p, ok := byCanon[canonicalRecord(typ, s)]; ok {
			out = append(out, p)
			continue
		}
		out = append(out, s)
	}
	return out
}

func stringsTrimDotLower(s string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
