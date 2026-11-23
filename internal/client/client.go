package client

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultEndpoint is the public h3llo cloud API.
const DefaultEndpoint = "https://api.h3llo.cloud"

// Client - HTTP клиент с HMAC подписью
type Client struct {
	baseURL    string
	keyID      string
	secretKey  string
	httpClient *http.Client
	maxRetries int
	userAgent  string
	projectID  string
}

// Config - конфигурация клиента
type Config struct {
	BaseURL   string
	KeyID     string
	SecretKey string
	Timeout   time.Duration
	// MaxRetries is the number of retries after the first attempt; negative means the default (3), 0 disables retries.
	MaxRetries int
	UserAgent  string
	// ProjectID is the default project of resources and data sources that do not set project_id.
	ProjectID string
}

// NewClient создает HTTP клиент с HMAC аутентификацией
func NewClient(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultEndpoint
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid API endpoint %q: expected a URL like %s", cfg.BaseURL, DefaultEndpoint)
	}
	if cfg.KeyID == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("HMAC credentials (key_id and secret_key) are required")
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 3
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "terraform-provider-h3"
	}

	return &Client{
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		userAgent: cfg.UserAgent,
		keyID:     cfg.KeyID,
		secretKey: cfg.SecretKey,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
		maxRetries: cfg.MaxRetries,
		projectID:  cfg.ProjectID,
	}, nil
}

// ProjectID is the provider default project ("" when not set).
func (c *Client) ProjectID() string { return c.projectID }

// Do выполняет HTTP запрос с HMAC подписью
func (c *Client) Do(ctx context.Context, method, path string, queryParams map[string]string, body interface{}, result interface{}) error {
	return c.DoWith(ctx, method, path, queryParams, body, result, nil)
}

// Options are per-request extras: conditional headers (If-Match,
// If-None-Match) and access to the response headers (ETag). The extra headers
// are not part of the HMAC signature, as in h3ars.
type Options struct {
	Headers map[string]string
	// RespHeader, when set, receives the headers of the final response
	// (also of an error response).
	RespHeader *http.Header
}

// DoWith is Do with Options.
func (c *Client) DoWith(ctx context.Context, method, path string, queryParams map[string]string, body interface{}, result interface{}, opts *Options) error {
	var bodyBytes []byte
	var err error

	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
	}

	// The query is sent URL-encoded; the signature covers the decoded, sorted
	// "k=v" pairs, which is what h3ars rebuilds from r.URL.Query().
	reqURL := c.baseURL + path
	canonicalQuery := ""
	if len(queryParams) > 0 {
		q := url.Values{}
		for k, v := range queryParams {
			q.Set(k, v)
		}
		reqURL += "?" + q.Encode()
		canonicalQuery = CanonicalQuery(queryParams)
	}

	var lastErr error
	var wait time.Duration
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}

		// Build and sign every attempt: the body reader is consumed by each send
		// and X-H3-Date must be fresh.
		req, err := http.NewRequestWithContext(ctx, method, reqURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("User-Agent", c.userAgent)
		if opts != nil {
			for k, v := range opts.Headers {
				req.Header.Set(k, v)
			}
		}
		if err := c.signRequest(req, canonicalQuery, bodyBytes); err != nil {
			return fmt.Errorf("failed to sign request: %w", err)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = fmt.Errorf("%s %s: %w", method, path, err)
			if !retryableNetError(method, err) {
				return lastErr
			}
			wait = backoff(attempt)
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if opts != nil && opts.RespHeader != nil {
			*opts.RespHeader = resp.Header.Clone()
		}
		if err != nil {
			lastErr = fmt.Errorf("%s %s: failed to read response: %w", method, path, err)
			if !idempotent(method) {
				return lastErr
			}
			wait = backoff(attempt)
			continue
		}

		if resp.StatusCode >= 400 {
			httpErr := &HTTPError{
				StatusCode: resp.StatusCode,
				Method:     method,
				URL:        reqURL,
				Body:       string(respBody),
			}
			if !retryableStatus(method, resp.StatusCode) || featureDisabled(resp.StatusCode, respBody) {
				return httpErr
			}
			lastErr = httpErr
			wait = retryAfter(resp.Header.Get("Retry-After"), attempt)
			continue
		}

		if result != nil && len(respBody) > 0 {
			if err := json.Unmarshal(respBody, result); err != nil {
				return fmt.Errorf("failed to unmarshal response: %w", err)
			}
		}
		return nil
	}

	if c.maxRetries == 0 {
		return lastErr
	}
	return fmt.Errorf("giving up after %d retries: %w", c.maxRetries, lastErr)
}

