package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func getSeededNameColor(name string, palette string) string {
	sum := 0
	for _, char := range name {
		sum += int(char)
	}

	switch strings.ToLower(palette) {
	case "vibrant":
		vibrantCodes := []string{"1", "2", "3", "4", "5", "6", "9", "10", "11", "12", "13", "14"}
		return vibrantCodes[sum%len(vibrantCodes)]
	case "muted":
		mutedCodes := []string{"240", "242", "244", "245", "102", "109", "143", "137"}
		return mutedCodes[sum%len(mutedCodes)]
	case "pastel":
		fallthrough
	default:
		pastelCodes := []string{"215", "219", "150", "153", "180", "186", "141", "117"}
		return pastelCodes[sum%len(pastelCodes)]
	}
}

// themeLabels are the labels for styleFields[0..ThemePropertiesCount-1].
var themeLabels = [ThemePropertiesCount]string{
	"Primary Theme Color",
	"Subtle Accents Color",
	"Me Messages Color",
	"Main Text Color",
	"Background Color",
	"Timestamp Text Color",
	"Name Palette Style",
}

// generalLabels are the labels for styleFields[ThemePropertiesCount..settingsCount-1].
var generalLabels = [settingsCount - ThemePropertiesCount]string{
	"Editor",
}

func (m tuiModel) renderStylesView() (string, string) {
	var left, right strings.Builder

	fields := m.styleFields
	if !m.isEditingStyles {
		fields[0] = m.cfg.Theme.PrimaryColor
		fields[1] = m.cfg.Theme.SubtleColor
		fields[2] = m.cfg.Theme.MeMessageColor
		fields[3] = m.cfg.Theme.MainTextColor
		fields[4] = m.cfg.Theme.BackgroundColor
		fields[5] = m.cfg.Theme.TimestampColor
		fields[6] = m.cfg.Theme.NameColorPalette
		fields[7] = m.cfg.Editor
	}

	// --- LEFT PANEL ---
	left.WriteString(activeStyle.Render("SETTINGS") + "\n")

	left.WriteString("\n" + subtleStyle.Render("─ THEME ─") + "\n")
	for i, lbl := range themeLabels {
		if m.activeFilter != "" && !strings.Contains(strings.ToLower(lbl), strings.ToLower(m.activeFilter)) {
			continue
		}
		if i == m.selectedIndex {
			left.WriteString(fmt.Sprintf("> %s\n", activeStyle.Render(lbl)))
		} else {
			left.WriteString(fmt.Sprintf("  %s\n", lbl))
		}
	}

	left.WriteString("\n" + subtleStyle.Render("─ GENERAL ─") + "\n")
	for i, lbl := range generalLabels {
		idx := ThemePropertiesCount + i
		if m.activeFilter != "" && !strings.Contains(strings.ToLower(lbl), strings.ToLower(m.activeFilter)) {
			continue
		}
		if idx == m.selectedIndex {
			left.WriteString(fmt.Sprintf("> %s\n", activeStyle.Render(lbl)))
		} else {
			left.WriteString(fmt.Sprintf("  %s\n", lbl))
		}
	}

	left.WriteString("\n" + subtleStyle.Render("Color Reference:") + "\n")
	left.WriteString("👉 https://en.wikipedia.org/wiki/ANSI_escape_code#8-bit\n\n")
	left.WriteString(subtleStyle.Render("Palettes: pastel, vibrant, muted"))

	// --- RIGHT PANEL ---
	if m.isEditingStyles {
		right.WriteString(activeStyle.Render("SETTINGS EDITOR") + "\n")
		right.WriteString(subtleStyle.Render("j/k to switch fields. Type to edit. Enter to save, Esc to exit.") + "\n\n")

		right.WriteString(subtleStyle.Render("THEME") + "\n")
		for i, lbl := range themeLabels {
			paddedLabel := fmt.Sprintf("%-22s: ", lbl)
			val := fields[i]
			if i == m.styleFieldIndex {
				right.WriteString(paddedLabel + activeStyle.Render(fmt.Sprintf("[%s_]", val)) + "\n")
			} else {
				right.WriteString(paddedLabel + val + "\n")
			}
		}

		right.WriteString("\n" + subtleStyle.Render("GENERAL") + "\n")
		for i, lbl := range generalLabels {
			idx := ThemePropertiesCount + i
			paddedLabel := fmt.Sprintf("%-22s: ", lbl)
			val := fields[idx]
			if idx == m.styleFieldIndex {
				right.WriteString(paddedLabel + activeStyle.Render(fmt.Sprintf("[%s_]", val)) + "\n")
			} else {
				right.WriteString(paddedLabel + val + "\n")
			}
		}
	} else {
		right.WriteString(activeStyle.Render("DETAILS") + "\n\n")

		sel := m.selectedIndex
		if sel < ThemePropertiesCount {
			lbl := themeLabels[sel]
			val := fields[sel]
			if val == "" && (lbl == "Main Text Color" || lbl == "Background Color") {
				right.WriteString(fmt.Sprintf("%-22s: %s\n", lbl, subtleStyle.Render("(inherited terminal default)")))
			} else {
				right.WriteString(fmt.Sprintf("%-22s: %s\n\n", lbl, val))
			}
			right.WriteString(subtleStyle.Render("Press [Enter] to edit all theme fields.") + "\n")
		} else {
			genIdx := sel - ThemePropertiesCount
			if genIdx < len(generalLabels) {
				lbl := generalLabels[genIdx]
				val := fields[sel]
				right.WriteString(fmt.Sprintf("%-22s: %s\n\n", lbl, val))
				right.WriteString(subtleStyle.Render("Supported: vim, nvim, nano") + "\n")
				right.WriteString(subtleStyle.Render("Priority: MSG_EDITOR env > this setting > EDITOR env") + "\n")
				right.WriteString(subtleStyle.Render("Press [Enter] to edit.") + "\n")
			}
		}
	}

	// Live swatch — always shown so you can preview theme changes in real time.
	right.WriteString("\n" + subtleStyle.Render("─── Live Theme Preview ───") + "\n\n")

	pCol := fields[0]
	sCol := fields[1]
	mCol := fields[2]
	txtCol := fields[3]
	bgCol := fields[4]
	tsCol := fields[5]
	palSeed := fields[6]

	swatchBox := lipgloss.NewStyle()
	if txtCol != "" {
		swatchBox = swatchBox.Foreground(lipgloss.Color(txtCol))
	}
	if bgCol != "" {
		swatchBox = swatchBox.Background(lipgloss.Color(bgCol))
	}

	swatchPrimary := lipgloss.NewStyle().Foreground(lipgloss.Color(pCol)).Bold(true)
	swatchSubtle := lipgloss.NewStyle().Foreground(lipgloss.Color(sCol))
	swatchMe := lipgloss.NewStyle().Foreground(lipgloss.Color(mCol))
	swatchTime := lipgloss.NewStyle().Foreground(lipgloss.Color(tsCol))

	aliceColor := getSeededNameColor("Alice", palSeed)
	bobColor := getSeededNameColor("Bob Smith", palSeed)

	swatchAlice := lipgloss.NewStyle().Foreground(lipgloss.Color(aliceColor)).Bold(true)
	swatchBob := lipgloss.NewStyle().Foreground(lipgloss.Color(bobColor)).Bold(true)

	previewBlock := []string{
		swatchPrimary.Render("[1] Unreads Tab Focus Highlight"),
		swatchAlice.Render("Alice:") + " This name color uses our automatic " + palSeed + " profile! " + swatchTime.Render("4:56 PM"),
		swatchMe.Render("Me:") + " Awesome, everything updates instantly. " + swatchTime.Render("4:57 PM"),
		swatchBob.Render("Bob Smith:") + " The layout grid looks completely uniform now! " + swatchTime.Render("4:58 PM"),
		swatchSubtle.Render("-------------------------------------------------"),
	}

	for _, line := range previewBlock {
		right.WriteString(swatchBox.Render(line) + "\n")
	}

	return left.String(), right.String()
}
