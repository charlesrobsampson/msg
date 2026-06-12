package main

import (
	"fmt"
	"strings"

	"github.com/charlesrobsampson/msg/client"
)

func (m tuiModel) renderNewMessageView() (string, string) {
	var left, right strings.Builder

	// Search input display
	cursor := "█"
	left.WriteString(activeStyle.Render("NEW MESSAGE") + "\n\n")
	left.WriteString(subtleStyle.Render("To: ") + m.newMessageQuery + cursor + "\n\n")

	contacts := m.newMessageContacts
	if len(contacts) == 0 {
		if m.newMessageQuery != "" {
			left.WriteString(subtleStyle.Render("No contacts found.") + "\n")
		} else {
			left.WriteString(subtleStyle.Render("Start typing a name or number...") + "\n")
		}
		right.WriteString(activeStyle.Render("COMPOSE") + "\n\n")
		right.WriteString("Search for a contact above,\nthen press Enter to open a draft.\n")
		return left.String(), right.String()
	}

	vh := m.getViewportHeight() - 6
	if vh < 1 {
		vh = 1
	}
	start := m.listOffset
	end := start + vh
	if end > len(contacts) {
		end = len(contacts)
	}
	for i := start; i < end; i++ {
		marker := " "
		if i == m.selectedIndex {
			marker = ">"
		}
		c := contacts[i]
		name := c.Name
		if name == "" {
			name = c.Number
		}
		left.WriteString(fmt.Sprintf("%s %s\n", marker, name))
	}

	// Right panel: selected contact detail
	right.WriteString(activeStyle.Render("CONTACT") + "\n\n")
	if m.selectedIndex < len(contacts) {
		sel := contacts[m.selectedIndex]
		name := sel.Name
		if name == "" {
			name = sel.Number
		}
		right.WriteString(fmt.Sprintf("Name   : %s\n", name))
		right.WriteString(fmt.Sprintf("Number : %s\n", sel.Number))

		// Show available conversation threads
		existing := client.FindAllConversationsByNumber(sel.Number, 200)
		if len(existing) > 0 {
			right.WriteString("\nExisting threads:\n")
			for _, c := range existing {
				right.WriteString(fmt.Sprintf("  • %s (%s)\n", providerDisplayLabel(extractConvPlatform(c.ConversationID)), c.ConversationID))
			}
		} else {
			right.WriteString("\nNo existing threads.\n")
		}
		right.WriteString("\nPress Enter to compose.\n")
	}
	return left.String(), right.String()
}
