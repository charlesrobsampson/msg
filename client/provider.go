package client

// Provider defines the interface for different messaging backends.
type Provider interface {
	// ID returns a unique identifier for the provider (e.g., "openmessage", "signal").
	ID() string

	// DisplayName returns a human-readable name for the provider.
	DisplayName() string

	// FetchConversations retrieves the list of conversations.
	FetchConversations(limit int) ([]Conversation, error)

	// SearchConversations searches for conversations matching the query.
	SearchConversations(query string, limit int) ([]Conversation, error)

	// FetchMessages retrieves messages for a specific conversation.
	FetchMessages(conversationID string, limit int, beforeTS int64, beforeID string) ([]Message, error)

	// SendMessage sends a text message to a conversation.
	// styled requests formatted rendering (e.g. Signal's text_mode=styled); ignored by providers that don't support it.
	SendMessage(conversationID, text string, styled bool) (bool, error)

	// MarkAsRead marks a conversation as read.
	MarkAsRead(conversationID string) error

	// FetchContacts retrieves contacts matching the query.
	FetchContacts(query string, limit int) ([]Contact, error)

	// IsUnreachable returns true if the provider's backend is currently unavailable.
	IsUnreachable() bool
}

// AttachmentSender is an optional extension for providers that support sending
// file attachments. Callers should type-assert before using.
type AttachmentSender interface {
	SendWithAttachments(conversationID, text string, styled bool, attachmentPaths []string) (bool, error)
}

// ReactionSender is an optional extension for providers that support sending
// emoji reactions to messages.
type ReactionSender interface {
	SendReaction(conversationID string, msg Message, emoji string, remove bool) (bool, error)
}
