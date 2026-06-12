package e2e

import (
	"strings"
	"testing"
)

func TestConfigShow(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("config")
	if !strings.Contains(out, "editor:") {
		t.Errorf("expected 'editor:' in config output, got:\n%s", out)
	}
}

func TestConfigSetEditor(t *testing.T) {
	env := SetupTestEnv(t)
	env.Run("config", "set", "editor", "nvim")
	out := env.Run("config")
	if !strings.Contains(out, "nvim") {
		t.Errorf("expected 'nvim' in config output after set, got:\n%s", out)
	}
}

func TestConfigSetEditorConfirms(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("config", "set", "editor", "vim")
	if !strings.Contains(out, "vim") {
		t.Errorf("expected confirmation message containing 'vim', got:\n%s", out)
	}
}

func TestConfigUnknownKey(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.RunAllowError("config", "set", "badkey", "value")
	if !strings.Contains(strings.ToLower(out), "unknown") {
		t.Errorf("expected 'unknown' error for bad config key, got:\n%s", out)
	}
}
