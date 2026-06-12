package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charlesrobsampson/msg/client"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const tuiRefreshInterval = 5 * time.Second

type tickMsg time.Time

func doTick() tea.Cmd {
	return tea.Tick(tuiRefreshInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

type AppState int

const (
	ViewUnreads AppState = iota
	ViewConversations
	ViewContacts
	ViewAliases
	ViewNewMessage
	ViewPlatforms
	ViewStyles
	ViewDraft
	ThemePropertiesCount = 7 // number of color/palette fields in the Theme section
	settingsCount        = 8 // ThemePropertiesCount + 1 (editor)
)

type externalEditorDoneMsg struct {
	sent   bool
	convID string
	errMsg string
}

type attachmentPickedMsg struct {
	paths  []string
	errMsg string
}

type providerStatusLoadedMsg struct {
	statuses []platformStatus
}


type platformOption struct {
	convID        string // prefixed: "signal:+1...", "sms:thread_id"
	providerID    string
	providerLabel string
	convName      string
	isNew         bool // true = no existing thread; first send creates it
}

func loadProviderStatusCmd(cfg client.Config) tea.Cmd {
	return func() tea.Msg {
		return providerStatusLoadedMsg{statuses: loadPlatformStatuses(cfg)}
	}
}

var (
	subtleStyle    lipgloss.Style
	activeStyle    lipgloss.Style
	boxStyle       lipgloss.Style
	titleStyle     lipgloss.Style
	timestampStyle lipgloss.Style
	meLabelStyle   lipgloss.Style
)

type tuiModel struct {
	state              AppState
	selectedIndex      int
	listOffset         int
	itemLineOffsets    []int // Added: maps item index to its starting line in pre-rendered buffer
	focusRight         bool
	scrollOffset       int
	bottomMessageIndex int
	reachedEnd         bool

	cfg        client.Config
	undoConfig client.Config

	unreads          []client.Conversation
	allConversations []client.Conversation
	conversations    []client.Conversation
	contacts         []client.Contact
	aliases          map[string]client.Alias
	aliasKeys        []string
	platforms        []string
	platformStatuses []platformStatus

	activeMessages  []client.Message
	showMetadata    bool
	showReactions   bool // reaction breakdown popup for highlighted message
	showEmojiPicker bool
	emojiPickerIdx  int

	isEditingAlias bool
	editFields     [4]string
	fieldIndex     int

	isEditingStyles bool
	styleFields     [settingsCount]string
	styleFieldIndex int

	draftBuffer      string
	draftCursor      int
	draftTargetID    string
	draftSourceState AppState
	draftStyled      bool
	draftPlatform    string

	draftAttachments     []string
	isEnteringAttachPath bool
	attachPathBuffer     string

	isSearching  bool
	searchBuffer string
	activeFilter string
	viewFilters  map[AppState]string

	errorMessage    string
	statusMessage   string
	providerWarning string

	newMessageQuery    string
	newMessageContacts []client.Contact

	pickingPlatform    bool
	platformOptions    []platformOption
	platformPickIdx    int
	platformPickAction string // "draft" | "styled-draft" | "ext-draft" | "ext-styled-draft"

	termWidth  int
	termHeight int
}

func updateStylesInTUI(cfg client.Config) {
	subtleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(cfg.Theme.SubtleColor))
	activeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(cfg.Theme.PrimaryColor)).Bold(true)
	timestampStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(cfg.Theme.TimestampColor))
	meLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(cfg.Theme.MeMessageColor)).Bold(true)

	// Start with a clean base structure
	baseBox := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(cfg.Theme.SubtleColor)).
		Padding(0, 1)

	// Dynamically inherit text colors purely if explicitly set in the user's config
	if cfg.Theme.MainTextColor != "" {
		baseBox = baseBox.Foreground(lipgloss.Color(cfg.Theme.MainTextColor))
	}
	if cfg.Theme.BackgroundColor != "" {
		baseBox = baseBox.Background(lipgloss.Color(cfg.Theme.BackgroundColor))
	}

	boxStyle = baseBox

	// Title style uses PrimaryColor for background, if empty it defaults to terminal highlight or similar
	titleStyle = lipgloss.NewStyle().
		Bold(true).
		Padding(0, 1)

	if cfg.Theme.PrimaryColor != "" {
		titleStyle = titleStyle.Background(lipgloss.Color(cfg.Theme.PrimaryColor)).Foreground(lipgloss.Color("255"))
	} else {
		// Fallback for terminal default: Reverse video or just bold
		titleStyle = titleStyle.Reverse(true)
	}
}

func (m tuiModel) hasDraft(convID string) bool {
	path, _ := client.GetDraftPath(convID)
	if _, err := os.Stat(path); err == nil {
		data, _ := os.ReadFile(path)
		return len(data) > 0
	}
	return false
}

// openDraftForConv enters the in-TUI draft view for the given prefixed conversation ID.
func (m *tuiModel) openDraftForConv(convID string, styled bool) {
	m.draftTargetID = convID
	m.draftSourceState = m.state
	m.draftBuffer, _ = client.LoadDraft(convID)
	m.draftCursor = len([]rune(m.draftBuffer))
	m.draftStyled = styled
	m.draftPlatform = extractConvPlatform(convID)
	m.state = ViewDraft
	m.activeMessages, _ = client.FetchMessages(convID, 100, 0, "")
}

// openAliasEditor opens the alias editor for the given prefixed conversation ID.
// convID must already be a prefixed ID (e.g. "sms:192", "signal:+1…").
func (m *tuiModel) openAliasEditor(convID, contactName string) {
	platform := extractConvPlatform(convID)
	m.aliases, _ = client.LoadAliases()
	// Key is the prefixed conv ID itself.
	if existing, ok := m.aliases[convID]; ok {
		m.editFields = [4]string{existing.Shortcut, existing.Name, existing.ConversationID, existing.Platform}
	} else {
		m.editFields = [4]string{"", contactName, convID, platform}
	}
	m.draftSourceState = m.state
	m.state = ViewAliases
	m.isEditingAlias = true
	m.fieldIndex = 0
	m.refreshAliasKeys()
}

// buildPlatformOptions returns draft options for a contact:
// existing conversations on each provider + "new conversation" options for providers without an existing thread.
func (m tuiModel) buildPlatformOptions(contact client.Contact) []platformOption {
	existing := client.FindAllConversationsByNumber(contact.Number, 200)
	seenProviders := make(map[string]bool)
	var opts []platformOption
	for _, c := range existing {
		pid := extractConvPlatform(c.ConversationID)
		seenProviders[pid] = true
		opts = append(opts, platformOption{
			convID:        c.ConversationID,
			providerID:    pid,
			providerLabel: providerDisplayLabel(pid),
			convName:      c.Name,
			isNew:         false,
		})
	}
	if contact.Number != "" {
		for _, p := range client.GetProviders() {
			pid := p.ID()
			if seenProviders[pid] {
				continue
			}
			switch pid {
			case "signal":
				opts = append(opts, platformOption{
					convID:        "signal:" + contact.Number,
					providerID:    "signal",
					providerLabel: "Signal",
					convName:      contact.Name,
					isNew:         true,
				})
			case "sms":
				opts = append(opts, platformOption{
					convID:        "sms:" + contact.Number,
					providerID:    "sms",
					providerLabel: "Google Messages",
					convName:      contact.Name,
					isNew:         true,
				})
			}
		}
	}
	return opts
}

func providerDisplayLabel(providerID string) string {
	for _, p := range client.GetProviders() {
		if p.ID() == providerID {
			return p.DisplayName()
		}
	}
	switch providerID {
	case "signal":
		return "Signal"
	case "sms":
		return "Google Messages"
	}
	return providerID
}

func (m *tuiModel) fetchMoreListItems() {
	switch m.state {
	case ViewConversations:
		if m.activeFilter == "" {
			limit := len(m.allConversations) + 100
			m.allConversations, _ = client.FetchConversations(limit)
			m.conversations = m.allConversations
		} else {
			// If we are filtering, we might want to fetch more from server-side search
			limit := len(m.conversations) + 100
			m.conversations, _ = client.SearchConversations(m.activeFilter, limit)
		}
	case ViewContacts:
		limit := len(m.contacts) + 50
		m.contacts, _ = client.FetchContacts(m.activeFilter, limit)
	default:
		return
	}
	m.syncItemOffsets()
}

func (m *tuiModel) syncItemOffsets() {
	leftWidth := (m.termWidth * 4) / 10
	if leftWidth < 25 {
		leftWidth = 25
	}
	availWidth := leftWidth - 4

	m.itemLineOffsets = []int{}
	currentLine := 0

	var items []string
	switch m.state {
	case ViewUnreads:
		for _, c := range m.unreads {
			items = append(items, c.Name) // Simplified for measurement
		}
	case ViewConversations:
		for _, c := range m.conversations {
			items = append(items, c.Name)
		}
	case ViewContacts:
		for _, c := range m.contacts {
			items = append(items, c.Name)
		}
	case ViewAliases:
		for _, k := range m.aliasKeys {
			items = append(items, k)
		}
	}

	for _, item := range items {
		m.itemLineOffsets = append(m.itemLineOffsets, currentLine)

		// Approximate line count after wrapping
		wrapStyle := lipgloss.NewStyle().Width(availWidth - 2)
		wrapped := wrapStyle.Render(item)
		lineCount := strings.Count(wrapped, "\n") + 1
		currentLine += lineCount
	}
}

func initialModel() tuiModel {
	cfg, _ := client.LoadConfig()
	updateStylesInTUI(cfg)
	client.InitProviders(cfg)

	m := tuiModel{
		state:         ViewUnreads,
		selectedIndex: 0,
		aliases:       make(map[string]client.Alias),
		platforms:     []string{"Google Messages", "Signal"},
		showMetadata:  false,
		cfg:           cfg,
		undoConfig:    cfg,
		termWidth:     80,
		termHeight:    24,
		viewFilters:   make(map[AppState]string),
	}
	m.refreshUnreads()
	if convs, err := client.FetchConversations(500); err == nil {
		m.allConversations = convs
		m.conversations = convs
	}
	if data, err := client.LoadAliases(); err == nil {
		m.aliases = data
		m.refreshAliasKeys()
	}

	m.loadActiveMessages()
	m.syncItemOffsets()
	return m
}

