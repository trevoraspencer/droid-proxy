// Package configedit performs targeted edits to a droid-proxy YAML config
// file's `models:` list while preserving comments and the rest of the document.
// It is used by the interactive `droid-proxy config` UI to add, update, and
// remove models without round-tripping the entire Config struct (which would
// drop comments and reorder fields).
package configedit

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

var displayEnvRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// Doc is an in-memory, comment-preserving view of a config file.
type Doc struct {
	path string
	mode os.FileMode
	root *yaml.Node
}

// Load reads and parses a YAML config file into an editable document.
func Load(path string) (*Doc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if root.Kind == 0 {
		// Empty file: synthesize an empty mapping document.
		root = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	return &Doc{path: path, mode: mode, root: &root}, nil
}

// Path returns the file path backing this document.
func (d *Doc) Path() string { return d.path }

func (d *Doc) mapping() (*yaml.Node, error) {
	if d.root == nil || len(d.root.Content) == 0 {
		return nil, fmt.Errorf("config: empty document")
	}
	n := d.root.Content[0]
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config: top-level node is not a mapping")
	}
	return n, nil
}

// modelsSeq returns the `models` sequence node, creating it if missing.
func (d *Doc) modelsSeq(create bool) (*yaml.Node, error) {
	root, err := d.mapping()
	if err != nil {
		return nil, err
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "models" {
			seq := root.Content[i+1]
			if seq.Kind != yaml.SequenceNode {
				if seq.Kind == yaml.ScalarNode && (seq.Tag == "!!null" || seq.Value == "") {
					seq.Kind = yaml.SequenceNode
					seq.Tag = "!!seq"
					seq.Value = ""
					return seq, nil
				}
				return nil, fmt.Errorf("config: `models` is not a sequence")
			}
			return seq, nil
		}
	}
	if !create {
		return nil, nil
	}
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "models"}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	root.Content = append(root.Content, key, seq)
	return seq, nil
}

// HasModel reports whether a model with the given alias exists.
func (d *Doc) HasModel(alias string) bool {
	seq, _ := d.modelsSeq(false)
	if seq == nil {
		return false
	}
	return indexOfAlias(seq, alias) >= 0
}

// Upsert adds m or replaces an existing model with the same alias. The model is
// structurally validated (hydrated from known_auth, then Validate) before the
// document is mutated. Env-var presence is intentionally NOT checked here so a
// model can be written before its API key is set.
func (d *Doc) Upsert(m *config.Model) error {
	if m == nil {
		return fmt.Errorf("nil model")
	}
	if strings.TrimSpace(m.Alias) == "" {
		return fmt.Errorf("model alias is required")
	}
	if err := validateModel(m); err != nil {
		return err
	}
	node, err := modelToNode(m)
	if err != nil {
		return err
	}
	seq, err := d.modelsSeq(true)
	if err != nil {
		return err
	}
	if idx := indexOfAlias(seq, m.Alias); idx >= 0 {
		seq.Content[idx] = node
		return nil
	}
	seq.Content = append(seq.Content, node)
	return nil
}

// Remove deletes the model with the given alias. Returns whether it existed.
func (d *Doc) Remove(alias string) (bool, error) {
	seq, err := d.modelsSeq(false)
	if err != nil {
		return false, err
	}
	if seq == nil {
		return false, nil
	}
	idx := indexOfAlias(seq, alias)
	if idx < 0 {
		return false, nil
	}
	seq.Content = append(seq.Content[:idx], seq.Content[idx+1:]...)
	return true, nil
}

// Bytes renders the document back to YAML with 2-space indentation.
func (d *Doc) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d.root); err != nil {
		return nil, err
	}
	_ = enc.Close()
	return buf.Bytes(), nil
}

// Save atomically writes the document back to its file, preserving permissions.
func (d *Doc) Save() error {
	out, err := d.Bytes()
	if err != nil {
		return err
	}
	dir := dirOf(d.path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, d.mode); err != nil {
		return err
	}
	return os.Rename(tmpName, d.path)
}

func indexOfAlias(seq *yaml.Node, alias string) int {
	alias = strings.TrimSpace(alias)
	for i, item := range seq.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(item.Content); j += 2 {
			if item.Content[j].Value == "alias" && strings.TrimSpace(item.Content[j+1].Value) == alias {
				return i
			}
		}
	}
	return -1
}

func validateModel(m *config.Model) error {
	clone := *m
	if err := config.HydrateModel(&clone); err != nil {
		return err
	}
	return clone.Validate()
}

func dirOf(path string) string {
	if i := strings.LastIndexAny(path, "/\\"); i >= 0 {
		return path[:i]
	}
	return "."
}

