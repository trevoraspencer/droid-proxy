package tui

import (
	"fmt"
	"net/url"
	"strings"

	"droid-proxy/internal/config"
)

func (m model) View() string {
	switch m.screen {
	case screenDashboard:
		return m.viewDashboard()
	case screenAddProvider:
		return m.viewAddProvider()
	case screenAddKey:
		return m.viewAddKey()
	case screenDiscover:
		return m.viewDiscover()
	case screenPickModel:
		return m.viewPickModel()
	case screenForm:
		return m.viewForm()
	case screenOAuthProviders:
		return m.viewOAuthProviders()
	case screenOAuthAccounts:
		return m.viewOAuthAccounts()
	case screenConfirmDelete:
		return m.viewConfirmDelete()
	case screenMessage:
		return m.viewMessage()
	}
	return ""
}

func (m model) header(sub string) string {
	status := warnStyle.Render("proxy stopped")
	if m.running {
		status = okStyle.Render("proxy running")
	}
	line := titleStyle.Render(" droid-proxy config ") + "  " + status
	if sub != "" {
		line += "\n" + subtleStyle.Render(sub)
	}
	return line + "\n\n"
}

func (m model) viewDashboard() string {
	var b strings.Builder
	b.WriteString(m.header(fmt.Sprintf("config: %s", m.be.configPath)))

	if m.loadErr != nil {
		b.WriteString(errStyle.Render("error loading config: "+m.loadErr.Error()) + "\n\n")
	}
	if len(m.models) == 0 {
		b.WriteString(subtleStyle.Render("No models configured yet. Press 'a' to add one.") + "\n")
	}
	for i, mod := range m.models {
		cursor := "  "
		if i == m.cursor {
			cursor = cursorStyle.Render("> ")
		}
		name := mod.Alias
		if i == m.cursor {
			name = selectedStyle.Render(name)
		}
		b.WriteString(fmt.Sprintf("%s%s\n", cursor, name))
		b.WriteString("    " + subtleStyle.Render(modelRouteSummary(mod)) + "  " + m.modelBadges(mod) + "\n")
	}

	b.WriteString("\n" + helpStyle.Render(
		"a add  K set-key  s sync  S sync-all  o oauth  d delete  r restart  ↑/↓ move  q quit"))
	return b.String()
}

func (m model) modelBadges(mod *config.Model) string {
	var parts []string
	if mod.OAuthProvider.IsValid() {
		on, total := m.be.oauthHealth(mod)
		parts = append(parts, badge(fmt.Sprintf("oauth %d/%d", on, total), on > 0))
	} else {
		parts = append(parts, badge("key", m.be.keySet(mod)))
	}
	parts = append(parts, badge("agent", mod.AgentReady()))
	parts = append(parts, badge("factory", m.factory[mod.Alias]))
	return strings.Join(parts, "  ")
}

func modelRouteSummary(mod *config.Model) string {
	return modelProviderLabel(mod) + " · " + string(mod.UpstreamProtocol)
}

func modelProviderLabel(mod *config.Model) string {
	if mod == nil {
		return "unknown provider"
	}
	if mod.KnownAuth != "" {
		if ka, ok := config.LookupKnownAuth(mod.KnownAuth); ok {
			return ka.Label()
		}
		return mod.KnownAuth
	}
	switch mod.OAuthProvider {
	case config.OAuthProviderCodex:
		return "Codex / ChatGPT OAuth"
	case config.OAuthProviderXAI:
		return "xAI OAuth"
	}
	if mod.BaseURL != "" {
		if u, err := url.Parse(mod.BaseURL); err == nil && u.Host != "" {
			return u.Host
		}
		return mod.BaseURL
	}
	return "custom provider"
}

func (m model) viewAddProvider() string {
	var b strings.Builder
	b.WriteString(m.header("Add a model — choose a provider"))
	for i, p := range m.providers {
		cursor := "  "
		label := p.label
		if i == m.provCursor {
			cursor = cursorStyle.Render("> ")
			label = selectedStyle.Render(label)
		}
		extra := ""
		if p.kind == pkKnown {
			if p.ka.NoAuth {
				extra = subtleStyle.Render("  (no key)")
			} else if m.be.keyEnvSet(p.ka.APIKeyEnv) {
				extra = okStyle.Render("  (key set)")
			} else {
				extra = subtleStyle.Render("  (" + p.ka.APIKeyEnv + ")")
			}
		}
		b.WriteString(fmt.Sprintf("%s%s%s\n", cursor, label, extra))
	}
	b.WriteString("\n" + helpStyle.Render("enter select  ↑/↓ move  esc back"))
	return b.String()
}

func (m model) viewAddKey() string {
	var b strings.Builder
	b.WriteString(m.header("Set API key for " + m.keyEnv))
	b.WriteString(m.keyInput.View() + "\n\n")
	b.WriteString(subtleStyle.Render("Stored in "+secretsPathHint()+" (chmod 600).") + "\n\n")
	b.WriteString(helpStyle.Render("enter save  esc back"))
	return b.String()
}

