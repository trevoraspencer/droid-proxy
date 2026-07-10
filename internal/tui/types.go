// Package tui implements the interactive `droid-proxy config` dashboard for
// onboarding providers and models: it writes the YAML config, the API key
// secrets file, and the Factory customModels entry from a single flow.
package tui

import (
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"

	"github.com/trevoraspencer/droid-proxy/internal/config"
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

const manualEntryLabel = "— Enter a model id manually —"

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
	BaseURL          string
	MaxOutputTokens  int
	MaxContextTokens int
	ExtraArgs        map[string]any
	Capabilities     config.Capabilities
}

type formField struct {
	key      string
	label    string
	input    textinput.Model
	optional bool
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

	form        []formField
	formCursor  int
	oauthPreset *oauthModelPreset

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
