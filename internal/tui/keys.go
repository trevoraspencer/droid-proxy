package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/trevoraspencer/droid-proxy/internal/config"
	"github.com/trevoraspencer/droid-proxy/internal/factory"
)

func (m model) keyDashboard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.models)-1 {
			m.cursor++
		}
	case "a":
		return m.startAddFlow()
	case "d":
		if sel := m.selectedModel(); sel != nil {
			m.confirmDel = sel
			m.screen = screenConfirmDelete
		}
	case "K":
		if sel := m.selectedModel(); sel != nil && strings.TrimSpace(sel.APIKeyEnv) != "" {
			m.beginKeyInput(sel.APIKeyEnv, true)
		}
	case "s":
		if sel := m.selectedModel(); sel != nil {
			model := sel
			return m, func() tea.Msg {
				err := m.be.syncFactory([]*config.Model{model})
				return actionDoneMsg{text: fmt.Sprintf("Synced %q to Factory settings.", model.Alias), err: err, reload: true}
			}
		}
	case "S":
		all := m.models
		return m, func() tea.Msg {
			err := m.be.syncFactory(all)
			return actionDoneMsg{text: fmt.Sprintf("Synced %d model(s) to Factory settings.", len(all)), err: err, reload: true}
		}
	case "o":
		m.screen = screenOAuthProviders
		m.oauthCursor = 0
	case "r":
		return m, func() tea.Msg {
			err := m.be.restartProxy()
			return actionDoneMsg{text: "Proxy restarted.", err: err, reload: true}
		}
	}
	return m, nil
}

func (m model) keyAddProvider(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.screen = screenDashboard
	case "up", "k":
		if m.provCursor > 0 {
			m.provCursor--
		}
	case "down", "j":
		if m.provCursor < len(m.providers)-1 {
			m.provCursor++
		}
	case "enter":
		m.sel = m.providers[m.provCursor]
		return m.afterProviderChosen()
	}
	return m, nil
}

func (m model) afterProviderChosen() (tea.Model, tea.Cmd) {
	switch m.sel.kind {
	case pkKnown:
		if m.sel.ka.NoAuth || m.be.keyEnvSet(m.sel.ka.APIKeyEnv) {
			return m.beginDiscover()
		}
		m.beginKeyInput(m.sel.ka.APIKeyEnv, false)
		return m, nil
	case pkOAuth:
		if m.sel.oauth == config.OAuthProviderXAI {
			m.pickCursor = 0
			m.pickItems = xaiOAuthPickItems()
			m.screen = screenPickModel
			return m, nil
		}
		m.buildForm()
		m.screen = screenForm
		return m, textinput.Blink
	default:
		m.buildForm()
		m.screen = screenForm
		return m, textinput.Blink
	}
}

func (m *model) beginKeyInput(env string, only bool) {
	ti := textinput.New()
	ti.Placeholder = "paste API key"
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '*'
	ti.Focus()
	ti.CharLimit = 512
	ti.Width = 60
	m.keyInput = ti
	m.keyEnv = env
	m.keyOnly = only
	m.screen = screenAddKey
}

func (m model) keyAddKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if m.keyOnly {
			m.screen = screenDashboard
		} else {
			m.screen = screenAddProvider
		}
		return m, nil
	case "enter":
		val := strings.TrimSpace(m.keyInput.Value())
		env := m.keyEnv
		if val == "" {
			return m, func() tea.Msg {
				return actionDoneMsg{err: fmt.Errorf("API key cannot be empty")}
			}
		}
		if m.keyOnly {
			return m, func() tea.Msg {
				err := m.be.setKey(env, val)
				return actionDoneMsg{text: fmt.Sprintf("Saved %s to %s", env, secretsPathHint()), err: err, reload: true}
			}
		}
		if err := m.be.setKey(env, val); err != nil {
			return m, func() tea.Msg { return actionDoneMsg{err: err} }
		}
		return m.beginDiscover()
	}
	var cmd tea.Cmd
	m.keyInput, cmd = m.keyInput.Update(msg)
	return m, cmd
}

func (m model) keyPickModel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.screen = screenAddProvider
	case "up", "k":
		if m.pickCursor > 0 {
			m.pickCursor--
		}
	case "down", "j":
		if m.pickCursor < len(m.pickItems)-1 {
			m.pickCursor++
		}
	case "enter":
		chosen := ""
		if m.pickCursor > 0 {
			chosen = m.pickItems[m.pickCursor]
		}
		m.buildForm()
		if m.sel.kind == pkOAuth && m.sel.oauth == config.OAuthProviderXAI {
			if preset, ok := xaiOAuthPresetByLabel(chosen); ok {
				m.applyOAuthPreset(preset)
			}
			m.screen = screenForm
			return m, textinput.Blink
		}
		m.setFormValue("upstream_model", chosen)
		if chosen != "" {
			m.setFormValue("alias", defaultAlias(chosen))
			m.setFormValue("display_name", defaultDisplay(chosen, m.sel.label))
		}
		m.screen = screenForm
		return m, textinput.Blink
	}
	return m, nil
}

