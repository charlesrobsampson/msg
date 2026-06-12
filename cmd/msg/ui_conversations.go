package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charlesrobsampson/msg/client"
	"github.com/charmbracelet/lipgloss"
)

type lineInfo struct {
	text         string
	messageIndex int
}

func (m tuiModel) getSenderStyle(name string, isMe bool) lipgloss.Style {
	if isMe {
		return meLabelStyle
	}
	colorCode := getSeededNameColor(name, m.cfg.Theme.NameColorPalette)
	return lipgloss.NewStyle().Foreground(lipgloss.Color(colorCode)).Bold(true)
}

// reactionPillLine builds the compact inline reaction display for one message,
// e.g. "    ❤️ 2  👍 1".  Returns "" if there are no reactions.
func reactionPillLine(reactions []client.Reaction) string {
	if len(reactions) == 0 {
		return ""
	}
	var parts []string
	for _, r := range reactions {
		count := r.Count
		if count == 0 {
			count = len(r.Senders)
		}
		if count == 0 {
			count = 1
		}
		if count == 1 {
			parts = append(parts, r.Emoji)
		} else {
			parts = append(parts, fmt.Sprintf("%s %d", r.Emoji, count))
		}
	}
	return "    " + subtleStyle.Render(strings.Join(parts, "  "))
}

// reactionPopupLines builds the per-sender breakdown shown when the user presses e.
func reactionPopupLines(reactions []client.Reaction, availWidth int) []string {
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Width(min(availWidth-4, 40))

	var body strings.Builder
	body.WriteString(activeStyle.Render("REACTIONS") + "\n")
	for _, r := range reactions {
		count := r.Count
		if count == 0 {
			count = len(r.Senders)
		}
		if count == 0 {
			count = 1
		}
		if len(r.Senders) > 0 {
			body.WriteString(fmt.Sprintf("%s  %s\n", r.Emoji, strings.Join(r.Senders, ", ")))
		} else {
			body.WriteString(fmt.Sprintf("%s  %d\n", r.Emoji, count))
		}
	}
	rendered := boxStyle.Render(strings.TrimSuffix(body.String(), "\n"))
	return strings.Split(rendered, "\n")
}

func (m tuiModel) renderSharedThreadPane(active client.Conversation, height int) (string, int) {
	leftWidth := (m.termWidth * 4) / 10
	if leftWidth < 25 {
		leftWidth = 25
	}
	rightWidth := m.termWidth - leftWidth - 6
	if m.state == ViewDraft {
		rightWidth = m.termWidth - 4
	}
	if rightWidth < 20 {
		rightWidth = 20
	}

	availWidth := rightWidth - 4
	availHeight := height - 2

	titleText := fmt.Sprintf(" THREAD: %s ", strings.ToUpper(active.Name))
	if m.focusRight {
		titleText += "[SCROLL ACTIVE]"
	}
	titleRow := activeStyle.Render(titleText)
	msgAreaHeight := availHeight - 1

	var allLines []lineInfo

	// HANDLE METADATA VIEW
	if m.showMetadata {
		allLines = append(allLines, lineInfo{text: activeStyle.Render("CONVERSATION METADATA"), messageIndex: -1})
		allLines = append(allLines, lineInfo{text: subtleStyle.Render(fmt.Sprintf("ID: %s", active.ConversationID)), messageIndex: -1})
		allLines = append(allLines, lineInfo{text: "", messageIndex: -1})
		allLines = append(allLines, lineInfo{text: activeStyle.Render("PARTICIPANTS:"), messageIndex: -1})

		var participants []client.Participant
		_ = json.Unmarshal([]byte(active.Participants), &participants)

		for _, p := range participants {
			pStyle := m.getSenderStyle(p.Name, p.IsMe)
			contactInfo := ""
			contacts, err := client.FetchContacts(p.Number, 1)
			if err == nil && len(contacts) > 0 {
				contactInfo = fmt.Sprintf(" | ID: %s", contacts[0].ContactID)
			}
			allLines = append(allLines, lineInfo{text: fmt.Sprintf("%s (%s%s)", pStyle.Render(p.Name), p.Number, contactInfo), messageIndex: -1})
		}
		for i := len(allLines); i <= msgAreaHeight; i++ {
			allLines = append(allLines, lineInfo{text: "", messageIndex: -1})
		}
	} else {
		// HANDLE MESSAGE THREAD VIEW
		isSignal := active.SourcePlatform == "signal"
		revealSpoilers := isSignal && m.focusRight
		for i := len(m.activeMessages) - 1; i >= 0; i-- {
			msg := m.activeMessages[i]
			senderLabel := msg.SenderName
			if senderLabel == "" {
				senderLabel = active.Name
			}
			if msg.IsFromMe {
				senderLabel = "Me"
			}
			sStyle := m.getSenderStyle(senderLabel, msg.IsFromMe)
			tStr := time.UnixMilli(msg.TimestampMS).Format("01/02 03:04 PM")
			formattedTime := timestampStyle.Render("[" + tStr + "]")

			prefix := fmt.Sprintf("%s %s: ", formattedTime, sStyle.Render(senderLabel))
			prefixLen := lipgloss.Width(prefix)

			displayBody := msg.Body
			if isSignal && displayBody != "" {
				displayBody = renderSignalBody(displayBody, revealSpoilers)
			}
			wrapWidth := availWidth - prefixLen
			if wrapWidth < 10 {
				wrapWidth = 10
			}
			wrapStyle := lipgloss.NewStyle().Width(wrapWidth)

			if displayBody != "" {
				wrappedBody := wrapStyle.Render(displayBody)
				bodyLines := strings.Split(wrappedBody, "\n")
				for j, line := range bodyLines {
					text := prefix + line
					if j > 0 {
						text = "    " + line
					}
					allLines = append(allLines, lineInfo{text: text, messageIndex: i})
				}
			}

			for ai, att := range msg.Attachments {
				typeLabel := att.MimeType
				if typeLabel == "" {
					typeLabel = "attachment"
				}
				label := fmt.Sprintf("📎 %s", typeLabel)
				if len(msg.Attachments) > 1 {
					label = fmt.Sprintf("📎 %d/%d %s", ai+1, len(msg.Attachments), typeLabel)
				}
				attachLine := subtleStyle.Render("    " + label)
				if displayBody == "" && ai == 0 {
					attachLine = prefix + subtleStyle.Render(label)
				}
				allLines = append(allLines, lineInfo{text: attachLine, messageIndex: i})
			}

			// Reaction pills sit on their own line below the message body / attachments.
			if pill := reactionPillLine(msg.Reactions); pill != "" {
				allLines = append(allLines, lineInfo{text: pill, messageIndex: i})
			}
		}
	}

	// 2. Viewport Clamping
	totalLines := len(allLines)
	endIdx := totalLines - m.scrollOffset
	if endIdx > totalLines {
		endIdx = totalLines
	}
	if endIdx < 0 {
		endIdx = 0
	}
	startIdx := endIdx - msgAreaHeight
	if startIdx < 0 {
		startIdx = 0
	}

	bottomMsgIdx := -1
	if endIdx > 0 {
		bottomMsgIdx = allLines[endIdx-1].messageIndex
	}

	// 3. Render visible window
	var final strings.Builder
	final.WriteString(titleRow + "\n")

	renderedLines := 0
	for i := startIdx; i < endIdx; i++ {
		lineText := allLines[i].text
		if !m.showMetadata && i == endIdx-1 {
			lineText = "> " + lineText
		}
		final.WriteString(lineText + "\n")
		renderedLines++
	}

	padding := msgAreaHeight - renderedLines
	for i := 0; i < padding; i++ {
		final.WriteString("\n")
	}

	// 4. Popup overlays — drawn over the last few lines of the pane.
	result := strings.TrimSuffix(final.String(), "\n")

	if m.showReactions && bottomMsgIdx >= 0 && bottomMsgIdx < len(m.activeMessages) && len(m.activeMessages[bottomMsgIdx].Reactions) > 0 {
		popupLines := reactionPopupLines(m.activeMessages[bottomMsgIdx].Reactions, availWidth)
		lines := strings.Split(result, "\n")
		insertAt := len(lines) - len(popupLines)
		if insertAt < 1 {
			insertAt = 1
		}
		for pi, pl := range popupLines {
			if insertAt+pi < len(lines) {
				lines[insertAt+pi] = pl
			}
		}
		result = strings.Join(lines, "\n")
	}

	return result, bottomMsgIdx
}

