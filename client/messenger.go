package client

import (
	"context"
	"fmt"
	"strings"
)

// Messenger is the narrow send-only facade consumed by Aether. It deliberately
// avoids the CLI/TUI's global provider registry so multiple providers and
// profiles can coexist without changing existing UX.
type Messenger interface {
	Send(ctx context.Context, recipient, text string) error
	Name() string
}

// MessengerConfig configures a single Messenger.
type MessengerConfig struct {
	Provider string // "signal" or "openmessage" (alias "sms")
	Account  string // signal account, e.g. "+1234567890"
	BaseURL  string // provider REST base URL; empty uses the provider default
	DBPath   string // optional local cache path; empty uses the default profile cache
}

// NewMessenger builds a Messenger for one provider. Unknown providers are
// rejected rather than silently falling back to another backend.
func NewMessenger(cfg MessengerConfig) (Messenger, error) {
	switch name := strings.ToLower(strings.TrimSpace(cfg.Provider)); name {
	case "signal":
		provider := NewSignalProviderWithDB(
			"signal",
			ProviderSettings{Type: "signal", URL: cfg.BaseURL},
			cfg.Account,
			cfg.DBPath,
		)
		return &providerMessenger{provider: provider, name: "signal"}, nil
	case "openmessage", "sms":
		provider := NewOpenMessageProvider(
			"sms",
			ProviderSettings{Type: "sms", URL: cfg.BaseURL},
			cfg.Account,
		)
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
	ok, err := m.provider.SendMessage(recipient, text, false)
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
