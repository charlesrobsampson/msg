package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

// TestSignalIdentityResolver covers UUID-to-number lookup and the fact that
// the identities response is cached across messages.
func TestSignalIdentityResolver(t *testing.T) {
	const (
		account = "+15555550303"
		uuid    = "2aea5c40-0f8e-4ad7-8099-880b50064454"
		phone   = "+18016739997"
	)

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.URL.Path; got != "/v1/identities/"+account {
			t.Errorf("path = %q, want identities for %s", got, account)
		}
		fmt.Fprintf(w, `[{"number":%q,"uuid":%q},{"number":"","uuid":"bac95088-a1cd-4b30-a4b4-49174ef8d633"}]`, phone, uuid)
	}))
	t.Cleanup(srv.Close)

	resolver := newSignalIdentityResolver(srv.URL, account)
	if got := resolver.Number("", uuid); got != phone {
		t.Fatalf("Number(%s) = %q, want %q", uuid, got, phone)
	}
	if got := resolver.Number(account, "bac95088-a1cd-4b30-a4b4-49174ef8d633"); got != "" {
		t.Fatalf("Number(unknown uuid) = %q, want empty", got)
	}
	if got := resolver.Number(account, uuid); got != phone {
		t.Fatalf("cached Number(%s) = %q, want %q", uuid, got, phone)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("identities calls = %d, want 1 (cached)", got)
	}
}

// TestSignalIdentityResolverKeepsCacheOnFailure proves a transient API error
// does not throw away a previously resolved number.
func TestSignalIdentityResolverKeepsCacheOnFailure(t *testing.T) {
	const (
		uuid  = "2aea5c40-0f8e-4ad7-8099-880b50064454"
		phone = "+18016739997"
	)

	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `[{"number":%q,"uuid":%q}]`, phone, uuid)
	}))
	t.Cleanup(srv.Close)

	resolver := newSignalIdentityResolver(srv.URL, "+15555550303")
	if got := resolver.Number("", uuid); got != phone {
		t.Fatalf("Number(%s) = %q, want %q", uuid, got, phone)
	}

	fail.Store(true)
	resolver.fetchedAt = resolver.fetchedAt.Add(-resolver.ttl)
	if got := resolver.Number("", uuid); got != phone {
		t.Fatalf("Number after refresh failure = %q, want stale %q", got, phone)
	}
}

func TestLooksLikeSignalUUID(t *testing.T) {
	if !looksLikeSignalUUID("2aea5c40-0f8e-4ad7-8099-880b50064454") {
		t.Error("valid UUID was not recognised")
	}
	if looksLikeSignalUUID("+18016739997") || looksLikeSignalUUID("not-a-uuid") {
		t.Error("non-UUID value was recognised as a UUID")
	}
}
