// Package tui implements the interactive `droid-proxy config` dashboard for
// onboarding providers and models: it writes the YAML config, the API key
// secrets file, and the Factory customModels entry from a single flow.
package tui

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"droid-proxy/internal/config"
	"droid-proxy/internal/factory"
	"droid-proxy/internal/secrets"
)

type screen int

const (
	screenDashboard screen = iota
	screenAddProvider
	screenAddKey
	screenDiscover
	screenPickModel
	screenForm
	screenOAuthProviders
	screenOAuthAccounts
	screenConfirmDelete
	screenMessage
)

type providerKind int

const (
	pkKnown providerKind = iota
	pkCustom
	pkOAuth
)

type providerChoice struct {
	kind  providerKind
	ka    config.KnownAuth
	oauth config.OAuthProvider
	label string
}

type oauthModelPreset struct {
	Label            string
	Alias            string
	DisplayName      string
	UpstreamModel    string
	MaxOutputTokens  int
	MaxContextTokens int
}

type formField struct {
	key      string
	label    string
	input    textinput.Model
	optional bool
}

// Run launches the interactive dashboard against the given config file.
func Run(configPath string) error {
	be := newBackend(configPath)
	m := newModel(be)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

type model struct {
	be     *backend
	screen screen
	width  int
	height int

	models  []*config.Model
	factory map[string]bool
	running bool
	cursor  int
	loadErr error

	providers  []providerChoice
	provCursor int

	sel        providerChoice
	pickItems  []string
	pickCursor int

	keyInput textinput.Model
	keyEnv   string
	keyOnly  bool

	spin spinner.Model

	form       []formField
	formCursor int

	oauthProvider config.OAuthProvider
	oauthCursor   int
	accounts      []accountRow

	message     string
	messageErr  bool
	pendingSync *config.Model
	confirmDel  *config.Model
}

type accountRow struct {
	selector string
	email    string
	disabled bool
	expiring bool
}

func newModel(be *backend) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return model{be: be, screen: screenDashboard, spin: sp, factory: map[string]bool{}}
}

func (m model) Init() tea.Cmd {
	return m.loadCmd()
}

// ---- messages ----

type modelsLoadedMsg struct {
	models  []*config.Model
	factory map[string]bool
	running bool
	err     error
}

type discoverMsg struct {
	ids []string
	err error
}

type actionDoneMsg struct {
	text   string
	err    error
	reload bool
}

type accountsLoadedMsg struct {
	rows []accountRow
	err  error
}

type execFinishedMsg struct{ err error }

// ---- commands ----

func (m model) loadCmd() tea.Cmd {
	be := m.be
	return func() tea.Msg {
		models, err := be.models()
		return modelsLoadedMsg{models: models, factory: be.factoryModels(), running: be.proxyRunning(), err: err}
	}
}

func (m model) discoverCmd(sel providerChoice, key string) tea.Cmd {
	be := m.be
	return func() tea.Msg {
		ids, err := be.discover(sel.ka, sel.ka.BaseURL, key)
		return discoverMsg{ids: ids, err: err}
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

// ---- update ----

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

func (m model) startAddFlow() (tea.Model, tea.Cmd) {
	m.providers = buildProviderChoices()
	m.provCursor = 0
	m.screen = screenAddProvider
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
	default: // custom
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

func (m model) beginDiscover() (tea.Model, tea.Cmd) {
	m.screen = screenDiscover
	key := os.Getenv(strings.TrimSpace(m.sel.ka.APIKeyEnv))
	return m, tea.Batch(m.spin.Tick, m.discoverCmd(m.sel, key))
}

func (m model) onDiscover(msg discoverMsg) (tea.Model, tea.Cmd) {
	m.pickCursor = 0
	if msg.err != nil || len(msg.ids) == 0 {
		// Fall back to manual entry.
		m.buildForm()
		m.screen = screenForm
		return m, textinput.Blink
	}
	m.pickItems = append([]string{manualEntryLabel}, msg.ids...)
	m.screen = screenPickModel
	return m, nil
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

// ---- helpers ----

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

func (b *backend) keyEnvSet(env string) bool {
	return strings.TrimSpace(os.Getenv(strings.TrimSpace(env))) != ""
}

func buildProviderChoices() []providerChoice {
	var out []providerChoice
	for _, ka := range config.KnownAuthList() {
		out = append(out, providerChoice{kind: pkKnown, ka: ka, label: ka.Label()})
	}
	out = append(out,
		providerChoice{kind: pkOAuth, oauth: config.OAuthProviderCodex, label: "Codex / ChatGPT (OAuth)"},
		providerChoice{kind: pkOAuth, oauth: config.OAuthProviderXAI, label: "xAI OAuth"},
		providerChoice{kind: pkCustom, label: "Custom OpenAI-compatible endpoint"},
	)
	return out
}

func xaiOAuthPresets() []oauthModelPreset {
	return []oauthModelPreset{
		{
			Label:            "Grok Build 0.1",
			Alias:            "grok-build-0.1",
			DisplayName:      "Grok Build 0.1 (xAI OAuth)",
			UpstreamModel:    "grok-build-0.1",
			MaxOutputTokens:  factory.DefaultMaxOutputTokens,
			MaxContextTokens: 256000,
		},
		{
			Label:            "Grok 4.3",
			Alias:            "grok-4.3",
			DisplayName:      "Grok 4.3 (xAI OAuth)",
			UpstreamModel:    "grok-4.3",
			MaxOutputTokens:  factory.DefaultMaxOutputTokens,
			MaxContextTokens: 1000000,
		},
	}
}

func xaiOAuthPickItems() []string {
	presets := xaiOAuthPresets()
	out := make([]string, 0, len(presets)+1)
	out = append(out, manualEntryLabel)
	for _, preset := range presets {
		out = append(out, preset.Label)
	}
	return out
}

func xaiOAuthPresetByLabel(label string) (oauthModelPreset, bool) {
	for _, preset := range xaiOAuthPresets() {
		if preset.Label == label {
			return preset, true
		}
	}
	return oauthModelPreset{}, false
}

func factoryReasoningForOAuthModel(provider config.OAuthProvider, upstreamModel string) config.FactoryReasoningMode {
	if provider != config.OAuthProviderXAI {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(upstreamModel), "grok-4.3") {
		return config.FactoryReasoningPassthrough
	}
	return config.FactoryReasoningDrop
}

func factoryProviderFor(up config.UpstreamProtocol) config.FactoryProvider {
	switch up {
	case config.UpstreamOpenAIResponses:
		return config.FactoryProviderOpenAI
	case config.UpstreamAnthropicMessages:
		return config.FactoryProviderAnthropic
	default:
		return config.FactoryProviderGeneric
	}
}

func upstreamForOAuth(p config.OAuthProvider) config.UpstreamProtocol {
	if p == config.OAuthProviderXAI {
		return config.UpstreamXAIResponses
	}
	return config.UpstreamCodexResponses
}

var aliasSanitize = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func defaultAlias(modelID string) string {
	id := modelID
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	id = aliasSanitize.ReplaceAllString(id, "-")
	return strings.Trim(strings.ToLower(id), "-")
}

func defaultDisplay(modelID, label string) string {
	base := modelID
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if label == "" {
		return base
	}
	return fmt.Sprintf("%s (%s)", base, label)
}

func isLoopbackBaseURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func secretsPathHint() string {
	return secrets.Path()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

const manualEntryLabel = "— Enter a model id manually —"