// switchView saves the current view's filter, then restores the target view's filter.
func (m *tuiModel) switchView(next AppState) {
	m.viewFilters[m.state] = m.activeFilter
	m.isSearching = false
	m.searchBuffer = ""
	m.state = next
	m.selectedIndex = 0
	m.listOffset = 0
	m.activeFilter = m.viewFilters[next]
}

func (m *tuiModel) applyActiveFilter() {
	switch m.state {
	case ViewUnreads:
		m.refreshUnreads()
	case ViewConversations:
		// Ensure we have a deep pool of conversations with full metadata
		if len(m.allConversations) == 0 {
			m.allConversations, _ = client.FetchConversations(500)
		}

		if m.activeFilter == "" {
			m.conversations = m.allConversations
		} else {
			// Perform local filtering on the deep pool to preserve Metadata/Participants
			var filtered []client.Conversation
			query := strings.ToLower(m.activeFilter)
			for _, c := range m.allConversations {
				// Match on name or participants
				if strings.Contains(strings.ToLower(c.Name), query) ||
					strings.Contains(strings.ToLower(c.Participants), query) {
					filtered = append(filtered, c)
				}
			}

			// If local filter yielded results, use them.
			// If not, fall back to server-side search for deeper historical matches (metadata may be missing)
			if len(filtered) > 0 {
				m.conversations = filtered
			} else {
				results, _ := client.SearchConversations(m.activeFilter, 100)
				// Enrich results if they exist in our full metadata pool
				for i, r := range results {
					for _, ac := range m.allConversations {
						if ac.ConversationID == r.ConversationID {
							results[i] = ac
							break
						}
					}
				}
				m.conversations = results
			}
		}
	case ViewContacts:
		m.contacts, _ = client.FetchContacts(m.activeFilter, 100)
	case ViewAliases:
		m.refreshAliasKeys()
	}
	m.syncItemOffsets()
	m.loadActiveMessages()
}

func (m *tuiModel) refreshAliasKeys() {
	m.aliasKeys = []string{}
	// Collect all shortcut names for display
	for _, a := range m.aliases {
		if m.activeFilter == "" || strings.Contains(strings.ToLower(a.Shortcut), strings.ToLower(m.activeFilter)) {
			m.aliasKeys = append(m.aliasKeys, a.Shortcut)
		}
	}
	m.syncItemOffsets()
}

func (m *tuiModel) refreshUnreads() {
	m.unreads = []client.Conversation{}
	if convs, err := client.FetchConversations(50); err == nil {
		for _, c := range convs {
			if c.UnreadCount > 0 {
				if m.activeFilter == "" || strings.Contains(strings.ToLower(c.Name), strings.ToLower(m.activeFilter)) {
					m.unreads = append(m.unreads, c)
				}
			}
		}
	}
	m.syncItemOffsets()
}

