package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charlesrobsampson/msg/client"
	"github.com/charmbracelet/lipgloss"
)

func (m tuiModel) renderDraftView() (string, string) {
	// Find the active conversation
	var active client.Conversation
	found := false

	// Search in conversations list
	for _, c := range m.conversations {
		if c.ConversationID == m.draftTargetID {
			active = c
			found = true
			break
		}
	}

	// Fallback to unreads
	if !found {
		for _, c := range m.unreads {
			if c.ConversationID == m.draftTargetID {
				active = c
				found = true
				break
			}
		}
	}

	// For new conversations (no existing thread), synthesize a minimal Conversation
	// so the draft pane still renders with the correct recipient and cursor visible.
	if !found {
		name := m.draftTargetID
		if idx := strings.Index(name, ":"); idx != -1 {
			name = name[idx+1:]
		}
		active = client.Conversation{
			ConversationID: m.draftTargetID,
			Name:           name,
			IsGroup:        false,
			SourcePlatform: m.draftPlatform,
		}
	}

	// 1. Participant Verification Logic
	var participants []client.Participant
	_ = json.Unmarshal([]byte(active.Participants), &participants)

	var recipient client.Participant
	hasMe := false
	recipientCount := 0

	for _, p := range participants {
		if p.IsMe {
			hasMe = true
		} else {
			recipient = p
			recipientCount++
		}
	}

	// Safety Check: Exactly 2 participants, one is Me, one is recipient
	isSafe := hasMe && recipientCount == 1 && len(participants) == 2

	// For providers that don't populate Participants (e.g. Signal), infer the
	// recipient from the conversation metadata when it's clearly a 1:1.
	if !isSafe && !active.IsGroup && len(participants) == 0 {
		_, rawID, _ := strings.Cut(active.ConversationID, ":")
		recipient = client.Participant{Name: active.Name, Number: rawID}
		isSafe = true
	}

	// Calculate heights
	vh := m.getViewportHeight()
	draftHeight := 6
	if vh < 15 {
		draftHeight = 4
	}
	convHeight := vh - draftHeight

	// 1. Render Conversation (Top)
	convContent, _ := m.renderSharedThreadPane(active, convHeight)

	if m.showEmojiPicker {
		pickerLines := renderEmojiPicker(m.emojiPickerIdx, m.termWidth-8)
		lines := strings.Split(convContent, "\n")
		insertAt := len(lines) - len(pickerLines) - 1
		if insertAt < 1 {
			insertAt = 1
		}
		for pi, pl := range pickerLines {
			if insertAt+pi < len(lines) {
				lines[insertAt+pi] = pl
			}
		}
		convContent = strings.Join(lines, "\n")
	}

	// 2. Render Draft Input (Bottom)
	var draft strings.Builder

	// Draft Header: Recipient Metadata
	var header string
	if isSafe {
		header = fmt.Sprintf(" SENDING TO: %s (%s) ", recipient.Name, recipient.Number)
	} else if !found && !active.IsGroup {
		// New conversation: show target from conv ID
		header = fmt.Sprintf(" NEW CONVERSATION TO: %s ", active.Name)
	} else {
		header = " SENDING TO: MULTIPLE RECIPIENTS (UNSAFE) "
	}
	draft.WriteString(activeStyle.Render(header) + "\n")

	// Build draft content with cursor block at the correct rune position.
	availWidth := m.termWidth - 8
	wrapStyle := lipgloss.NewStyle().Width(availWidth)
	runes := []rune(m.draftBuffer)
	cur := m.draftCursor
	if cur > len(runes) {
		cur = len(runes)
	}
	draftWithCursor := string(runes[:cur]) + "█" + string(runes[cur:])
	wrappedDraft := wrapStyle.Render(draftWithCursor)

	lines := strings.Split(wrappedDraft, "\n")

	// Find the wrapped line that contains the cursor so we always scroll it into view.
	cursorLine := 0
	for i, l := range lines {
		if strings.Contains(l, "█") {
			cursorLine = i
			break
		}
	}
	maxVisible := draftHeight - 2
	startLine := 0
	if len(lines) > maxVisible {
		startLine = cursorLine - (maxVisible - 1)
		if startLine < 0 {
			startLine = 0
		}
		if startLine+maxVisible > len(lines) {
			startLine = len(lines) - maxVisible
		}
	}

	draftText := strings.Join(lines[startLine:], "\n")
	draft.WriteString(draftText + "\n")

	// Show pending attachments
	if len(m.draftAttachments) > 0 {
		draft.WriteString(subtleStyle.Render(fmt.Sprintf("📎 %d attachment(s) queued (ctrl+r removes last):", len(m.draftAttachments))) + "\n")
		for i, p := range m.draftAttachments {
			draft.WriteString(subtleStyle.Render(fmt.Sprintf("  %d. %s", i+1, filepath.Base(p))) + "\n")
		}
	}

	return convContent, draft.String()
}

