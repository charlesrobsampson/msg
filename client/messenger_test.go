package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubProvider is a minimal Provider whose SendMessage result is fixed. It lets
// the facade's (bool, error) normalization be tested without a real backend.
type stubProvider struct{}

func (stubProvider) ID() string          { return "stub" }
func (stubProvider) DisplayName() string { return "Stub" }
func (stubProvider) FetchConversations(int) ([]Conversation, error) {
	return nil, nil
}
func (stubProvider) SearchConversations(string, int) ([]Conversation, error) {
	return nil, nil
}
func (stubProvider) FetchMessages(string, int, int64, string) ([]Message, error) {
	return nil, nil
}
func (stubProvider) SendMessage(string, string, bool) (bool, error) { return false, nil }
func (stubProvider) MarkAsRead(string) error                        { return nil }
func (stubProvider) FetchContacts(string, int) ([]Contact, error)   { return nil, nil }
func (stubProvider) IsUnreachable() bool                            { return false }

func TestMessengerFacade(t *testing.T) {
	t.Run("unknown provider", func(t *testing.T) {
		m, err := NewMessenger(MessengerConfig{Provider: "telegram"})
		if err == nil {
			t.Fatal("NewMessenger() accepted an unknown provider")
		}
		if m != nil {
			t.Fatalf("NewMessenger() = %v, want nil messenger", m)
		}
	})

	t.Run("signal send", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		type captured struct {
			path string
			body SignalSendRequest
		}
		requests := make(chan captured, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body SignalSendRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding signal send body: %v", err)
			}
			requests <- captured{path: r.URL.Path, body: body}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"timestamp":123}`))
		}))
		defer srv.Close()

		dbPath := filepath.Join(t.TempDir(), "signal-cache.db")
		m, err := NewMessenger(MessengerConfig{
			Provider: "signal",
			Account:  "+15555550100",
			BaseURL:  srv.URL,
			DBPath:   dbPath,
		})
		if err != nil {
			t.Fatalf("NewMessenger() error = %v", err)
		}
		if m == nil {
			t.Fatal("NewMessenger() = nil, want messenger")
		}
		if got := m.Name(); got != "signal" {
			t.Fatalf("Name() = %q, want %q", got, "signal")
		}

		if err := m.Send(context.Background(), "+15555550102", "hello from facade"); err != nil {
			t.Fatalf("Send() error = %v", err)
		}

		select {
		case got := <-requests:
			wantPath := "/v1/" + url.PathEscape("+15555550100") + "/sendv2"
			if got.path != wantPath {
				t.Errorf("request path = %q, want %q", got.path, wantPath)
			}
			if got.body.Number != "+15555550100" {
				t.Errorf("account = %q, want %q", got.body.Number, "+15555550100")
			}
			if got.body.Message != "hello from facade" {
				t.Errorf("message = %q, want %q", got.body.Message, "hello from facade")
			}
			if len(got.body.Recipients) != 1 || got.body.Recipients[0] != "+15555550102" {
				t.Errorf("recipients = %v, want [+15555550102]", got.body.Recipients)
			}
		default:
			t.Fatal("signal-cli fake received no request")
		}

		if _, err := os.Stat(dbPath); err != nil {
			t.Fatalf("DBPath %q was not used: %v", dbPath, err)
		}
		if _, err := os.Stat(filepath.Join(home, ".config", "msg")); !os.IsNotExist(err) {
			t.Fatalf("facade touched $HOME: %v", err)
		}
	})

	t.Run("false nil becomes error", func(t *testing.T) {
		m := &providerMessenger{provider: stubProvider{}, name: "stub"}
		if err := m.Send(context.Background(), "+15555550102", "hello"); err == nil {
			t.Fatal("Send() returned nil for a (false, nil) provider result")
		}
	})
}

// TestMessengerSendTimeout proves Send cannot outlive MessengerConfig.Timeout,
// even when the provider keeps the request open.
func TestMessengerSendTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	defer close(block)

	m, err := NewMessenger(MessengerConfig{
		Provider: "signal",
		Account:  "+15555550100",
		BaseURL:  srv.URL,
		DBPath:   filepath.Join(t.TempDir(), "signal-cache.db"),
		Timeout:  40 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMessenger() error = %v", err)
	}

	start := time.Now()
	err = m.Send(context.Background(), "+15555550102", "hello")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Send() returned nil for a hung provider")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Send() took %s, want it bounded by the configured timeout", elapsed)
	}
}

// TestProviderStatusError proves a non-2xx provider response is typed, carries
// Retry-After, and that the legacy 404 path fallback still works.
func TestProviderStatusError(t *testing.T) {
	newMessenger := func(t *testing.T, handler http.HandlerFunc) Messenger {
		t.Helper()
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)
		m, err := NewMessenger(MessengerConfig{
			Provider: "signal",
			Account:  "+15555550100",
			BaseURL:  srv.URL,
			DBPath:   filepath.Join(t.TempDir(), "signal-cache.db"),
			Timeout:  time.Second,
		})
		if err != nil {
			t.Fatalf("NewMessenger() error = %v", err)
		}
		return m
	}

	t.Run("429 is typed and carries Retry-After", func(t *testing.T) {
		m := newMessenger(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "slow down", http.StatusTooManyRequests)
		})
		err := m.Send(context.Background(), "+15555550102", "hello")
		var statusErr *StatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("Send() error = %v, want *StatusError", err)
		}
		if statusErr.StatusCode != http.StatusTooManyRequests {
			t.Errorf("StatusCode = %d, want 429", statusErr.StatusCode)
		}
		if statusErr.RetryAfter != 2*time.Second {
			t.Errorf("RetryAfter = %s, want 2s", statusErr.RetryAfter)
		}
	})

	t.Run("500 is typed", func(t *testing.T) {
		m := newMessenger(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "broken", http.StatusInternalServerError)
		})
		err := m.Send(context.Background(), "+15555550102", "hello")
		var statusErr *StatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("Send() error = %v, want *StatusError", err)
		}
		if statusErr.StatusCode != http.StatusInternalServerError {
			t.Errorf("StatusCode = %d, want 500", statusErr.StatusCode)
		}
	})

	t.Run("404 falls back to the next signal path", func(t *testing.T) {
		var attempts int
		m := newMessenger(t, func(w http.ResponseWriter, r *http.Request) {
			attempts++
			if attempts == 1 {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"timestamp":123}`))
		})
		if err := m.Send(context.Background(), "+15555550102", "hello"); err != nil {
			t.Fatalf("Send() error = %v, want the fallback to succeed", err)
		}
		if attempts != 2 {
			t.Fatalf("attempts = %d, want 2", attempts)
		}
	})
}
