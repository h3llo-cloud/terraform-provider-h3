package client

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Client - HTTP клиент с HMAC подписью
type Client struct {
	baseURL    string
	keyID      string
	secretKey  string
	httpClient *http.Client
	maxRetries int
}

// Config - конфигурация клиента
type Config struct {
	BaseURL    string
	KeyID      string
	SecretKey  string
	Timeout    time.Duration
	MaxRetries int
}

// NewClient создает HTTP клиент с HMAC аутентификацией
func NewClient(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	if cfg.KeyID == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("HMAC credentials (key_id and secret_key) are required")
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}

	return &Client{
		baseURL:   cfg.BaseURL,
		keyID:     cfg.KeyID,
		secretKey: cfg.SecretKey,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
		maxRetries: cfg.MaxRetries,
	}, nil
}

// Do выполняет HTTP запрос с HMAC подписью
func (c *Client) Do(ctx context.Context, method, path string, queryParams map[string]string, body interface{}, result interface{}) error {
	var bodyBytes []byte
	var err error

	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
	}

	// Формируем URL
	url := c.baseURL + path

	// Добавляем query parameters
	if len(queryParams) > 0 {
		var queryParts []string
		for k, v := range queryParams {
			queryParts = append(queryParts, k+"="+v)
		}
		sort.Strings(queryParts) // ОБЯЗАТЕЛЬНАЯ сортировка
		url += "?" + strings.Join(queryParts, "&")
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Подписываем запрос HMAC
	if err := c.signRequest(req, bodyBytes); err != nil {
		return fmt.Errorf("failed to sign request: %w", err)
	}

	// Retry logic с exponential backoff
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response: %w", err)
			continue
		}

		// Retry на 5xx
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("server error (%d): %s", resp.StatusCode, string(respBody))
			continue
		}

		// Client error - не ретраим
		if resp.StatusCode >= 400 {
			return &HTTPError{
				StatusCode: resp.StatusCode,
				Method:     method,
				URL:        url,
				Body:       string(respBody),
			}
		}

		// Success
		if result != nil && len(respBody) > 0 {
			if err := json.Unmarshal(respBody, result); err != nil {
				return fmt.Errorf("failed to unmarshal response: %w", err)
			}
		}

		return nil
	}

	return fmt.Errorf("request failed after %d retries: %w", c.maxRetries, lastErr)
}

// signRequest добавляет HMAC подпись к запросу
func (c *Client) signRequest(req *http.Request, body []byte) error {
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

	// Canonical query (уже отсортирован в Do())
	canonicalQuery := ""
	if req.URL.RawQuery != "" {
		canonicalQuery = req.URL.RawQuery
	}

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
