package tui

import (
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case modelsLoadedMsg:
		m.models, m.factory, m.running, m.loadErr = msg.models, msg.factory, msg.running, msg.err
		if m.factory == nil {
			m.factory = map[string]bool{}
		}
		if m.cursor >= len(m.models) {
			m.cursor = maxInt(0, len(m.models)-1)
		}
		return m, nil
	case discoverMsg:
		return m.onDiscover(msg)
	case accountsLoadedMsg:
		m.accounts = msg.rows
		if msg.err != nil {
			m.accounts = nil
		}
		if m.oauthCursor >= len(m.accounts) {
			m.oauthCursor = maxInt(0, len(m.accounts)-1)
		}
		return m, nil
	case actionDoneMsg:
		m.screen = screenMessage
		m.messageErr = msg.err != nil
		if msg.err != nil {
			m.message = msg.err.Error()
		} else {
			m.message = msg.text
		}
		if msg.reload {
			return m, m.loadCmd()
		}
		return m, nil
	case execFinishedMsg:
		m.screen = screenOAuthAccounts
		return m, m.accountsCmd(m.oauthProvider)
	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch m.screen {
	case screenDashboard:
		return m.keyDashboard(msg)
	case screenAddProvider:
		return m.keyAddProvider(msg)
	case screenAddKey:
		return m.keyAddKey(msg)
	case screenPickModel:
		return m.keyPickModel(msg)
	case screenForm:
		return m.keyForm(msg)
	case screenOAuthProviders:
		return m.keyOAuthProviders(msg)
	case screenOAuthAccounts:
		return m.keyOAuthAccounts(msg)
	case screenConfirmDelete:
		return m.keyConfirmDelete(msg)
	case screenMessage:
		return m.keyMessage(msg)
	case screenDiscover:
		if msg.String() == "esc" {
			m.screen = screenDashboard
		}
		return m, nil
	}
	return m, nil
}
