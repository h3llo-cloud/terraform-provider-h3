package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// HTTPError представляет HTTP ошибку от API
type HTTPError struct {
	StatusCode int
	Method     string
	URL        string
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s %s - %s",
		e.StatusCode, e.Method, e.URL, e.Body)
}

func (e *HTTPError) IsNotFound() bool {
	return e.StatusCode == 404
}

func (e *HTTPError) IsUnauthorized() bool {
	return e.StatusCode == 401
}

// IsConflict reports a 409 response.
func (e *HTTPError) IsConflict() bool {
	return e.StatusCode == 409
}

// IsEIPAlreadyBound reports the platform error EIP_ALREADY_BOUND (409): the
// selected Elastic IP is attached to, or reserved by, another resource.
func (e *HTTPError) IsEIPAlreadyBound() bool {
	return e.StatusCode == 409 && strings.Contains(e.Body, "EIP_ALREADY_BOUND")
}

// AsHTTPError unwraps err to *HTTPError (also through a "giving up after N retries" wrapper).
func AsHTTPError(err error) (*HTTPError, bool) {
	var h *HTTPError
	if errors.As(err, &h) {
		return h, true
	}
	return nil, false
}

// IsNotFound reports whether err is an HTTP 404 from the API.
func IsNotFound(err error) bool {
	h, ok := AsHTTPError(err)
	return ok && h.IsNotFound()
}

// IsEIPDenied reports the platform error EIP_DENIED (403): the project may not
// lease another Elastic IP (quota or balance).
func (e *HTTPError) IsEIPDenied() bool {
	return e.StatusCode == 403 && strings.Contains(e.Body, "EIP_DENIED")
}

// IsPreconditionFailed reports a 412 (If-Match / If-None-Match not met).
func (e *HTTPError) IsPreconditionFailed() bool {
	return e.StatusCode == 412
}

// Code returns the machine-readable error code of the response body:
// RFC 9457 problem `code` (h3dns), `{"error":{"code"}}` (h3ovn security
// groups) or `{"code"}`; "" when there is none.
func (e *HTTPError) Code() string {
	var p struct {
		Code  string          `json:"code"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(e.Body), &p) != nil {
		return ""
	}
	if p.Code != "" {
		return p.Code
	}
	var inner struct {
		Code string `json:"code"`
	}
	if len(p.Error) > 0 && json.Unmarshal(p.Error, &inner) == nil {
		return inner.Code
	}
	return ""
}

// Detail returns the human-readable message of a problem/JSON error body, or
// the raw body.
func (e *HTTPError) Detail() string {
	var p struct {
		Title   string          `json:"title"`
		Detail  string          `json:"detail"`
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(e.Body), &p) == nil {
		switch {
		case p.Detail != "":
			return p.Detail
		case p.Message != "":
			return p.Message
		case p.Title != "":
			return p.Title
		}
		var inner struct {
			Message string `json:"message"`
		}
		if len(p.Error) > 0 && json.Unmarshal(p.Error, &inner) == nil && inner.Message != "" {
			return inner.Message
		}
		var str string
		if len(p.Error) > 0 && json.Unmarshal(p.Error, &str) == nil && str != "" {
			return str
		}
	}
	return strings.TrimSpace(e.Body)
}

// IsPreconditionFailed reports whether err is an HTTP 412 from the API.
func IsPreconditionFailed(err error) bool {
	h, ok := AsHTTPError(err)
	return ok && h.IsPreconditionFailed()
}
