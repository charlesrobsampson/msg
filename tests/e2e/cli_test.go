package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charlesrobsampson/msg/tests/mocks"
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	// Allow a pre-built binary (Docker/CI sets MSG_TEST_BINARY to skip the build step).
	if envBin := os.Getenv("MSG_TEST_BINARY"); envBin != "" {
		testBinaryPath = envBin
		return m.Run()
	}

	tmpDir, err := os.MkdirTemp("", "msg-e2e-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to create temp dir:", err)
		return 1
	}
	defer os.RemoveAll(tmpDir)

	testBinaryPath = filepath.Join(tmpDir, "msg")

	// Resolve project root from this package (tests/e2e/).
	wd, _ := os.Getwd()
	projectRoot, _ := filepath.Abs(filepath.Join(wd, "../.."))

	build := exec.Command("go", "build", "-o", testBinaryPath, "./cmd/msg")
	build.Dir = projectRoot
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintln(os.Stderr, "failed to build msg binary:")
		fmt.Fprintln(os.Stderr, string(out))
		return 1
	}

	return m.Run()
}

// --- list ---

func TestList(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("list")
	for _, want := range []string{"Alice Smith", "Bob Jones", "Team Chat"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in list output, got:\n%s", want, out)
		}
	}
}

func TestListShowsSignalConversations(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("list")
	if !strings.Contains(out, "Signal Group Chat") {
		t.Errorf("expected Signal Group Chat in list, got:\n%s", out)
	}
}

func TestListUnreadCount(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("list")
	// Alice has 2 unreads — the count should appear somewhere near her name.
	if !strings.Contains(out, "2") {
		t.Errorf("expected unread count (2) in list output, got:\n%s", out)
	}
}

// --- search ---

func TestSearchFindsMatch(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("search", "alice")
	if !strings.Contains(out, "Alice Smith") {
		t.Errorf("expected Alice Smith in search results, got:\n%s", out)
	}
}

func TestSearchExcludesNonMatch(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("search", "alice")
	if strings.Contains(out, "Bob Jones") {
		t.Errorf("did not expect Bob Jones in search for 'alice', got:\n%s", out)
	}
}

// --- read ---

func TestReadShowsMessages(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("read", "openmessage:conv-alice", "--leave-unread")
	for _, want := range []string{"Are you free this weekend", "What trail", "bring snacks"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in read output, got:\n%s", want, out)
		}
	}
}

func TestReadMarksConversationRead(t *testing.T) {
	env := SetupTestEnv(t)
	// Read without --leave-unread should call mark-read on the mock.
	env.Run("read", "openmessage:conv-alice")
	// After mark-read the in-memory unread count drops to 0.
	if count := env.MockOM.GetUnreadCount("conv-alice"); count != 0 {
		t.Errorf("expected unread count 0 after read, got %d", count)
	}
}

// --- send ---

func TestSendDryRunDoesNotSend(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("send", "sms:conv-alice", "Should not be sent")
	// Dry-run output should reference --send flag.
	if !strings.Contains(out, "--send") {
		t.Errorf("expected --send hint in dry-run output, got:\n%s", out)
	}
	if env.MockOM.SentCount() != 0 {
		t.Errorf("expected 0 sent messages in dry run, got %d", env.MockOM.SentCount())
	}
}

func TestSendCommitDelivers(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("send", "sms:conv-alice", "Hello from e2e test", "--send")
	outLower := strings.ToLower(out)
	if !strings.Contains(outLower, "sent") && !strings.Contains(outLower, "success") {
		t.Errorf("expected success in send output, got:\n%s", out)
	}
	if env.MockOM.SentCount() != 1 {
		t.Errorf("expected 1 sent message, got %d", env.MockOM.SentCount())
	}
	if env.MockOM.LastSent().Body != "Hello from e2e test" {
		t.Errorf("expected body 'Hello from e2e test', got %q", env.MockOM.LastSent().Body)
	}
}

// --- unread ---

func TestUnreadShowsUnreadConversations(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("unread")
	if !strings.Contains(out, "Alice") {
		t.Errorf("expected Alice (2 unreads) in unread output, got:\n%s", out)
	}
}

func TestUnreadExcludesReadConversations(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("unread")
	// Bob has 0 unreads and should not appear unless the output is empty.
	if strings.Contains(out, "Bob Jones") && !strings.Contains(out, "No unread") {
		t.Errorf("did not expect Bob Jones (0 unreads) in unread output, got:\n%s", out)
	}
}

// --- contacts ---

func TestContactsSearch(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("contacts", "alice")
	if !strings.Contains(out, "Alice Smith") {
		t.Errorf("expected Alice Smith in contacts, got:\n%s", out)
	}
	if !strings.Contains(out, mocks.TestContact1Number) {
		t.Errorf("expected %s in contacts, got:\n%s", mocks.TestContact1Number, out)
	}
}

func TestContactsMergedAcrossProviders(t *testing.T) {
	env := SetupTestEnv(t)
	// Alice exists in both OpenMessage and Signal contacts with the same phone number.
	// The client layer merges by number so she should appear exactly once.
	out := env.Run("contacts", "alice")
	count := strings.Count(out, "Alice Smith")
	if count != 1 {
		t.Errorf("expected Alice Smith once (merged across providers), got %d occurrences in:\n%s", count, out)
	}
}

