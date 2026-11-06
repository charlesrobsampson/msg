package main

import (
	"net/http"
	"testing"
)

// TestReceiverServerTimeouts proves the webhook receiver cannot be held open by
// a slow or idle client.
func TestReceiverServerTimeouts(t *testing.T) {
	srv := newReceiverServer(":0", http.NewServeMux())
	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout must be non-zero")
	}
	if srv.ReadTimeout <= 0 {
		t.Error("ReadTimeout must be non-zero")
	}
	if srv.WriteTimeout <= 0 {
		t.Error("WriteTimeout must be non-zero")
	}
	if srv.Handler == nil {
		t.Error("Handler must be set")
	}
}
