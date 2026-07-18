package config

// DeepInfra official-source record.
//
// This file is the committed dated record of official DeepInfra documentation
// URLs and behavioral facts used for deterministic local fixtures and audits.
// No live documentation or provider request is made during validation.
//
// Official sources reviewed 2026-07-15:
//   - https://deepinfra.com/docs (inference base URL and OpenAI compatibility)
//   - https://deepinfra.com/models/list (public catalog discovery endpoint)
//   - https://deepinfra.com/docs/openai/chat (Chat Completions and tier guidance)
//
// Known official-page contradictions:
//   - As of 2026-07-15, the Chat overview documents service_tier "flex"
//     alongside "priority" and "default", while the generated OpenAPI tier
//     enum omits "flex". This mission treats the discrepancy as a reason for
//     generic pass-through rather than a restrictive local enum. The proxy
//     does not impose a local tier enum and forwards configured service_tier
//     values unchanged; the effective tier is determined and echoed by
//     DeepInfra.

// DeepInfraBehaviorRecord captures the behavioral facts pinned to official
// DeepInfra sources as of the retrieval date. Deterministic validation
// compares registry tuples, fake fixtures, provider docs, and examples
// against these fields.
type DeepInfraBehaviorRecord struct {
	// InferenceBaseURL is the OpenAI Chat Completions inference base.
	InferenceBaseURL string
	// DiscoveryMethod is the HTTP method for public catalog discovery.
	DiscoveryMethod string
	// DiscoveryURL is the full unauthenticated catalog discovery URL.
	DiscoveryURL string
	// DiscoveryAuth describes the discovery authentication policy.
	DiscoveryAuth string
	// DiscoveryResponseShape describes the expected JSON shape.
	DiscoveryResponseShape string
	// DiscoveryIDField is the JSON field name for model IDs.
	DiscoveryIDField string
	// DiscoveryTypeField is the JSON field name for type filtering.
	DiscoveryTypeField string
	// DiscoveryTypeValue is the required value for the type field.
	DiscoveryTypeValue string
	// CredentialEnv is the environment variable convention for the token.
	CredentialEnv string
	// AuthScheme describes the inference authentication scheme.
	AuthScheme string
	// RequestTierStandard describes the Standard request behavior.
	RequestTierStandard string
	// RequestTierPriority is the exact Priority request value.
	RequestTierPriority string
	// RequestTierFlex is the exact Flex request value.
	RequestTierFlex string
	// EffectiveTierPriority is the response literal for successful Priority.
	EffectiveTierPriority string
	// EffectiveTierFlex is the response literal for successful Flex.
	EffectiveTierFlex string
	// EffectiveTierDefault is the response literal for fallback (effective Standard).
	EffectiveTierDefault string
	// FlexEnumContraction records the known official-page contradiction.
	FlexEnumContraction string
	// PassthroughDecision records how the contradiction is resolved.
	PassthroughDecision string
	// ReasoningFields lists documented reasoning-related pass-through fields.
	ReasoningFields []string
	// CacheFields lists documented cache-related pass-through fields.
	CacheFields []string
	// OpaqueIDNote describes how model IDs are treated.
	OpaqueIDNote string
	// ModelDependentNote describes mutable model-dependent capabilities.
	ModelDependentNote string
	// MockValidationNote qualifies the validation approach.
	MockValidationNote string
}

// deepInfraSourceRecord is the committed record pinned to official sources
// reviewed 2026-07-15.
var deepInfraSourceRecord = DeepInfraBehaviorRecord{
	InferenceBaseURL:       "https://api.deepinfra.com/v1/openai",
	DiscoveryMethod:        "GET",
	DiscoveryURL:           "https://api.deepinfra.com/models/list",
	DiscoveryAuth:          "unauthenticated (no Authorization or credential header)",
	DiscoveryResponseShape: "bare top-level JSON array",
	DiscoveryIDField:       "model_name",
	DiscoveryTypeField:     "reported_type",
	DiscoveryTypeValue:     "text-generation",
	CredentialEnv:          "DEEPINFRA_TOKEN",
	AuthScheme:             "Bearer",
	RequestTierStandard:    "omitted (Standard requests do not set service_tier)",
	RequestTierPriority:    "priority",
	RequestTierFlex:        "flex",
	EffectiveTierPriority:  "priority",
	EffectiveTierFlex:      "flex",
	EffectiveTierDefault:   "default (effective Standard fallback when Priority is unsupported)",
	FlexEnumContraction:    "As of 2026-07-15, the Chat overview documents service_tier \"flex\" while the generated OpenAPI tier enum omits it.",
	PassthroughDecision:    "The proxy resolves this in favor of generic pass-through rather than a restrictive local enum; configured service_tier values are forwarded unchanged and the effective tier is echoed by DeepInfra.",
	ReasoningFields: []string{
		"reasoning_effort",
		"reasoning",
		"reasoning_content",
		"chat_template_kwargs",
	},
	CacheFields: []string{
		"prompt_cache_key",
		"cache_control",
	},
	OpaqueIDNote:       "Model IDs, version suffixes, and deploy_id values are opaque and preserved byte-for-byte without normalization.",
	ModelDependentNote: "Tier eligibility, reasoning capabilities, cache support, context windows, and output limits are model-dependent and mutable.",
	MockValidationNote: "This mission validates generic OpenAI Chat transport through local fakes, not live credentialed calls.",
}

// DeepInfraSourceRecord returns the committed dated official-source record
// for DeepInfra behavioral facts. Registry tests, official-shape fakes,
// provider docs, and examples agree with this record.
func DeepInfraSourceRecord() DeepInfraBehaviorRecord {
	return deepInfraSourceRecord
}

// DeepInfraSourceURLs returns the official documentation URLs and the
// retrieval date used for the source record.
func DeepInfraSourceURLs() (urls []string, asOf string) {
	return []string{
		"https://deepinfra.com/docs",
		"https://deepinfra.com/models/list",
		"https://deepinfra.com/docs/openai/chat",
	}, "2026-07-15"
}