func (m *tuiModel) loadStylesViewState() {
	m.styleFields[0] = m.cfg.Theme.PrimaryColor
	m.styleFields[1] = m.cfg.Theme.SubtleColor
	m.styleFields[2] = m.cfg.Theme.MeMessageColor
	m.styleFields[3] = m.cfg.Theme.MainTextColor
	m.styleFields[4] = m.cfg.Theme.BackgroundColor
	m.styleFields[5] = m.cfg.Theme.TimestampColor
	m.styleFields[6] = m.cfg.Theme.NameColorPalette
	m.styleFields[7] = m.cfg.Editor
	m.styleFieldIndex = 0
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(doTick(), loadProviderStatusCmd(m.cfg), tea.EnableBracketedPaste)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		return m, nil

	case providerStatusLoadedMsg:
		m.platformStatuses = msg.statuses
		// Count enabled providers and how many are actually working.
		enabledCount := 0
		badCount := 0
		for _, ps := range m.platformStatuses {
			if !ps.enabled {
				continue
			}
			enabledCount++
			if ps.state != "connected" {
				badCount++
			}
		}
		switch {
		case enabledCount == 0:
			// No providers configured at all — go straight to providers page.
			m.state = ViewPlatforms
			m.selectedIndex = 0
			m.providerWarning = ""
		case badCount == enabledCount:
			// All enabled providers are unhealthy — go to providers page.
			m.state = ViewPlatforms
			m.selectedIndex = 0
			m.providerWarning = ""
		case badCount > 0:
			// Some providers need attention — show a banner.
			m.providerWarning = fmt.Sprintf("%d provider(s) need attention — press 6 to manage", badCount)
		default:
			m.providerWarning = ""
		}
		return m, nil

	case string:
		if msg == "reload-aliases" {
			m.aliases, _ = client.LoadAliases()
			m.refreshAliasKeys()
			m.selectedIndex = 0
			m.statusMessage = "Aliases config reloaded from disk file!"
			return m, nil
		}
		if msg == "refresh-provider-status" {
			m.platformStatuses = loadPlatformStatuses(m.cfg)
			m.statusMessage = "Provider status refreshed."
			return m, nil
		}
		if msg == "signal-linked" {
			m.platformStatuses = loadPlatformStatuses(m.cfg)
			m.statusMessage = "Signal linked — starting message receiver..."
			return m, func() tea.Msg {
				ensureSignalReceiverRunning()
				return "refresh-provider-status"
			}
		}

	case attachmentPickedMsg:
		if msg.errMsg != "" {
			m.statusMessage = msg.errMsg
		} else if len(msg.paths) > 0 {
			m.draftAttachments = append(m.draftAttachments, msg.paths...)
			m.statusMessage = fmt.Sprintf("%d attachment(s) queued (ctrl+r to remove last)", len(m.draftAttachments))
		}
		return m, nil

	case externalEditorDoneMsg:
		if msg.errMsg != "" {
			m.statusMessage = msg.errMsg
		} else if msg.sent {
			m.draftAttachments = nil
			m.statusMessage = "Message sent!"
			m.loadActiveMessages()
			if m.state == ViewUnreads {
				m.refreshUnreads()
			}
		} else {
			m.statusMessage = "Draft saved."
		}
		return m, nil

	case tickMsg:
		// Only refresh the message pane when the user is at the bottom (scrollOffset == 0)
		// so we don't jolt them while they're reading old messages.
		if m.scrollOffset == 0 {
			m.refreshMessages()
		}
		return m, doTick()

	case tea.KeyMsg:
		m.statusMessage = ""

		// INTERACTIVE INPUT ROUTE 1: IN-APP ALIAS FIELDS
		if m.isEditingAlias {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "esc":
				m.isEditingAlias = false
				// If we have a stored state, return to it, otherwise default to ViewConversations
				if m.draftSourceState != 0 {
					m.state = m.draftSourceState
				} else {
					m.state = ViewConversations
				}
				m.statusMessage = "Editing aborted."
				return m, nil
			case "up":
				if m.fieldIndex > 0 {
					m.fieldIndex--
				}
			case "down":
				if m.fieldIndex < ThemePropertiesCount-1 {
					m.fieldIndex++
				}
			case "backspace":
				if len(m.editFields[m.fieldIndex]) > 0 {
					m.editFields[m.fieldIndex] = m.editFields[m.fieldIndex][:len(m.editFields[m.fieldIndex])-1]
				}
			case "enter":
				shortcut := m.editFields[0]
				name := m.editFields[1]
				convID := m.editFields[2]
				platform := m.editFields[3]

				if convID == "" || platform == "" {
					m.statusMessage = "Conversation ID and Platform required."
					return m, nil
				}
				if shortcut == "" {
					m.statusMessage = "Shortcut cannot be empty."
					return m, nil
				}

				// convID in editFields[2] is the canonical prefixed ID (e.g. "sms:192").
				// Derive platform from prefix if editFields[3] is empty or inconsistent.
				if p, _, ok := strings.Cut(convID, ":"); ok && p != "" {
					platform = p
				}
				aliasKey := convID // key = prefixed conv ID

				// Check for shortcut collisions
				for k, a := range m.aliases {
					if a.Shortcut == shortcut && k != aliasKey {
						m.statusMessage = "Shortcut already in use by another conversation."
						return m, nil
					}
				}

				m.aliases[aliasKey] = client.Alias{
					Shortcut:       shortcut,
					Name:           name,
					ConversationID: convID,
					Platform:       platform,
				}

				client.WriteAllAliases(m.aliases)

				// Reset alias view state
				m.activeFilter = ""
				m.selectedIndex = 0
				m.listOffset = 0
				m.refreshAliasKeys()
				m.isEditingAlias = false
				m.statusMessage = "Alias card successfully updated!"
				return m, nil

			default:
				keyStr := msg.String()
				if len(keyStr) == 1 {
					m.editFields[m.fieldIndex] += keyStr
				}
			}
			return m, nil
		}

		// INTERACTIVE INPUT ROUTE 2: LIVE STYLE FIELD PROPERTIES
		if m.isEditingStyles {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "esc":
				m.isEditingStyles = false
				return m, nil
			case "up":
				if m.styleFieldIndex > 0 {
					m.styleFieldIndex--
				}
			case "down":
				if m.styleFieldIndex < settingsCount-1 {
					m.styleFieldIndex++
				}
			case "backspace":
				if len(m.styleFields[m.styleFieldIndex]) > 0 {
					m.styleFields[m.styleFieldIndex] = m.styleFields[m.styleFieldIndex][:len(m.styleFields[m.styleFieldIndex])-1]
				}
			case "enter":
				// Capture current state to undo buffer checkpoint before overwriting
				m.undoConfig = m.cfg

				m.cfg.Theme.PrimaryColor = m.styleFields[0]
				m.cfg.Theme.SubtleColor = m.styleFields[1]
				m.cfg.Theme.MeMessageColor = m.styleFields[2]
				m.cfg.Theme.MainTextColor = m.styleFields[3]
				m.cfg.Theme.BackgroundColor = m.styleFields[4]
				m.cfg.Theme.TimestampColor = m.styleFields[5]
				m.cfg.Theme.NameColorPalette = m.styleFields[6]
				m.cfg.Editor = m.styleFields[7]

				_ = client.SaveConfig(m.cfg)
				updateStylesInTUI(m.cfg)

				m.isEditingStyles = false
				m.statusMessage = "Settings saved! Tap 'u' to undo."
				return m, nil
			default:
				keyStr := msg.String()
				if len(keyStr) == 1 {
					m.styleFields[m.styleFieldIndex] += keyStr
				}
			}
			return m, nil
		}

		// INTERACTIVE INPUT ROUTE 3: MESSAGE DRAFTING
		if m.state == ViewDraft {
			// Emoji picker intercepts all keys while open.
			if m.showEmojiPicker {
				switch msg.String() {
				case "ctrl+c":
					return m, tea.Quit
				case "esc", "ctrl+e":
					m.showEmojiPicker = false
				case "left", "h":
					if m.emojiPickerIdx%emojiCols > 0 {
						m.emojiPickerIdx--
					}
				case "right", "l":
					if m.emojiPickerIdx%emojiCols < emojiCols-1 && m.emojiPickerIdx+1 < len(commonEmojis) {
						m.emojiPickerIdx++
					}
				case "up", "k":
					if m.emojiPickerIdx >= emojiCols {
						m.emojiPickerIdx -= emojiCols
					}
				case "down", "j":
					if m.emojiPickerIdx+emojiCols < len(commonEmojis) {
						m.emojiPickerIdx += emojiCols
					}
				case "enter":
					if m.emojiPickerIdx < len(commonEmojis) {
						m.draftInsert(commonEmojis[m.emojiPickerIdx])
					}
					m.showEmojiPicker = false
				}
				return m, nil
			}

			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "esc":
				_ = client.SaveDraft(m.draftTargetID, m.draftBuffer)
				m.state = m.draftSourceState
				m.draftStyled = false
				m.draftPlatform = ""
				m.statusMessage = "Draft saved locally."
				return m, nil
			case "ctrl+o":
				// ctrl+m = Enter (0x0D), ctrl+t = SIGINFO on macOS — both intercepted by the terminal.
				// ctrl+o (0x0F) is safe on all platforms.
				if platformSupportsStyled(m.draftPlatform) {
					m.draftStyled = !m.draftStyled
				}
				return m, nil
			case "ctrl+s":
				if m.draftBuffer != "" || len(m.draftAttachments) > 0 {
					success, err := client.SendMessageWithAttachments(m.draftTargetID, m.draftBuffer, m.draftStyled, m.draftAttachments)
					if err == nil && success {
						_ = client.DeleteDraft(m.draftTargetID)
						m.draftBuffer = ""
						m.draftCursor = 0
						m.draftStyled = false
						m.draftPlatform = ""
						m.draftAttachments = nil
						m.state = m.draftSourceState
						m.statusMessage = "Message dispatched successfully!"
						m.loadActiveMessages()
						if m.draftSourceState == ViewUnreads {
							m.refreshUnreads()
						}
					} else if err != nil {
						m.statusMessage = fmt.Sprintf("Send failed: %v", err)
					} else {
						m.statusMessage = "Send failed: no response from server"
					}
				}
				return m, nil
			case "ctrl+p":
				return m, pickAttachments()
			case "ctrl+r":
				if len(m.draftAttachments) > 0 {
					removed := m.draftAttachments[len(m.draftAttachments)-1]
					m.draftAttachments = m.draftAttachments[:len(m.draftAttachments)-1]
					m.statusMessage = fmt.Sprintf("Removed: %s", filepath.Base(removed))
				}
				return m, nil
			case "left":
				if m.draftCursor > 0 {
					m.draftCursor--
				}
			case "right":
				if m.draftCursor < len([]rune(m.draftBuffer)) {
					m.draftCursor++
				}
			case "up":
				m.draftMoveUp()
			case "down":
				m.draftMoveDown()
			case "shift+up":
				if !m.reachedEnd {
					m.scrollOffset++
					if len(m.activeMessages) > 50 && m.scrollOffset > len(m.activeMessages)-50 {
						m.loadOlderMessages()
					}
				}
			case "shift+down":
				if m.scrollOffset > 0 {
					m.scrollOffset--
				}
			case "home", "ctrl+a":
				m.draftMoveLineStart()
			case "end":
				m.draftMoveLineEnd()
			case "ctrl+e":
				m.showEmojiPicker = true
				m.emojiPickerIdx = 0
				return m, nil
			case "backspace":
				m.draftBackspace()
			case "delete", "ctrl+d":
				m.draftDeleteForward()
			case "enter":
				m.draftInsert("\n")
			default:
				if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace || msg.Paste {
					m.draftInsert(string(msg.Runes))
				}
			}
			return m, nil
		}

		// INTERACTIVE INPUT ROUTE 4: GLOBAL SEARCH
		if m.isSearching {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "esc":
				m.isSearching = false
				m.searchBuffer = ""
				m.activeFilter = ""
				m.viewFilters[m.state] = ""
				m.selectedIndex = 0
				m.listOffset = 0
				m.applyActiveFilter()
				return m, nil
			case "enter":
				// Filter is already live — just lock it in
				m.isSearching = false
				m.searchBuffer = ""
				return m, nil
			case "backspace":
				if r := []rune(m.searchBuffer); len(r) > 0 {
					m.searchBuffer = string(r[:len(r)-1])
				}
			default:
				if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
					m.searchBuffer += string(msg.Runes)
				}
			}
			// Live filter: apply immediately and persist per-view
			m.activeFilter = m.searchBuffer
			m.viewFilters[m.state] = m.activeFilter
			m.selectedIndex = 0
			m.listOffset = 0
			m.applyActiveFilter()
			return m, nil
		}

		// INTERACTIVE INPUT ROUTE 5: NEW MESSAGE COMPOSE
		if m.state == ViewNewMessage && !m.pickingPlatform {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "esc":
				if m.newMessageQuery == "" {
					// Already clear — leave the view.
					m.switchView(ViewUnreads)
					m.refreshUnreads()
					m.loadActiveMessages()
					return m, nil
				}
				m.newMessageQuery = ""
				m.newMessageContacts, _ = client.FetchContacts("", 100)
				m.selectedIndex = 0
				return m, nil
			case "up":
				if m.selectedIndex > 0 {
					m.selectedIndex--
				}
				return m, nil
			case "down":
				if m.selectedIndex < len(m.newMessageContacts)-1 {
					m.selectedIndex++
				}
				return m, nil
			case "enter":
				if m.selectedIndex < len(m.newMessageContacts) {
					contact := m.newMessageContacts[m.selectedIndex]
					opts := m.buildPlatformOptions(contact)
					switch len(opts) {
					case 0:
						m.statusMessage = "No messaging platforms available for " + contact.Name
					case 1:
						m.openDraftForConv(opts[0].convID, false)
					default:
						m.pickingPlatform = true
						m.platformOptions = opts
						m.platformPickIdx = 0
						m.platformPickAction = "draft"
					}
				}
				return m, nil
			case "backspace":
				if len(m.newMessageQuery) > 0 {
					m.newMessageQuery = m.newMessageQuery[:len(m.newMessageQuery)-1]
					m.newMessageContacts, _ = client.FetchContacts(m.newMessageQuery, 100)
					m.selectedIndex = 0
				}
				return m, nil
			default:
				if len(msg.String()) == 1 {
					m.newMessageQuery += msg.String()
					m.newMessageContacts, _ = client.FetchContacts(m.newMessageQuery, 100)
					m.selectedIndex = 0
					return m, nil
				}
				// Non-character keys (tab, shift+tab, etc.) fall through to global handler.
			}
		}

		// INTERACTIVE INPUT ROUTE 6: PLATFORM PICKER
		if m.pickingPlatform {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "esc":
				m.pickingPlatform = false
				m.platformOptions = nil
				return m, nil
			case "up", "k":
				if m.platformPickIdx > 0 {
					m.platformPickIdx--
				}
				return m, nil
			case "down", "j":
				if m.platformPickIdx < len(m.platformOptions)-1 {
					m.platformPickIdx++
				}
				return m, nil
			case "enter":
				if m.platformPickIdx < len(m.platformOptions) {
					opt := m.platformOptions[m.platformPickIdx]
					m.pickingPlatform = false
					m.platformOptions = nil
					switch m.platformPickAction {
					case "alias":
						m.openAliasEditor(opt.convID, opt.convName)
					case "styled-draft":
						m.openDraftForConv(opt.convID, true)
					case "ext-draft":
						m.draftSourceState = m.state
						return m, m.openExternalEditorForDraft(opt.convID, false)
					case "ext-styled-draft":
						m.draftSourceState = m.state
						return m, m.openExternalEditorForDraft(opt.convID, true)
					default: // "draft"
						m.openDraftForConv(opt.convID, false)
					}
				}
				return m, nil
			}
			return m, nil
		}

		// GLOBAL KEYBIND NAVIGATION HUB
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit

		case "tab", "shift+tab":
			tabViews := []AppState{ViewUnreads, ViewConversations, ViewContacts, ViewAliases, ViewNewMessage, ViewPlatforms, ViewStyles}
			cur := 0
			for i, v := range tabViews {
				if v == m.state {
					cur = i
					break
				}
			}
			if msg.String() == "tab" {
				cur = (cur + 1) % len(tabViews)
			} else {
				cur = (cur - 1 + len(tabViews)) % len(tabViews)
			}
			next := tabViews[cur]
			m.switchView(next)
			switch next {
			case ViewUnreads:
				m.refreshUnreads()
				m.loadActiveMessages()
			case ViewConversations:
				m.allConversations, _ = client.FetchConversations(500)
				m.applyActiveFilter()
			case ViewContacts:
				m.applyActiveFilter()
			case ViewAliases:
				m.aliases, _ = client.LoadAliases()
				m.applyActiveFilter()
			case ViewNewMessage:
				m.newMessageQuery = ""
				m.newMessageContacts, _ = client.FetchContacts("", 100)
			case ViewPlatforms:
				m.platformStatuses = loadPlatformStatuses(m.cfg)
			case ViewStyles:
				m.focusRight = false
				m.loadStylesViewState()
			}
			return m, nil

		case "/":
			if !m.isEditingAlias && !m.isEditingStyles && m.state != ViewDraft {
				m.isSearching = true
				m.searchBuffer = m.activeFilter // pre-populate so existing filter stays visible
				return m, nil
			}

		case "esc":
			if m.activeFilter != "" {
				m.activeFilter = ""
				m.viewFilters[m.state] = ""
				m.selectedIndex = 0
				m.listOffset = 0
				m.applyActiveFilter()
				m.statusMessage = "Filter cleared."
				return m, nil
			}

		case "d":
			if m.state == ViewPlatforms && m.selectedIndex < len(m.platformStatuses) {
				sel := m.platformStatuses[m.selectedIndex]
				if sel.canDisconnect {
					switch sel.providerType {
					case "google":
						for _, p := range client.GetProviders() {
							if omp, ok := p.(*client.OpenMessageProvider); ok {
								if err := omp.Disconnect(); err != nil {
									m.statusMessage = "Disconnect failed: " + err.Error()
								} else {
									m.statusMessage = "Google Messages disconnected. Press [p] to re-register."
									m.platformStatuses = loadPlatformStatuses(m.cfg)
								}
								break
							}
						}
					case "signal":
						for _, p := range client.GetProviders() {
							if sp, ok := p.(*client.SignalProvider); ok {
								if err := sp.Unregister(); err != nil {
									m.statusMessage = "Unregister failed: " + err.Error()
								} else {
									m.statusMessage = "Signal unregistered. Push support disabled. Press [p] to re-link."
									m.platformStatuses = loadPlatformStatuses(m.cfg)
								}
								break
							}
						}
					}
				}
				return m, nil
			}

			var targetID string

			if m.state == ViewUnreads && len(m.unreads) > 0 {
				targetID = m.unreads[m.selectedIndex].ConversationID
			} else if m.state == ViewConversations && len(m.conversations) > 0 {
				targetID = m.conversations[m.selectedIndex].ConversationID
			} else if m.state == ViewContacts && len(m.contacts) > 0 {
				contact := m.contacts[m.selectedIndex]
				opts := m.buildPlatformOptions(contact)
				switch len(opts) {
				case 0:
					m.statusMessage = "No messaging platforms available for " + contact.Name
				case 1:
					m.openDraftForConv(opts[0].convID, false)
				default:
					m.pickingPlatform = true
					m.platformOptions = opts
					m.platformPickIdx = 0
					m.platformPickAction = "draft"
				}
				return m, nil
			}

			if targetID != "" {
				m.draftTargetID = targetID
				m.draftSourceState = m.state
				m.draftBuffer, _ = client.LoadDraft(targetID)
				m.draftCursor = len([]rune(m.draftBuffer))
				m.draftStyled = false
				m.draftPlatform = extractConvPlatform(targetID)
				m.state = ViewDraft
				m.activeMessages, _ = client.FetchMessages(targetID, 100, 0, "")
			}

		case "s":
			// Open in-TUI draft in styled mode — only for platforms that support it.
			var targetID string
			if m.state == ViewUnreads && len(m.unreads) > 0 {
				targetID = m.unreads[m.selectedIndex].ConversationID
			} else if m.state == ViewConversations && len(m.conversations) > 0 {
				targetID = m.conversations[m.selectedIndex].ConversationID
			} else if m.state == ViewContacts && len(m.contacts) > 0 {
				contact := m.contacts[m.selectedIndex]
				opts := m.buildPlatformOptions(contact)
				// Filter to styled-capable platforms only
				var styledOpts []platformOption
				for _, o := range opts {
					if platformSupportsStyled(o.providerID) {
						styledOpts = append(styledOpts, o)
					}
				}
				switch len(styledOpts) {
				case 0:
					m.statusMessage = "No platforms that support styled drafts are available for " + contact.Name
				case 1:
					m.openDraftForConv(styledOpts[0].convID, true)
				default:
					m.pickingPlatform = true
					m.platformOptions = styledOpts
					m.platformPickIdx = 0
					m.platformPickAction = "styled-draft"
				}
				return m, nil
			}
			if targetID != "" {
				platform := extractConvPlatform(targetID)
				if !platformSupportsStyled(platform) {
					m.statusMessage = fmt.Sprintf("Styled mode is not supported for %s conversations.", platform)
					return m, nil
				}
				m.draftTargetID = targetID
				m.draftSourceState = m.state
				m.draftBuffer, _ = client.LoadDraft(targetID)
				m.draftCursor = len([]rune(m.draftBuffer))
				m.draftStyled = true
				m.draftPlatform = platform
				m.state = ViewDraft
				m.activeMessages, _ = client.FetchMessages(targetID, 100, 0, "")
			}

		case "a":
			var targetName, targetConvID string

			if m.state == ViewConversations && len(m.conversations) > 0 {
				c := m.conversations[m.selectedIndex]
				targetName = c.Name
				targetConvID = c.ConversationID
			} else if m.state == ViewContacts && len(m.contacts) > 0 {
				contact := m.contacts[m.selectedIndex]
				opts := m.buildPlatformOptions(contact)
				switch len(opts) {
				case 0:
					m.statusMessage = "No messaging platforms available for " + contact.Name
				case 1:
					m.openAliasEditor(opts[0].convID, contact.Name)
				default:
					m.pickingPlatform = true
					m.platformOptions = opts
					m.platformPickIdx = 0
					m.platformPickAction = "alias"
				}
				return m, nil
			}

			if targetName != "" {
				m.openAliasEditor(targetConvID, targetName)
			} else {
				m.statusMessage = "Cannot create alias for selected item."
			}

		case "1":
			m.switchView(ViewUnreads)
			m.refreshUnreads() // refreshUnreads already respects activeFilter
			m.loadActiveMessages()
		case "2":
			m.switchView(ViewConversations)
			m.allConversations, _ = client.FetchConversations(500)
			m.applyActiveFilter()
		case "3":
			m.switchView(ViewContacts)
			m.applyActiveFilter()
		case "4":
			m.switchView(ViewAliases)
			m.aliases, _ = client.LoadAliases()
			m.applyActiveFilter()
		case "5":
			m.switchView(ViewNewMessage)
			m.newMessageQuery = ""
			m.newMessageContacts, _ = client.FetchContacts("", 100)
		case "6":
			m.switchView(ViewPlatforms)
			m.platformStatuses = loadPlatformStatuses(m.cfg)
		case "7":
			m.switchView(ViewStyles)
			m.focusRight = false
			m.loadStylesViewState()

		case "up", "k":
			m.showReactions = false
			if m.focusRight && !m.reachedEnd {
				m.scrollOffset++
				// Fetch proactively when 50 messages from the end
				if len(m.activeMessages) > 50 && m.scrollOffset > (len(m.activeMessages)-50) {
					m.loadOlderMessages()
				}
			} else if !m.focusRight { // Left panel navigation
				if m.selectedIndex > 0 {
					m.selectedIndex--

					// Scroll-off: ensure item start line is above listOffset + 3
					if m.selectedIndex < len(m.itemLineOffsets) {
						itemStartLine := m.itemLineOffsets[m.selectedIndex]
						if itemStartLine < m.listOffset+3 {
							m.listOffset = itemStartLine - 3
							if m.listOffset < 0 {
								m.listOffset = 0
							}
						}
					}

					if m.state != ViewStyles {
						m.loadActiveMessages()
					}
				}
			}

		case "down", "j":
			m.showReactions = false
			if m.focusRight {
				if m.scrollOffset > 0 {
					m.scrollOffset--
				}
			} else {
				maxLen := m.getMaxListLen()
				if m.selectedIndex < maxLen-1 {
					m.selectedIndex++
					vh := m.getViewportHeight() - 2
					// Line-aware scroll down: ensure item end line is within [listOffset, listOffset + vh - 3]
					if m.selectedIndex < len(m.itemLineOffsets) {
						itemStartLine := m.itemLineOffsets[m.selectedIndex]
						itemEndLine := itemStartLine + 1
						if m.selectedIndex+1 < len(m.itemLineOffsets) {
							itemEndLine = m.itemLineOffsets[m.selectedIndex+1]
						}

						// If item stretches past viewport buffer, scroll listOffset down
						if itemEndLine > m.listOffset+vh-3 {
							m.listOffset = itemEndLine - (vh - 3)
						}
					}

					// Autoload: if at the end of current list, fetch more
					if m.selectedIndex >= maxLen-20 {
						m.fetchMoreListItems()
					}

					if m.state != ViewStyles {
						m.loadActiveMessages()
					}
				}
			}

		case "J":
			if m.focusRight {
				m.scrollOffset = 0
				m.statusMessage = "Snapped to latest messages."
			}

		case "o":
			if m.state == ViewUnreads || m.state == ViewConversations {
				// Prefer attachments of the message the cursor is on.
				idx := m.msgAtScrollPos()
				if idx >= 0 && idx < len(m.activeMessages) && len(m.activeMessages[idx].Attachments) > 0 {
					return m, openAttachments(m.activeMessages[idx].Attachments)
				}
				// Fall back to the most recent message that has an attachment.
				for _, msg := range m.activeMessages {
					if len(msg.Attachments) > 0 {
						return m, openAttachments(msg.Attachments)
					}
				}
				m.statusMessage = "No attachment found in this conversation."
			}

		case "u":
			if m.state == ViewStyles {
				tmp := m.cfg
				m.cfg = m.undoConfig
				m.undoConfig = tmp

				_ = client.SaveConfig(m.cfg)
				updateStylesInTUI(m.cfg)
				m.loadStylesViewState()
				m.statusMessage = "Last style adjustment rolled back successfully!"
			}

		case "r":
			if m.state == ViewStyles {
				m.undoConfig = m.cfg
				m.cfg = client.NewDefaultConfig()
				_ = client.SaveConfig(m.cfg)
				updateStylesInTUI(m.cfg)
				m.loadStylesViewState()
				m.selectedIndex = 0
				m.statusMessage = "Theme configuration reset to system defaults! Tap 'u' to revert."
			} else if m.state == ViewUnreads && len(m.unreads) > 0 {
				active := m.unreads[m.selectedIndex]
				if err := client.MarkAsRead(active.ConversationID); err != nil {
					m.statusMessage = fmt.Sprintf("Failed to mark '%s' as read: %v", active.Name, err)
				} else {
					m.statusMessage = fmt.Sprintf("Marked '%s' as read", active.Name)
					m.refreshUnreads()
					if m.selectedIndex >= len(m.unreads) {
						m.selectedIndex = max(0, len(m.unreads)-1)
					}
					m.loadActiveMessages()
				}
			} else if m.state == ViewPlatforms && m.selectedIndex < len(m.platformStatuses) {
				sel := m.platformStatuses[m.selectedIndex]
				if sel.canReconnect && sel.providerType == "google" {
					providers := client.GetProviders()
					for _, p := range providers {
						if omp, ok := p.(*client.OpenMessageProvider); ok {
							if err := omp.ReconnectGoogle(); err != nil {
								m.statusMessage = "Reconnect failed: " + err.Error()
							} else {
								m.statusMessage = "Reconnecting Google Messages..."
								m.platformStatuses = loadPlatformStatuses(m.cfg)
							}
							break
						}
					}
				}
			}

		case "v":
			if m.state == ViewUnreads || m.state == ViewConversations {
				m.focusRight = !m.focusRight
				m.scrollOffset = 0
			}

		case "V":
			if (m.state == ViewUnreads && len(m.unreads) > 0) || (m.state == ViewConversations && len(m.conversations) > 0) {
				return m, m.openThreadInSystemVim()
			}

		case "m":
			if m.state == ViewUnreads || m.state == ViewConversations {
				m.showMetadata = !m.showMetadata
				m.loadActiveMessages()
			}

		case "enter":
			if m.state == ViewStyles {
				m.isEditingStyles = true
				m.loadStylesViewState()
				m.styleFieldIndex = m.selectedIndex
			} else if m.state == ViewContacts && len(m.contacts) > 0 {
				m.activeFilter = m.contacts[m.selectedIndex].Name
				m.state = ViewConversations
				m.selectedIndex = 0
				m.listOffset = 0
				m.applyActiveFilter()
			} else if m.state == ViewUnreads && len(m.unreads) > 0 {
				selectedConvID := m.unreads[m.selectedIndex].ConversationID
				m.state = ViewConversations
				m.allConversations, _ = client.FetchConversations(500)
				m.conversations = m.allConversations
				m.selectedIndex = 0
				for i, c := range m.conversations {
					if c.ConversationID == selectedConvID {
						m.selectedIndex = i
						break
					}
				}
				m.loadActiveMessages()
			}

		case "e":
			if m.state == ViewUnreads || m.state == ViewConversations {
				m.showReactions = !m.showReactions
				return m, nil
			}
			if m.state == ViewPlatforms && m.selectedIndex < len(m.platformStatuses) {
				sel := m.platformStatuses[m.selectedIndex]
				if sel.canToggleEnabled && sel.configKey != "" {
					cfg, err := client.LoadConfig()
					if err == nil {
						if cfg.Providers == nil {
							cfg.Providers = make(map[string]client.ProviderSettings)
						}
						p := cfg.Providers[sel.configKey]
						p.Type = sel.configKey
						p.Enabled = !sel.enabled
						cfg.Providers[sel.configKey] = p
						_ = client.SaveConfig(cfg)
						m.cfg = cfg
						client.InitProviders(cfg)
						m.platformStatuses = loadPlatformStatuses(cfg)
						if sel.enabled {
							m.statusMessage = sel.name + " disabled."
						} else {
							m.statusMessage = sel.name + " enabled."
						}
					}
				}
				return m, nil
			}
			if m.state == ViewAliases && len(m.aliasKeys) > 0 {
				m.isEditingAlias = true
				m.fieldIndex = 0
				shortcut := m.aliasKeys[m.selectedIndex]

				var alias client.Alias
				for _, a := range m.aliases {
					if a.Shortcut == shortcut {
						alias = a
						break
					}
				}

				m.editFields[0] = alias.Shortcut
				m.editFields[1] = alias.Name
				m.editFields[2] = alias.ConversationID
				m.editFields[3] = alias.Platform
				m.statusMessage = "Editing Alias. Use Up/Down arrows to shift fields. Press Enter to Save, Esc to cancel."
			}
		case "D":
			if m.state == ViewAliases && len(m.aliasKeys) > 0 {
				shortcut := m.aliasKeys[m.selectedIndex]

				var targetKey string
				for k, a := range m.aliases {
					if a.Shortcut == shortcut {
						targetKey = k
						break
					}
				}

				if targetKey != "" {
					delete(m.aliases, targetKey)
					client.WriteAllAliases(m.aliases)
					m.refreshAliasKeys()
					m.selectedIndex = 0
					m.statusMessage = fmt.Sprintf("Alias '%s' deleted successfully", shortcut)
				}
			} else {
				var targetID string
				if m.state == ViewUnreads && len(m.unreads) > 0 {
					targetID = m.unreads[m.selectedIndex].ConversationID
				} else if m.state == ViewConversations && len(m.conversations) > 0 {
					targetID = m.conversations[m.selectedIndex].ConversationID
				} else if m.state == ViewContacts && len(m.contacts) > 0 {
					contact := m.contacts[m.selectedIndex]
					opts := m.buildPlatformOptions(contact)
					switch len(opts) {
					case 0:
						m.statusMessage = "No messaging platforms available for " + contact.Name
					case 1:
						m.draftSourceState = m.state
						return m, m.openExternalEditorForDraft(opts[0].convID, false)
					default:
						m.pickingPlatform = true
						m.platformOptions = opts
						m.platformPickIdx = 0
						m.platformPickAction = "ext-draft"
					}
					return m, nil
				}
				if targetID != "" {
					m.draftSourceState = m.state
					return m, m.openExternalEditorForDraft(targetID, false)
				}
			}

		case "S":
			// Open external editor in styled mode — only for platforms that support it.
			var targetID string
			if m.state == ViewUnreads && len(m.unreads) > 0 {
				targetID = m.unreads[m.selectedIndex].ConversationID
			} else if m.state == ViewConversations && len(m.conversations) > 0 {
				targetID = m.conversations[m.selectedIndex].ConversationID
			} else if m.state == ViewContacts && len(m.contacts) > 0 {
				contact := m.contacts[m.selectedIndex]
				opts := m.buildPlatformOptions(contact)
				var styledOpts []platformOption
				for _, o := range opts {
					if platformSupportsStyled(o.providerID) {
						styledOpts = append(styledOpts, o)
					}
				}
				switch len(styledOpts) {
				case 0:
					m.statusMessage = "No platforms that support styled drafts are available for " + contact.Name
				case 1:
					m.draftSourceState = m.state
					return m, m.openExternalEditorForDraft(styledOpts[0].convID, true)
				default:
					m.pickingPlatform = true
					m.platformOptions = styledOpts
					m.platformPickIdx = 0
					m.platformPickAction = "ext-styled-draft"
				}
				return m, nil
			}
			if targetID != "" {
				platform := extractConvPlatform(targetID)
				if !platformSupportsStyled(platform) {
					m.statusMessage = fmt.Sprintf("Styled mode is not supported for %s conversations.", platform)
					return m, nil
				}
				m.draftSourceState = m.state
				return m, m.openExternalEditorForDraft(targetID, true)
			}

		case "E":
			if m.state == ViewAliases {
				path, _ := client.GetConfigPath()
				c := exec.Command(resolveEditor(m.cfg), path)
				return m, tea.ExecProcess(c, func(err error) tea.Msg {
					return "reload-aliases"
				})
			}

		case "p":
			if m.state == ViewPlatforms && m.selectedIndex < len(m.platformStatuses) {
				sel := m.platformStatuses[m.selectedIndex]
				if sel.canPair {
					switch sel.providerType {
					case "google":
						c := exec.Command(os.Args[0], "pair", "google")
						return m, tea.ExecProcess(c, func(err error) tea.Msg {
							return "refresh-provider-status"
						})
					case "signal":
						c := exec.Command(os.Args[0], "link", "signal")
						return m, tea.ExecProcess(c, func(err error) tea.Msg {
							return "signal-linked"
						})
					}
				}
			}

		case "t":
			if m.state == ViewPlatforms && m.selectedIndex < len(m.platformStatuses) {
				sel := m.platformStatuses[m.selectedIndex]
				if sel.canStart {
					provType := sel.providerType
					serverCmd := sel.serverCmd
					configKey := sel.configKey
					port := sel.serverPort
					m.statusMessage = "Starting " + sel.name + " server..."
					return m, func() tea.Msg {
						if serverCmd != "" {
							startProviderServer(configKey, serverCmd)
						} else {
							switch provType {
							case "google":
								ensureServerRunning()
							case "signal":
								ensureSignalRunning(port)
							}
						}
						time.Sleep(1500 * time.Millisecond)
						if provType == "signal" {
							ensureSignalReceiverRunning()
						}
						return "refresh-provider-status"
					}
				} else if sel.canStop {
					provType := sel.providerType
					serverCmd := sel.serverCmd
					configKey := sel.configKey
					port := sel.serverPort
					m.statusMessage = "Stopping " + sel.name + " server..."
					return m, func() tea.Msg {
						if serverCmd != "" {
							stopProviderServer(configKey, port)
						} else {
							switch provType {
							case "google":
								stopOpenMessageServer()
							case "signal":
								stopSignalServer()
								stopSignalReceiver()
							}
						}
						time.Sleep(1000 * time.Millisecond)
						return "refresh-provider-status"
					}
				}
				return m, nil
			}

		case "ctrl+p":
			return m, pickAttachments()

		case "w":
			if m.state == ViewPlatforms && m.selectedIndex < len(m.platformStatuses) {
				sel := m.platformStatuses[m.selectedIndex]
				if sel.canStartReceiver {
					m.statusMessage = "Starting Signal message receiver..."
					return m, func() tea.Msg {
						ensureSignalReceiverRunning()
						return "refresh-provider-status"
					}
				}
				return m, nil
			}
		}
	}
	return m, nil
}