// --- alias ---

func TestAliasCreateEnablesShortcutSend(t *testing.T) {
	env := SetupTestEnv(t)

	// Create alias pointing at Alice's conversation.
	env.Run("alias", "alice-msg", "openmessage:conv-alice", "openmessage")

	// Dry-run via alias — should resolve and show preview.
	out := env.Run("send", "alice-msg", "Alias test")
	if !strings.Contains(out, "--send") {
		t.Errorf("expected --send hint after alias-resolved dry run, got:\n%s", out)
	}
	// Nothing should have been sent.
	if env.MockOM.SentCount() != 0 {
		t.Errorf("expected 0 sent messages after alias dry run, got %d", env.MockOM.SentCount())
	}
}

func TestAliasCreateAndSendCommit(t *testing.T) {
	env := SetupTestEnv(t)
	env.Run("alias", "alice-msg", "openmessage:conv-alice", "openmessage")
	env.Run("send", "alice-msg", "Hi via alias", "--send")
	if env.MockOM.SentCount() != 1 {
		t.Errorf("expected 1 sent message via alias, got %d", env.MockOM.SentCount())
	}
	if env.MockOM.LastSent().Body != "Hi via alias" {
		t.Errorf("expected body 'Hi via alias', got %q", env.MockOM.LastSent().Body)
	}
}

// --- signal ---

func TestSignalSendDryRun(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("send", "signal:"+mocks.TestContact1Number, "Signal test")
	if !strings.Contains(out, "--send") {
		t.Errorf("expected dry-run --send hint, got:\n%s", out)
	}
	if env.MockSignal.SentCount() != 0 {
		t.Errorf("expected 0 Signal sends in dry run, got %d", env.MockSignal.SentCount())
	}
}

func TestSignalSendCommit(t *testing.T) {
	env := SetupTestEnv(t)
	out := env.Run("send", "signal:"+mocks.TestContact1Number, "Hey via Signal", "--send")
	outLower := strings.ToLower(out)
	if !strings.Contains(outLower, "sent") && !strings.Contains(outLower, "success") {
		t.Errorf("expected success in Signal send output, got:\n%s", out)
	}
	if env.MockSignal.SentCount() != 1 {
		t.Errorf("expected 1 Signal send, got %d", env.MockSignal.SentCount())
	}
	if env.MockSignal.LastSent().Body != "Hey via Signal" {
		t.Errorf("expected body 'Hey via Signal', got %q", env.MockSignal.LastSent().Body)
	}
}

// TestSignalSendAppearsInRead guards the "send → cache → read" round-trip that
// the TUI relies on: after a successful send the sent message must be retrievable
// via `msg read` (i.e., it must be written to the local Signal cache DB).
func TestSignalSendAppearsInRead(t *testing.T) {
	env := SetupTestEnv(t)
	body := "Round-trip test message"
	env.Run("send", "signal:"+mocks.TestContact1Number, body, "--send")

	// Give the sync webhook a moment to be saved (the mock fires it async).
	time.Sleep(200 * time.Millisecond)

	out := env.Run("read", "signal:"+mocks.TestContact1Number, "--leave-unread")
	if !strings.Contains(out, body) {
		t.Errorf("sent message %q not found in read output after send:\n%s", body, out)
	}
}

func TestSignalIncomingRoundTrip(t *testing.T) {
	env := SetupTestEnv(t)

	ts := time.Now().UnixMilli()
	env.MockSignal.InjectIncoming(mocks.TestContact2Number, "Bob Jones", "Incoming Signal message!", ts)

	// Allow the receiver goroutine time to write to SQLite.
	time.Sleep(300 * time.Millisecond)

	out := env.Run("read", "signal:"+mocks.TestContact2Number, "--leave-unread")
	if !strings.Contains(out, "Incoming Signal message!") {
		t.Errorf("expected injected Signal message in read output, got:\n%s", out)
	}
}

// TestProfileIsolatesSignalCache guards against the bug where all profiles
// shared the same signal-cache.db, causing messages from one account to bleed
// into another account's inbox.
func TestProfileIsolatesSignalCache(t *testing.T) {
	env := SetupTestEnv(t) // default profile

	// Write a named profile config pointing at the same mock servers.
	env.WriteProfileConfig("isolated")

	// Inject a message into the default profile via the running receiver.
	ts := time.Now().UnixMilli()
	env.MockSignal.InjectIncoming(mocks.TestContact2Number, "Bob Jones", "Should stay in default profile", ts)
	time.Sleep(300 * time.Millisecond)

	// Default profile: message must be visible.
	defaultOut := env.Run("read", "signal:"+mocks.TestContact2Number, "--leave-unread")
	if !strings.Contains(defaultOut, "Should stay in default profile") {
		t.Fatalf("default profile should see the injected message, got:\n%s", defaultOut)
	}

	// Named profile: different DB file — message must NOT be visible.
	isolatedOut := env.Run("--profile", "isolated", "read", "signal:"+mocks.TestContact2Number, "--leave-unread")
	if strings.Contains(isolatedOut, "Should stay in default profile") {
		t.Errorf("profile 'isolated' should have an empty Signal cache; message leaked from default profile:\n%s", isolatedOut)
	}
}

