package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charlesrobsampson/msg/client"
	"github.com/charlesrobsampson/msg/tests/mocks"
)

// testBinaryPath is set once in TestMain and shared across all tests.
var testBinaryPath string

// TestEnv is a fully isolated test environment for a single test.
type TestEnv struct {
	t            *testing.T
	BinaryPath   string
	TempHome     string
	MockOM       *mocks.OpenMessageMock
	MockSignal   *mocks.SignalMock
	ReceiverPort int
}

// SetupTestEnv creates an isolated environment: temp HOME, in-process mock servers
// seeded with default data, and a running Signal webhook receiver subprocess.
func SetupTestEnv(t *testing.T) *TestEnv {
	t.Helper()

	tempHome := t.TempDir()

	mockOM := mocks.NewOpenMessageMock()
	t.Cleanup(mockOM.Close)

	mockSignal := mocks.NewSignalMock()
	t.Cleanup(mockSignal.Close)

	mocks.SeedDefault(mockOM, mockSignal)

	writeTestConfig(t, tempHome, mockOM.URL(), mockSignal.URL())

	receiverPort := findFreePort(t)

	env := &TestEnv{
		t:            t,
		BinaryPath:   testBinaryPath,
		TempHome:     tempHome,
		MockOM:       mockOM,
		MockSignal:   mockSignal,
		ReceiverPort: receiverPort,
	}

	env.startSignalReceiver()
	mockSignal.WebhookURL = fmt.Sprintf("http://127.0.0.1:%d/api/signal/webhook", receiverPort)

	return env
}

// Run executes the msg binary with the given arguments and returns combined output.
// Fails the test if the process exits non-zero.
func (e *TestEnv) Run(args ...string) string {
	e.t.Helper()
	cmd := exec.Command(e.BinaryPath, args...)
	cmd.Env = e.testEnvVars()
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("msg %s failed: %v\nOutput:\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// RunAllowError runs the binary but does not fail the test on non-zero exit.
func (e *TestEnv) RunAllowError(args ...string) string {
	e.t.Helper()
	cmd := exec.Command(e.BinaryPath, args...)
	cmd.Env = e.testEnvVars()
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func (e *TestEnv) testEnvVars() []string {
	return []string{
		"HOME=" + e.TempHome,
		"MSG_SKIP_STARTUP=true",
		"SIGNAL_ACCOUNT=" + mocks.TestSignalAccount,
		"MSG_SIGNAL_RECEIVER_PORT=" + strconv.Itoa(e.ReceiverPort),
		"PATH=" + os.Getenv("PATH"),
	}
}

func (e *TestEnv) startSignalReceiver() {
	e.t.Helper()
	cmd := exec.Command(e.BinaryPath, "signal-receiver")
	cmd.Env = []string{
		"HOME=" + e.TempHome,
		"MSG_SIGNAL_RECEIVER_PORT=" + strconv.Itoa(e.ReceiverPort),
		"PATH=" + os.Getenv("PATH"),
	}
	if err := cmd.Start(); err != nil {
		e.t.Fatalf("failed to start signal receiver: %v", err)
	}
	e.t.Cleanup(func() { cmd.Process.Kill() })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", e.ReceiverPort), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("signal receiver did not start on port %d within 5s", e.ReceiverPort)
}

func writeTestConfig(t *testing.T, home, omURL, signalURL string) {
	t.Helper()
	cfgDir := filepath.Join(home, ".config", "msg")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}

	cfg := client.Config{
		Account: mocks.TestSignalAccount,
		Providers: map[string]client.ProviderSettings{
			"openmessage": {
				Type:    "openmessage",
				Enabled: true,
				URL:     omURL,
				Port:    extractPort(omURL),
			},
			"signal": {
				Type:    "signal",
				Enabled: true,
				URL:     signalURL,
				Port:    extractPort(signalURL),
			},
		},
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal test config: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config.json")
	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		t.Fatalf("write test config: %v", err)
	}
}

// WriteProfileConfig writes a named profile config pointing at the same mock
// servers as the default env. Used to test profile isolation.
func (e *TestEnv) WriteProfileConfig(profile string) {
	e.t.Helper()
	cfgDir := filepath.Join(e.TempHome, ".config", "msg")
	cfg := client.Config{
		Account: mocks.TestSignalAccount,
		Providers: map[string]client.ProviderSettings{
			"openmessage": {Type: "openmessage", Enabled: true, URL: e.MockOM.URL()},
			"signal":      {Type: "signal", Enabled: true, URL: e.MockSignal.URL()},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		e.t.Fatalf("marshal profile config: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config-"+profile+".json")
	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		e.t.Fatalf("write profile config: %v", err)
	}
}

func findFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func extractPort(rawURL string) int {
	// "http://127.0.0.1:12345" → 12345
	parts := strings.Split(rawURL, ":")
	if len(parts) < 3 {
		return 0
	}
	port, _ := strconv.Atoi(parts[2])
	return port
}
