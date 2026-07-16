// Package providerapi queries configured providers for their available model
// IDs so the interactive config UI can offer a pick-list instead of requiring
// the user to paste a model slug. Callers fall back to free-text entry when
// discovery is unsupported or fails.
package providerapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
)

const maxModelsResponseBytes int64 = 4 << 20
const defaultModelsPath = "models"

// ListOptions controls model discovery against a provider profile.
type ListOptions struct {
	BaseURL      string
	ModelsPath   string
	APIKey       string
	AuthHeader   string
	AuthScheme   string
	ExtraHeaders map[string]string
	// IDField is the JSON field name for model IDs in the discovery response.
	// Empty means "id" (the OpenAI-compatible default). Set to "model_name"
	// for providers like DeepInfra whose catalog uses a different field.
	IDField string
	// TypeField is the JSON field name for type-based filtering. Empty means
	// no type filtering.
	TypeField string
	// TypeValue is the required exact value for TypeField. Only records whose
	// field matches exactly are retained.
	TypeValue string
}

// ListModels performs GET {baseURL}/models and returns the discovered model
// IDs (sorted, de-duplicated). authHeader/authScheme default to
// "Authorization"/"Bearer"; pass an empty apiKey for no-auth upstreams.
func ListModels(ctx context.Context, baseURL, apiKey, authHeader, authScheme string) ([]string, error) {
	return ListModelsWithOptions(ctx, ListOptions{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		AuthHeader: authHeader,
		AuthScheme: authScheme,
	})
}

// ListModelsWithOptions performs provider model discovery and returns model IDs
// sorted and de-duplicated. ModelsPath is appended to BaseURL and defaults to
// "models"; ExtraHeaders carries profile-required headers such as API versions.
func ListModelsWithOptions(ctx context.Context, opts ListOptions) ([]string, error) {
	endpoint, err := modelsEndpoint(opts.BaseURL, opts.ModelsPath)
	if err != nil {
		return nil, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range opts.ExtraHeaders {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		req.Header.Set(k, v)
	}
	if strings.TrimSpace(opts.APIKey) != "" {
		header := strings.TrimSpace(opts.AuthHeader)
		if header == "" {
			header = "Authorization"
		}
		scheme := strings.TrimSpace(opts.AuthScheme)
		if strings.EqualFold(header, "Authorization") && scheme == "" {
			scheme = "Bearer"
		}
		value := opts.APIKey
		if scheme != "" {
			value = scheme + " " + opts.APIKey
		}
		req.Header.Set(header, value)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider returned %s", resp.Status)
	}
	body, err := readLimitedModelsBody(resp.Body)
	if err != nil {
		return nil, err
	}
	ids, err := parseModelIDs(body, opts)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no models returned by provider")
	}
	return ids, nil
}

func modelsEndpoint(baseURL, modelsPath string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", fmt.Errorf("provider base_url is empty")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	p := strings.TrimSpace(modelsPath)
	if p == "" {
		p = defaultModelsPath
	}
	parts := []string{u.Path}
	for _, part := range strings.Split(p, "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	u.Path = path.Join(parts...)
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u.String(), nil
}

func readLimitedModelsBody(r io.Reader) ([]byte, error) {
	lr := &io.LimitedReader{R: r, N: maxModelsResponseBytes + 1}
	body, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("read provider models response: %w", err)
	}
	if int64(len(body)) > maxModelsResponseBytes {
		return nil, fmt.Errorf("provider models response too large")
	}
	return body, nil
}

// parseModelIDs handles the common shapes: {"data":[{"id":...}]} (OpenAI),
// {"models":[{"id"|"name":...}]}, and a bare array of objects or strings.
// When opts specifies IDField, TypeField, and TypeValue, it uses those to
// extract IDs and apply exact type filtering (e.g. DeepInfra's bare-array
// model_name/reported_type contract).
func parseModelIDs(body []byte, opts ListOptions) ([]string, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, fmt.Errorf("empty response")
	}

	idField := strings.TrimSpace(opts.IDField)
	typeField := strings.TrimSpace(opts.TypeField)
	typeValue := strings.TrimSpace(opts.TypeValue)

	// Validate configured discovery fields against known supported values.
	// Unsupported fields fail explicitly rather than silently filtering every
	// record and returning an empty catalog, which would mask a
	// misconfiguration as "no models available".
	if idField != "" && idField != "id" && idField != "name" && idField != "model_name" {
		return nil, fmt.Errorf("unsupported discovery ID field %q: supported fields are id, name, model_name", idField)
	}
	if typeField != "" && typeField != "reported_type" {
		return nil, fmt.Errorf("unsupported discovery type field %q: supported field is reported_type", typeField)
	}

	type modelObj struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		ModelName    string `json:"model_name"`
		ReportedType string `json:"reported_type"`
	}
	pick := func(objs []modelObj) []string {
		var out []string
		for _, o := range objs {
			// Type filtering: when configured, retain only exact matches.
			if typeField != "" && typeValue != "" {
				var actual string
				switch typeField {
				case "reported_type":
					actual = o.ReportedType
				}
				if actual != typeValue {
					continue
				}
			}
			// ID extraction: use the configured field or fall back to id/name.
			var id string
			switch idField {
			case "model_name":
				id = o.ModelName
			case "name":
				id = o.Name
			default:
				id = firstNonEmpty(o.ID, o.Name)
			}
			if id = strings.TrimSpace(id); id != "" {
				out = append(out, id)
			}
		}
		return out
	}

	if strings.HasPrefix(trimmed, "{") {
		var wrapper struct {
			Data   []modelObj `json:"data"`
			Models []modelObj `json:"models"`
		}
		if err := json.Unmarshal(body, &wrapper); err == nil {
			if ids := pick(wrapper.Data); len(ids) > 0 {
				return dedupeSort(ids), nil
			}
			if ids := pick(wrapper.Models); len(ids) > 0 {
				return dedupeSort(ids), nil
			}
		}
		return nil, fmt.Errorf("unrecognized models response shape")
	}

	if strings.HasPrefix(trimmed, "[") {
		var objs []modelObj
		if err := json.Unmarshal(body, &objs); err == nil {
			if ids := pick(objs); len(ids) > 0 {
				return dedupeSort(ids), nil
			}
		}
		var strs []string
		if err := json.Unmarshal(body, &strs); err == nil {
			return dedupeSort(strs), nil
		}
	}
	return nil, fmt.Errorf("unrecognized models response shape")
}

func dedupeSort(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