// draftInsert inserts text at the cursor position and advances the cursor.
func (m *tuiModel) draftInsert(text string) {
	runes := []rune(m.draftBuffer)
	ins := []rune(text)
	result := make([]rune, 0, len(runes)+len(ins))
	result = append(result, runes[:m.draftCursor]...)
	result = append(result, ins...)
	result = append(result, runes[m.draftCursor:]...)
	m.draftBuffer = string(result)
	m.draftCursor += len(ins)
}

// draftBackspace deletes the rune immediately before the cursor.
func (m *tuiModel) draftBackspace() {
	if m.draftCursor == 0 {
		return
	}
	runes := []rune(m.draftBuffer)
	result := make([]rune, 0, len(runes)-1)
	result = append(result, runes[:m.draftCursor-1]...)
	result = append(result, runes[m.draftCursor:]...)
	m.draftBuffer = string(result)
	m.draftCursor--
}

// draftDeleteForward deletes the rune at the cursor position.
func (m *tuiModel) draftDeleteForward() {
	runes := []rune(m.draftBuffer)
	if m.draftCursor >= len(runes) {
		return
	}
	result := make([]rune, 0, len(runes)-1)
	result = append(result, runes[:m.draftCursor]...)
	result = append(result, runes[m.draftCursor+1:]...)
	m.draftBuffer = string(result)
}

// draftLineCol returns the 0-based logical line and column of the cursor.
func (m tuiModel) draftLineCol() (line, col int) {
	runes := []rune(m.draftBuffer)
	n := m.draftCursor
	if n > len(runes) {
		n = len(runes)
	}
	for i := 0; i < n; i++ {
		if runes[i] == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	return
}

// draftMoveUp moves the cursor to the same column on the previous logical line.
func (m *tuiModel) draftMoveUp() {
	line, col := m.draftLineCol()
	if line == 0 {
		m.draftCursor = 0
		return
	}
	lines := strings.Split(m.draftBuffer, "\n")
	start := 0
	for i := 0; i < line-1; i++ {
		start += len([]rune(lines[i])) + 1
	}
	prevLen := len([]rune(lines[line-1]))
	if col > prevLen {
		col = prevLen
	}
	m.draftCursor = start + col
}

// draftMoveDown moves the cursor to the same column on the next logical line.
func (m *tuiModel) draftMoveDown() {
	line, col := m.draftLineCol()
	lines := strings.Split(m.draftBuffer, "\n")
	if line >= len(lines)-1 {
		m.draftCursor = len([]rune(m.draftBuffer))
		return
	}
	start := 0
	for i := 0; i <= line; i++ {
		start += len([]rune(lines[i])) + 1
	}
	nextLen := len([]rune(lines[line+1]))
	if col > nextLen {
		col = nextLen
	}
	m.draftCursor = start + col
}

// draftMoveLineStart moves the cursor to the beginning of the current logical line.
func (m *tuiModel) draftMoveLineStart() {
	line, _ := m.draftLineCol()
	lines := strings.Split(m.draftBuffer, "\n")
	start := 0
	for i := 0; i < line; i++ {
		start += len([]rune(lines[i])) + 1
	}
	m.draftCursor = start
}

// draftMoveLineEnd moves the cursor to the end of the current logical line.
func (m *tuiModel) draftMoveLineEnd() {
	line, _ := m.draftLineCol()
	lines := strings.Split(m.draftBuffer, "\n")
	start := 0
	for i := 0; i < line; i++ {
		start += len([]rune(lines[i])) + 1
	}
	m.draftCursor = start + len([]rune(lines[line]))
}
