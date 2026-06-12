package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type SignalProvider struct {
	id          string
	baseURL     string
	account     string
	unreachable bool
	db          *SignalDB
}

type SignalGroup struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	InternalID string `json:"internal_id"`
}

type SignalContact struct {
	Number string `json:"number"`
	Name   string `json:"name"`
}

type SignalSendRequest struct {
	Message           string   `json:"message"`
	Number            string   `json:"number"`
	Recipients        []string `json:"recipients,omitempty"`
	TextMode          string   `json:"text_mode,omitempty"`
	Base64Attachments []string `json:"base64_attachments,omitempty"`
}

func NewSignalProvider(id string, cfg ProviderSettings, account string) *SignalProvider {
	port := cfg.Port
	if port == 0 {
		port = 18081
	}

	if envPort := os.Getenv("SIGNAL_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			port = p
		}
	}

	baseURL := cfg.URL
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	}

	if account == "" {
		account = os.Getenv("SIGNAL_ACCOUNT")
	}

	db, _ := OpenSignalDB()

	return &SignalProvider{
		id:      id,
		baseURL: baseURL,
		account: account,
		db:      db,
	}
}

func (p *SignalProvider) ID() string {
	return p.id
}

func (p *SignalProvider) GetBaseURL() string {
	return p.baseURL
}

func (p *SignalProvider) DisplayName() string {
	return "Signal"
}

func (p *SignalProvider) FetchConversations(limit int) ([]Conversation, error) {
	if p.account == "" {
		return nil, nil // Or return a "needs setup" conversation
	}

	var all []Conversation
	seen := make(map[string]bool)

	// 1. Fetch from local DB first to get active threads
	var dbConvs map[string]struct {
		UnreadCount   int
		LastMessageTS int64
	}
	if p.db != nil {
		dbConvs, _ = p.db.FetchAllConversations()
	}

	// 2. Fetch Groups from API
	groupsURL := fmt.Sprintf("%s/v1/groups/%s", p.baseURL, p.account)
	resp, err := http.Get(groupsURL)
	if err != nil {
		p.unreachable = true
	} else {
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			p.unreachable = true
		} else {
			p.unreachable = false
			var groups []SignalGroup
			if err := json.NewDecoder(resp.Body).Decode(&groups); err == nil {
				for _, g := range groups {
					// Canonical group ID always uses the "group." prefix — that is what
					// the webhook receiver stores in the local DB and what signal-cli
					// accepts as a send recipient.
					convID := g.InternalID
					if convID == "" {
						convID = g.ID
					}
					if !strings.HasPrefix(convID, "group.") {
						convID = "group." + convID
					}
					conv := Conversation{
						ConversationID: convID,
						Name:           g.Name,
						IsGroup:        true,
						SourcePlatform: "signal",
						ProviderID:     "signal",
					}
					if meta, ok := dbConvs[convID]; ok {
						conv.UnreadCount = meta.UnreadCount
						conv.LastMessageTS = meta.LastMessageTS
					}
					all = append(all, conv)
					seen[convID] = true
				}
			}
		}
	}

	// 3. Fetch Contacts from API
	contactsURL := fmt.Sprintf("%s/v1/contacts/%s", p.baseURL, p.account)
	resp, err = http.Get(contactsURL)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var contacts []SignalContact
			if err := json.NewDecoder(resp.Body).Decode(&contacts); err == nil {
				for _, c := range contacts {
					name := c.Name
					if name == "" {
						name = c.Number
					}
					conv := Conversation{
						ConversationID: c.Number,
						Name:           name,
						IsGroup:        false,
						SourcePlatform: "signal",
						ProviderID:     "signal",
					}
					if meta, ok := dbConvs[c.Number]; ok {
						conv.UnreadCount = meta.UnreadCount
						conv.LastMessageTS = meta.LastMessageTS
					}
					all = append(all, conv)
					seen[c.Number] = true
				}
			}
		}
	}

	// 4. Add remaining conversations from DB (ones not in API's contact/group list).
	// Groups matched by the API in step 2 are already marked seen (even when their
	// DB ID encoding differs), so only truly unknown threads reach here.
	for id, meta := range dbConvs {
		if !seen[id] {
			all = append(all, Conversation{
				ConversationID: id,
				Name:           id,
				IsGroup:        strings.HasPrefix(id, "group."),
				SourcePlatform: "signal",
				ProviderID:     "signal",
				UnreadCount:    meta.UnreadCount,
				LastMessageTS:  meta.LastMessageTS,
			})
		}
	}

	// Sort by LastMessageTS descending to prioritize active conversations
	sort.Slice(all, func(i, j int) bool {
		return all[i].LastMessageTS > all[j].LastMessageTS
	})

	if len(all) > limit {
		all = all[:limit]
	}

	return all, nil
}

