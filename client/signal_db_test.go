package client

import (
	"path/filepath"
	"testing"
)

// TestForwardOutboxPersistence proves the message and outbox row commit
// together, duplicate message IDs do not add rows, and pending rows can be
// listed, counted, and deleted (A07).
func TestForwardOutboxPersistence(t *testing.T) {
	db, err := OpenSignalDBAt(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("OpenSignalDBAt: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	entry := ForwardOutboxEntry{
		Profile:     "work",
		MessageID:   "rcv-1",
		URL:         "http://127.0.0.1:9/receive",
		Payload:     `{"sender":"+15555550102","text":"!test","message_id":"rcv-1"}`,
		CreatedAtMS: 1700000000000,
	}

	inserted, err := db.SaveMessageWithForward(Message{MessageID: "rcv-1", Body: "!test"}, "conv-1", nil, &entry)
	if err != nil {
		t.Fatalf("SaveMessageWithForward: %v", err)
	}
	if !inserted {
		t.Fatal("first outbox insert reported no row")
	}

	inserted, err = db.SaveMessageWithForward(Message{MessageID: "rcv-1", Body: "!test"}, "conv-1", nil, &entry)
	if err != nil {
		t.Fatalf("duplicate SaveMessageWithForward: %v", err)
	}
	if inserted {
		t.Fatal("duplicate message_id inserted a second outbox row")
	}

	pending, err := db.PendingForwards()
	if err != nil {
		t.Fatalf("PendingForwards: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending rows = %d, want 1", len(pending))
	}
	if pending[0].Profile != entry.Profile || pending[0].MessageID != entry.MessageID ||
		pending[0].URL != entry.URL || pending[0].Payload != entry.Payload ||
		pending[0].CreatedAtMS != entry.CreatedAtMS {
		t.Fatalf("pending row = %+v, want %+v", pending[0], entry)
	}

	count, err := db.PendingForwardCount()
	if err != nil {
		t.Fatalf("PendingForwardCount: %v", err)
	}
	if count != 1 {
		t.Fatalf("pending count = %d, want 1", count)
	}

	if err := db.DeleteForward(entry.MessageID); err != nil {
		t.Fatalf("DeleteForward: %v", err)
	}
	pending, err = db.PendingForwards()
	if err != nil {
		t.Fatalf("PendingForwards after delete: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending rows after delete = %d, want 0", len(pending))
	}
}
