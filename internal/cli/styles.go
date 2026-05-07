package cli

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
)

var (
	brandColor   = compat.AdaptiveColor{Light: lipgloss.Color("#6A3FD9"), Dark: lipgloss.Color("#7D56F4")}
	successColor = compat.AdaptiveColor{Light: lipgloss.Color("#028A51"), Dark: lipgloss.Color("#04B575")}
	warnColor    = compat.AdaptiveColor{Light: lipgloss.Color("#B38600"), Dark: lipgloss.Color("#FFCC00")}
	errorColor   = compat.AdaptiveColor{Light: lipgloss.Color("#CC3333"), Dark: lipgloss.Color("#FF5555")}
	dimColor     = compat.AdaptiveColor{Light: lipgloss.Color("#555555"), Dark: lipgloss.Color("#6B6B6B")}
	keyColor     = compat.AdaptiveColor{Light: lipgloss.Color("#555555"), Dark: lipgloss.Color("#B0B0B0")}
	fgColor      = compat.AdaptiveColor{Light: lipgloss.Color("#1A1A1A"), Dark: lipgloss.Color("#FFFFFF")}

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(brandColor).
			MarginBottom(1)

	headingStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(fgColor)

	successStyle = lipgloss.NewStyle().Foreground(successColor)
	warnStyle    = lipgloss.NewStyle().Foreground(warnColor)
	errorStyle   = lipgloss.NewStyle().Foreground(errorColor)
	dimStyle     = lipgloss.NewStyle().Foreground(dimColor)
	keyStyle     = lipgloss.NewStyle().Foreground(keyColor)
	valueStyle   = lipgloss.NewStyle().Foreground(fgColor)

	bulletSuccess = successStyle.Render("  ✓")
	bulletWarn    = warnStyle.Render("  ⚠")
	bulletFail    = errorStyle.Render("  ✗")
	bulletInfo    = dimStyle.Render("  →")
)

func printTitle(text string) {
	fmt.Println(titleStyle.Render(text))
}

func printSuccess(format string, args ...interface{}) {
	fmt.Println(bulletSuccess + " " + successStyle.Render(fmt.Sprintf(format, args...)))
}

func printWarn(format string, args ...interface{}) {
	fmt.Println(bulletWarn + " " + warnStyle.Render(fmt.Sprintf(format, args...)))
}

func printError(format string, args ...interface{}) {
	fmt.Println(bulletFail + " " + errorStyle.Render(fmt.Sprintf(format, args...)))
}

func printInfo(format string, args ...interface{}) {
	fmt.Println(bulletInfo + " " + dimStyle.Render(fmt.Sprintf(format, args...)))
}

func printKV(key, value string) {
	fmt.Printf("  %s %s\n", keyStyle.Render(key+":"), valueStyle.Render(value))
}

func printSection(title string) {
	fmt.Println()
	fmt.Println(headingStyle.Render(title))
}

func printBullet(text string) {
	fmt.Println(dimStyle.Render("  •") + " " + text)
}

func bold(text string) string {
	return lipgloss.NewStyle().Bold(true).Render(text)
}

func renderKV(pairs [][2]string) string {
	var b strings.Builder
	maxKey := 0
	for _, p := range pairs {
		if len(p[0]) > maxKey {
			maxKey = len(p[0])
		}
	}
	for _, p := range pairs {
		b.WriteString("  ")
		b.WriteString(keyStyle.Render(fmt.Sprintf("%-*s", maxKey+1, p[0]+":")))
		b.WriteString(" ")
		b.WriteString(valueStyle.Render(p[1]))
		b.WriteString("\n")
	}
	return b.String()
}