func (m tuiModel) View() string {
	var s strings.Builder

	s.WriteString(m.renderTabs() + "\n")
	if m.isSearching {
		banner := activeStyle.Width(m.termWidth).Render(fmt.Sprintf("filter: %s█", m.searchBuffer))
		s.WriteString(banner + "\n")
	} else if m.activeFilter != "" {
		banner := subtleStyle.Width(m.termWidth).Render(fmt.Sprintf("filter: %s  (esc to clear)", m.activeFilter))
		s.WriteString(banner + "\n")
	}
	if m.state == ViewDraft && platformSupportsStyled(m.draftPlatform) {
		s.WriteString(m.renderDraftModeBar() + "\n")
	} else if m.providerWarning != "" && m.state != ViewPlatforms {
		s.WriteString(activeStyle.Render("[!] "+m.providerWarning) + "\n")
	}
	s.WriteString("\n")

	var leftContent, rightContent string
	if m.pickingPlatform {
		leftContent, rightContent = m.renderPlatformPicker()
	} else {
		switch m.state {
		case ViewUnreads:
			leftContent, rightContent = m.renderUnreadsView()
		case ViewConversations:
			leftContent, rightContent = m.renderConversationsView()
		case ViewContacts:
			leftContent, rightContent = m.renderContactsView()
		case ViewAliases:
			leftContent, rightContent = m.renderAliasesView()
		case ViewNewMessage:
			leftContent, rightContent = m.renderNewMessageView()
		case ViewPlatforms:
			leftContent, rightContent = m.renderPlatformsView()
		case ViewStyles:
			leftContent, rightContent = m.renderStylesView()
		case ViewDraft:
			leftContent, rightContent = m.renderDraftView()
		}
	}

	if m.state == ViewDraft {
		vh := m.getViewportHeight()
		draftHeight := 6
		if vh < 15 {
			draftHeight = 4
		}
		convHeight := vh - draftHeight

		topPane := boxStyle.Width(m.termWidth - 4).Height(convHeight).Render(leftContent)
		bottomPane := boxStyle.Width(m.termWidth - 4).Height(draftHeight-1).Render(rightContent)
		bw := lipgloss.Width(strings.SplitN(bottomPane, "\n", 2)[0])
		s.WriteString(lipgloss.JoinVertical(lipgloss.Left,
			topPane,
			activePaneLine(bw),
			bottomPane,
		))
	} else {
		vh := m.getViewportHeight() - 1 // -1 for the indicator line above each pane
		leftWidth := (m.termWidth * 4) / 10
		if leftWidth < 25 {
			leftWidth = 25
		}
		rightWidth := m.termWidth - leftWidth - 6

		if rightWidth < 20 {
			rightWidth = 20
		}

		rightActive := m.isRightPaneActive()
		leftPane := boxStyle.Width(leftWidth).Height(vh).Render(leftContent)
		rightPane := boxStyle.Width(rightWidth).Height(vh).Render(rightContent)
		lw := lipgloss.Width(strings.SplitN(leftPane, "\n", 2)[0])
		rw := lipgloss.Width(strings.SplitN(rightPane, "\n", 2)[0])

		var leftTop, rightTop string
		if rightActive {
			leftTop = strings.Repeat(" ", lw)
			rightTop = activePaneLine(rw)
		} else {
			leftTop = activePaneLine(lw)
			rightTop = strings.Repeat(" ", rw)
		}

		leftCol := lipgloss.JoinVertical(lipgloss.Left, leftTop, leftPane)
		rightCol := lipgloss.JoinVertical(lipgloss.Left, rightTop, rightPane)
		s.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, leftCol, rightCol))
	}

	if m.statusMessage != "" {
		s.WriteString(fmt.Sprintf("\n\n[STATUS] %s", m.statusMessage))
	}
	s.WriteString("\n" + m.renderFooter())

	// Final wrap to ensure no bleeding beyond terminal width
	return lipgloss.NewStyle().MaxWidth(m.termWidth).MaxHeight(m.termHeight).Render(s.String())
}