// Retry policy.
//
// A create (POST) or PATCH is not idempotent: if the request reached the API
// and the answer was lost (timeout, reset, 500), a blind retry can create a
// second VM, disk or database. Such requests are retried only when the API
// (or the gateway in front of it) said it did not process them: 429, 502,
// 503, 504, or when the connection was never established. GET, PUT, DELETE
// and HEAD are also retried on other network errors and on 500.
const (
	backoffBase   = time.Second
	backoffMax    = 30 * time.Second
	retryAfterMax = 60 * time.Second
)

func idempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
		return true
	}
	return false
}

func retryableStatus(method string, code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	case http.StatusInternalServerError:
		return idempotent(method)
	}
	return false
}

// featureDisabled: a 503 with a *_DISABLED code (e.g. SSH_ACCESS_DISABLED) is
// a platform flag that is off, not an overload: retrying does not help.
func featureDisabled(code int, body []byte) bool {
	return code == http.StatusServiceUnavailable && strings.Contains(string(body), "_DISABLED")
}

func retryableNetError(method string, err error) bool {
	if idempotent(method) {
		return true
	}
	// Connection never established: the request did not reach the API.
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

// backoff is exponential (1s, 2s, 4s ... capped at 30s) with up to 50% jitter.
func backoff(attempt int) time.Duration {
	d := backoffBase << uint(attempt)
	if d > backoffMax || d <= 0 {
		d = backoffMax
	}
	return d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
}

// retryAfter honors Retry-After (seconds or HTTP date), capped at 60s;
// without the header it falls back to backoff.
func retryAfter(h string, attempt int) time.Duration {
	if h == "" {
		return backoff(attempt)
	}
	var d time.Duration
	if secs, err := strconv.Atoi(strings.TrimSpace(h)); err == nil {
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(h); err == nil {
		d = time.Until(t)
	} else {
		return backoff(attempt)
	}
	if d < 0 {
		d = 0
	}
	if d > retryAfterMax {
		d = retryAfterMax
	}
	return d
}

// CanonicalQuery is the query part of the canonical request: decoded "k=v"
// pairs sorted lexicographically and joined with "&" (h3ars BuildCanonicalRequest).
func CanonicalQuery(params map[string]string) string {
	parts := make([]string, 0, len(params))
	for k, v := range params {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

// signRequest добавляет HMAC подпись к запросу
func (c *Client) signRequest(req *http.Request, canonicalQuery string, body []byte) error {
	// Timestamp
	date := time.Now().UTC().Format(time.RFC3339)

	// Body hash
	bodyHash := sha256.Sum256(body)
	bodyHashHex := hex.EncodeToString(bodyHash[:])

	// Canonical headers (должны быть отсортированы!)
	signedHeaders := []string{
		"x-h3-date:" + date,
		"x-h3-key-id:" + c.keyID,
	}
	sort.Strings(signedHeaders)
	canonicalHeaders := strings.Join(signedHeaders, "\n")

	// Canonical request
	canonical := strings.Join([]string{
		req.Method,
		req.URL.Path,
		canonicalQuery,
		canonicalHeaders,
		bodyHashHex,
	}, "\n")

	// HMAC signature
	mac := hmac.New(sha256.New, []byte(c.secretKey))
	mac.Write([]byte(canonical))
	signature := hex.EncodeToString(mac.Sum(nil))

	// Добавляем заголовки
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-H3-Key-Id", c.keyID)
	req.Header.Set("X-H3-Date", date)
	req.Header.Set("X-H3-Signature", signature)

	return nil
}
