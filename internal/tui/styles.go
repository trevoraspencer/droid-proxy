package tui

import "github.com/charmbracelet/lipgloss"

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("63")).
			Padding(0, 1)

	subtleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	helpStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)

	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)

	okStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))

	warnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))

	errStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))

	badgeOnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	badgeOffStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

func badge(label string, on bool) string {
	if on {
		return badgeOnStyle.Render("+ " + label)
	}
	return badgeOffStyle.Render("- " + label)
}
