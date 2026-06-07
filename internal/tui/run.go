package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/bubbles/spinner"
)

// Run launches the interactive dashboard against the given config file.
func Run(configPath string) error {
	be := newBackend(configPath)
	m := newModel(be)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func newModel(be *backend) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return model{be: be, screen: screenDashboard, spin: sp, factory: map[string]bool{}}
}

func (m model) Init() tea.Cmd {
	return m.loadCmd()
}