// activePaneLine renders the active-panel indicator: a row of ▁ characters
// (lower-one-eighth block) in the primary colour. ▁ sits flush at the bottom
// of its character cell, so the visual gap to the box border below is only
// the font's own line-spacing — as close as a terminal can get.
func activePaneLine(width int) string {
	return activeStyle.Render(strings.Repeat("▁", width))
}

// isRightPaneActive returns true when the right (or bottom, in draft view) pane
// should be treated as the focused one for the indicator line.
func (m tuiModel) isRightPaneActive() bool {
	if m.state == ViewStyles {
		return m.isEditingStyles
	}
	return m.focusRight
}

// extractConvPlatform returns the provider prefix from a prefixed conversation ID (e.g. "signal" from "signal:+1...").
// "openmessage" is normalized to "sms" for backward compat with older stored conversation IDs.
func extractConvPlatform(prefixedID string) string {
	if idx := strings.Index(prefixedID, ":"); idx != -1 {
		p := prefixedID[:idx]
		if p == "openmessage" {
			return "sms"
		}
		return p
	}
	return ""
}

// platformSupportsStyled returns true for providers that support styled/formatted text.
func platformSupportsStyled(platform string) bool {
	return platform == "signal"
}

// selectedConvPlatform returns the provider ID for the currently hovered conversation
// in ViewUnreads or ViewConversations. Returns "" for other views or when nothing is selected.
func (m tuiModel) selectedConvPlatform() string {
	switch m.state {
	case ViewUnreads:
		if len(m.unreads) > 0 && m.selectedIndex < len(m.unreads) {
			return extractConvPlatform(m.unreads[m.selectedIndex].ConversationID)
		}
	case ViewConversations:
		if len(m.conversations) > 0 && m.selectedIndex < len(m.conversations) {
			return extractConvPlatform(m.conversations[m.selectedIndex].ConversationID)
		}
	}
	return ""
}

