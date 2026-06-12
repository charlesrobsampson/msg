package mocks

import (
	"encoding/json"
	"time"

	"github.com/charlesrobsampson/msg/client"
)

const (
	TestSignalAccount  = "+15550001111"
	TestContact1Number = "+15551234567"
	TestContact2Number = "+15557654321"
)

// SeedDefault populates both mocks with a standard set of realistic test data.
func SeedDefault(om *OpenMessageMock, sig *SignalMock) {
	now := time.Now().UnixMilli()

	participants1, _ := json.Marshal([]client.Participant{
		{Name: "Alice Smith", Number: TestContact1Number, IsMe: false},
		{Name: "Me", Number: TestSignalAccount, IsMe: true},
	})
	participants2, _ := json.Marshal([]client.Participant{
		{Name: "Bob Jones", Number: TestContact2Number, IsMe: false},
		{Name: "Me", Number: TestSignalAccount, IsMe: true},
	})

	om.Conversations = []client.Conversation{
		{
			ConversationID: "conv-alice",
			Name:           "Alice Smith",
			IsGroup:        false,
			Participants:   string(participants1),
			LastMessageTS:  now - 60_000,
			UnreadCount:    2,
			SourcePlatform: "sms",
		},
		{
			ConversationID: "conv-bob",
			Name:           "Bob Jones",
			IsGroup:        false,
			Participants:   string(participants2),
			LastMessageTS:  now - 3_600_000,
			UnreadCount:    0,
			SourcePlatform: "google_messages",
		},
		{
			ConversationID: "conv-team",
			Name:           "Team Chat",
			IsGroup:        true,
			LastMessageTS:  now - 7_200_000,
			UnreadCount:    0,
			SourcePlatform: "google_messages",
		},
	}

	om.Messages["conv-alice"] = []client.Message{
		{MessageID: "a-1", SenderName: "Alice Smith", Body: "Hey! Are you free this weekend?", IsFromMe: false, TimestampMS: now - 120_000},
		{MessageID: "a-2", SenderName: "Me", Body: "Yeah, thinking about hiking. You in?", IsFromMe: true, TimestampMS: now - 90_000},
		{MessageID: "a-3", SenderName: "Alice Smith", Body: "Absolutely! What trail?", IsFromMe: false, TimestampMS: now - 60_000},
		{MessageID: "a-4", SenderName: "Alice Smith", Body: "Also bring snacks please", IsFromMe: false, TimestampMS: now - 45_000},
	}
	om.Messages["conv-bob"] = []client.Message{
		{MessageID: "b-1", SenderName: "Bob Jones", Body: "Can you review my PR?", IsFromMe: false, TimestampMS: now - 7_200_000},
		{MessageID: "b-2", SenderName: "Me", Body: "On it", IsFromMe: true, TimestampMS: now - 3_600_000},
	}
	om.Messages["conv-team"] = []client.Message{
		{MessageID: "t-1", SenderName: "Alice Smith", Body: "Good morning team", IsFromMe: false, TimestampMS: now - 7_200_000},
		{MessageID: "t-2", SenderName: "Me", Body: "Morning!", IsFromMe: true, TimestampMS: now - 7_180_000},
	}

	om.Contacts = []client.Contact{
		{ContactID: "contact-1", Name: "Alice Smith", Number: TestContact1Number},
		{ContactID: "contact-2", Name: "Bob Jones", Number: TestContact2Number},
	}

	sig.Accounts = []string{TestSignalAccount}
	sig.Contacts[TestSignalAccount] = []SignalContactData{
		{Number: TestContact1Number, Name: "Alice Smith"},
		{Number: TestContact2Number, Name: "Bob Jones"},
	}
	sig.Groups[TestSignalAccount] = []SignalGroupData{
		{ID: "group.SIG01==", Name: "Signal Group Chat"},
	}
}
