package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type forwardedRequest struct {
	method      string
	path        string
	contentType string
	token       string
	body        ForwardMessage
}

func TestForwardInbound(t *testing.T) {
	t.Run("posts normalized inbound", func(t *testing.T) {
		requests := make(chan forwardedRequest, 8)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body ForwardMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding forwarded body: %v", err)
			}
			requests <- forwardedRequest{
				method:      r.Method,
				path:        r.URL.Path,
				contentType: r.Header.Get("Content-Type"),
				token:       r.Header.Get("X-Aether-Token"),
				body:        body,
			}
			w.WriteHeader(http.StatusAccepted)
		}))
		defer srv.Close()

		spoolDir := t.TempDir()
		t.Setenv("TMPDIR", spoolDir)

		f := NewForwarder(srv.URL, "ingress-secret")
		f.Sleep = func(time.Duration) { t.Fatal("sleep called on a successful first attempt") }
		f.Logf = func(string, ...any) {}

		want := ForwardMessage{
			Sender:    "+15555550102",
			Text:      "!test",
			Provider:  "signal",
			GroupID:   "group.abc123",
			MessageID: "rcv-1700000000000-+15555550102",
		}
		if err := f.Forward(context.Background(), want); err != nil {
			t.Fatalf("Forward() error = %v", err)
		}

		select {
		case got := <-requests:
			if got.method != http.MethodPost {
				t.Errorf("method = %q, want POST", got.method)
			}
			if got.path != "/" {
				t.Errorf("path = %q, want /", got.path)
			}
			if !strings.HasPrefix(got.contentType, "application/json") {
				t.Errorf("content type = %q, want application/json", got.contentType)
			}
			if got.token != "ingress-secret" {
				t.Errorf("X-Aether-Token = %q, want %q", got.token, "ingress-secret")
			}
			if !reflect.DeepEqual(got.body, want) {
				t.Errorf("forwarded body = %+v, want %+v", got.body, want)
			}
		default:
			t.Fatal("fake Aether received no request")
		}

		entries, err := os.ReadDir(spoolDir)
		if err != nil {
			t.Fatalf("reading temp dir: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("forwarding created spool files: %v", entries)
		}
	})

	t.Run("retries with bounded backoff then succeeds", func(t *testing.T) {
		var attempts int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			if attempts < 3 {
				http.Error(w, "not ready", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		}))
		defer srv.Close()

		var delays []time.Duration
		f := NewForwarder(srv.URL, "")
		f.MaxAttempts = 3
		f.BaseDelay = 10 * time.Millisecond
		f.Sleep = func(d time.Duration) { delays = append(delays, d) }
		f.Logf = func(string, ...any) {}

		if err := f.Forward(context.Background(), ForwardMessage{Sender: "+1", Text: "hi"}); err != nil {
			t.Fatalf("Forward() error = %v", err)
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3", attempts)
		}
		if want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}; !reflect.DeepEqual(delays, want) {
			t.Fatalf("backoff delays = %v, want %v", delays, want)
		}
	})

	t.Run("gives up after bounded attempts and logs", func(t *testing.T) {
		var attempts int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			http.Error(w, "still broken", http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		var delays []time.Duration
		var logs []string
		f := NewForwarder(srv.URL, "")
		f.MaxAttempts = 3
		f.BaseDelay = time.Millisecond
		f.Sleep = func(d time.Duration) { delays = append(delays, d) }
		f.Logf = func(format string, args ...any) { logs = append(logs, format) }

		if err := f.Forward(context.Background(), ForwardMessage{Sender: "+1", Text: "hi"}); err == nil {
			t.Fatal("Forward() returned nil for an unreachable Aether")
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3", attempts)
		}
		if len(delays) != 2 {
			t.Fatalf("backoff sleeps = %d, want 2", len(delays))
		}
		if len(logs) == 0 {
			t.Fatal("Forward() did not log the failed delivery")
		}
	})

	t.Run("disabled without a URL", func(t *testing.T) {
		f := NewForwarder("", "ingress-secret")
		if f.Enabled() {
			t.Fatal("Enabled() = true for an empty URL")
		}
		if err := f.Forward(context.Background(), ForwardMessage{Sender: "+1", Text: "hi"}); err != nil {
			t.Fatalf("Forward() error = %v, want nil when disabled", err)
		}
	})

}

type forwardedCapture struct {
	token string
	body  ForwardMessage
}