// activeConvHasAttachment returns true if any loaded message in the active conversation
// has an attachment.
func (m tuiModel) activeConvHasAttachment() bool {
	for _, msg := range m.activeMessages {
		if len(msg.Attachments) > 0 {
			return true
		}
	}
	return false
}

// highlightedMsgHasReactions returns true when the message currently at the
// scroll position has reactions (so the e hint is relevant).
func (m tuiModel) highlightedMsgHasReactions() bool {
	idx := m.msgAtScrollPos()
	return idx >= 0 && idx < len(m.activeMessages) && len(m.activeMessages[idx].Reactions) > 0
}

// selectedContactHasStyledPlatform returns true if the hovered contact has at least one
// platform that supports styled drafts.
func (m tuiModel) selectedContactHasStyledPlatform() bool {
	if m.state != ViewContacts || len(m.contacts) == 0 || m.selectedIndex >= len(m.contacts) {
		return false
	}
	for _, opt := range m.buildPlatformOptions(m.contacts[m.selectedIndex]) {
		if platformSupportsStyled(opt.providerID) {
			return true
		}
	}
	return false
}

// styledModeRef returns a compact syntax reference for the platform's styled mode.
func styledModeRef(platform string) string {
	switch platform {
	case "signal":
		return "*italic*  **bold**  ~strikethrough~  ||spoiler||  `monospace`  (escape: \\\\char)"
	default:
		return ""
	}
}

func (m tuiModel) renderDraftModeBar() string {
	modes := []struct {
		label  string
		active bool
	}{
		{"PLAIN", !m.draftStyled},
		{"STYLED", m.draftStyled},
	}
	var parts []string
	for _, mode := range modes {
		if mode.active {
			parts = append(parts, titleStyle.Render(" "+mode.label+" "))
		} else {
			parts = append(parts, subtleStyle.Render(" "+mode.label+" "))
		}
	}
	modeSelector := strings.Join(parts, subtleStyle.Render(" | "))

	if m.draftStyled {
		if ref := styledModeRef(m.draftPlatform); ref != "" {
			return modeSelector + "   " + subtleStyle.Render(ref)
		}
	}
	return modeSelector
}

func (m tuiModel) renderTabs() string {
	tabs := []string{"[1] Unreads", "[2] Conversations", "[3] Contacts", "[4] Aliases", "[5] New Message", "[6] Platforms", "[7] Settings"}
	var renderedTabs []string
	for i, t := range tabs {
		if int(m.state) == i {
			renderedTabs = append(renderedTabs, titleStyle.Render(t))
		} else {
			renderedTabs = append(renderedTabs, " "+t+" ")
		}
	}

	// Join with pipes and wrap based on terminal width
	fullTabs := strings.Join(renderedTabs, " | ")
	return lipgloss.NewStyle().Width(m.termWidth).Render(fullTabs)
}

