package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const emojiCols = 10

var commonEmojis = []string{
	// faces
	"😀", "😂", "🥹", "😍", "🤔", "😎", "🥺", "😢", "😡", "🤯",
	// hands
	"👍", "👎", "👏", "🙌", "🤝", "🙏", "💪", "🤞", "👌", "🤙",
	// hearts & symbols
	"❤️", "🧡", "💛", "💚", "💙", "💜", "💔", "💯", "🔥", "✨",
	// celebrations
	"🎉", "🎊", "🏆", "🥇", "🌟", "💫", "🎈", "🎁", "🍕", "🍺",
	// misc
	"🐶", "🐱", "🌈", "☀️", "🌙", "⭐", "💎", "🚀", "💀", "🤌",
}

func renderEmojiPicker(selectedIdx int, availWidth int) []string {
	boxW := availWidth - 2
	if boxW < 32 {
		boxW = 32
	}
	if boxW > 52 {
		boxW = 52
	}

	selectStyle := lipgloss.NewStyle().Reverse(true)

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Width(boxW)

	rows := (len(commonEmojis) + emojiCols - 1) / emojiCols
	var rowStrings []string
	for r := 0; r < rows; r++ {
		var sb strings.Builder
		for c := 0; c < emojiCols; c++ {
			idx := r*emojiCols + c
			if idx >= len(commonEmojis) {
				break
			}
			if c > 0 {
				sb.WriteString(" ")
			}
			if idx == selectedIdx {
				sb.WriteString(selectStyle.Render(commonEmojis[idx]))
			} else {
				sb.WriteString(commonEmojis[idx])
			}
		}
		rowStrings = append(rowStrings, sb.String())
	}

	rendered := boxStyle.Render(strings.Join(rowStrings, "\n"))
	return strings.Split(rendered, "\n")
}
