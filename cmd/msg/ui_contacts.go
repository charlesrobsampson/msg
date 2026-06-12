package main

import (
	"fmt"
	"strings"

	"github.com/charlesrobsampson/msg/client"
	"github.com/charmbracelet/lipgloss"
)

func (m tuiModel) renderContactsView() (string, string) {
	var left, right strings.Builder
	left.WriteString(activeStyle.Render("ADDRESS BOOK") + "\n")

	if len(m.contacts) == 0 {
		return "No matching contact records.", ""
	}

	vh := m.getViewportHeight() - 2
	leftWidth := (m.termWidth * 4) / 10
	if leftWidth < 25 {
		leftWidth = 25
	}
	availWidth := leftWidth - 4

	var allLines []string
	for i, c := range m.contacts {
		marker := " "
		if i == m.selectedIndex {
			marker = ">"
		}

		platforms := strings.Join(c.AvailablePlatforms, ", ")
		name := c.Name
		if name == "" {
			name = "Unknown"
		}
		name += subtleStyle.Render(" [" + platforms + "]")

		// Use same wrapping logic as conversations view
		wrapStyle := lipgloss.NewStyle().Width(availWidth - 2)
		wrapped := wrapStyle.Render(name)
		for _, line := range strings.Split(wrapped, "\n") {
			allLines = append(allLines, marker+" "+line)
		}
	}

	// Viewport logic
	end := m.listOffset + vh
	if end > len(allLines) {
		end = len(allLines)
	}
	start := m.listOffset
	if start > len(allLines) {
		start = 0
	}

	for i := start; i < end; i++ {
		left.WriteString(allLines[i] + "\n")
	}

	// Pad if list is short
	for i := (end - start); i < vh; i++ {
		left.WriteString("\n")
	}

	if m.selectedIndex >= len(m.contacts) {
		m.selectedIndex = len(m.contacts) - 1
		if m.selectedIndex < 0 {
			m.selectedIndex = 0
		}
	}

	active := m.contacts[m.selectedIndex]
	right.WriteString(activeStyle.Render("CONTACT PROFILE CARD") + "\n\n")
	right.WriteString(fmt.Sprintf("Contact ID : %s\n", active.ContactID))
	right.WriteString(fmt.Sprintf("Full Name  : %s\n", active.Name))
	right.WriteString(fmt.Sprintf("Phone Num  : %s\n", active.Number))
	right.WriteString(fmt.Sprintf("Platforms  : %s\n\n", strings.Join(active.AvailablePlatforms, ", ")))

	// Find aliases for this contact
	right.WriteString(activeStyle.Render("ASSOCIATED ALIASES") + "\n")
	found := false

	// Fetch conversations to check for participation
	conversations, err := client.FetchConversations(500)
	if err == nil {
		for _, c := range conversations {
			// Check if contact participates in this conversation
			participants, err := c.GetParticipants()
			if err != nil {
				continue
			}

			isParticipant := false
			for _, p := range participants {
				if p.Number == active.Number {
					isParticipant = true
					break
				}
			}
			// Providers like Signal embed the number directly in the conversation ID
			// and don't populate Participants. Fall back to ID matching for 1:1s.
			if !isParticipant && !c.IsGroup {
				if _, rawID, ok := strings.Cut(c.ConversationID, ":"); ok && rawID == active.Number {
					isParticipant = true
				}
			}

			if isParticipant {
				// Check if there's an alias for this conversation
				for _, a := range m.aliases {
					if a.ConversationID == c.ConversationID {
						right.WriteString(fmt.Sprintf("- %s (%s)\n", a.Name, a.Shortcut))
						found = true
					}
				}
			}
		}
	}

	if !found {
		right.WriteString("None\n")
	}

	return left.String(), right.String()
}

