package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

type MockSignalAPI struct {
	server *httptest.Server
	mu     sync.Mutex
	
	// State
	Accounts []string
	Contacts map[string][]map[string]interface{}
	Groups   map[string][]map[string]interface{}
	
	// Webhook config
	WebhookURL string
}

func NewMockSignalAPI() *MockSignalAPI {
	m := &MockSignalAPI{
		Contacts: make(map[string][]map[string]interface{}),
		Groups:   make(map[string][]map[string]interface{}),
	}
	
	mux := http.NewServeMux()
	
	mux.HandleFunc("/v1/accounts", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(m.Accounts)
	})
	
	mux.HandleFunc("/v1/contacts/", func(w http.ResponseWriter, r *http.Request) {
		account := strings.TrimPrefix(r.URL.Path, "/v1/contacts/")
		m.mu.Lock()
		contacts := m.Contacts[account]
		m.mu.Unlock()
		json.NewEncoder(w).Encode(contacts)
	})
	
	mux.HandleFunc("/v1/groups/", func(w http.ResponseWriter, r *http.Request) {
		account := strings.TrimPrefix(r.URL.Path, "/v1/groups/")
		m.mu.Lock()
		groups := m.Groups[account]
		m.mu.Unlock()
		json.NewEncoder(w).Encode(groups)
	})
	
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

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]int64{"timestamp": 1781022475000})

		if m.WebhookURL != "" {
			go m.triggerWebhook(req.Number, req.Recipients, req.Message)
		}
	}
	mux.HandleFunc("/v2/send", sendHandler)
	mux.HandleFunc("/v1/send/v2", sendHandler)
	mux.HandleFunc("/v1/send", sendHandler)
	
	m.server = httptest.NewServer(mux)
	return m
}

func (m *MockSignalAPI) URL() string {
	return m.server.URL
}

func (m *MockSignalAPI) Close() {
	m.server.Close()
}

func (m *MockSignalAPI) triggerWebhook(sender string, recipients []string, message string) {
	for _, rec := range recipients {
		// Prepare payload
		payload := map[string]interface{}{
			"envelope": map[string]interface{}{
				"source":     sender,
				"sourceName": "Mock Sender",
				"timestamp":  1781022475000,
				"dataMessage": map[string]interface{}{
					"message":   message,
					"timestamp": 1781022475000,
				},
			},
			"account": rec,
		}
		
		body, _ := json.Marshal([]interface{}{payload})
		
		// In a real E2E test, we'd need to know where the receiver is listening.
		// For now we assume m.WebhookURL is set correctly by the test.
		http.Post(m.WebhookURL, "application/json", bytes.NewBuffer(body))
	}
}
