package database

import (
	"testing"
	"time"
)

// SetPollIntervalForTest shortens polling for tests in package database_test.
func SetPollIntervalForTest(t *testing.T, d time.Duration) {
	old := pollInterval
	pollInterval = d
	t.Cleanup(func() { pollInterval = old })
}