func (p *SignalProvider) SearchConversations(query string, limit int) ([]Conversation, error) {
	// For now, just filter the full list
	convs, err := p.FetchConversations(1000)
	if err != nil {
		return nil, err
	}
	var filtered []Conversation
	for _, c := range convs {
		if strings.Contains(strings.ToLower(c.Name), strings.ToLower(query)) {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (p *SignalProvider) FetchMessages(conversationID string, limit int, beforeTS int64, beforeID string) ([]Message, error) {
	if p.db == nil {
		return []Message{}, nil
	}
	return p.db.FetchMessages(conversationID, limit, beforeTS, p.baseURL)
}

// signalSendBody builds the appropriate request for a 1:1 or group conversation.
// Groups use the "groupId" field (bare base64 without "group." prefix);
// 1:1s use the "recipients" field.
func (p *SignalProvider) signalSendBody(conversationID, text string, styled bool) SignalSendRequest {
	req := SignalSendRequest{Message: text, Number: p.account}
	if styled {
		req.TextMode = "styled"
	}
	if strings.HasPrefix(conversationID, "group.") {
		// We store group.INTERNAL_ID internally, but /v2/send requires group.base64(INTERNAL_ID)
		// (the double-base64 "id" format returned by GET /v1/groups).
		inner := strings.TrimPrefix(conversationID, "group.")
		req.Recipients = []string{"group." + base64.StdEncoding.EncodeToString([]byte(inner))}
	} else {
		req.Recipients = []string{conversationID}
	}
	return req
}

func (p *SignalProvider) SendMessage(conversationID, text string, styled bool) (bool, error) {
	if p.account == "" {
		return false, fmt.Errorf("SIGNAL_ACCOUNT not set")
	}

	reqBody := p.signalSendBody(conversationID, text, styled)
	jsonPayload, _ := json.Marshal(reqBody)

	// Try the canonical v2 path first, fall back to older endpoints for compatibility.
	accountPath := "/v1/" + url.PathEscape(p.account) + "/sendv2"
	for _, sendPath := range []string{accountPath, "/v2/send", "/v1/send"} {
		sendURL := fmt.Sprintf("%s%s", p.baseURL, sendPath)
		resp, err := http.Post(sendURL, "application/json", bytes.NewBuffer(jsonPayload))
		if err != nil {
			p.unreachable = true
			return false, err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		p.unreachable = false

		if resp.StatusCode == 404 {
			// This path doesn't exist on this server version — try the next one.
			jsonPayload = append([]byte(nil), jsonPayload...)
			continue
		}

		if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
			// Cache the sent message locally so it appears immediately in FetchMessages.
			if p.db != nil {
				var result struct {
					Timestamp int64 `json:"timestamp"`
				}
				json.Unmarshal(body, &result)

				ts := result.Timestamp
				if ts == 0 {
					ts = time.Now().UnixMilli()
				}

				p.db.SaveMessage(Message{
					MessageID:   fmt.Sprintf("sent-%d", ts),
					SenderName:  "Me",
					Body:        text,
					IsFromMe:    true,
					TimestampMS: ts,
				}, conversationID, nil)
			}
			return true, nil
		}

		return false, fmt.Errorf("signal API %s returned HTTP %d: %s", sendPath, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return false, fmt.Errorf("signal API: no working send endpoint found")
}

func (p *SignalProvider) SendWithAttachments(conversationID, text string, styled bool, attachmentPaths []string) (bool, error) {
	var b64 []string
	for _, path := range attachmentPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			return false, fmt.Errorf("reading %q: %w", path, err)
		}
		mt := mime.TypeByExtension(filepath.Ext(path))
		if mt == "" {
			mt = "application/octet-stream"
		}
		if idx := strings.Index(mt, ";"); idx != -1 {
			mt = strings.TrimSpace(mt[:idx])
		}
		b64 = append(b64, fmt.Sprintf("data:%s;base64,%s", mt, base64.StdEncoding.EncodeToString(data)))
	}

	reqBody := p.signalSendBody(conversationID, text, styled)
	reqBody.Base64Attachments = b64
	jsonPayload, _ := json.Marshal(reqBody)

	accountPath := "/v1/" + url.PathEscape(p.account) + "/sendv2"
	for _, sendPath := range []string{accountPath, "/v2/send", "/v1/send"} {
		url := fmt.Sprintf("%s%s", p.baseURL, sendPath)
		resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonPayload))
		if err != nil {
			p.unreachable = true
			return false, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		p.unreachable = false

		if resp.StatusCode == 404 {
			continue
		}
		if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
			if p.db != nil {
				var result struct {
					Timestamp int64 `json:"timestamp"`
				}
				json.Unmarshal(body, &result)
				ts := result.Timestamp
				if ts == 0 {
					ts = time.Now().UnixMilli()
				}
				note := text
				if note == "" && len(attachmentPaths) > 0 {
					note = fmt.Sprintf("[%d attachment(s)]", len(attachmentPaths))
				}
				// Store all sent attachment paths so `o` can open them.
				var rawAttachments []RawAttachment
				for _, path := range attachmentPaths {
					mt := mime.TypeByExtension(filepath.Ext(path))
					if mt == "" {
						mt = "application/octet-stream"
					}
					if idx := strings.Index(mt, ";"); idx != -1 {
						mt = strings.TrimSpace(mt[:idx])
					}
					rawAttachments = append(rawAttachments, RawAttachment{ID: path, MimeType: mt})
				}
				p.db.SaveMessage(Message{
					MessageID:   fmt.Sprintf("sent-%d", ts),
					SenderName:  "Me",
					Body:        note,
					IsFromMe:    true,
					TimestampMS: ts,
				}, conversationID, rawAttachments)
			}
			return true, nil
		}
		return false, fmt.Errorf("signal API %s returned HTTP %d: %s", sendPath, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return false, fmt.Errorf("signal API: no working send endpoint found")
}

func (p *SignalProvider) MarkAsRead(conversationID string) error {
	if p.db != nil {
		if err := p.db.MarkAsRead(conversationID); err != nil {
			return err
		}
		// Best-effort: send a read receipt to the Signal network so the sender's
		// phone also shows the message as read. Failures are silently ignored
		// because the local unread count is already updated.
		timestamps, _ := p.db.FetchUnreadReceivedTimestamps(conversationID, 50)
		if len(timestamps) > 0 {
			go p.sendReadReceipt(conversationID, timestamps)
		}
	}
	return nil
}

// sendReadReceipt notifies the Signal network that messages have been read.
// It tries the account-scoped path first, then the generic /v1/receipts path.
func (p *SignalProvider) sendReadReceipt(conversationID string, timestamps []int64) {
	if p.account == "" || p.unreachable {
		return
	}

	recipient := conversationID
	if strings.HasPrefix(conversationID, "group.") {
		inner := strings.TrimPrefix(conversationID, "group.")
		recipient = "group." + base64.StdEncoding.EncodeToString([]byte(inner))
	}

	payload := map[string]interface{}{
		"receipt_type":          "read",
		"recipient":             recipient,
		"target_sent_timestamp": timestamps,
	}
	jsonPayload, _ := json.Marshal(payload)

	accountPath := fmt.Sprintf("%s/v1/receipts/%s", p.baseURL, url.PathEscape(p.account))
	genericPath := fmt.Sprintf("%s/v1/receipts", p.baseURL)
	for _, endpoint := range []string{accountPath, genericPath} {
		resp, err := http.Post(endpoint, "application/json", bytes.NewBuffer(jsonPayload))
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusNoContent {
			return
		}
		if resp.StatusCode == 404 {
			continue
		}
		return
	}
}

// findNumberByName looks up a contact's phone number by their display name.
// Used as a fallback for group messages stored before sender_number tracking.
func (p *SignalProvider) findNumberByName(name string) string {
	if name == "" || p.account == "" {
		return ""
	}
	contacts, err := p.FetchContacts(name, 20)
	if err != nil {
		return ""
	}
	for _, c := range contacts {
		if strings.EqualFold(c.Name, name) {
			return c.Number
		}
	}
	return ""
}

func (p *SignalProvider) SendReaction(conversationID string, msg Message, emoji string, remove bool) (bool, error) {
	if p.account == "" {
		return false, fmt.Errorf("SIGNAL_ACCOUNT not set")
	}

	var targetAuthor string
	if msg.IsFromMe {
		// Reacting to our own message: we are the target author.
		targetAuthor = p.account
	} else {
		targetAuthor = msg.SenderNumber
		if targetAuthor == "" {
			if !strings.HasPrefix(conversationID, "group.") {
				// 1:1 DM: the conversation ID is the other person's number.
				targetAuthor = conversationID
			} else {
				// Group message with no stored sender number (pre-migration).
				// Fall back to a contacts lookup by display name.
				targetAuthor = p.findNumberByName(msg.SenderName)
			}
		}
	}
	if targetAuthor == "" {
		return false, fmt.Errorf("cannot determine target author for reaction (sender %q not in contacts)", msg.SenderName)
	}

	recipient := conversationID
	if strings.HasPrefix(conversationID, "group.") {
		inner := strings.TrimPrefix(conversationID, "group.")
		recipient = "group." + base64.StdEncoding.EncodeToString([]byte(inner))
	}

	var jsonPayload []byte
	var endpoint string
	if remove {
		// DELETE /v1/reactions/{number} — removal omits the "reaction" field per schema
		payload := map[string]interface{}{
			"recipient":     recipient,
			"target_author": targetAuthor,
			"timestamp":     msg.TimestampMS,
		}
		jsonPayload, _ = json.Marshal(payload)
		endpoint = fmt.Sprintf("%s/v1/reactions/%s", p.baseURL, url.PathEscape(p.account))
		req, err := http.NewRequest(http.MethodDelete, endpoint, bytes.NewBuffer(jsonPayload))
		if err != nil {
			return false, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			p.unreachable = true
			return false, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		p.unreachable = false
		if resp.StatusCode >= 400 {
			return false, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return true, nil
	}

	// POST /v1/reactions/{number}
	payload := map[string]interface{}{
		"reaction":      emoji,
		"recipient":     recipient,
		"target_author": targetAuthor,
		"timestamp":     msg.TimestampMS,
	}
	jsonPayload, _ = json.Marshal(payload)
	endpoint = fmt.Sprintf("%s/v1/reactions/%s", p.baseURL, url.PathEscape(p.account))
	resp, err := http.Post(endpoint, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		p.unreachable = true
		return false, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	p.unreachable = false
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return true, nil
}

func (p *SignalProvider) FetchContacts(query string, limit int) ([]Contact, error) {
	if p.account == "" {
		return nil, nil
	}

	url := fmt.Sprintf("%s/v1/contacts/%s", p.baseURL, p.account)
	resp, err := http.Get(url)
	if err != nil {
		p.unreachable = true
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		p.unreachable = true
		return nil, fmt.Errorf("signal contacts API returned %d", resp.StatusCode)
	}
	p.unreachable = false

	var sContacts []SignalContact
	if err := json.NewDecoder(resp.Body).Decode(&sContacts); err != nil {
		return nil, err
	}

	var contacts []Contact
	for _, sc := range sContacts {
		if query == "" || strings.Contains(strings.ToLower(sc.Name), strings.ToLower(query)) || strings.Contains(sc.Number, query) {
			contacts = append(contacts, Contact{
				ContactID: sc.Number,
				Name:      sc.Name,
				Number:    sc.Number,
			})
		}
	}

	if len(contacts) > limit {
		contacts = contacts[:limit]
	}
	return contacts, nil
}

func (p *SignalProvider) IsUnreachable() bool {
	return p.unreachable
}

// Unregister calls POST /v1/unregister/{account} to disable push support for this device.
// delete_account and delete_local_data are always false to avoid destructive data loss.
func (p *SignalProvider) Unregister() error {
	if p.account == "" {
		return fmt.Errorf("no Signal account configured")
	}
	payload := map[string]bool{
		"delete_account":    false,
		"delete_local_data": false,
	}
	jsonPayload, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/v1/unregister/%s", p.baseURL, p.account)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonPayload))
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