func (m *model) buildForm() {
	var fields []formField
	add := func(key, label, placeholder string, optional bool) {
		ti := textinput.New()
		ti.Placeholder = placeholder
		ti.CharLimit = 256
		ti.Width = 60
		fields = append(fields, formField{key: key, label: label, input: ti, optional: optional})
	}
	if m.sel.kind == pkCustom {
		add("base_url", "Base URL", "https://api.example.com/v1", false)
		add("api_key_env", "API key env var (blank = none)", "EXAMPLE_API_KEY", true)
	}
	add("upstream_model", "Upstream model", "provider model id", false)
	if m.sel.kind == pkOAuth {
		add("base_url", "OAuth base URL (blank = provider default)", "https://api.example.com/v1", true)
		add("oauth_account", "OAuth account (blank = any)", "email or sub", true)
	}
	add("alias", "Alias (Droid model id)", "my-model", false)
	add("display_name", "Display name", "My Model", true)
	add("max_output_tokens", "Max output tokens (blank = default)", strconv.Itoa(factory.DefaultMaxOutputTokens), true)
	add("max_context_tokens", "Max context tokens (blank = default)", "256000", true)
	fields[0].input.Focus()
	m.form = fields
	m.formCursor = 0
}

func (m *model) applyOAuthPreset(p oauthModelPreset) {
	m.setFormValue("upstream_model", p.UpstreamModel)
	m.setFormValue("base_url", p.BaseURL)
	m.setFormValue("alias", p.Alias)
	m.setFormValue("display_name", p.DisplayName)
	if p.MaxOutputTokens > 0 {
		m.setFormValue("max_output_tokens", strconv.Itoa(p.MaxOutputTokens))
	}
	if p.MaxContextTokens > 0 {
		m.setFormValue("max_context_tokens", strconv.Itoa(p.MaxContextTokens))
	}
}

func (m *model) setFormValue(key, val string) {
	for i := range m.form {
		if m.form[i].key == key {
			m.form[i].input.SetValue(val)
		}
	}
}

func (m model) formValue(key string) string {
	for i := range m.form {
		if m.form[i].key == key {
			return strings.TrimSpace(m.form[i].input.Value())
		}
	}
	return ""
}

func (m model) keyForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.screen = screenDashboard
		return m, nil
	case "tab", "down":
		m.focusForm(m.formCursor + 1)
		return m, nil
	case "shift+tab", "up":
		m.focusForm(m.formCursor - 1)
		return m, nil
	case "enter":
		if m.formCursor < len(m.form)-1 {
			m.focusForm(m.formCursor + 1)
			return m, nil
		}
		return m.submitForm()
	}
	var cmd tea.Cmd
	m.form[m.formCursor].input, cmd = m.form[m.formCursor].input.Update(msg)
	return m, cmd
}

func (m *model) focusForm(idx int) {
	if len(m.form) == 0 {
		return
	}
	if idx < 0 {
		idx = 0
	}
	if idx > len(m.form)-1 {
		idx = len(m.form) - 1
	}
	for i := range m.form {
		m.form[i].input.Blur()
	}
	m.form[idx].input.Focus()
	m.formCursor = idx
}

func (m model) submitForm() (tea.Model, tea.Cmd) {
	built, err := m.buildModelFromForm()
	if err != nil {
		return m, func() tea.Msg { return actionDoneMsg{err: err} }
	}
	if err := m.be.addModel(built); err != nil {
		return m, func() tea.Msg { return actionDoneMsg{err: err} }
	}
	m.pendingSync = built
	m.screen = screenMessage
	m.messageErr = false
	note := ""
	if m.sel.kind == pkOAuth {
		note = fmt.Sprintf("\nRun OAuth login: droid-proxy auth %s", m.sel.oauth)
	}
	m.message = fmt.Sprintf("Added model %q to %s.%s\n\nSync to Factory settings now? (y/n)", built.Alias, m.be.configPath, note)
	return m, m.loadCmd()
}

