package tui

import (
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

func (m model) loadCmd() tea.Cmd {
	be := m.be
	return func() tea.Msg {
		models, err := be.models()
		return modelsLoadedMsg{models: models, factory: be.factoryModels(), running: be.proxyRunning(), err: err}
	}
}

func (m model) discoverCmd(sel providerChoice, key string, generation int) tea.Cmd {
	be := m.be
	return func() tea.Msg {
		ids, err := be.discover(sel.ka, sel.ka.BaseURL, key)
		return discoverMsg{ids: ids, err: err, generation: generation}
	}
}

func (m model) accountsCmd(provider config.OAuthProvider) tea.Cmd {
	be := m.be
	return func() tea.Msg {
		tokens, err := be.oauthAccounts(provider)
		if err != nil {
			return accountsLoadedMsg{err: err}
		}
		now := time.Now()
		rows := make([]accountRow, 0, len(tokens))
		for _, t := range tokens {
			rows = append(rows, accountRow{
				selector: t.AccountSelector(),
				email:    t.Email,
				disabled: t.Disabled,
				expiring: t.NeedsRefresh(now),
			})
		}
		return accountsLoadedMsg{rows: rows}
	}
}

func execAuthCmd(provider config.OAuthProvider, configPath string, device bool) tea.Cmd {
	exe, err := os.Executable()
	if err != nil {
		return func() tea.Msg { return execFinishedMsg{err: err} }
	}
	args := []string{"auth", string(provider), "--config", configPath}
	if device {
		args = append(args, "--device")
	}
	c := exec.Command(exe, args...)
	return tea.ExecProcess(c, func(err error) tea.Msg { return execFinishedMsg{err: err} })
}

func (m model) selectedModel() *config.Model {
	if m.cursor < 0 || m.cursor >= len(m.models) {
		return nil
	}
	return m.models[m.cursor]
}

func (m model) selectedAccount() *accountRow {
	if m.oauthCursor < 0 || m.oauthCursor >= len(m.accounts) {
		return nil
	}
	return &m.accounts[m.oauthCursor]
}

func (m model) startAddFlow() (tea.Model, tea.Cmd) {
	m.providers = buildProviderChoices()
	m.provCursor = 0
	m.screen = screenAddProvider
	return m, nil
}

func (m model) beginDiscover() (tea.Model, tea.Cmd) {
	m.discoverGeneration++
	gen := m.discoverGeneration
	m.screen = screenDiscover
	key := os.Getenv(strings.TrimSpace(m.sel.ka.APIKeyEnv))
	return m, tea.Batch(m.spin.Tick, m.discoverCmd(m.sel, key, gen))
}

func (m model) onDiscover(msg discoverMsg) (tea.Model, tea.Cmd) {
	// Ignore stale discovery results from a cancelled or superseded request.
	if msg.generation != m.discoverGeneration {
		return m, nil
	}
	m.pickCursor = 0
	if msg.err != nil || len(msg.ids) == 0 {
		m.buildForm()
		m.discoverFeedback = discoveryFallbackMessage(msg.err)
		m.screen = screenForm
		return m, textinput.Blink
	}
	m.discoverFeedback = ""
	m.pickItems = append([]string{manualEntryLabel}, msg.ids...)
	m.screen = screenPickModel
	return m, nil
}

// discoveryFallbackMessage returns a concise, actionable, generic, and
// secret-safe message for display when best-effort model discovery fails or
// returns no models. It never includes the raw error, URLs, HTTP status
// details, response bodies, or credentials.
func discoveryFallbackMessage(err error) string {
	if err != nil {
		return "Model discovery was unavailable. Enter a model ID manually below."
	}
	return "No models were found via discovery. Enter a model ID manually below."
}
