package e2e

import (
	"strings"
	"testing"
)

func TestProviderStatus(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("provider", "status")
	outLower := strings.ToLower(out)
	if !strings.Contains(outLower, "connected") {
		t.Errorf("expected 'connected' in provider status output, got:\n%s", out)
	}
}

func TestProviderReconnect(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("provider", "reconnect", "google")
	outLower := strings.ToLower(out)
	if !strings.Contains(outLower, "reconnect") {
		t.Errorf("expected reconnect message in output, got:\n%s", out)
	}
	if !env.MockOM.ReconnectCalled() {
		t.Errorf("expected /api/google/reconnect to be called on the mock")
	}
}

func TestProviderDisconnect(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("provider", "disconnect", "google")
	outLower := strings.ToLower(out)
	if !strings.Contains(outLower, "disconnect") {
		t.Errorf("expected disconnect message in output, got:\n%s", out)
	}
	if !env.MockOM.DisconnectCalled() {
		t.Errorf("expected /api/unpair to be called on the mock")
	}
}

func TestProviderStatusSubcommand(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("provider", "status")
	// Should mention Google Messages or OpenMessage
	if !strings.Contains(out, "Google") && !strings.Contains(out, "OpenMessage") {
		t.Errorf("expected provider name in status output, got:\n%s", out)
	}
}

func TestProviderUnknownSubcommand(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.RunAllowError("provider", "badsubcmd")
	if !strings.Contains(strings.ToLower(out), "usage") {
		t.Errorf("expected usage hint for unknown provider subcommand, got:\n%s", out)
	}
}

func TestProviderReconnectUnknown(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.RunAllowError("provider", "reconnect", "unknown-provider")
	if !strings.Contains(strings.ToLower(out), "unknown") {
		t.Errorf("expected 'unknown provider' message, got:\n%s", out)
	}
}

func TestProviderHelp(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.RunAllowError("provider", "--help")
	if !strings.Contains(out, "Usage: msg provider") {
		t.Errorf("expected 'Usage: msg provider' in --help output, got:\n%s", out)
	}
}