// modelYAML mirrors config.Model with omitempty so node output stays minimal.
type modelYAML struct {
	Alias            string            `yaml:"alias"`
	DisplayName      string            `yaml:"display_name,omitempty"`
	FactoryProvider  string            `yaml:"factory_provider"`
	UpstreamProtocol string            `yaml:"upstream_protocol,omitempty"`
	KnownAuth        string            `yaml:"known_auth,omitempty"`
	BaseURL          string            `yaml:"base_url,omitempty"`
	APIKeyEnv        string            `yaml:"api_key_env,omitempty"`
	OAuthProvider    string            `yaml:"oauth_provider,omitempty"`
	OAuthAccount     string            `yaml:"oauth_account,omitempty"`
	UpstreamModel    string            `yaml:"upstream_model,omitempty"`
	MaxOutputTokens  int               `yaml:"max_output_tokens,omitempty"`
	MaxContextTokens int               `yaml:"max_context_tokens,omitempty"`
	ExtraHeaders     map[string]string `yaml:"extra_headers,omitempty"`
	ExtraArgs        map[string]any    `yaml:"extra_args,omitempty"`
	Capabilities     *capsYAML         `yaml:"capabilities,omitempty"`
}

type capsYAML struct {
	Streaming        *bool  `yaml:"streaming,omitempty"`
	Tools            *bool  `yaml:"tools,omitempty"`
	ToolResultSafe   *bool  `yaml:"tool_result_safe,omitempty"`
	Images           *bool  `yaml:"images,omitempty"`
	JSONMode         *bool  `yaml:"json_mode,omitempty"`
	StructuredOutput *bool  `yaml:"structured_output,omitempty"`
	Reasoning        string `yaml:"reasoning,omitempty"`
	FactoryReasoning string `yaml:"factory_reasoning,omitempty"`
	PromptCaching    *bool  `yaml:"prompt_caching,omitempty"`
}

func modelToNode(m *config.Model) (*yaml.Node, error) {
	my := modelYAML{
		Alias:            m.Alias,
		DisplayName:      m.DisplayName,
		FactoryProvider:  string(m.FactoryProvider),
		UpstreamProtocol: string(m.UpstreamProtocol),
		KnownAuth:        m.KnownAuth,
		BaseURL:          m.BaseURL,
		APIKeyEnv:        m.APIKeyEnv,
		OAuthProvider:    string(m.OAuthProvider),
		OAuthAccount:     m.OAuthAccount,
		UpstreamModel:    m.UpstreamModel,
		MaxOutputTokens:  m.MaxOutputTokens,
		MaxContextTokens: m.MaxContextTokens,
		ExtraHeaders:     m.ExtraHeaders,
		ExtraArgs:        m.ExtraArgs,
	}
	if caps := capsToYAML(m.Capabilities); caps != nil {
		my.Capabilities = caps
	}
	var doc yaml.Node
	b, err := yaml.Marshal(my)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("failed to build model node")
	}
	return doc.Content[0], nil
}

func capsToYAML(c config.Capabilities) *capsYAML {
	out := capsYAML{
		Streaming:        c.Streaming,
		Tools:            c.Tools,
		ToolResultSafe:   c.ToolResultSafe,
		Images:           c.Images,
		JSONMode:         c.JSONMode,
		StructuredOutput: c.StructuredOutput,
		PromptCaching:    c.PromptCaching,
	}
	if c.Reasoning != "" && c.Reasoning != config.ReasoningNone {
		out.Reasoning = string(c.Reasoning)
	}
	if c.FactoryReasoning != "" {
		out.FactoryReasoning = string(c.FactoryReasoning)
	}
	if out == (capsYAML{}) {
		return nil
	}
	return &out
}

// LoadModels parses just the models from a config file for display purposes,
// hydrating each from known_auth but skipping env-var validation so models
// render even when their API key is not yet set. Env references in string
// values are left unexpanded.
func LoadModels(path string) ([]*config.Model, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	expanded := expandEnvForDisplay(string(raw))
	var mf struct {
		Models []*config.Model `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(expanded), &mf); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	for _, m := range mf.Models {
		_ = config.HydrateModel(m)
	}
	return mf.Models, nil
}

func expandEnvForDisplay(s string) string {
	return displayEnvRef.ReplaceAllStringFunc(s, func(match string) string {
		parts := displayEnvRef.FindStringSubmatch(match)
		name := parts[1]
		if val, ok := os.LookupEnv(name); ok {
			return val
		}
		if parts[2] != "" {
			return parts[3]
		}
		return match
	})
}