func (m model) buildModelFromForm() (*config.Model, error) {
	alias := m.formValue("alias")
	upstreamModel := m.formValue("upstream_model")
	if alias == "" {
		return nil, fmt.Errorf("alias is required")
	}
	if upstreamModel == "" {
		return nil, fmt.Errorf("upstream model is required")
	}
	built := &config.Model{
		Alias:         alias,
		DisplayName:   m.formValue("display_name"),
		UpstreamModel: upstreamModel,
	}
	if mt := m.formValue("max_output_tokens"); mt != "" {
		n, err := strconv.Atoi(mt)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("max output tokens must be a non-negative integer")
		}
		built.MaxOutputTokens = n
	}
	if mt := m.formValue("max_context_tokens"); mt != "" {
		n, err := strconv.Atoi(mt)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("max context tokens must be a non-negative integer")
		}
		built.MaxContextTokens = n
	}
	switch m.sel.kind {
	case pkKnown:
		built.KnownAuth = m.sel.ka.Name
		built.FactoryProvider = factoryProviderFor(m.sel.ka.UpstreamProtocol)
		built.UpstreamProtocol = m.sel.ka.UpstreamProtocol
	case pkCustom:
		base := m.formValue("base_url")
		keyEnv := m.formValue("api_key_env")
		if base == "" {
			return nil, fmt.Errorf("base URL is required for a custom endpoint")
		}
		if keyEnv == "" && !isLoopbackBaseURL(base) {
			return nil, fmt.Errorf("remote endpoints require an API key env var (loopback URLs may omit it)")
		}
		built.BaseURL = base
		built.APIKeyEnv = keyEnv
		built.FactoryProvider = config.FactoryProviderGeneric
		built.UpstreamProtocol = config.UpstreamOpenAIChat
	case pkOAuth:
		built.OAuthProvider = m.sel.oauth
		built.OAuthAccount = m.formValue("oauth_account")
		built.BaseURL = m.formValue("base_url")
		built.FactoryProvider = config.FactoryProviderOpenAI
		built.UpstreamProtocol = upstreamForOAuth(m.sel.oauth)
		if mode := factoryReasoningForOAuthModel(m.sel.oauth, upstreamModel); mode != "" {
			built.Capabilities.FactoryReasoning = mode
		}
	}
	if strings.TrimSpace(built.DisplayName) == "" {
		built.DisplayName = defaultDisplay(upstreamModel, m.sel.label)
	}
	return built, nil
}

func (m model) keyMessage(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.pendingSync != nil {
		switch msg.String() {
		case "y", "Y":
			model := m.pendingSync
			m.pendingSync = nil
			m.screen = screenDashboard
			return m, tea.Batch(func() tea.Msg {
				err := m.be.syncFactory([]*config.Model{model})
				return actionDoneMsg{text: fmt.Sprintf("Synced %q to Factory settings.", model.Alias), err: err, reload: true}
			})
		case "n", "N", "esc", "enter":
			m.pendingSync = nil
			m.screen = screenDashboard
			return m, m.loadCmd()
		}
		return m, nil
	}
	switch msg.String() {
	case "esc", "enter", "q":
		m.screen = screenDashboard
		return m, m.loadCmd()
	}
	return m, nil
}

func (m model) keyConfirmDelete(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		target := m.confirmDel
		m.confirmDel = nil
		return m, func() tea.Msg {
			err := m.be.removeModel(target.Alias)
			return actionDoneMsg{text: fmt.Sprintf("Removed model %q.", target.Alias), err: err, reload: true}
		}
	case "n", "N", "esc":
		m.confirmDel = nil
		m.screen = screenDashboard
	}
	return m, nil
}

func (m model) keyOAuthProviders(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	providers := []config.OAuthProvider{config.OAuthProviderCodex, config.OAuthProviderXAI}
	switch msg.String() {
	case "esc":
		m.screen = screenDashboard
	case "up", "k":
		if m.oauthCursor > 0 {
			m.oauthCursor--
		}
	case "down", "j":
		if m.oauthCursor < len(providers)-1 {
			m.oauthCursor++
		}
	case "enter":
		m.oauthProvider = providers[m.oauthCursor]
		m.oauthCursor = 0
		m.screen = screenOAuthAccounts
		return m, m.accountsCmd(m.oauthProvider)
	}
	return m, nil
}

func (m model) keyOAuthAccounts(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.screen = screenOAuthProviders
	case "up", "k":
		if m.oauthCursor > 0 {
			m.oauthCursor--
		}
	case "down", "j":
		if m.oauthCursor < len(m.accounts)-1 {
			m.oauthCursor++
		}
	case "l":
		return m, execAuthCmd(m.oauthProvider, m.be.configPath, false)
	case "L":
		return m, execAuthCmd(m.oauthProvider, m.be.configPath, true)
	case "x":
		if row := m.selectedAccount(); row != nil {
			provider, account, disabled := m.oauthProvider, row.selector, !row.disabled
			return m, func() tea.Msg {
				if err := m.be.setOAuthDisabled(provider, account, disabled); err != nil {
					return actionDoneMsg{err: err}
				}
				return execFinishedMsg{}
			}
		}
	case "D":
		if row := m.selectedAccount(); row != nil {
			provider, account := m.oauthProvider, row.selector
			return m, func() tea.Msg {
				if err := m.be.oauthLogout(provider, account); err != nil {
					return actionDoneMsg{err: err}
				}
				return execFinishedMsg{}
			}
		}
	}
	return m, nil
}
