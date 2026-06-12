package main

import (
	"fmt"
	"strings"

	"github.com/charlesrobsampson/msg/client"
)

func (m tuiModel) renderAliasesView() (string, string) {
	// If the quick editor card layer is active, render form prompts instead of lists
	if m.isEditingAlias {
		var editor strings.Builder
		editor.WriteString(activeStyle.Render("TARGETED IN-TUI ALIAS EDITOR CARD") + "\n")
		editor.WriteString(subtleStyle.Render("Navigate fields with j/k. Edit values directly. Enter to commit.") + "\n\n")

		labels := [4]string{"Shortcut/Key ", "Contact Name ", "Convo/Chan ID", "App Platform "}
		for i := 0; i < 4; i++ {
			marker := "  "
			valStyle := subtleStyle
			if i == m.fieldIndex {
				marker = "> "
				valStyle = activeStyle
			}
			editor.WriteString(fmt.Sprintf("%s%s : %s\n", marker, labels[i], valStyle.Render(m.editFields[i])))
		}

		return editor.String(), ""
	}

	// Default View Layout (What we had historically)
	var left, right strings.Builder
	left.WriteString(activeStyle.Render("MANAGED ALIASES") + "\n")

	if len(m.aliasKeys) == 0 {
		return "No local aliases registered.\nRun 'msg alias' via your shell CLI.", ""
	}

	for i, k := range m.aliasKeys {
		marker := " "
		if i == m.selectedIndex {
			marker = ">"
		}
		left.WriteString(fmt.Sprintf("%s %-25s\n", marker, k))
	}

	if m.selectedIndex >= 0 && m.selectedIndex < len(m.aliasKeys) {
		shortcut := m.aliasKeys[m.selectedIndex]
		
		// Find alias object by shortcut
		var alias client.Alias
		for _, a := range m.aliases {
			if a.Shortcut == shortcut {
				alias = a
				break
			}
		}

		right.WriteString(activeStyle.Render("ALIAS DETAIL ROUTER") + "\n\n")
		right.WriteString(fmt.Sprintf("Shortcut   : %s\n", alias.Shortcut))
		right.WriteString(fmt.Sprintf("Assoc Name : %s\n", alias.Name))
		right.WriteString(fmt.Sprintf("Channel ID : %s\n", alias.ConversationID))
		right.WriteString(fmt.Sprintf("Platform   : %s\n", strings.ToUpper(alias.Platform)))
	} else {
		right.WriteString(subtleStyle.Render("No alias selected."))
	}

	return left.String(), right.String()
}

