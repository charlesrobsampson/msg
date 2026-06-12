package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m tuiModel) renderUnreadsView() (string, string) {
	var left strings.Builder
	left.WriteString(activeStyle.Render("NEW UNREAD MESSAGES") + "\n")

	if len(m.unreads) == 0 {
		msg := "Inbox clean! No unread chats found."
		if m.activeFilter != "" {
			msg = "No matching unread chats."
		}
		return msg, "All incoming threads caught up."
	}

	vh := m.getViewportHeight() - 2
	leftWidth := (m.termWidth * 4) / 10
	if leftWidth < 25 {
		leftWidth = 25
	}
	availWidth := leftWidth - 4

	// 1. Pre-render all items into lines
	var allLines []string
	for i, c := range m.unreads {
		marker := " "
		if i == m.selectedIndex {
			marker = ">"
		}
		draftTag := ""
		if m.hasDraft(c.ConversationID) {
			draftTag = subtleStyle.Render(" (draft)")
		}

		platformTag := subtleStyle.Render(" [" + c.SourcePlatform + "]")
		name := fmt.Sprintf("%s (%d)%s%s", c.Name, c.UnreadCount, draftTag, platformTag)

		// Wrap name based on availWidth - 2 (for marker and space)
		wrapStyle := lipgloss.NewStyle().Width(availWidth - 2)
		wrapped := wrapStyle.Render(name)
		itemLines := strings.Split(wrapped, "\n")

		for j, line := range itemLines {
			if j == 0 {
				allLines = append(allLines, marker+" "+line)
			} else {
				// Indent wrapped lines of the same item
				allLines = append(allLines, "  "+line)
			}
		}
	}

	// 2. Viewport Clamping (allLines is indexed by line)
	end := m.listOffset + vh
	if end > len(allLines) {
		end = len(allLines)
	}
	// Safety: ensure listOffset hasn't gone out of bounds after a search/refresh
	start := m.listOffset
	if start > len(allLines) {
		start = 0
	}

	renderedLines := 0
	for i := start; i < end; i++ {
		left.WriteString(allLines[i] + "\n")
		renderedLines++
	}

	// Pad if list is short to keep height consistent
	for i := renderedLines; i < vh; i++ {
		left.WriteString("\n")
	}

	active := m.unreads[m.selectedIndex]
	// Seamlessly hook right panel preview directly to our shared messaging timeline pane
	threadPane, _ := m.renderSharedThreadPane(active, m.getViewportHeight())
	return strings.TrimSuffix(left.String(), "\n"), threadPane
}
