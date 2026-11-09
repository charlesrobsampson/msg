package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const defaultProviderTimeout = 10 * time.Second

// defaultProviderHTTPClient bounds CLI sends too, not just the Messenger facade.
var defaultProviderHTTPClient = newProviderHTTPClient(0)

func newProviderHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultProviderTimeout
	}
	return &http.Client{Timeout: timeout}
}

// doRequest builds a context-aware request and sends it with client. A nil
// client falls back to the bounded default.
func doRequest(ctx context.Context, client *http.Client, method, url string, body []byte, contentType string) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if client == nil {
		client = defaultProviderHTTPClient
	}
	return client.Do(req)
}

// ErrReactionsUnsupported is returned when a provider cannot attach a reaction
// to an existing message. Callers may fall back to a text acknowledgement.
var ErrReactionsUnsupported = errors.New("client: provider does not support reactions")

// Reaction targets one existing message. Recipient is a phone number or a
// provider-specific conversation ID (a group ID for groups). TargetAuthor is
// the author of the target message, and TargetTimestampMS is its provider
// timestamp.
type ReactionRequest struct {
	Recipient         string
	TargetAuthor      string
	TargetTimestampMS int64
	Emoji             string
	Remove            bool
}

// Messenger is the narrow send-only facade consumed by Aether. It deliberately
// avoids the CLI/TUI's global provider registry so multiple providers and
// profiles can coexist without changing existing UX.
type Messenger interface {
	Send(ctx context.Context, recipient, text string) error
	React(ctx context.Context, reaction ReactionRequest) error
	Name() string
}

// MessengerConfig configures a single Messenger.
type MessengerConfig struct {
	Provider string        // "signal" or "openmessage" (alias "sms")
	Account  string        // signal account, e.g. "+1234567890"
	BaseURL  string        // provider REST base URL; empty uses the provider default
	DBPath   string        // optional local cache path; empty uses the default profile cache
	Timeout  time.Duration // per-request HTTP timeout; zero uses 10s
}

// contextSender is an optional provider extension. Providers that implement it
// can honour cancellation; older providers fall back to SendMessage.
type contextSender interface {
	SendMessageContext(ctx context.Context, conversationID, text string, styled bool) (bool, error)
}

// NewMessenger builds a Messenger for one provider. Unknown providers are
// rejected rather than silently falling back to another backend.
func NewMessenger(cfg MessengerConfig) (Messenger, error) {
	client := newProviderHTTPClient(cfg.Timeout)
	switch name := strings.ToLower(strings.TrimSpace(cfg.Provider)); name {
	case "signal":
		provider := NewSignalProviderWithDB(
			"signal",
			ProviderSettings{Type: "signal", URL: cfg.BaseURL},
			cfg.Account,
			cfg.DBPath,
		)
		provider.httpClient = client
		return &providerMessenger{provider: provider, name: "signal"}, nil
	case "openmessage", "sms":
		provider := NewOpenMessageProvider(
			"sms",
			ProviderSettings{Type: "sms", URL: cfg.BaseURL},
			cfg.Account,
		)
		provider.httpClient = client
		return &providerMessenger{provider: provider, name: "openmessage"}, nil
	default:
		return nil, fmt.Errorf("client: unknown messenger provider %q", cfg.Provider)
	}
}

// providerMessenger adapts the msg Provider interface to the facade.
type providerMessenger struct {
	provider Provider
	name     string
}

func (m *providerMessenger) Send(ctx context.Context, recipient, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var (
		ok  bool
		err error
	)
	if sender, supported := m.provider.(contextSender); supported {
		ok, err = sender.SendMessageContext(ctx, recipient, text, false)
	} else {
		ok, err = m.provider.SendMessage(recipient, text, false)
	}
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("client: %s rejected message to %s", m.name, recipient)
	}
	return nil
}

func (m *providerMessenger) Name() string {
	return m.name
}

// reactionSender is the optional provider extension that attaches reactions.
type reactionSender interface {
	SendReaction(conversationID string, msg Message, emoji string, remove bool) (bool, error)
}

// contextReactionSender is the cancellation-aware variant. Providers that
// implement it are preferred so a reaction respects the queue's attempt
// deadline.
type contextReactionSender interface {
	SendReactionContext(ctx context.Context, conversationID string, msg Message, emoji string, remove bool) (bool, error)
}

func (m *providerMessenger) React(ctx context.Context, reaction ReactionRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(reaction.Emoji) == "" {
		return errors.New("client: reaction emoji is empty")
	}
	if reaction.TargetTimestampMS == 0 {
		return errors.New("client: reaction target timestamp is required")
	}

	target := Message{SenderNumber: reaction.TargetAuthor, TimestampMS: reaction.TargetTimestampMS}
	var (
		ok  bool
		err error
	)
	if sender, supported := m.provider.(contextReactionSender); supported {
		ok, err = sender.SendReactionContext(ctx, reaction.Recipient, target, reaction.Emoji, reaction.Remove)
	} else if sender, supported := m.provider.(reactionSender); supported {
		ok, err = sender.SendReaction(reaction.Recipient, target, reaction.Emoji, reaction.Remove)
	} else {
		return ErrReactionsUnsupported
	}
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("client: %s rejected reaction to %s", m.name, reaction.Recipient)
	}
	return nil
}