// TestForwardRetryClassification proves terminal 4xx responses are attempted
// once, while 429 and 5xx responses retry and honour Retry-After.
func TestForwardRetryClassification(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		retryAfter  string
		wantAttempt int32
	}{
		{name: "400 terminal", status: http.StatusBadRequest, wantAttempt: 1},
		{name: "401 terminal", status: http.StatusUnauthorized, wantAttempt: 1},
		{name: "403 terminal", status: http.StatusForbidden, wantAttempt: 1},
		{name: "404 terminal", status: http.StatusNotFound, wantAttempt: 1},
		{name: "409 terminal", status: http.StatusConflict, wantAttempt: 1},
		{name: "429 retryable", status: http.StatusTooManyRequests, retryAfter: "2", wantAttempt: 3},
		{name: "500 retryable", status: http.StatusInternalServerError, wantAttempt: 3},
		{name: "503 retryable", status: http.StatusServiceUnavailable, wantAttempt: 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var attempts int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&attempts, 1)
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				http.Error(w, http.StatusText(tc.status), tc.status)
			}))
			defer srv.Close()

			var delays []time.Duration
			f := NewForwarder(srv.URL, "")
			f.MaxAttempts = 3
			f.BaseDelay = time.Millisecond
			f.Sleep = func(d time.Duration) { delays = append(delays, d) }
			f.Logf = func(string, ...any) {}

			err := f.Forward(context.Background(), ForwardMessage{Sender: "+1", Text: "hi"})
			if err == nil {
				t.Fatal("Forward() returned nil for a failing Aether")
			}
			if got := atomic.LoadInt32(&attempts); got != tc.wantAttempt {
				t.Fatalf("attempts = %d, want %d", got, tc.wantAttempt)
			}
			if tc.retryAfter != "" {
				want := 2 * time.Second
				if len(delays) != 2 {
					t.Fatalf("backoff sleeps = %d, want 2", len(delays))
				}
				for i, delay := range delays {
					if delay < want {
						t.Errorf("delay %d = %s, want at least %s from Retry-After", i, delay, want)
					}
				}
			}
		})
	}
}

