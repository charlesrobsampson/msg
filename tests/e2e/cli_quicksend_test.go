package e2e

import (
	"strings"
	"testing"
)

func TestQuickSendViaAliasDryRun(t *testing.T) {
	env := SetupTestEnv(t)
	env.Run("alias", "qs-alice", "sms:conv-alice", "sms")
	out := env.Run("quick-send", "qs-alice", "Alias dry-run test")
	if !strings.Contains(strings.ToLower(out), "dry run") {
		t.Errorf("expected dry-run output for quick-send via alias, got:\n%s", out)
	}
	if env.MockOM.SentCount() != 0 {
		t.Errorf("expected 0 sent in dry-run, got %d", env.MockOM.SentCount())
	}
}

func TestQuickSendViaAliasCommit(t *testing.T) {
	env := SetupTestEnv(t)
	env.Run("alias", "qs-alice", "sms:conv-alice", "sms")
	env.Run("quick-send", "qs-alice", "Hello via quick-send", "--send")
	if env.MockOM.SentCount() != 1 {
		t.Errorf("expected 1 sent message via quick-send alias, got %d", env.MockOM.SentCount())
	}
	if env.MockOM.LastSent().Body != "Hello via quick-send" {
		t.Errorf("expected body 'Hello via quick-send', got %q", env.MockOM.LastSent().Body)
	}
}

func TestQuickSendViaContactName(t *testing.T) {
	env := SetupTestEnv(t)
	// "Alice Smith" is a contact; quick-send should resolve to her conversation.
	out := env.Run("quick-send", "Alice Smith", "Contact name dry-run")
	if !strings.Contains(strings.ToLower(out), "dry run") && !strings.Contains(strings.ToLower(out), "alice") && !strings.Contains(strings.ToLower(out), "conv") {
		t.Errorf("expected dry-run output resolving Alice via contact, got:\n%s", out)
	}
	if env.MockOM.SentCount() != 0 {
		t.Errorf("expected 0 sent in contact dry-run, got %d", env.MockOM.SentCount())
	}
}

func TestQuickSendShortSendFlag(t *testing.T) {
	env := SetupTestEnv(t)
	env.Run("alias", "qs-bob", "sms:conv-bob", "sms")
	env.Run("quick-send", "qs-bob", "Short flag test", "-s")
	if env.MockOM.SentCount() != 1 {
		t.Errorf("expected 1 sent message via quick-send -s, got %d", env.MockOM.SentCount())
	}
}
