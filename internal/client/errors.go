package client

import "fmt"

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
