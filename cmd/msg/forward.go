package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultForwardAttempts  = 3
	defaultForwardBaseDelay = 200 * time.Millisecond
	maxForwardDelay         = 2 * time.Second
	forwardHTTPTimeout      = 5 * time.Second
)

// ForwardMessage is the normalized inbound envelope POSTed to Aether's
// /receive endpoint. It mirrors plan.md §3.2.
type ForwardMessage struct {
	Sender    string `json:"sender"`
	Text      string `json:"text"`
	Provider  string `json:"provider,omitempty"`
	GroupID   string `json:"group_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
}

// Forwarder posts inbound messages to Aether with a bounded number of retries
// and exponential backoff. It never spools messages: msg's own database remains
// the replay source.
type Forwarder struct {
	URL         string
	Token       string
	Client      *http.Client
	MaxAttempts int
	BaseDelay   time.Duration
	Sleep       func(time.Duration)
	Logf        func(format string, args ...any)
}

// NewForwarder builds a forwarder. An empty URL means forwarding is disabled.
func NewForwarder(url, token string) *Forwarder {
	return &Forwarder{
		URL:         strings.TrimSpace(url),
		Token:       strings.TrimSpace(token),
		Client:      &http.Client{Timeout: forwardHTTPTimeout},
		MaxAttempts: defaultForwardAttempts,
		BaseDelay:   defaultForwardBaseDelay,
	}
}

// newForwarderFromArgs resolves the signal-receiver's forwarding flags, falling
// back to MSG_FORWARD_URL and AETHER_INGRESS_TOKEN. Flags win over the
// environment.
func newForwarderFromArgs(args []string, getenv func(string) string) *Forwarder {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	url := strings.TrimSpace(getenv("MSG_FORWARD_URL"))
	token := strings.TrimSpace(getenv("AETHER_INGRESS_TOKEN"))

	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--forward":
			if i+1 < len(args) {
				i++
				url = strings.TrimSpace(args[i])
			}
		case strings.HasPrefix(arg, "--forward="):
			url = strings.TrimSpace(strings.TrimPrefix(arg, "--forward="))
		case arg == "--forward-token":
			if i+1 < len(args) {
				i++
				token = strings.TrimSpace(args[i])
			}
		case strings.HasPrefix(arg, "--forward-token="):
			token = strings.TrimSpace(strings.TrimPrefix(arg, "--forward-token="))
		}
	}

	return NewForwarder(url, token)
}

// Enabled reports whether a destination URL was configured.
func (f *Forwarder) Enabled() bool {
	return f != nil && strings.TrimSpace(f.URL) != ""
}

// Forward delivers one message, retrying transient failures up to MaxAttempts
// times. It returns the last error after giving up; callers in the receiver log
// and ignore it so a broken Aether never fails message ingestion.
func (f *Forwarder) Forward(ctx context.Context, msg ForwardMessage) error {
	if !f.Enabled() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	attempts := f.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	delay := f.BaseDelay
	if delay < 0 {
		delay = 0
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		lastErr = f.post(ctx, msg)
		if lastErr == nil {
			return nil
		}
		if attempt == attempts {
			break
		}

		f.logf("attempt %d/%d to %s failed: %v; retrying in %s", attempt, attempts, f.URL, lastErr, delay)
		f.nap(delay)
		delay = nextForwardDelay(delay)
	}

	f.logf("delivery to %s failed after %d attempt(s): %v", f.URL, attempts, lastErr)
	return lastErr
}

func (f *Forwarder) post(ctx context.Context, msg ForwardMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal inbound message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build forward request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if f.Token != "" {
		req.Header.Set("X-Aether-Token", f.Token)
	}

	resp, err := f.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("post to %s: %w", f.URL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if text := strings.TrimSpace(string(detail)); text != "" {
			return fmt.Errorf("post to %s: status %s: %s", f.URL, resp.Status, text)
		}
		return fmt.Errorf("post to %s: status %s", f.URL, resp.Status)
	}

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

func (f *Forwarder) httpClient() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: forwardHTTPTimeout}
}

func (f *Forwarder) nap(d time.Duration) {
	if d <= 0 {
		return
	}
	if f.Sleep != nil {
		f.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (f *Forwarder) logf(format string, args ...any) {
	if f.Logf != nil {
		f.Logf(format, args...)
		return
	}
	fmt.Printf("[forward] "+format+"\n", args...)
}

func nextForwardDelay(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	d *= 2
	if d > maxForwardDelay {
		return maxForwardDelay
	}
	return d
}
