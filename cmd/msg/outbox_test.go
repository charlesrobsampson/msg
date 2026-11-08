package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charlesrobsampson/msg/client"
)

// forwardInboundForTest mirrors the routing half of saveSignalPayload for tests
// that only care about per-profile enablement and metric labels. Durability is
// covered by the outbox tests.
func forwardInboundForTest(t *testing.T, account string, msg ForwardMessage) {
	t.Helper()
	profile, url, enabled, err := resolveForwardTarget(account)
	if err != nil || !enabled {
		return
	}
	inboundForwards.Enqueue(profile, url, os.Getenv("AETHER_INGRESS_TOKEN"), msg)
}

func openOutboxTestDB(t *testing.T) *client.SignalDB {
	t.Helper()
	db, err := client.OpenSignalDBAt(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("OpenSignalDBAt: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func outboxEntry(t *testing.T, profile, url string, msg ForwardMessage, createdAtMS int64) *client.ForwardOutboxEntry {
	t.Helper()
	entry, err := forwardOutboxEntry(profile, url, msg, time.UnixMilli(createdAtMS))
	if err != nil {
		t.Fatalf("forwardOutboxEntry: %v", err)
	}
	return entry
}

func mustPutForward(t *testing.T, db *client.SignalDB, entry *client.ForwardOutboxEntry) {
	t.Helper()
	inserted, err := db.PutForward(*entry)
	if err != nil {
		t.Fatalf("PutForward: %v", err)
	}
	if !inserted {
		t.Fatalf("PutForward(%q) inserted no row", entry.MessageID)
	}
}

// resetDBCache closes the receiver-wide DB cache so tests can simulate a
// process restart against the same temp HOME.
func resetDBCache(t *testing.T) {
	t.Helper()
	closeDBs := func() {
		dbCacheMu.Lock()
		for _, db := range dbByAcct {
			_ = db.Close()
		}
		dbByAcct = make(map[string]*client.SignalDB)
		dbCacheMu.Unlock()
	}
	closeDBs()
	t.Cleanup(closeDBs)
}

func quietForwardQueue(t *testing.T, depth int, registry *Metrics) *ForwardQueue {
	t.Helper()
	queue := NewForwardQueueWithMetrics(depth, registry)
	queue.newForwarder = func(url, token string) *Forwarder {
		forwarder := NewForwarder(url, token)
		forwarder.Sleep = func(time.Duration) {}
		forwarder.Logf = func(string, ...any) {}
		return forwarder
	}
	t.Cleanup(queue.Close)
	return queue
}

// TestOutboxSurvivesRestart proves a forwardable webhook save leaves a pending
// outbox row when no worker can take it, and a fresh receiver replays it once
// after the process restarts (A07).
func TestOutboxSurvivesRestart(t *testing.T) {
	t.Setenv("AETHER_INGRESS_TOKEN", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	resetDBCache(t)

	account := "+15555550401"
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	writeForwardingConfig(t, home, "config.json", account, true, srv.URL)

	// A closed queue simulates a receiver that stops before the worker can
	// deliver: the save is durable but nothing is sent.
	queue := swapForwardQueue(t, defaultForwardQueueDepth)
	queue.Close()

	webhook := httptest.NewServer(newWebhookHandler(saveSignalPayload))
	t.Cleanup(webhook.Close)

	// The same message delivered twice (signal-cli retry) must produce one row.
	for range 2 {
		resp, err := http.Post(webhook.URL, "application/json",
			strings.NewReader(webhookBody(account, "+15555550102", "!test", 1700000000400)))
		if err != nil {
			t.Fatalf("post webhook: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("webhook status = %d, want 200", resp.StatusCode)
		}
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("forward attempts before restart = %d, want 0", got)
	}

	// Restart: drop the cached connection and reopen the profile DB.
	resetDBCache(t)
	db, err := client.OpenSignalDBForProfile("")
	if err != nil {
		t.Fatalf("reopen profile DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	pending, err := db.PendingForwardCount()
	if err != nil {
		t.Fatalf("PendingForwardCount: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending rows before replay = %d, want 1", pending)
	}

	replayQueue := quietForwardQueue(t, 4, NewMetrics())
	summary, err := replayOutbox(context.Background(), replayQueue, db, "", "")
	if err != nil {
		t.Fatalf("replayOutbox: %v", err)
	}
	replayQueue.Close()

	if summary.Pending != 1 || summary.Replayed != 1 {
		t.Fatalf("replay summary = %+v, want Pending=1 Replayed=1", summary)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("forward attempts after replay = %d, want 1", got)
	}
	pending, err = db.PendingForwardCount()
	if err != nil {
		t.Fatalf("PendingForwardCount after replay: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pending rows after replay = %d, want 0", pending)
	}
}

// TestOutboxTerminalDrop proves a terminal failure clears the outbox row and
// records the terminal drop rather than retrying forever (A07).
func TestOutboxTerminalDrop(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	db := openOutboxTestDB(t)
	const profile = "work"
	msg := ForwardMessage{Sender: "+15555550102", Text: "!test", Provider: "signal", MessageID: "terminal-outbox"}
	mustPutForward(t, db, outboxEntry(t, profile, srv.URL, msg, 1700000000000))

	registry := NewMetrics()
	queue := quietForwardQueue(t, 4, registry)
	if _, err := replayOutbox(context.Background(), queue, db, profile, ""); err != nil {
		t.Fatalf("replayOutbox: %v", err)
	}
	queue.Close()

	if got := requests.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	if got := registry.Dropped(profile, DropTerminal); got != 1 {
		t.Errorf("dropped{reason=terminal} = %d, want 1", got)
	}
	pending, err := db.PendingForwardCount()
	if err != nil {
		t.Fatalf("PendingForwardCount: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pending rows after terminal failure = %d, want 0", pending)
	}
}

// TestWebhookAckFailsWhenOutboxWriteFails proves the ACK waits for the durable
// write: a closed DB yields 500 so signal-cli retries (A07).
func TestWebhookAckFailsWhenOutboxWriteFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	resetDBCache(t)

	account := "+15555550403"
	db := dbForAccount(account)
	if db == nil {
		t.Fatal("could not open the profile DB")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close profile DB: %v", err)
	}

	webhook := httptest.NewServer(newWebhookHandler(saveSignalPayload))
	t.Cleanup(webhook.Close)

	resp, err := http.Post(webhook.URL, "application/json",
		strings.NewReader(webhookBody(account, "+15555550102", "!test", 1700000000500)))
	if err != nil {
		t.Fatalf("post webhook: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("webhook status with a failed durable write = %d, want 500", resp.StatusCode)
	}
}

// TestReplayCommand proves pending rows are replayed in creation order and
// cleared after delivery, which is what `msg replay` drains (A07).
func TestReplayCommand(t *testing.T) {
	var mu sync.Mutex
	var order []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg ForwardMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Errorf("decode forward: %v", err)
		}
		mu.Lock()
		order = append(order, msg.MessageID)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)

	db := openOutboxTestDB(t)
	const profile = "work"
	for i, id := range []string{"replay-1", "replay-2"} {
		msg := ForwardMessage{Sender: "+15555550102", Text: "!test", Provider: "signal", MessageID: id}
		mustPutForward(t, db, outboxEntry(t, profile, srv.URL, msg, 1700000000000+int64(i)))
	}

	queue := quietForwardQueue(t, 4, NewMetrics())
	summary, err := replayOutbox(context.Background(), queue, db, profile, "")
	if err != nil {
		t.Fatalf("replayOutbox: %v", err)
	}
	queue.Close()

	if summary.Pending != 2 || summary.Replayed != 2 {
		t.Fatalf("replay summary = %+v, want Pending=2 Replayed=2", summary)
	}
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "replay-1" || got[1] != "replay-2" {
		t.Fatalf("delivery order = %v, want [replay-1 replay-2]", got)
	}
	pending, err := db.PendingForwardCount()
	if err != nil {
		t.Fatalf("PendingForwardCount: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pending rows after replay = %d, want 0", pending)
	}
}

// TestOutboxMetrics proves the outbox gauge falls to zero after delivery and
// the replay counter and exposition families move (A07, plan §3.19).
func TestOutboxMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)

	db := openOutboxTestDB(t)
	const profile = "work"
	msg := ForwardMessage{Sender: "+15555550102", Text: "!test", Provider: "signal", MessageID: "metrics-outbox"}
	mustPutForward(t, db, outboxEntry(t, profile, srv.URL, msg, 1700000000000))

	registry := NewMetrics()
	queue := quietForwardQueue(t, 4, registry)
	if _, err := replayOutbox(context.Background(), queue, db, profile, ""); err != nil {
		t.Fatalf("replayOutbox: %v", err)
	}
	queue.Close()

	if got := registry.OutboxReplayed(profile); got != 1 {
		t.Errorf("outbox replayed = %d, want 1", got)
	}
	if got := registry.OutboxPending(profile); got != 0 {
		t.Errorf("outbox pending after delivery = %d, want 0", got)
	}

	var exposition strings.Builder
	registry.writeExposition(&exposition)
	for _, family := range []string{forwardOutboxPendingName, forwardOutboxReplayedName} {
		if !strings.Contains(exposition.String(), family) {
			t.Errorf("exposition missing %s:\n%s", family, exposition.String())
		}
	}
}
