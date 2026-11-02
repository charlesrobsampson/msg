package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
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

	t.Run("flags override env and env is the fallback", func(t *testing.T) {
		env := func(key string) string {
			switch key {
			case "MSG_FORWARD_URL":
				return "http://env.example/receive"
			case "AETHER_INGRESS_TOKEN":
				return "env-token"
			default:
				return ""
			}
		}

		fromEnv := newForwarderFromArgs(nil, env)
		if fromEnv.URL != "http://env.example/receive" || fromEnv.Token != "env-token" {
			t.Fatalf("env forwarder = %+v, want env URL/token", fromEnv)
		}

		fromFlags := newForwarderFromArgs([]string{
			"--forward", "http://flag.example/receive",
			"--forward-token=flag-token",
		}, env)
		if fromFlags.URL != "http://flag.example/receive" || fromFlags.Token != "flag-token" {
			t.Fatalf("flag forwarder = %+v, want flag URL/token", fromFlags)
		}
	})
}
