package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type OpenMessageProvider struct {
	id          string
	baseURL     string
	account     string
	unreachable bool
}

func NewOpenMessageProvider(id string, cfg ProviderSettings, account string) *OpenMessageProvider {
	port := cfg.Port
	if port == 0 {
		port = 7007
	}

	// Override with environment variable if present
	if envPort := os.Getenv("OPENMESSAGES_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			port = p
		}
	}

	baseURL := cfg.URL
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	}

	return &OpenMessageProvider{
		id:      id,
		baseURL: baseURL,
		account: account,
	}
}

func (p *OpenMessageProvider) ID() string {
	return p.id
}

func (p *OpenMessageProvider) DisplayName() string {
	return "Google Messages"
}

func (p *OpenMessageProvider) FetchConversations(limit int) ([]Conversation, error) {
	url := fmt.Sprintf("%s/api/conversations?limit=%d", p.baseURL, limit)
	resp, err := http.Get(url)
	if err != nil {
		p.unreachable = true
		return nil, err
	}
	defer resp.Body.Close()
	p.unreachable = false

	body, _ := io.ReadAll(resp.Body)
	var conversations []Conversation
	if err := json.Unmarshal(body, &conversations); err != nil {
		return nil, err
	}
	return conversations, nil
}

func (p *OpenMessageProvider) SearchConversations(query string, limit int) ([]Conversation, error) {
	escapedQuery := url.QueryEscape(query)
	url := fmt.Sprintf("%s/api/search?q=%s&limit=%d", p.baseURL, escapedQuery, limit)
	resp, err := http.Get(url)
	if err != nil {
		p.unreachable = true
		return nil, err
	}
	defer resp.Body.Close()
	p.unreachable = false

	body, _ := io.ReadAll(resp.Body)
	var conversations []Conversation
	if err := json.Unmarshal(body, &conversations); err != nil {
		return nil, err
	}
	return conversations, nil
}

// omMessage is the raw API shape for OpenMessage messages — a superset of Message
// that also captures attachment fields the web UI uses.
type omMessage struct {
	MessageID   string `json:"MessageID"`
	SenderName  string `json:"SenderName"`
	Body        string `json:"Body"`
	IsFromMe    bool   `json:"IsFromMe"`
	TimestampMS int64  `json:"TimestampMS"`
	MediaID     string `json:"MediaID"`
	MimeType    string `json:"MimeType"`
	Reactions   string `json:"Reactions"`
}

func (p *OpenMessageProvider) FetchMessages(conversationID string, limit int, beforeTS int64, beforeID string) ([]Message, error) {
	reqURL := fmt.Sprintf("%s/api/conversations/%s/messages?limit=%d", p.baseURL, conversationID, limit)

	if beforeTS > 0 {
		reqURL += fmt.Sprintf("&before=%d", beforeTS)
	}
	if beforeID != "" {
		reqURL += fmt.Sprintf("&before_id=%s", beforeID)
	}

	resp, err := http.Get(reqURL)
	if err != nil {
		p.unreachable = true
		return nil, err
	}
	defer resp.Body.Close()
	p.unreachable = false

	body, _ := io.ReadAll(resp.Body)
	var raw []omMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	messages := make([]Message, len(raw))
	for i, r := range raw {
		messages[i] = Message{
			MessageID:   r.MessageID,
			SenderName:  r.SenderName,
			Body:        r.Body,
			IsFromMe:    r.IsFromMe,
			TimestampMS: r.TimestampMS,
		}
		if r.MediaID != "" {
			url := fmt.Sprintf("%s/api/media/%s", p.baseURL, r.MessageID)
			messages[i].MimeType = r.MimeType
			messages[i].AttachmentURL = url
			messages[i].Attachments = []Attachment{{URL: url, MimeType: r.MimeType}}
		}
		if r.Reactions != "" {
			// openmessage stores reactions as [{"emoji":"😂","count":2}] — no senders.
			var raw []struct {
				Emoji string `json:"emoji"`
				Count int    `json:"count"`
			}
			if json.Unmarshal([]byte(r.Reactions), &raw) == nil {
				for _, rr := range raw {
					messages[i].Reactions = append(messages[i].Reactions, Reaction{
						Emoji: rr.Emoji,
						Count: rr.Count,
					})
				}
			}
		}
	}
	return messages, nil
}

func (p *OpenMessageProvider) MarkAsRead(id string) error {
	payload := map[string]string{"conversation_id": id}
	jsonPayload, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/api/mark-read", p.baseURL)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		p.unreachable = true
		return err
	}
	resp.Body.Close()
	p.unreachable = false
	return nil
}

// ensureConversationID resolves a phone number to an openmessage conversation ID.
// If id starts with '+', it calls POST /api/new-conversation to get or create a
// real conversation ID; otherwise id is returned unchanged.
func (p *OpenMessageProvider) ensureConversationID(id string) (string, error) {
	if !strings.HasPrefix(id, "+") {
		return id, nil
	}
	payload := map[string]string{"phone_number": id}
	jsonPayload, _ := json.Marshal(payload)
	resp, err := http.Post(fmt.Sprintf("%s/api/new-conversation", p.baseURL), "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		p.unreachable = true
		return "", err
	}
	defer resp.Body.Close()
	p.unreachable = false
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("new-conversation returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.ConversationID == "" {
		return "", fmt.Errorf("new-conversation returned unexpected body: %s", string(body))
	}
	return result.ConversationID, nil
}