func (m model) viewDiscover() string {
	var b strings.Builder
	b.WriteString(m.header("Adding " + m.sel.label))
	b.WriteString(m.spin.View() + " querying provider for available models...\n\n")
	b.WriteString(helpStyle.Render("esc cancel"))
	return b.String()
}

func (m model) viewPickModel() string {
	var b strings.Builder
	b.WriteString(m.header("Choose a model from " + m.sel.label))
	start, end := windowAround(m.pickCursor, len(m.pickItems), 15)
	for i := start; i < end; i++ {
		cursor := "  "
		label := m.pickItems[i]
		if i == m.pickCursor {
			cursor = cursorStyle.Render("> ")
			label = selectedStyle.Render(label)
		}
		b.WriteString(fmt.Sprintf("%s%s\n", cursor, label))
	}
	if end < len(m.pickItems) {
		b.WriteString(subtleStyle.Render(fmt.Sprintf("  ...%d more", len(m.pickItems)-end)) + "\n")
	}
	b.WriteString("\n" + helpStyle.Render("enter select  ↑/↓ move  esc back"))
	return b.String()
}

func (m model) viewForm() string {
	var b strings.Builder
	b.WriteString(m.header("New model — " + m.sel.label))
	for i, f := range m.form {
		marker := "  "
		label := f.label
		if i == m.formCursor {
			marker = cursorStyle.Render("> ")
			label = selectedStyle.Render(label)
		}
		opt := ""
		if f.optional {
			opt = subtleStyle.Render(" (optional)")
		}
		b.WriteString(fmt.Sprintf("%s%s%s\n  %s\n", marker, label, opt, f.input.View()))
	}
	b.WriteString("\n" + helpStyle.Render("tab/↑↓ move  enter next/submit  esc cancel"))
	return b.String()
}

func (m model) viewOAuthProviders() string {
	var b strings.Builder
	b.WriteString(m.header("OAuth accounts — choose a provider"))
	labels := []string{"Codex / ChatGPT", "xAI"}
	for i, l := range labels {
		cursor := "  "
		if i == m.oauthCursor {
			cursor = cursorStyle.Render("> ")
			l = selectedStyle.Render(l)
		}
		b.WriteString(fmt.Sprintf("%s%s\n", cursor, l))
	}
	b.WriteString("\n" + helpStyle.Render("enter select  esc back"))
	return b.String()
}

func (m model) viewOAuthAccounts() string {
	var b strings.Builder
	b.WriteString(m.header("OAuth accounts — " + string(m.oauthProvider)))
	if len(m.accounts) == 0 {
		b.WriteString(subtleStyle.Render("No accounts. Press 'l' to log in via browser.") + "\n")
	}
	for i, a := range m.accounts {
		cursor := "  "
		name := a.selector
		if name == "" {
			name = "(default)"
		}
		if i == m.oauthCursor {
			cursor = cursorStyle.Render("> ")
			name = selectedStyle.Render(name)
		}
		state := okStyle.Render("active")
		if a.disabled {
			state = badgeOffStyle.Render("disabled")
		} else if a.expiring {
			state = warnStyle.Render("needs refresh")
		}
		email := ""
		if a.email != "" && a.email != a.selector {
			email = subtleStyle.Render("  " + a.email)
		}
		b.WriteString(fmt.Sprintf("%s%s  [%s]%s\n", cursor, name, state, email))
	}
	b.WriteString("\n" + helpStyle.Render("l login  L login(device)  x enable/disable  D logout  ↑/↓ move  esc back"))
	return b.String()
}

func (m model) viewConfirmDelete() string {
	var b strings.Builder
	b.WriteString(m.header("Confirm delete"))
	if m.confirmDel != nil {
		b.WriteString(fmt.Sprintf("Remove model %q from config (and Factory settings)?\n\n", m.confirmDel.Alias))
	}
	b.WriteString(helpStyle.Render("y confirm  n cancel"))
	return b.String()
}

func (m model) viewMessage() string {
	var b strings.Builder
	b.WriteString(m.header(""))
	if m.messageErr {
		b.WriteString(errStyle.Render(m.message) + "\n\n")
	} else {
		b.WriteString(m.message + "\n\n")
	}
	if m.pendingSync != nil {
		b.WriteString(helpStyle.Render("y sync to Factory  n skip"))
	} else {
		b.WriteString(helpStyle.Render("enter continue"))
	}
	return b.String()
}

func windowAround(cursor, total, size int) (int, int) {
	if total <= size {
		return 0, total
	}
	start := cursor - size/2
	if start < 0 {
		start = 0
	}
	end := start + size
	if end > total {
		end = total
		start = end - size
	}
	return start, end
}
