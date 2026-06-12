package e2e

import (
	"strings"
	"testing"
)

func TestReadMissingID(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.RunAllowError("read")
	if !strings.Contains(strings.ToLower(out), "error") && !strings.Contains(strings.ToLower(out), "missing") {
		t.Errorf("expected error message for 'read' with no args, got:\n%s", out)
	}
}

func TestSendMissingBody(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.RunAllowError("send", "sms:conv-alice")
	if !strings.Contains(strings.ToLower(out), "error") && !strings.Contains(strings.ToLower(out), "missing") {
		t.Errorf("expected error message for 'send' with no body, got:\n%s", out)
	}
	if env.MockOM.SentCount() != 0 {
		t.Errorf("expected 0 sent messages for missing-body send, got %d", env.MockOM.SentCount())
	}
}

func TestContactsMissingQuery(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.RunAllowError("contacts")
	if !strings.Contains(strings.ToLower(out), "error") && !strings.Contains(strings.ToLower(out), "missing") {
		t.Errorf("expected error message for 'contacts' with no query, got:\n%s", out)
	}
}

func TestQuickSendMissingArgs(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.RunAllowError("quick-send", "alice")
	if !strings.Contains(strings.ToLower(out), "error") && !strings.Contains(strings.ToLower(out), "missing") {
		t.Errorf("expected error message for 'quick-send' with one arg, got:\n%s", out)
	}
}

func TestAliasMissingArgs(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.RunAllowError("alias", "a", "b")
	if !strings.Contains(strings.ToLower(out), "error") && !strings.Contains(strings.ToLower(out), "missing") {
		t.Errorf("expected error message for 'alias' with too few args, got:\n%s", out)
	}
}

func TestUnknownFlagList(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.RunAllowError("list", "--badFlag")
	if !strings.Contains(out, "unknown flag") {
		t.Errorf("expected 'unknown flag' error for --badFlag on list, got:\n%s", out)
	}
}

func TestUnknownFlagUnread(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.RunAllowError("unread", "--badFlag")
	if !strings.Contains(out, "unknown flag") {
		t.Errorf("expected 'unknown flag' error for --badFlag on unread, got:\n%s", out)
	}
}

func TestUnknownFlagSend(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.RunAllowError("send", "sms:conv-alice", "body", "--badFlag")
	if !strings.Contains(out, "unknown flag") {
		t.Errorf("expected 'unknown flag' error for --badFlag on send, got:\n%s", out)
	}
	if env.MockOM.SentCount() != 0 {
		t.Errorf("expected 0 sent messages when unknown flag used, got %d", env.MockOM.SentCount())
	}
}

func TestUnknownFlagPrintsCommandHelp(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.RunAllowError("list", "--badFlag")
	if !strings.Contains(out, "Usage: msg list") {
		t.Errorf("expected command help after unknown flag, got:\n%s", out)
	}
}