func (m tuiModel) renderConversationsView() (string, string) {
	var left strings.Builder
	left.WriteString(activeStyle.Render("LIVE CONVERSATIONS") + "\n")
	if len(m.conversations) == 0 {
		msg := "No conversations tracked."
		if m.activeFilter != "" {
			msg = "No matching conversations."
		}
		return msg, ""
	}

	vh := m.getViewportHeight() - 2
	leftWidth := (m.termWidth * 4) / 10
	if leftWidth < 25 {
		leftWidth = 25
	}
	availWidth := leftWidth - 4

	var allLines []string
	for i, c := range m.conversations {
		marker := " "
		if i == m.selectedIndex {
			marker = ">"
		}
		unreadIndicator := ""
		if c.UnreadCount > 0 {
			unreadIndicator = fmt.Sprintf(" (%d)", c.UnreadCount)
		}
		draftTag := ""
		if m.hasDraft(c.ConversationID) {
			draftTag = subtleStyle.Render(" (draft)")
		}
		aliasTag := ""
		for _, a := range m.aliases {
			if a.ConversationID == c.ConversationID {
				aliasTag = subtleStyle.Render(" (" + a.Shortcut + ")")
				break
			}
		}

		platformTag := subtleStyle.Render(" [" + c.SourcePlatform + "]")
		name := c.Name + unreadIndicator + draftTag + aliasTag + platformTag
		if i == m.selectedIndex && !m.focusRight {
			name = activeStyle.Render(c.Name+unreadIndicator) + draftTag + aliasTag + platformTag
		}

		wrapStyle := lipgloss.NewStyle().Width(availWidth - 2)
		wrapped := wrapStyle.Render(name)
		for _, line := range strings.Split(wrapped, "\n") {
			allLines = append(allLines, marker+" "+line)
		}
	}

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
	for i := (end - start); i < vh; i++ {
		left.WriteString("\n")
	}

	if m.selectedIndex >= len(m.conversations) {
		m.selectedIndex = len(m.conversations) - 1
		if m.selectedIndex < 0 {
			m.selectedIndex = 0
		}
	}

	active := m.conversations[m.selectedIndex]
	threadPane, bottomMsgIdx := m.renderSharedThreadPane(active, m.getViewportHeight())
	m.bottomMessageIndex = bottomMsgIdx
	return strings.TrimSuffix(left.String(), "\n"), threadPane
}