func (p *OpenMessageProvider) SendMessage(id, text string, styled bool) (bool, error) {
	convID, err := p.ensureConversationID(id)
	if err != nil {
		return false, err
	}
	payload := map[string]string{"conversation_id": convID, "message": text}
	jsonPayload, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/api/send", p.baseURL)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		p.unreachable = true
		return false, err
	}
	defer resp.Body.Close()
	p.unreachable = false

	body, _ := io.ReadAll(resp.Body)
	var result SendResponse
	json.Unmarshal(body, &result)
	if !result.Success {
		msg := result.Status
		if msg == "" {
			msg = "provider rejected the message — check 'msg provider status'"
		}
		return false, fmt.Errorf("%s", msg)
	}
	return true, nil
}

func (p *OpenMessageProvider) SendWithAttachments(conversationID, text string, styled bool, attachmentPaths []string) (bool, error) {
	if len(attachmentPaths) == 0 {
		return p.SendMessage(conversationID, text, styled)
	}

	convID, err := p.ensureConversationID(conversationID)
	if err != nil {
		return false, err
	}
	conversationID = convID

	for i, path := range attachmentPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			return false, fmt.Errorf("reading %q: %w", path, err)
		}

		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		_ = mw.WriteField("conversation_id", conversationID)

		// Only attach the text body to the first file's caption.
		if i == 0 && text != "" {
			_ = mw.WriteField("caption", text)
		}

		mt := mime.TypeByExtension(filepath.Ext(path))
		if mt == "" {
			mt = "application/octet-stream"
		}
		if idx := strings.Index(mt, ";"); idx != -1 {
			mt = strings.TrimSpace(mt[:idx])
		}

		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filepath.Base(path)))
		h.Set("Content-Type", mt)
		part, err := mw.CreatePart(h)
		if err != nil {
			return false, err
		}
		if _, err := part.Write(data); err != nil {
			return false, err
		}
		mw.Close()

		reqURL := fmt.Sprintf("%s/api/send-media", p.baseURL)
		resp, err := http.Post(reqURL, mw.FormDataContentType(), &body)
		if err != nil {
			p.unreachable = true
			return false, err
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		p.unreachable = false

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			return false, fmt.Errorf("send-media returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
		}
	}

	// Text was already sent as caption of the first attachment, so nothing more to do.
	return true, nil
}

func (p *OpenMessageProvider) SendReaction(conversationID string, msg Message, emoji string, remove bool) (bool, error) {
	action := "add"
	if remove {
		action = "remove"
	}
	payload := map[string]string{
		"conversation_id": conversationID,
		"message_id":      msg.MessageID,
		"emoji":           emoji,
		"action":          action,
	}
	jsonPayload, _ := json.Marshal(payload)

	resp, err := http.Post(fmt.Sprintf("%s/api/react", p.baseURL), "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		p.unreachable = true
		return false, err
	}
	defer resp.Body.Close()
	p.unreachable = false

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return true, nil
}

func (p *OpenMessageProvider) FetchContacts(query string, limit int) ([]Contact, error) {
	escapedQuery := url.QueryEscape(query)
	url := fmt.Sprintf("%s/api/contacts?q=%s&limit=%d", p.baseURL, escapedQuery, limit)

	resp, err := http.Get(url)
	if err != nil {
		p.unreachable = true
		return nil, err
	}
	defer resp.Body.Close()
	p.unreachable = false

	body, _ := io.ReadAll(resp.Body)
	var contacts []Contact
	if err := json.Unmarshal(body, &contacts); err != nil {
		return nil, err
	}
	return contacts, nil
}

func (p *OpenMessageProvider) IsUnreachable() bool {
	return p.unreachable
}

// OMGoogleStatus mirrors the GoogleStatusSnapshot returned by /api/status.
type OMGoogleStatus struct {
	Connected    bool   `json:"connected"`
	Paired       bool   `json:"paired"`
	NeedsPairing bool   `json:"needs_pairing"`
	LastError    string `json:"last_error,omitempty"`
}

// OMStatus is the top-level payload from GET /api/status.
type OMStatus struct {
	Connected bool            `json:"connected"`
	Google    *OMGoogleStatus `json:"google,omitempty"`
}

// GetStatus calls GET /api/status and returns the current provider status.
func (p *OpenMessageProvider) GetStatus() (*OMStatus, error) {
	resp, err := http.Get(fmt.Sprintf("%s/api/status", p.baseURL))
	if err != nil {
		p.unreachable = true
		return nil, err
	}
	defer resp.Body.Close()
	p.unreachable = false
	var s OMStatus
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Disconnect calls POST /api/unpair to disconnect and remove stored credentials.
// After this, re-pairing via QR code is required to reconnect.
func (p *OpenMessageProvider) Disconnect() error {
	resp, err := http.Post(fmt.Sprintf("%s/api/unpair", p.baseURL), "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// ReconnectGoogle calls POST /api/google/reconnect to attempt a soft reconnect
// using existing credentials (no QR code needed).
func (p *OpenMessageProvider) ReconnectGoogle() error {
	resp, err := http.Post(fmt.Sprintf("%s/api/google/reconnect", p.baseURL), "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
