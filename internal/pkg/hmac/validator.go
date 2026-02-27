package hmac

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

func BuildCanonicalRequest(r *http.Request, body []byte) string {
	method := r.Method
	path := r.URL.Path

	query := r.URL.Query()
	var queryParts []string
	for k, values := range query {
		for _, v := range values {
			queryParts = append(queryParts, k+"="+v)
		}
	}
	sort.Strings(queryParts)
	canonicalQuery := strings.Join(queryParts, "&")

	signedHeaders := []string{
		"x-h3-date:" + r.Header.Get("X-H3-Date"),
		"x-h3-key-id:" + r.Header.Get("X-H3-Key-Id"),
	}
	sort.Strings(signedHeaders)
	canonicalHeaders := strings.Join(signedHeaders, "\n")

	bodyHash := sha256.Sum256(body)
	bodyHashHex := hex.EncodeToString(bodyHash[:])

	return strings.Join([]string{
		method,
		path,
		canonicalQuery,
		canonicalHeaders,
		bodyHashHex,
	}, "\n")
}

func VerifyHMAC(secretKey, canonical, signature string) bool {
	h := hmac.New(sha256.New, []byte(secretKey))
	h.Write([]byte(canonical))
	expected := hex.EncodeToString(h.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func ReadBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(r.Body)
}

func ExtractHeaders(r *http.Request) (keyID, date, signature string, err error) {
	keyID = r.Header.Get("X-H3-Key-Id")
	date = r.Header.Get("X-H3-Date")
	signature = r.Header.Get("X-H3-Signature")

	if keyID == "" || date == "" || signature == "" {
		return "", "", "", fmt.Errorf("missing required HMAC headers")
	}

	return keyID, date, signature, nil
}
