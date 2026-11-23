package client_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"h3terraform/internal/acctest"
	"h3terraform/internal/client"
)

// The server (h3ars) verifies the signature over the decoded, sorted query.
// Values with spaces, '&', '=' or non-ASCII must still verify and arrive intact.
func TestQueryIsEscapedAndSignatureVerifies(t *testing.T) {
	var gotQuery map[string]string
	var verified bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		canonical := acctest.BuildCanonicalRequest(r, body)
		verified = acctest.VerifyHMAC("secret", canonical, r.Header.Get("X-H3-Signature"))
		gotQuery = map[string]string{}
		for k, v := range r.URL.Query() {
			gotQuery[k] = v[0]
		}
		if ua := r.Header.Get("User-Agent"); ua != "tf-test/1" {
			t.Errorf("User-Agent = %q", ua)
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, err := client.NewClient(client.Config{BaseURL: srv.URL + "/", KeyID: "k", SecretKey: "secret", MaxRetries: 0, UserAgent: "tf-test/1"})
	if err != nil {
		t.Fatal(err)
	}
	q := map[string]string{"project_id": "p1", "name": "a b&c=d", "lang": "ру"}
	if err := c.Do(context.Background(), "GET", "/api/x/v1/items", q, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !verified {
		t.Fatal("server could not verify the signature")
	}
	for k, v := range q {
		if gotQuery[k] != v {
			t.Fatalf("query %s = %q, want %q", k, gotQuery[k], v)
		}
	}
}

func TestDefaultEndpointAndValidation(t *testing.T) {
	if _, err := client.NewClient(client.Config{KeyID: "k", SecretKey: "s"}); err != nil {
		t.Fatalf("empty endpoint must default to %s: %v", client.DefaultEndpoint, err)
	}
	if _, err := client.NewClient(client.Config{BaseURL: "api.h3llo.cloud", KeyID: "k", SecretKey: "s"}); err == nil {
		t.Fatal("endpoint without scheme must be rejected")
	}
	if _, err := client.NewClient(client.Config{BaseURL: client.DefaultEndpoint}); err == nil {
		t.Fatal("missing credentials must be rejected")
	}
}

func TestMaxRetriesZeroDisablesRetries(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c, _ := client.NewClient(client.Config{BaseURL: srv.URL, KeyID: "k", SecretKey: "s", MaxRetries: 0})
	if err := c.Do(context.Background(), "GET", "/x", nil, nil, nil); err == nil {
		t.Fatal("expected an error")
	}
	if n != 1 {
		t.Fatalf("attempts = %d, want 1", n)
	}
}

func TestHTTPErrorCodeAndDetail(t *testing.T) {
	cases := []struct{ body, code, detail string }{
		{`{"type":"https://docs.h3llo.cloud/dns/errors/precondition-failed","title":"Precondition failed","status":412,"code":"ETAG_MISMATCH","detail":"etag differs"}`, "ETAG_MISMATCH", "etag differs"},
		{`{"error":{"code":"REVISION_CONFLICT","message":"group changed","timestamp":"x"}}`, "REVISION_CONFLICT", "group changed"},
		{`EIP_ALREADY_BOUND: eip is bound`, "", "EIP_ALREADY_BOUND: eip is bound"},
		{`{"error":"Authentication failed"}`, "", "Authentication failed"},
	}
	for _, c := range cases {
		e := &client.HTTPError{StatusCode: 409, Body: c.body}
		if got := e.Code(); got != c.code {
			t.Errorf("Code(%s) = %q, want %q", c.body, got, c.code)
		}
		if got := e.Detail(); got != c.detail {
			t.Errorf("Detail(%s) = %q, want %q", c.body, got, c.detail)
		}
	}
}
