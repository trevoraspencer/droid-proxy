// Package upstream contains the components that connect droid-proxy to remote model providers:
// alias resolution, the shared HTTP client, auth wiring, and header filtering.
package upstream

import (
	"fmt"
	"strings"

	"droid-proxy/internal/config"
)

// Router resolves a client-supplied model alias to a configured Model entry.
type Router struct {
	models    map[string]*config.Model
	ordered   []*config.Model
	caseFold  bool
	aliasKeys []string
}

// NewRouter builds a router over the given models. Duplicate aliases produce an error.
func NewRouter(models []*config.Model) (*Router, error) {
	r := &Router{
		models:   make(map[string]*config.Model, len(models)),
		ordered:  make([]*config.Model, 0, len(models)),
		caseFold: true,
	}
	for _, m := range models {
		if m == nil {
			continue
		}
		key := r.key(m.Alias)
		if _, exists := r.models[key]; exists {
			return nil, fmt.Errorf("duplicate model alias %q", m.Alias)
		}
		r.models[key] = m
		r.ordered = append(r.ordered, m)
		r.aliasKeys = append(r.aliasKeys, m.Alias)
	}
	return r, nil
}

func (r *Router) key(alias string) string {
	if r.caseFold {
		return strings.ToLower(strings.TrimSpace(alias))
	}
	return strings.TrimSpace(alias)
}

// Resolve looks up a model by alias.
func (r *Router) Resolve(alias string) (*config.Model, error) {
	m, ok := r.models[r.key(alias)]
	if !ok {
		return nil, &NotFoundError{Alias: alias, Known: r.aliasKeys}
	}
	return m, nil
}

// List returns models in configured order.
func (r *Router) List() []*config.Model {
	out := make([]*config.Model, len(r.ordered))
	copy(out, r.ordered)
	return out
}

// NotFoundError indicates an unknown alias was requested.
type NotFoundError struct {
	Alias string
	Known []string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("model %q not configured (known: %s)", e.Alias, strings.Join(e.Known, ", "))
}
