package client

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

var providers []Provider

type Contact struct {
	ContactID          string   `json:"ContactID"`
	Name               string   `json:"Name"`
	Number             string   `json:"Number"`
	ProviderID         string   `json:"provider_id"`
	AvailablePlatforms []string `json:"available_platforms"`
}

type Participant struct {
	Name   string `json:"name"`
	Number string `json:"number"`
	IsMe   bool   `json:"is_me"`
}

type Conversation struct {
	ConversationID string `json:"ConversationID"`
	Name           string `json:"Name"`
	IsGroup        bool   `json:"IsGroup"`
	Participants   string `json:"Participants"` // Received as an encoded JSON string
	LastMessageTS  int64  `json:"LastMessageTS"`
	UnreadCount    int    `json:"UnreadCount"`
	SourcePlatform string `json:"source_platform"`
	ProviderID     string `json:"provider_id"`
}

// RawAttachment holds an unresolved attachment ID (signal attachment ID or local
// file path) and its MIME type as stored in the DB.
type RawAttachment struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type"`
}

// Attachment is a resolved attachment with a display URL and MIME type.
type Attachment struct {
	URL      string
	MimeType string
}

// Reaction represents one emoji and the people who sent it.
type Reaction struct {
	Emoji   string   `json:"emoji"`
	Count   int      `json:"count"`
	Senders []string `json:"senders,omitempty"`
}

type Message struct {
	MessageID    string       `json:"MessageID"`
	SenderName   string       `json:"SenderName"`
	SenderNumber string       `json:"SenderNumber,omitempty"`
	Body         string       `json:"Body"`
	IsFromMe     bool         `json:"IsFromMe"`
	TimestampMS  int64        `json:"TimestampMS"`
	Attachments  []Attachment `json:"Attachments,omitempty"`
	Reactions    []Reaction   `json:"Reactions,omitempty"`
	// Deprecated: use Attachments[0] — kept for providers that haven't migrated.
	AttachmentURL string `json:"AttachmentURL,omitempty"`
	MimeType      string `json:"MimeType,omitempty"`
}

type SendResponse struct {
	Status  string `json:"status"`
	Success bool   `json:"success"`
}

func RegisterProvider(p Provider) {
	providers = append(providers, p)
}

func InitProviders(cfg Config) {
	providers = nil // Clear existing providers to avoid duplicates on re-init
	seenIDs := map[string]bool{}
	for name, pCfg := range cfg.Providers {
		if !pCfg.Enabled {
			continue
		}
		switch pCfg.Type {
		case "sms":
			if seenIDs["sms"] {
				continue
			}
			seenIDs["sms"] = true
			RegisterProvider(NewOpenMessageProvider("sms", pCfg, cfg.Account))
		case "signal":
			if seenIDs[name] {
				continue
			}
			seenIDs[name] = true
			RegisterProvider(NewSignalProvider(name, pCfg, cfg.Account))
		}
	}
}

func FetchConversations(limit int) ([]Conversation, error) {
	var all []Conversation
	for _, p := range providers {
		convs, err := p.FetchConversations(limit)
		if err != nil {
			continue // Log warning?
		}
		for i := range convs {
			convs[i].ProviderID = p.ID()
			// Prefix ID for routing
			convs[i].ConversationID = p.ID() + ":" + convs[i].ConversationID
		}
		all = append(all, convs...)
	}

	// Sort by LastMessageTS descending
	sort.Slice(all, func(i, j int) bool {
		return all[i].LastMessageTS > all[j].LastMessageTS
	})

	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func SearchConversations(query string, limit int) ([]Conversation, error) {
	var all []Conversation
	for _, p := range providers {
		convs, err := p.SearchConversations(query, limit)
		if err != nil {
			continue
		}
		for i := range convs {
			convs[i].ProviderID = p.ID()
			convs[i].ConversationID = p.ID() + ":" + convs[i].ConversationID
		}
		all = append(all, convs...)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].LastMessageTS > all[j].LastMessageTS
	})

	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func routeToProvider(prefixedID string) (Provider, string, error) {
	parts := strings.SplitN(prefixedID, ":", 2)
	if len(parts) < 2 {
		return nil, "", fmt.Errorf("invalid prefixed conversation ID: %s", prefixedID)
	}
	providerID := parts[0]
	if providerID == "openmessage" { // backward compat: renamed to "sms"
		providerID = "sms"
	}
	originalID := parts[1]

	for _, p := range providers {
		if p.ID() == providerID {
			return p, originalID, nil
		}
	}
	return nil, "", fmt.Errorf("provider not found: %s", providerID)
}

func FetchMessages(prefixedID string, limit int, beforeTS int64, beforeID string) ([]Message, error) {
	p, id, err := routeToProvider(prefixedID)
	if err != nil {
		return nil, err
	}
	return p.FetchMessages(id, limit, beforeTS, beforeID)
}

