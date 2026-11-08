package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestMetricsEndpoint proves the loopback listener exposes the plan §3.19 msg
// families in Prometheus text format and that an empty address disables it.
func TestMetricsEndpoint(t *testing.T) {
	registry := NewMetrics()
	registry.RecordAttempt("primary", AttemptOK, 5*time.Millisecond)
	registry.SetQueueDepth("primary", 3)
	registry.RecordDropped("primary", DropQueueFull)

	addr, stop, err := startMetricsServer("127.0.0.1:0", registry)
	if err != nil {
		t.Fatalf("startMetricsServer: %v", err)
	}
	t.Cleanup(stop)

	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("/metrics Content-Type = %q, want text/plain", got)
	}

	text := string(body)
	for _, want := range []string{
		"# TYPE msg_forward_attempts_total counter",
		`msg_forward_attempts_total{profile="primary",result="ok"} 1`,
		"# TYPE msg_forward_duration_seconds summary",
		`msg_forward_duration_seconds{profile="primary",result="ok"}_sum`,
		`msg_forward_duration_seconds{profile="primary",result="ok"}_count 1`,
		"# TYPE msg_forward_queue_depth gauge",
		`msg_forward_queue_depth{profile="primary"} 3`,
		"# TYPE msg_forward_dropped_total counter",
		`msg_forward_dropped_total{profile="primary",reason="queue_full"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics body is missing %q:\n%s", want, text)
		}
	}

	// An empty address disables the listener instead of failing.
	disabledAddr, disabledStop, err := startMetricsServer("", registry)
	if err != nil {
		t.Fatalf("startMetricsServer(empty): %v", err)
	}
	if disabledAddr != "" {
		t.Errorf("disabled metrics address = %q, want empty", disabledAddr)
	}
	disabledStop()

	// Metrics must never bind a public interface.
	if _, _, err := startMetricsServer("0.0.0.0:0", registry); err == nil {
		t.Error("startMetricsServer accepted a non-loopback address")
	}
}
