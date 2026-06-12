package mocks

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// SignalContactData matches the Signal CLI REST API contact format.
type SignalContactData struct {
	Number string `json:"number"`
	Name   string `json:"name"`
}

// SignalGroupData matches the Signal CLI REST API group format.
type SignalGroupData struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SignalSentMessage records a message sent through the Signal mock.
type SignalSentMessage struct {
	Number      string
	Recipients  []string
	Body        string
	TimestampMS int64
}

// SignalMock is an in-memory mock of the Signal CLI REST API.
type SignalMock struct {
	server *httptest.Server
	mu     sync.Mutex

	Accounts   []string
	Contacts   map[string][]SignalContactData
	Groups     map[string][]SignalGroupData
	WebhookURL string

	sent []SignalSentMessage
}

// NewSignalMock starts a mock server on a random port.
func NewSignalMock() *SignalMock {
	return newSignalMockOnPort(0)
}

// NewSignalMockOnPort starts a mock server on a fixed port (for Docker/manual use).
func NewSignalMockOnPort(port int) *SignalMock {
	return newSignalMockOnPort(port)
}

func newSignalMockOnPort(port int) *SignalMock {
	m := &SignalMock{
		Contacts: make(map[string][]SignalContactData),
		Groups:   make(map[string][]SignalGroupData),
	}
	mux := http.NewServeMux()
	m.registerHandlers(mux)
	m.server = newHTTPTestServer(mux, port)
	return m
}

func (m *SignalMock) registerHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("/v1/accounts", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		writeJSON(w, m.Accounts)
	})

	mux.HandleFunc("/v1/contacts/", func(w http.ResponseWriter, r *http.Request) {
		account := strings.TrimPrefix(r.URL.Path, "/v1/contacts/")
		m.mu.Lock()
		contacts := m.Contacts[account]
		m.mu.Unlock()
		if contacts == nil {
			contacts = []SignalContactData{}
		}
		writeJSON(w, contacts)
	})

	mux.HandleFunc("/v1/groups/", func(w http.ResponseWriter, r *http.Request) {
		account := strings.TrimPrefix(r.URL.Path, "/v1/groups/")
		m.mu.Lock()
		groups := m.Groups[account]
		m.mu.Unlock()
		if groups == nil {
			groups = []SignalGroupData{}
		}
		writeJSON(w, groups)
	})

	// Register all known send path variants so the mock works regardless of
	// which path the client's endpoint-discovery loop lands on.
	sendHandler := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Message    string   `json:"message"`
			Number     string   `json:"number"`
			Recipients []string `json:"recipients"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ts := time.Now().UnixMilli()
		m.mu.Lock()
		m.sent = append(m.sent, SignalSentMessage{
			Number:      req.Number,
			Recipients:  req.Recipients,
			Body:        req.Message,
			TimestampMS: ts,
		})
		webhookURL := m.WebhookURL
		m.mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]int64{"timestamp": ts})

		if webhookURL != "" {
			go m.triggerSyncWebhook(webhookURL, req.Number, req.Recipients, req.Message, ts)
		}
	}
	mux.HandleFunc("/v2/send", sendHandler)
	mux.HandleFunc("/v1/send/v2", sendHandler)
	mux.HandleFunc("/v1/send", sendHandler)

	mux.HandleFunc("/v1/qrcodelink/raw", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{
			"device_link_uri": "sgnl://linkdevice?uuid=mock-uuid&pub_key=mock-key",
		})
	})
}

func (m *SignalMock) URL() string { return m.server.URL }
func (m *SignalMock) Close()      { m.server.Close() }

func (m *SignalMock) SentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func (m *SignalMock) LastSent() SignalSentMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		return SignalSentMessage{}
	}
	return m.sent[len(m.sent)-1]
}

// InjectIncoming POSTs a simulated incoming message to the configured webhook URL.
// The signal receiver must already be running (SetupTestEnv starts it automatically).
func (m *SignalMock) InjectIncoming(source, sourceName, message string, ts int64) {
	m.mu.Lock()
	webhookURL := m.WebhookURL
	m.mu.Unlock()

	if webhookURL == "" {
		return
	}

	payload := []map[string]interface{}{
		{
			"envelope": map[string]interface{}{
				"source":     source,
				"sourceName": sourceName,
				"timestamp":  ts,
				"dataMessage": map[string]interface{}{
					"message":   message,
					"timestamp": ts,
				},
			},
			"account": TestSignalAccount,
		},
	}

	body, _ := json.Marshal(payload)
	http.Post(webhookURL, "application/json", bytes.NewBuffer(body))
}

// triggerSyncWebhook simulates a "sent from another device" sync event for a message we just sent.
func (m *SignalMock) triggerSyncWebhook(webhookURL, sender string, recipients []string, message string, ts int64) {
	for _, rec := range recipients {
		payload := []map[string]interface{}{
			{
				"envelope": map[string]interface{}{
					"source":     sender,
					"sourceName": "Me",
					"timestamp":  ts,
					"syncMessage": map[string]interface{}{
						"sentMessage": map[string]interface{}{
							"destination": rec,
							"message":     message,
							"timestamp":   ts,
						},
					},
				},
				"account": TestSignalAccount,
			},
		}
		body, _ := json.Marshal(payload)
		http.Post(webhookURL, "application/json", bytes.NewBuffer(body))
	}
}
