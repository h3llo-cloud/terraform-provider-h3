package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(url string, retries int, timeout time.Duration) *Client {
	c, err := NewClient(Config{BaseURL: url, KeyID: "k", SecretKey: "s", MaxRetries: retries, Timeout: timeout})
	if err != nil {
		panic(err)
	}
	return c
}

func countingServer(t *testing.T, h func(n int32, w http.ResponseWriter)) (*httptest.Server, *int32) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h(atomic.AddInt32(&n, 1), w)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// A POST that may have been processed (500, timeout) must not be sent twice:
// the retry could create a duplicate VM/disk/database.
func TestPostIsNotRetriedOn500(t *testing.T) {
	srv, n := countingServer(t, func(_ int32, w http.ResponseWriter) { w.WriteHeader(500) })
	err := newTestClient(srv.URL, 3, 5*time.Second).Do(context.Background(), "POST", "/api/vms/v1", nil, map[string]int{"cpu": 2}, nil)
	if err == nil || *n != 1 {
		t.Fatalf("POST on 500: attempts=%d err=%v, want 1 attempt and an error", *n, err)
	}
	if h, ok := AsHTTPError(err); !ok || h.StatusCode != 500 {
		t.Fatalf("want HTTPError 500, got %v", err)
	}
}

func TestPostIsNotRetriedOnTimeout(t *testing.T) {
	srv, n := countingServer(t, func(_ int32, w http.ResponseWriter) { time.Sleep(300 * time.Millisecond) })
	err := newTestClient(srv.URL, 3, 100*time.Millisecond).Do(context.Background(), "POST", "/api/vms/v1", nil, map[string]int{"cpu": 2}, nil)
	if err == nil || atomic.LoadInt32(n) != 1 {
		t.Fatalf("POST on timeout: attempts=%d err=%v, want 1", *n, err)
	}
}

func TestPostIsRetriedOn429And503WithRetryAfter(t *testing.T) {
	for _, code := range []int{429, 502, 503, 504} {
		srv, n := countingServer(t, func(i int32, w http.ResponseWriter) {
			if i == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(code)
				return
			}
			w.Write([]byte(`{"id":"x"}`))
		})
		var out map[string]string
		start := time.Now()
		if err := newTestClient(srv.URL, 3, 5*time.Second).Do(context.Background(), "POST", "/api/vms/v1", nil, map[string]int{"cpu": 2}, &out); err != nil {
			t.Fatalf("%d: %v", code, err)
		}
		if *n != 2 || out["id"] != "x" {
			t.Fatalf("%d: attempts=%d out=%v", code, *n, out)
		}
		if time.Since(start) > 500*time.Millisecond {
			t.Fatalf("%d: Retry-After: 0 not honored (took %s)", code, time.Since(start))
		}
	}
}

func TestPostIsRetriedWhenConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens: dial fails, the request never reached the API
	c := newTestClient(url, 1, time.Second)
	c.maxRetries = 1
	err := c.Do(context.Background(), "POST", "/x", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "giving up after 1 retries") {
		t.Fatalf("want a retried dial error, got %v", err)
	}
}

func TestGetIsRetriedOn500(t *testing.T) {
	srv, n := countingServer(t, func(i int32, w http.ResponseWriter) {
		if i == 1 {
			w.WriteHeader(500)
			return
		}
		w.Write([]byte(`{}`))
	})
	if err := newTestClient(srv.URL, 1, 5*time.Second).Do(context.Background(), "GET", "/x", nil, nil, nil); err != nil || *n != 2 {
		t.Fatalf("GET on 500: attempts=%d err=%v", *n, err)
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	srv, n := countingServer(t, func(_ int32, w http.ResponseWriter) { w.WriteHeader(404) })
	err := newTestClient(srv.URL, 3, 5*time.Second).Do(context.Background(), "GET", "/x", nil, nil, nil)
	if !IsNotFound(err) || *n != 1 {
		t.Fatalf("404: attempts=%d err=%v", *n, err)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	if d := retryAfter("2", 0); d != 2*time.Second {
		t.Fatalf("seconds: %s", d)
	}
	if d := retryAfter("3600", 0); d != retryAfterMax {
		t.Fatalf("cap: %s", d)
	}
	future := time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat)
	if d := retryAfter(future, 0); d < 3*time.Second || d > 6*time.Second {
		t.Fatalf("http date: %s", d)
	}
	if d := retryAfter("", 0); d < backoffBase/2 || d > backoffBase {
		t.Fatalf("fallback backoff: %s", d)
	}
	if d := backoff(10); d > backoffMax {
		t.Fatalf("backoff cap: %s", d)
	}
}

// A platform flag that is off (503 *_DISABLED) is a final answer, not an overload.
func TestFeatureDisabledIsNotRetried(t *testing.T) {
	srv, n := countingServer(t, func(_ int32, w http.ResponseWriter) {
		w.WriteHeader(503)
		w.Write([]byte(`{"error":"SSH access management is disabled","code":"SSH_ACCESS_DISABLED"}`))
	})
	err := newTestClient(srv.URL, 3, 5*time.Second).Do(context.Background(), "GET", "/api/vms/v1/x/ssh-access", nil, nil, nil)
	if h, ok := AsHTTPError(err); !ok || h.StatusCode != 503 || *n != 1 {
		t.Fatalf("err=%v attempts=%d", err, *n)
	}
}
