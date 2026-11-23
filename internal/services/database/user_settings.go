package database

import (
	"fmt"
	"regexp"
	"strconv"
)

// User settings of h3db (PG-ROLES-PARAMS, TRD 06 §2.2). The provider checks
// the name and that the value is written as the API returns it (30s, 64MB):
// otherwise every plan would show a change from "30000" to "30s". The bounds
// that depend on the database (work_mem up to 1/8 of RAM) are checked by the
// API (422 VALIDATION_FAILED).

var userSettingKinds = map[string]string{
	"default_transaction_isolation":       "enum",
	"statement_timeout":                   "duration",
	"lock_timeout":                        "duration",
	"idle_in_transaction_session_timeout": "duration",
	"idle_session_timeout":                "duration",
	"work_mem":                            "memory",
}

var isolationLevels = map[string]bool{"read committed": true, "repeatable read": true, "serializable": true}

var settingNumRe = regexp.MustCompile(`^(\d{1,13})([a-zA-Z]*)$`)

var durationMul = map[string]int64{"": 1, "ms": 1, "s": 1000, "min": 60000, "h": 3600000, "d": 86400000}
var memoryMul = map[string]int64{"": 1, "kB": 1, "MB": 1024, "GB": 1024 * 1024}

func canonicalDuration(ms int64) string {
	if ms == 0 {
		return "0"
	}
	for _, u := range []struct {
		s string
		n int64
	}{{"d", 86400000}, {"h", 3600000}, {"min", 60000}, {"s", 1000}} {
		if ms%u.n == 0 {
			return strconv.FormatInt(ms/u.n, 10) + u.s
		}
	}
	return strconv.FormatInt(ms, 10) + "ms"
}

func canonicalMemory(kb int64) string {
	switch {
	case kb%(1024*1024) == 0:
		return strconv.FormatInt(kb/(1024*1024), 10) + "GB"
	case kb%1024 == 0:
		return strconv.FormatInt(kb/1024, 10) + "MB"
	}
	return strconv.FormatInt(kb, 10) + "kB"
}

// checkUserSetting returns "" when name and value are accepted as written.
func checkUserSetting(name, value string) string {
	kind, ok := userSettingKinds[name]
	if !ok {
		return fmt.Sprintf("%q cannot be set; allowed: default_transaction_isolation, statement_timeout, lock_timeout, "+
			"idle_in_transaction_session_timeout, idle_session_timeout, work_mem", name)
	}
	if kind == "enum" {
		if !isolationLevels[value] {
			return fmt.Sprintf("%s must be one of: read committed, repeatable read, serializable", name)
		}
		return ""
	}
	m := settingNumRe.FindStringSubmatch(value)
	units := durationMul
	if kind == "memory" {
		units = memoryMul
	}
	if m == nil {
		return fmt.Sprintf("%s: %q is not an integer with a unit (e.g. 30s, 64MB)", name, value)
	}
	mul, ok := units[m[2]]
	if !ok {
		return fmt.Sprintf("%s: unknown unit %q", name, m[2])
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	canon := canonicalDuration(n * mul)
	if kind == "memory" {
		canon = canonicalMemory(n * mul)
	}
	if canon != value {
		return fmt.Sprintf("%s: write %q (the form the API returns) instead of %q", name, canon, value)
	}
	return ""
}
