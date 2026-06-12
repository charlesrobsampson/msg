package main

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Signal markdown regexes — longest delimiters processed first to avoid
// partial matches (e.g. *** before ** before *).
var (
	reSignalBoldItalic = regexp.MustCompile(`\*\*\*([^*\n]+?)\*\*\*`)
	reSignalBold       = regexp.MustCompile(`\*\*([^*\n]+?)\*\*`)
	reSignalItalic     = regexp.MustCompile(`\*([^*\n]+?)\*`)
	reSignalStrike     = regexp.MustCompile(`~([^~\n]+?)~`)
	reSignalSpoiler    = regexp.MustCompile(`\|\|([^|\n]+?)\|\|`)
	reSignalMono       = regexp.MustCompile("`([^`\n]+)`")
)

var (
	signalBoldItalicStyle = lipgloss.NewStyle().Bold(true).Italic(true)
	signalBoldStyle       = lipgloss.NewStyle().Bold(true)
	signalItalicStyle     = lipgloss.NewStyle().Italic(true)
	signalStrikeStyle     = lipgloss.NewStyle().Strikethrough(true)
	signalMonoStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("222")).Background(lipgloss.Color("235"))
	// Revealed spoiler: dim italic so it reads as "was hidden".
	signalSpoilerRevealedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
	// CLI spoiler: same fg and bg — text invisible until terminal selection
	// inverts the colors, making it readable on highlight.
	signalSpoilerCLIStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Background(lipgloss.Color("8"))
)

// renderSignalBody parses Signal's markdown subset and returns a string with
// lipgloss ANSI styling applied. When revealSpoilers is false, spoiler spans
// are replaced with block characters (████). When true, the plain text shows.
func renderSignalBody(body string, revealSpoilers bool) string {
	body = reSignalBoldItalic.ReplaceAllStringFunc(body, func(s string) string {
		return signalBoldItalicStyle.Render(reSignalBoldItalic.FindStringSubmatch(s)[1])
	})
	body = reSignalBold.ReplaceAllStringFunc(body, func(s string) string {
		return signalBoldStyle.Render(reSignalBold.FindStringSubmatch(s)[1])
	})
	body = reSignalItalic.ReplaceAllStringFunc(body, func(s string) string {
		return signalItalicStyle.Render(reSignalItalic.FindStringSubmatch(s)[1])
	})
	body = reSignalStrike.ReplaceAllStringFunc(body, func(s string) string {
		return signalStrikeStyle.Render(reSignalStrike.FindStringSubmatch(s)[1])
	})
	body = reSignalSpoiler.ReplaceAllStringFunc(body, func(s string) string {
		inner := reSignalSpoiler.FindStringSubmatch(s)[1]
		if revealSpoilers {
			return signalSpoilerRevealedStyle.Render(inner)
		}
		return strings.Repeat("█", len([]rune(inner)))
	})
	body = reSignalMono.ReplaceAllStringFunc(body, func(s string) string {
		return signalMonoStyle.Render(reSignalMono.FindStringSubmatch(s)[1])
	})
	return body
}

// renderSignalBodyCLI applies Signal markdown for plain terminal output.
// Spoilers use same-color fg/bg so the text is hidden until highlighted.
func renderSignalBodyCLI(body string) string {
	body = reSignalBoldItalic.ReplaceAllStringFunc(body, func(s string) string {
		return signalBoldItalicStyle.Render(reSignalBoldItalic.FindStringSubmatch(s)[1])
	})
	body = reSignalBold.ReplaceAllStringFunc(body, func(s string) string {
		return signalBoldStyle.Render(reSignalBold.FindStringSubmatch(s)[1])
	})
	body = reSignalItalic.ReplaceAllStringFunc(body, func(s string) string {
		return signalItalicStyle.Render(reSignalItalic.FindStringSubmatch(s)[1])
	})
	body = reSignalStrike.ReplaceAllStringFunc(body, func(s string) string {
		return signalStrikeStyle.Render(reSignalStrike.FindStringSubmatch(s)[1])
	})
	body = reSignalSpoiler.ReplaceAllStringFunc(body, func(s string) string {
		return signalSpoilerCLIStyle.Render(reSignalSpoiler.FindStringSubmatch(s)[1])
	})
	body = reSignalMono.ReplaceAllStringFunc(body, func(s string) string {
		return signalMonoStyle.Render(reSignalMono.FindStringSubmatch(s)[1])
	})
	return body
}