func (m tuiModel) renderFooter() string {
	wrap := func(s string) string {
		return subtleStyle.Width(m.termWidth).Render(s)
	}

	baseHelp := "\n Navigation: k/j | Search: / | Switch Views: 1-7 or Tab/Shift+Tab"
	profile := client.GetProfile()
	if profile != "" {
		baseHelp = fmt.Sprintf("\n [Profile: %s] | Navigation: k/j | Search: / | Switch Views: 1-7 or Tab/Shift+Tab", profile)
	}
	if m.showEmojiPicker {
		return wrap("\n Emoji: ←→↑↓ / h j k l Navigate | Enter: Insert | Ctrl+E / Esc: Close")
	}
	if m.pickingPlatform {
		return wrap("\n Platform Picker: ↑/↓ Navigate | Enter: Select | Esc: Cancel")
	}
	if m.isEditingAlias {
		return wrap("\n Alias Editor: ↑/↓ Swap rows | Type to update field | Enter: Save | Esc: Cancel")
	}
	if m.isEditingStyles {
		return wrap("\n Theme Form: Type ANSI color code (0-255) | Enter: Save | Esc: Cancel")
	}
	if m.state == ViewDraft {
		base := " Ctrl+S: Send | Ctrl+P: Attach | Ctrl+R: Remove last | Ctrl+E: Emoji | Esc: Save & Return"
		if platformSupportsStyled(m.draftPlatform) {
			base = " Ctrl+O: Mode | " + base
		}
		hint := "\n Drafting: ←→↑↓ Navigate | Shift+↑↓ Scroll conv | " + base
		if runtime.GOOS == "darwin" {
			hint += " | Ctrl+Cmd+Spc: System Emoji"
		}
		return wrap(hint)
	}

	if m.focusRight {
		hint := "\n Conv Panel: k/j Scroll | J: Snap Latest | v: Switch Focus | Shift+↑↓: Scroll | Esc: Unfocus"
		if m.activeConvHasAttachment() {
			hint += " | o: Open Attachment"
		}
		return wrap(hint)
	}

	switch m.state {
	case ViewUnreads:
		baseHelp += " | v: Focus Conv | V: Vim | m: Metadata | a: Alias"
		baseHelp += " | enter: Open Thread | d: Draft"
		if platformSupportsStyled(m.selectedConvPlatform()) {
			baseHelp += " | s: Styled Draft"
		}
		baseHelp += " | D: Ext Draft"
		if platformSupportsStyled(m.selectedConvPlatform()) {
			baseHelp += " | S: Styled Ext Draft"
		}
		baseHelp += " | r: Mark Read"
		if m.activeConvHasAttachment() {
			baseHelp += " | o: Open Attachment"
		}
		if m.highlightedMsgHasReactions() {
			baseHelp += " | e: Reactions"
		}
	case ViewConversations:
		baseHelp += " | v: Focus Conv | V: Vim | m: Metadata | a: Alias"
		baseHelp += " | d: Draft"
		if platformSupportsStyled(m.selectedConvPlatform()) {
			baseHelp += " | s: Styled Draft"
		}
		baseHelp += " | D: Ext Draft"
		if platformSupportsStyled(m.selectedConvPlatform()) {
			baseHelp += " | S: Styled Ext Draft"
		}
		if m.activeConvHasAttachment() {
			baseHelp += " | o: Open Attachment"
		}
		if m.highlightedMsgHasReactions() {
			baseHelp += " | e: Reactions"
		}
	case ViewContacts:
		baseHelp += " | a: Alias | enter: View Threads | d: Draft | D: Ext Draft"
		if m.selectedContactHasStyledPlatform() {
			baseHelp += " | S: Styled Ext Draft"
		}
	case ViewAliases:
		baseHelp += " | e: Edit | E: Vim Edit | D: Delete"
	case ViewPlatforms:
		baseHelp += " | t: Start/Stop | w: Receiver | e: Enable/Disable | r: Reconnect | p: Pair | d: Disconnect"
	case ViewNewMessage:
		return wrap("\n Type to search contacts | ↑/↓: Navigate | Enter: Draft | Esc: Clear/Exit | Tab: Switch View")
	case ViewStyles:
		baseHelp += " | enter: Edit Row | u: Undo | r: Reset Defaults"
	}

	baseHelp += " | q: Quit"
	return wrap(baseHelp)
}

func (m *tuiModel) loadActiveMessages() {
	m.activeMessages = []client.Message{}
	m.scrollOffset = 0
	m.reachedEnd = false
	var targetID string

	if m.state == ViewUnreads && len(m.unreads) > 0 {
		if m.selectedIndex >= len(m.unreads) {
			m.selectedIndex = len(m.unreads) - 1
		}
		targetID = m.unreads[m.selectedIndex].ConversationID
	} else if m.state == ViewConversations && len(m.conversations) > 0 {
		if m.selectedIndex >= len(m.conversations) {
			m.selectedIndex = len(m.conversations) - 1
		}
		targetID = m.conversations[m.selectedIndex].ConversationID
	}

	if targetID != "" {
		if msgs, err := client.FetchMessages(targetID, 100, 0, ""); err == nil {
			m.activeMessages = msgs
		}
	}
}

// refreshMessages fetches the latest messages and appends any that are newer
// than what we already have. Unlike loadActiveMessages, it does NOT reset the
// scroll position, so a user scrolling up is not interrupted.
func (m *tuiModel) refreshMessages() {
	var targetID string
	if m.state == ViewUnreads && len(m.unreads) > 0 {
		if m.selectedIndex < len(m.unreads) {
			targetID = m.unreads[m.selectedIndex].ConversationID
		}
	} else if m.state == ViewConversations && len(m.conversations) > 0 {
		if m.selectedIndex < len(m.conversations) {
			targetID = m.conversations[m.selectedIndex].ConversationID
		}
	} else if m.state == ViewDraft {
		targetID = m.draftTargetID
	}
	if targetID == "" {
		return
	}

	msgs, err := client.FetchMessages(targetID, 100, 0, "")
	if err != nil || len(msgs) == 0 {
		return
	}

	// Find the newest timestamp we already have.
	var latestKnown int64
	for _, msg := range m.activeMessages {
		if msg.TimestampMS > latestKnown {
			latestKnown = msg.TimestampMS
		}
	}

	// Prepend any messages newer than what we have.
	var added bool
	for _, msg := range msgs {
		if msg.TimestampMS > latestKnown {
			m.activeMessages = append(m.activeMessages, msg)
			added = true
		}
	}

	if added && m.scrollOffset > 0 {
		// Adjust scroll so the user's reading position appears unchanged.
		m.scrollOffset++
	}

	// Update reactions (and body edits) on existing messages in-place.
	freshByID := make(map[string]client.Message, len(msgs))
	for _, msg := range msgs {
		freshByID[msg.MessageID] = msg
	}
	for i, existing := range m.activeMessages {
		if fresh, ok := freshByID[existing.MessageID]; ok {
			m.activeMessages[i] = fresh
		}
	}
}

func (m *tuiModel) loadOlderMessages() {
	if m.reachedEnd || len(m.activeMessages) == 0 {
		return
	}
	var targetID string
	if m.state == ViewUnreads && len(m.unreads) > 0 {
		if m.selectedIndex >= len(m.unreads) {
			m.selectedIndex = len(m.unreads) - 1
		}
		targetID = m.unreads[m.selectedIndex].ConversationID
	} else if m.state == ViewConversations && len(m.conversations) > 0 {
		if m.selectedIndex >= len(m.conversations) {
			m.selectedIndex = len(m.conversations) - 1
		}
		targetID = m.conversations[m.selectedIndex].ConversationID
	} else if m.state == ViewDraft {
		targetID = m.draftTargetID
	}
	if targetID == "" {
		return
	}
	oldestMessage := m.activeMessages[len(m.activeMessages)-1]
	// Fetch in batches of 100
	olderMsgs, err := client.FetchMessages(targetID, 100, oldestMessage.TimestampMS, oldestMessage.MessageID)
	if err != nil || len(olderMsgs) == 0 {
		m.reachedEnd = true
		return
	}
	m.activeMessages = append(m.activeMessages, olderMsgs...)
}

func (m tuiModel) getViewportHeight() int {
	// Fixed height calculation based on terminal height
	// Tabs (2) + Spacer (2) + Footer (3) + Status (1) + Borders (2) = ~10 rows of overhead
	h := m.termHeight - 10
	if h < 5 {
		return 5
	}
	return h
}

func (m tuiModel) getMaxListLen() int {
	switch m.state {
	case ViewUnreads:
		return len(m.unreads)
	case ViewConversations:
		return len(m.conversations)
	case ViewContacts:
		return len(m.contacts)
	case ViewAliases:
		return len(m.aliasKeys)
	case ViewPlatforms:
		return len(m.platforms)
	case ViewStyles:
		return settingsCount
	default:
		return 0
	}
}

// resolveEditor returns the editor to use: MSG_EDITOR > config > EDITOR env > "vim".
// The explicit config setting wins over the generic EDITOR env var so the settings
// page has the expected effect.
func resolveEditor(cfg client.Config) string {
	for _, e := range []string{os.Getenv("MSG_EDITOR"), cfg.Editor, os.Getenv("EDITOR")} {
		if e != "" {
			return e
		}
	}
	return "vim"
}

// isVimLike returns true for editors that accept vim-compatible flags and vimscript.
func isVimLike(editor string) bool {
	return editor == "vim" || editor == "nvim"
}

func (m tuiModel) openThreadInSystemVim() tea.Cmd {
	var sb strings.Builder
	var activeName string
	if m.state == ViewUnreads && len(m.unreads) > 0 {
		activeName = m.unreads[m.selectedIndex].Name
	} else if m.state == ViewConversations && len(m.conversations) > 0 {
		activeName = m.conversations[m.selectedIndex].Name
	}
	if activeName == "" {
		return nil
	}

	sb.WriteString(fmt.Sprintf("# THREAD HISTORY: %s\n\n", strings.ToUpper(activeName)))

	for i := len(m.activeMessages) - 1; i >= 0; i-- {
		msg := m.activeMessages[i]
		sender := msg.SenderName
		if msg.IsFromMe {
			sender = "Me"
		} else if sender == "" {
			sender = activeName
		}

		t := time.UnixMilli(msg.TimestampMS).Format("01/02 03:04 PM")
		body := msg.Body
		for ai, att := range msg.Attachments {
			typeLabel := att.MimeType
			if typeLabel == "" {
				typeLabel = "attachment"
			}
			note := fmt.Sprintf("[Attachment (%s): %s]", typeLabel, att.URL)
			if len(msg.Attachments) > 1 {
				note = fmt.Sprintf("[Attachment %d/%d (%s): %s]", ai+1, len(msg.Attachments), typeLabel, att.URL)
			}
			if body != "" {
				body = body + "\n" + note
			} else {
				body = note
			}
		}
		sb.WriteString(fmt.Sprintf("[%s] **%s**:\n%s\n\n", t, sender, body))
	}

	tmpFile, err := os.CreateTemp("", "msg-thread-*.md")
	if err != nil {
		return nil
	}
	defer tmpFile.Close()
	tmpFile.WriteString(sb.String())

	editor := resolveEditor(m.cfg)

	var args []string
	if isVimLike(editor) {
		args = []string{"-R", "+$", "-c", "set filetype=markdown", "-c", "set number", "-c", "set relativenumber", tmpFile.Name()}
	} else {
		args = []string{tmpFile.Name()}
	}

	c := exec.Command(editor, args...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		os.Remove(tmpFile.Name())
		return nil
	})
}

