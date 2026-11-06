package client

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// StatusError is a non-2xx provider response. It lets callers distinguish a
// retryable 429/5xx from a terminal 4xx instead of parsing an error string.
type StatusError struct {
	Provider   string
	StatusCode int
	RetryAfter time.Duration
	Body       string
}

func (e *StatusError) Error() string {
	if e == nil {
		return "<nil>"
	}
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("%s provider returned HTTP %d", e.Provider, e.StatusCode)
	}
	return fmt.Sprintf("%s provider returned HTTP %d: %s", e.Provider, e.StatusCode, body)
}

// ParseRetryAfter accepts either delta-seconds or an HTTP date, the two forms
// allowed by RFC 7231. An empty or malformed value yields zero.
func ParseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(raw); err == nil {
		if wait := time.Until(when); wait > 0 {
			return wait
		}
	}
	return 0
}