func MarkAsRead(prefixedID string) error {
	p, id, err := routeToProvider(prefixedID)
	if err != nil {
		return err
	}
	return p.MarkAsRead(id)
}

func SendMessage(prefixedID, text string, styled bool) (bool, error) {
	p, id, err := routeToProvider(prefixedID)
	if err != nil {
		return false, err
	}
	return p.SendMessage(id, text, styled)
}

func SendReaction(prefixedID string, msg Message, emoji string, remove bool) (bool, error) {
	p, id, err := routeToProvider(prefixedID)
	if err != nil {
		return false, err
	}
	if rs, ok := p.(ReactionSender); ok {
		return rs.SendReaction(id, msg, emoji, remove)
	}
	return false, fmt.Errorf("%s does not support sending reactions", p.DisplayName())
}

// SendMessageWithAttachments sends a message with optional file attachments.
// If the provider does not implement AttachmentSender and attachments are given,
// it returns an error rather than silently dropping them.
func SendMessageWithAttachments(prefixedID, text string, styled bool, attachmentPaths []string) (bool, error) {
	p, id, err := routeToProvider(prefixedID)
	if err != nil {
		return false, err
	}
	if len(attachmentPaths) > 0 {
		if as, ok := p.(AttachmentSender); ok {
			return as.SendWithAttachments(id, text, styled, attachmentPaths)
		}
		return false, fmt.Errorf("%s does not support sending attachments", p.DisplayName())
	}
	return p.SendMessage(id, text, styled)
}

func SortMessagesNewestToOldest(messages []Message) {
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].TimestampMS > messages[j].TimestampMS
	})
}

func (c *Conversation) GetParticipants() ([]Participant, error) {
	var p []Participant
	if c.Participants == "" {
		return p, nil
	}
	err := json.Unmarshal([]byte(c.Participants), &p)
	return p, err
}

func FetchContacts(query string, limit int) ([]Contact, error) {
	var all []Contact
	for _, p := range providers {
		contacts, err := p.FetchContacts(query, limit)
		if err != nil {
			continue
		}
		for i := range contacts {
			contacts[i].ProviderID = p.ID()
			if contacts[i].AvailablePlatforms == nil {
				contacts[i].AvailablePlatforms = []string{p.ID()}
			}
		}
		all = append(all, contacts...)
	}

	// Group by phone number
	grouped := make(map[string]*Contact)
	var result []Contact

	for _, c := range all {
		if c.Number == "" {
			result = append(result, c)
			continue
		}

		if existing, ok := grouped[c.Number]; ok {
			// Merge platforms
			found := false
			for _, p := range existing.AvailablePlatforms {
				if p == c.ProviderID {
					found = true
					break
				}
			}
			if !found {
				existing.AvailablePlatforms = append(existing.AvailablePlatforms, c.ProviderID)
			}
			// Prefer non-empty name
			if existing.Name == "" || existing.Name == existing.Number {
				existing.Name = c.Name
			}
		} else {
			copy := c
			grouped[c.Number] = &copy
		}
	}

	for _, c := range grouped {
		result = append(result, *c)
	}

	// Sort by name
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})

	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func FindConversationByNumber(targetNumber string, maxSearchThreads int) (string, error) {
	if targetNumber == "" {
		return "", fmt.Errorf("cannot lookup empty phone number")
	}

	conversations, err := FetchConversations(maxSearchThreads)
	if err != nil {
		return "", err
	}

	for _, c := range conversations {
		if c.IsGroup {
			continue
		}

		participants, err := c.GetParticipants()
		if err != nil {
			continue
		}

		for _, p := range participants {
			if p.IsMe {
				continue
			}

			if p.Number == targetNumber {
				return c.ConversationID, nil
			}
		}
	}

	return "", fmt.Errorf("no active 1-on-1 conversation thread found for number: %s", targetNumber)
}

// FindAllConversationsByNumber returns all 1-on-1 conversations for the given phone number across all providers.
func FindAllConversationsByNumber(number string, limit int) []Conversation {
	convs, _ := FetchConversations(limit)
	var found []Conversation
	seen := make(map[string]bool)
	for _, c := range convs {
		if c.IsGroup {
			continue
		}
		// For providers that embed the number directly in the conversation ID (e.g. Signal),
		// match without relying on participant metadata.
		if _, rawID, ok := strings.Cut(c.ConversationID, ":"); ok && rawID == number {
			if !seen[c.ConversationID] {
				found = append(found, c)
				seen[c.ConversationID] = true
			}
			continue
		}
		ps, _ := c.GetParticipants()
		for _, p := range ps {
			if !p.IsMe && p.Number == number && !seen[c.ConversationID] {
				found = append(found, c)
				seen[c.ConversationID] = true
				break
			}
		}
	}
	return found
}

func FetchSupportedPlatforms() []string {
	// This could be dynamic based on providers, but keeping it simple for now
	return []string{"sms", "whatsapp", "signal", "imessage"}
}

func GetProviders() []Provider {
	return providers
}