func (m tuiModel) openExternalEditorForDraft(convID string, styled bool) tea.Cmd {
	// Snapshot attachments at closure-creation time so the editor can't
	// observe subsequent changes to m.draftAttachments.
	attachments := append([]string(nil), m.draftAttachments...)

	editor := resolveEditor(m.cfg)

	// Build thread history content
	var activeName string
	for _, c := range m.conversations {
		if c.ConversationID == convID {
			activeName = c.Name
			break
		}
	}
	if activeName == "" {
		for _, c := range m.unreads {
			if c.ConversationID == convID {
				activeName = c.Name
				break
			}
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# THREAD: %s\n\n", strings.ToUpper(activeName)))
	for i := len(m.activeMessages) - 1; i >= 0; i-- {
		msg := m.activeMessages[i]
		sender := msg.SenderName
		if msg.IsFromMe {
			sender = "Me"
		} else if sender == "" {
			sender = activeName
		}
		t := time.UnixMilli(msg.TimestampMS).Format("01/02 03:04 PM")
		body := msg.Body
		for ai, att := range msg.Attachments {
			typeLabel := att.MimeType
			if typeLabel == "" {
				typeLabel = "attachment"
			}
			note := fmt.Sprintf("[Attachment (%s): %s]", typeLabel, att.URL)
			if len(msg.Attachments) > 1 {
				note = fmt.Sprintf("[Attachment %d/%d (%s): %s]", ai+1, len(msg.Attachments), typeLabel, att.URL)
			}
			if body != "" {
				body = body + "\n" + note
			} else {
				body = note
			}
		}
		sb.WriteString(fmt.Sprintf("[%s] **%s**:\n%s\n\n", t, sender, body))
	}

	threadFile, err := os.CreateTemp("", "msg-thread-*.md")
	if err != nil {
		return nil
	}
	threadFile.WriteString(sb.String())
	threadFile.Close()

	draftPath, err := client.GetDraftPath(convID)
	if err != nil {
		os.Remove(threadFile.Name())
		return nil
	}
	_ = os.MkdirAll(filepath.Dir(draftPath), 0755)
	if _, err := os.Stat(draftPath); os.IsNotExist(err) {
		_ = os.WriteFile(draftPath, []byte(""), 0644)
	}

	if isVimLike(editor) {
		// Full experience: split thread+draft panes, vimscript send keybinds, send sentinel.
		sentinelPath := draftPath + ".send"
		_ = os.Remove(sentinelPath)

		setupFile, err := os.CreateTemp("", "msg-vim-setup-*.vim")
		if err != nil {
			os.Remove(threadFile.Name())
			return nil
		}
		fmt.Fprintf(setupFile, `
function! MsgSendAndQuit()
  call writefile([], '%s')
  silent! update
  qa!
endfunction

command! -nargs=0 MsgSend call MsgSendAndQuit()
cabbrev wq MsgSend

augroup MsgDraft
  autocmd!
  autocmd QuitPre * silent! update
augroup END

nnoremap <C-s> :call MsgSendAndQuit()<CR>
inoremap <C-s> <Esc>:call MsgSendAndQuit()<CR>

startinsert
`, sentinelPath)
		setupFile.Close()

		args := []string{
			threadFile.Name(),
			"-c", "norm! G",
			"-c", "setlocal readonly nomodifiable filetype=markdown",
			"-c", "set number",
			"-c", fmt.Sprintf("botright 15split %s", draftPath),
			"-c", "setlocal filetype=markdown",
			"-c", fmt.Sprintf("source %s", setupFile.Name()),
		}

		c := exec.Command(editor, args...)
		return tea.ExecProcess(c, func(execErr error) tea.Msg {
			os.Remove(threadFile.Name())
			os.Remove(setupFile.Name())

			_, sentinelErr := os.Stat(sentinelPath)
			os.Remove(sentinelPath)

			if sentinelErr == nil {
				data, readErr := os.ReadFile(draftPath)
				body := strings.TrimSpace(string(data))
				if readErr == nil && (body != "" || len(attachments) > 0) {
					success, sendErr := client.SendMessageWithAttachments(convID, body, styled, attachments)
					if sendErr == nil && success {
						_ = client.DeleteDraft(convID)
						return externalEditorDoneMsg{sent: true, convID: convID}
					}
					return externalEditorDoneMsg{sent: false, convID: convID, errMsg: "Failed to send message."}
				}
			}

			return externalEditorDoneMsg{sent: false, convID: convID}
		})
	}

	// Non-vim editors: open only the draft file; exiting saves the draft (no auto-send).
	os.Remove(threadFile.Name())
	c := exec.Command(editor, draftPath)
	return tea.ExecProcess(c, func(execErr error) tea.Msg {
		return externalEditorDoneMsg{sent: false, convID: convID}
	})
}

// msgAtScrollPos returns the index into m.activeMessages that corresponds to the
// message currently shown at the bottom of the thread viewport.  It mirrors the
// line-building logic in renderSharedThreadPane so the two stay in sync.
func (m tuiModel) msgAtScrollPos() int {
	if len(m.activeMessages) == 0 {
		return -1
	}

	leftWidth := (m.termWidth * 4) / 10
	if leftWidth < 25 {
		leftWidth = 25
	}
	rightWidth := m.termWidth - leftWidth - 6
	if rightWidth < 20 {
		rightWidth = 20
	}
	availWidth := rightWidth - 4

	type msgSpan struct{ start, end, idx int }
	var spans []msgSpan
	totalLines := 0

	// Mirror the reverse render order: activeMessages[len-1] → first in allLines
	for i := len(m.activeMessages) - 1; i >= 0; i-- {
		msg := m.activeMessages[i]
		start := totalLines

		lines := 0
		if msg.Body != "" {
			// Approximate prefix (timestamp + sender label ≈ 30 chars)
			wrapWidth := availWidth - 30
			if wrapWidth < 10 {
				wrapWidth = 10
			}
			bodyRunes := len([]rune(msg.Body))
			l := (bodyRunes + wrapWidth - 1) / wrapWidth
			if l < 1 {
				l = 1
			}
			lines += l
		}
		lines += len(msg.Attachments)
		if len(msg.Reactions) > 0 {
			lines++
		}
		if lines == 0 {
			lines = 1
		}

		totalLines += lines
		spans = append(spans, msgSpan{start, totalLines - 1, i})
	}

	endIdx := totalLines - m.scrollOffset
	if endIdx > totalLines {
		endIdx = totalLines
	}
	if endIdx <= 0 {
		return 0
	}
	targetLine := endIdx - 1

	for _, s := range spans {
		if s.start <= targetLine && targetLine <= s.end {
			return s.idx
		}
	}
	return 0
}

// openAttachments launches the system default handler for one or more attachment URLs.
// On macOS, `open` accepts multiple paths in one call. On Linux, each is fired
// independently without waiting (xdg-open is not meant to be waited on).
func openAttachments(attachments []client.Attachment) tea.Cmd {
	var urls []string
	for _, a := range attachments {
		if a.URL != "" {
			urls = append(urls, a.URL)
		}
	}
	if len(urls) == 0 {
		return nil
	}
	if runtime.GOOS == "darwin" {
		return tea.ExecProcess(exec.Command("open", urls...), nil)
	}
	return func() tea.Msg {
		for _, u := range urls {
			exec.Command("xdg-open", u).Start()
		}
		return nil
	}
}

// pickAttachments opens a file picker and returns an attachmentPickedMsg.
// On macOS it uses the native Finder dialog (osascript, always available).
// Falls back to fzf if osascript is not found, then shows an install hint.
func pickAttachments() tea.Cmd {
	// macOS native file picker — no installation required, opens a GUI dialog.
	if _, err := exec.LookPath("osascript"); err == nil {
		return func() tea.Msg {
			script := `set theFiles to choose file with prompt "Select attachments:" with multiple selections allowed
set output to ""
repeat with i from 1 to count of theFiles
	set output to output & POSIX path of item i of theFiles & "\n"
end repeat
return output`
			out, err := exec.Command("osascript", "-e", script).Output()
			if err != nil {
				// User cancelled or osascript error — treat as no selection.
				return attachmentPickedMsg{}
			}
			var paths []string
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if p := strings.TrimSpace(line); p != "" {
					paths = append(paths, p)
				}
			}
			return attachmentPickedMsg{paths: paths}
		}
	}

	// Fallback: fzf terminal picker.
	if _, err := exec.LookPath("fzf"); err != nil {
		return func() tea.Msg {
			return attachmentPickedMsg{errMsg: "no file picker available — install fzf (e.g. brew install fzf)"}
		}
	}
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("msg-attach-%d", time.Now().UnixNano()))
	homeDir, _ := os.UserHomeDir()
	script := fmt.Sprintf(
		`find '%s' -not -path '*/\.*' -type f 2>/dev/null | fzf --multi --print0 --header='TAB: multi-select  ENTER: confirm  ESC: cancel' > '%s'`,
		homeDir, tmpFile,
	)
	cmd := exec.Command("/bin/sh", "-c", script)
	return tea.ExecProcess(cmd, func(_ error) tea.Msg {
		defer os.Remove(tmpFile)
		data, _ := os.ReadFile(tmpFile)
		if len(data) == 0 {
			return attachmentPickedMsg{}
		}
		var paths []string
		for _, raw := range bytes.Split(data, []byte{0}) {
			if s := strings.TrimSpace(string(raw)); s != "" {
				paths = append(paths, s)
			}
		}
		return attachmentPickedMsg{paths: paths}
	})
}

func (m tuiModel) renderPlatformPicker() (string, string) {
	var left, right strings.Builder
	left.WriteString(activeStyle.Render("CHOOSE PLATFORM") + "\n\n")
	for i, opt := range m.platformOptions {
		marker := " "
		if i == m.platformPickIdx {
			marker = ">"
		}
		label := opt.providerLabel
		suffix := ""
		if opt.isNew {
			suffix = subtleStyle.Render("  (new conversation)")
		}
		left.WriteString(fmt.Sprintf("%s %s%s\n", marker, label, suffix))
	}

	right.WriteString(activeStyle.Render("DETAILS") + "\n\n")
	if m.platformPickIdx < len(m.platformOptions) {
		sel := m.platformOptions[m.platformPickIdx]
		right.WriteString(fmt.Sprintf("Platform : %s\n", sel.providerLabel))
		right.WriteString(fmt.Sprintf("Contact  : %s\n", sel.convName))
		if sel.isNew {
			right.WriteString("Thread   : None — will be created on first send\n")
			right.WriteString(fmt.Sprintf("Send to  : %s\n", sel.convID))
		} else {
			right.WriteString(fmt.Sprintf("Thread ID: %s\n", sel.convID))
		}
	}
	return left.String(), right.String()
}

func runTUI() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running interactive engine frame: %v\n", err)
	}
}
