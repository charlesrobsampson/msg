package e2e

import (
	"strconv"
	"strings"
	"testing"
)

func TestListLimit(t *testing.T) {
	env := SetupTestEnv(t)
	// Seed has 3 OM conversations + 1 Signal group = 4 total; limit to 1.
	out := env.Run("list", "-l", "1")
	lines := nonEmptyLines(out)
	// Header row + separator + 1 data row = 3 lines
	if len(lines) > 3 {
		t.Errorf("expected at most 1 data row with -l 1, got %d lines:\n%s", len(lines), out)
	}
}

func TestSearchLimit(t *testing.T) {
	env := SetupTestEnv(t)
	// "a" matches at least Alice and Team Chat (has no 'a'? — Alice Smith and Signal group)
	// Use a broad query and limit to 1 to confirm limit is honoured.
	out := env.Run("search", "alice", "-l", "1")
	// Should contain Alice but not more than one data row.
	if !strings.Contains(out, "Alice") {
		t.Errorf("expected Alice in search output, got:\n%s", out)
	}
	// Count result rows (lines with a '|' character are table rows).
	tableRows := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "|") && !strings.Contains(l, "---") {
			tableRows++
		}
	}
	// Header row + 1 result row = 2; should not exceed 2 table rows.
	if tableRows > 2 {
		t.Errorf("expected at most 1 result row with -l 1, got %d table rows:\n%s", tableRows-1, out)
	}
}

func TestReadLimit(t *testing.T) {
	env := SetupTestEnv(t)
	// conv-alice has 4 messages; limit to 2.
	out := env.Run("read", "sms:conv-alice", "-l", "2", "--leave-unread")
	// The most-recent 2 messages: "Absolutely! What trail?" and "Also bring snacks please"
	if !strings.Contains(out, "bring snacks") {
		t.Errorf("expected last message in -l 2 output, got:\n%s", out)
	}
	// The oldest message should be excluded.
	if strings.Contains(out, "Are you free this weekend") {
		t.Errorf("oldest message should be excluded with -l 2, got:\n%s", out)
	}
}

func TestReadLeaveUnread(t *testing.T) {
	env := SetupTestEnv(t)
	// -u / --leave-unread should not call mark-read.
	env.Run("read", "sms:conv-alice", "-u")
	if count := env.MockOM.GetUnreadCount("conv-alice"); count != 2 {
		t.Errorf("expected unread count 2 after -u read, got %d", count)
	}
}

func TestUnreadCountFlag(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("unread", "-c")
	out = strings.TrimSpace(out)
	if _, err := strconv.Atoi(out); err != nil {
		t.Errorf("expected -c to print a plain integer, got %q", out)
	}
}

func TestUnreadCountFlagLongForm(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("unread", "--count")
	out = strings.TrimSpace(out)
	if _, err := strconv.Atoi(out); err != nil {
		t.Errorf("expected --count to print a plain integer, got %q", out)
	}
}

func TestContactsLimit(t *testing.T) {
	env := SetupTestEnv(t)
	// Seed has Alice and Bob; query "" would need a query arg, use "a" which matches Alice.
	// The contacts mock returns all matching; limit to 1.
	out := env.Run("contacts", "alice", "-l", "1")
	if !strings.Contains(out, "Alice") {
		t.Errorf("expected Alice in contacts output, got:\n%s", out)
	}
	// Should not contain Bob.
	if strings.Contains(out, "Bob") {
		t.Errorf("Bob should not appear in contacts -l 1 (only Alice matches 'alice'), got:\n%s", out)
	}
}

func TestSendStyledDryRun(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("send", "sms:conv-alice", "Hello *world*", "--styled")
	if !strings.Contains(out, "Style") {
		t.Errorf("expected 'Style' in --styled dry-run output, got:\n%s", out)
	}
	if env.MockOM.SentCount() != 0 {
		t.Errorf("expected 0 sent in --styled dry-run, got %d", env.MockOM.SentCount())
	}
}

func TestHelpFlag(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.RunAllowError("list", "--help")
	if !strings.Contains(out, "Usage: msg list") {
		t.Errorf("expected 'Usage: msg list' in --help output, got:\n%s", out)
	}
}

func TestHelpShortFlag(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.RunAllowError("read", "-h")
	if !strings.Contains(out, "Usage: msg read") {
		t.Errorf("expected 'Usage: msg read' in -h output, got:\n%s", out)
	}
}

func TestSendShortFlag(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("send", "sms:conv-alice", "Short flag test", "-s")
	outLower := strings.ToLower(out)
	if !strings.Contains(outLower, "sent") && !strings.Contains(outLower, "success") {
		t.Errorf("expected success with -s flag, got:\n%s", out)
	}
	if env.MockOM.SentCount() != 1 {
		t.Errorf("expected 1 sent message with -s, got %d", env.MockOM.SentCount())
	}
}

// nonEmptyLines returns non-blank lines from a string.
func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