func TestPerProfileForwarding(t *testing.T) {
	t.Setenv("AETHER_INGRESS_TOKEN", "")
	swapForwardQueue(t, defaultForwardQueueDepth)

	newCaptureServer := func(t *testing.T) (*httptest.Server, chan forwardedCapture) {
		t.Helper()
		got := make(chan forwardedCapture, 4)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body ForwardMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode forwarded body: %v", err)
			}
			got <- forwardedCapture{token: r.Header.Get("X-Aether-Token"), body: body}
			w.WriteHeader(http.StatusAccepted)
		}))
		t.Cleanup(srv.Close)
		return srv, got
	}

	writeConfig := func(t *testing.T, home, filename, account string, enabled bool, url string) {
		t.Helper()
		dir := filepath.Join(home, ".config", "msg")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir config dir: %v", err)
		}
		body := fmt.Sprintf(`{"account":%q,"forwarding":{"enabled":%t,"url":%q}}`, account, enabled, url)
		if err := os.WriteFile(filepath.Join(dir, filename), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", filename, err)
		}
	}

	want := ForwardMessage{
		Sender:    "+15555550102",
		Text:      "!test",
		Provider:  "signal",
		MessageID: "m1",
	}

	expectForwarded := func(t *testing.T, got chan forwardedCapture) forwardedCapture {
		t.Helper()
		select {
		case captured := <-got:
			if !reflect.DeepEqual(captured.body, want) {
				t.Fatalf("forwarded body = %+v, want %+v", captured.body, want)
			}
			return captured
		case <-time.After(2 * time.Second):
			t.Fatal("enabled profile did not forward")
			return forwardedCapture{}
		}
	}

	expectNotForwarded := func(t *testing.T, got chan forwardedCapture) {
		t.Helper()
		select {
		case captured := <-got:
			t.Fatalf("forwarded unexpectedly: %+v", captured)
		case <-time.After(100 * time.Millisecond):
		}
	}

	t.Run("only the enabled profile posts", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		srv, got := newCaptureServer(t)
		writeConfig(t, home, "config.json", "+15555550100", true, srv.URL)
		writeConfig(t, home, "config-bob.json", "+15555550101", false, srv.URL)

		forwardInbound(context.Background(), "+15555550100", want)
		expectForwarded(t, got)

		forwardInbound(context.Background(), "+15555550101", want)
		expectNotForwarded(t, got)
	})

	t.Run("toggling the profile stops the next message", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		srv, got := newCaptureServer(t)
		writeConfig(t, home, "config.json", "+15555550100", true, srv.URL)

		forwardInbound(context.Background(), "+15555550100", want)
		expectForwarded(t, got)

		writeConfig(t, home, "config.json", "+15555550100", false, srv.URL)
		forwardInbound(context.Background(), "+15555550100", want)
		expectNotForwarded(t, got)
	})

	t.Run("enabled with an empty url does not post", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		_, got := newCaptureServer(t)
		writeConfig(t, home, "config.json", "+15555550100", true, "")

		forwardInbound(context.Background(), "+15555550100", want)
		expectNotForwarded(t, got)
	})

	t.Run("token comes from the environment", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("AETHER_INGRESS_TOKEN", "ingress-secret")
		srv, got := newCaptureServer(t)
		writeConfig(t, home, "config.json", "+15555550100", true, srv.URL)

		forwardInbound(context.Background(), "+15555550100", want)
		if captured := expectForwarded(t, got); captured.token != "ingress-secret" {
			t.Fatalf("X-Aether-Token = %q, want %q", captured.token, "ingress-secret")
		}
	})

	t.Run("missing config does not forward", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		_, got := newCaptureServer(t)

		forwardInbound(context.Background(), "+15555550100", want)
		expectNotForwarded(t, got)
	})

	t.Run("malformed config does not forward", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		_, got := newCaptureServer(t)
		dir := filepath.Join(home, ".config", "msg")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir config dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"forwarding":`), 0o644); err != nil {
			t.Fatalf("write malformed config: %v", err)
		}

		forwardInbound(context.Background(), "+15555550100", want)
		expectNotForwarded(t, got)
	})
}

// writeForwardingConfig writes one profile config under a temp HOME.
func writeForwardingConfig(t *testing.T, home, filename, account string, enabled bool, url string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "msg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	body := fmt.Sprintf(`{"account":%q,"forwarding":{"enabled":%t,"url":%q}}`, account, enabled, url)
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
}

// swapForwardQueue installs a test queue in place of the receiver's global one
// and restores (and drains) it when the test ends.
func swapForwardQueue(t *testing.T, depth int) *ForwardQueue {
	t.Helper()
	previous := inboundForwards
	queue := NewForwardQueue(depth)
	inboundForwards = queue
	t.Cleanup(func() {
		queue.Close()
		inboundForwards = previous
	})
	return queue
}

func webhookBody(account, sender, text string, ts int64) string {
	return fmt.Sprintf(`{"account":%q,"envelope":{"source":%q,"timestamp":%d,"dataMessage":{"message":%q,"timestamp":%d}}}`,
		account, sender, ts, text, ts)
}

// TestForwardDoesNotBlockWebhook proves the webhook ACK happens after the DB
// write but before the forward completes: the fake Aether is still blocked when
// the client receives 200.
func TestForwardDoesNotBlockWebhook(t *testing.T) {
	t.Setenv("AETHER_INGRESS_TOKEN", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	account := "+15555550301"
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	arrived := make(chan ForwardMessage, 1)

	forwardSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg ForwardMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Errorf("decoding forwarded body: %v", err)
		}
		arrived <- msg
		<-release
		w.WriteHeader(http.StatusAccepted)
	}))
	// Register the server close before the queue swap so the queue's cleanup
	// (which drains jobs) runs first. The unblock cleanup is registered last,
	// so it runs before both even when an assertion fails early.
	t.Cleanup(forwardSrv.Close)
	writeForwardingConfig(t, home, "config.json", account, true, forwardSrv.URL)
	swapForwardQueue(t, defaultForwardQueueDepth)
	t.Cleanup(unblock)
	// Warm the profile DB so the measured ACK covers the write, not the
	// first-time SQLite open (which is slow under -race).
	if dbForAccount(account) == nil {
		t.Fatal("could not open the profile DB")
	}

	webhook := newWebhookHandler(saveSignalPayload)
	srv := httptest.NewServer(webhook)
	defer srv.Close()

	start := time.Now()
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(webhookBody(account, "+15555550102", "!test", 1700000000000)))
	if err != nil {
		t.Fatalf("post webhook: %v", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook status = %d, want 200", resp.StatusCode)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("webhook ACK took %s while the forward was blocked, want under 100ms", elapsed)
	}

	select {
	case got := <-arrived:
		if got.Sender != "+15555550102" || got.Text != "!test" {
			t.Fatalf("forwarded message = %+v, want sender/text preserved", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forward never reached the fake Aether")
	}
	unblock()
}

// TestForwardQueueFullDoesNotBlock proves a saturated forward queue drops the
// newest message, counts it, and still ACKs the webhook immediately.
func TestForwardQueueFullDoesNotBlock(t *testing.T) {
	t.Setenv("AETHER_INGRESS_TOKEN", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	account := "+15555550302"
	gate := make(chan struct{})
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gate) }) }
	started := make(chan struct{}, 1)

	forwardSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-gate
		w.WriteHeader(http.StatusAccepted)
	}))
	// Register the server close before the queue swap so the queue's cleanup
	// (which drains buffered jobs) runs first. The gate cleanup is registered
	// last, so it runs before both even when an assertion fails early.
	t.Cleanup(forwardSrv.Close)
	writeForwardingConfig(t, home, "config.json", account, true, forwardSrv.URL)
	queue := swapForwardQueue(t, 1)
	t.Cleanup(openGate)
	if dbForAccount(account) == nil {
		t.Fatal("could not open the profile DB")
	}

	webhook := newWebhookHandler(saveSignalPayload)
	srv := httptest.NewServer(webhook)
	defer srv.Close()

	post := func(ts int64) {
		t.Helper()
		start := time.Now()
		resp, err := http.Post(srv.URL, "application/json", strings.NewReader(webhookBody(account, "+15555550102", "!test", ts)))
		if err != nil {
			t.Fatalf("post webhook: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("webhook status = %d, want 200", resp.StatusCode)
		}
		if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
			t.Fatalf("webhook ACK took %s with a saturated forward queue", elapsed)
		}
	}

	// The first job occupies the worker; the second fills the depth-1 buffer;
	// the third finds it full and is dropped.
	post(1700000000100)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("forward worker never started")
	}
	post(1700000000200)
	post(1700000000300)

	if got := queue.Dropped(); got != 1 {
		t.Fatalf("dropped forwards = %d, want 1", got)
	}
	openGate()
}
