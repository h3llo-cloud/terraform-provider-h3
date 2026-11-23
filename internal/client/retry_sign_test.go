package client

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// A retry after a 503 must be signed again (fresh X-H3-Date) and carry the
// full body: the body reader is consumed by the first send.
func TestRetryResignsAndResendsBody(t *testing.T) {
	var mu sync.Mutex
	var sigs, dates, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sigs = append(sigs, r.Header.Get("X-H3-Signature"))
		dates = append(dates, r.Header.Get("X-H3-Date"))
		bodies = append(bodies, string(b))
		n := len(sigs)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, keyID: "k", secretKey: "s", httpClient: &http.Client{Timeout: 5 * time.Second}, maxRetries: 1}
	var out map[string]any
	if err := c.Do(context.Background(), http.MethodPost, "/api/disks/v1", nil, map[string]string{"name": "d1"}, &out); err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 2 {
		t.Fatalf("attempts = %d, want 2", len(sigs))
	}
	for i := range sigs {
		if sigs[i] == "" || dates[i] == "" {
			t.Fatalf("attempt %d is not signed: sig=%q date=%q", i, sigs[i], dates[i])
		}
		if want := sign("s", "POST", "/api/disks/v1", dates[i], "k", bodies[i]); sigs[i] != want {
			t.Fatalf("attempt %d: signature does not match its own date and body", i)
		}
	}
	if bodies[1] != bodies[0] || bodies[1] == "" {
		t.Fatalf("retry body = %q, first = %q", bodies[1], bodies[0])
	}
}

// sign recomputes the canonical-request signature like h3ars does (no query).
func sign(secret, method, path, date, keyID, body string) string {
	bh := sha256.Sum256([]byte(body))
	canonical := method + "\n" + path + "\n\n" + "x-h3-date:" + date + "\nx-h3-key-id:" + keyID + "\n" + hex.EncodeToString(bh[:])
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(canonical))
	return hex.EncodeToString(m.Sum(nil))
}
