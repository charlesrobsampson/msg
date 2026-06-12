package mocks

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charlesrobsampson/msg/client"
)

// OmSentMessage records a message sent through the OpenMessage mock.
type OmSentMessage struct {
	ConversationID string
	Body           string
	TimestampMS    int64
}

// OpenMessageMock is an in-memory mock of the OpenMessage backend HTTP API.
type OpenMessageMock struct {
	server *httptest.Server
	mu     sync.Mutex

	Conversations    []client.Conversation
	Messages         map[string][]client.Message // keyed by conversation ID without provider prefix
	Contacts         []client.Contact
	sent             []OmSentMessage
	reconnectCalled  bool
	disconnectCalled bool
}

// NewOpenMessageMock starts a mock server on a random port.
func NewOpenMessageMock() *OpenMessageMock {
	return newOpenMessageMockOnPort(0)
}

// NewOpenMessageMockOnPort starts a mock server on a fixed port (for Docker/manual use).
func NewOpenMessageMockOnPort(port int) *OpenMessageMock {
	return newOpenMessageMockOnPort(port)
}

func newOpenMessageMockOnPort(port int) *OpenMessageMock {
	m := &OpenMessageMock{
		Messages: make(map[string][]client.Message),
	}
	mux := http.NewServeMux()
	m.registerHandlers(mux)
	m.server = newHTTPTestServer(mux, port)
	return m
}

func (m *OpenMessageMock) registerHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{
			"connected": true,
			"google":    map[string]interface{}{"connected": true, "needs_pairing": false},
		})
	})

	mux.HandleFunc("/api/google/reconnect", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.reconnectCalled = true
		m.mu.Unlock()
		writeJSON(w, map[string]bool{"success": true})
	})

	mux.HandleFunc("/api/unpair", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.disconnectCalled = true
		m.mu.Unlock()
		writeJSON(w, map[string]bool{"success": true})
	})

	mux.HandleFunc("/api/conversations", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		limit := parseIntParam(r, "limit", 100)
		convs := m.Conversations
		if len(convs) > limit {
			convs = convs[:limit]
		}
		writeJSON(w, convs)
	})

	mux.HandleFunc("/api/search", func(w http.ResponseWriter, r *http.Request) {
		q := strings.ToLower(r.URL.Query().Get("q"))
		m.mu.Lock()
		defer m.mu.Unlock()
		var results []client.Conversation
		for _, c := range m.Conversations {
			if strings.Contains(strings.ToLower(c.Name), q) {
				results = append(results, c)
			}
		}
		writeJSON(w, results)
	})

	// /api/conversations/{id}/messages
	mux.HandleFunc("/api/conversations/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/conversations/")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) != 2 || parts[1] != "messages" {
			http.NotFound(w, r)
			return
		}
		convID := parts[0]
		limit := parseIntParam(r, "limit", 100)
		beforeTS := parseInt64Param(r, "before", 0)

		m.mu.Lock()
		msgs := m.Messages[convID]
		m.mu.Unlock()

		var filtered []client.Message
		for _, msg := range msgs {
			if beforeTS == 0 || msg.TimestampMS < beforeTS {
				filtered = append(filtered, msg)
			}
		}
		if len(filtered) > limit {
			filtered = filtered[len(filtered)-limit:]
		}
		writeJSON(w, filtered)
	})

	mux.HandleFunc("/api/mark-read", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ConversationID string `json:"conversation_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		for i, c := range m.Conversations {
			if c.ConversationID == req.ConversationID {
				m.Conversations[i].UnreadCount = 0
				break
			}
		}
		m.mu.Unlock()
		writeJSON(w, map[string]bool{"success": true})
	})

	mux.HandleFunc("/api/send", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ConversationID string `json:"conversation_id"`
			Message        string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ts := time.Now().UnixMilli()
		m.mu.Lock()
		m.sent = append(m.sent, OmSentMessage{
			ConversationID: req.ConversationID,
			Body:           req.Message,
			TimestampMS:    ts,
		})
		m.Messages[req.ConversationID] = append(m.Messages[req.ConversationID], client.Message{
			MessageID:   fmt.Sprintf("sent-%d", ts),
			SenderName:  "Me",
			Body:        req.Message,
			IsFromMe:    true,
			TimestampMS: ts,
		})
		m.mu.Unlock()
		writeJSON(w, map[string]interface{}{"success": true, "status": "sent"})
	})

	mux.HandleFunc("/api/contacts", func(w http.ResponseWriter, r *http.Request) {
		q := strings.ToLower(r.URL.Query().Get("q"))
		limit := parseIntParam(r, "limit", 100)
		m.mu.Lock()
		defer m.mu.Unlock()
		var results []client.Contact
		for _, c := range m.Contacts {
			if q == "" || strings.Contains(strings.ToLower(c.Name), q) || strings.Contains(c.Number, q) {
				results = append(results, c)
			}
		}
		if len(results) > limit {
			results = results[:limit]
		}
		writeJSON(w, results)
	})
}

func (m *OpenMessageMock) URL() string { return m.server.URL }
func (m *OpenMessageMock) Close()      { m.server.Close() }

func (m *OpenMessageMock) SentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func (m *OpenMessageMock) LastSent() OmSentMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		return OmSentMessage{}
	}
	return m.sent[len(m.sent)-1]
}

// GetUnreadCount returns the current in-memory unread count for a conversation ID.
func (m *OpenMessageMock) GetUnreadCount(convID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.Conversations {
		if c.ConversationID == convID {
			return c.UnreadCount
		}
	}
	return -1 // not found
}

func (m *OpenMessageMock) ReconnectCalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reconnectCalled
}

func (m *OpenMessageMock) DisconnectCalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.disconnectCalled
}

// --- shared helpers used by both mocks ---

// newHTTPTestServer starts an httptest.Server on the given port (0 = random).
// Always binds to 127.0.0.1 (IPv4) for portability.
func newHTTPTestServer(handler http.Handler, port int) *httptest.Server {
	l, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		panic(fmt.Sprintf("mocks: failed to listen on 127.0.0.1:%d: %v", port, err))
	}
	s := httptest.NewUnstartedServer(handler)
	s.Listener = l
	s.Start()
	return s
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func parseIntParam(r *http.Request, key string, defaultVal int) int {
	s := r.URL.Query().Get(key)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return v
}

func parseInt64Param(r *http.Request, key string, defaultVal int64) int64 {
	s := r.URL.Query().Get(key)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return defaultVal
	}
	return v
}
