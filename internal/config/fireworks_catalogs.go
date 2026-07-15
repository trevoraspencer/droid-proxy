package config

// Fireworks static catalog data. Source-pinned per provider-sources.md.
//
// Official sources (reviewed 2026-07-15):
//   - https://docs.fireworks.ai/firepass
//   - https://docs.fireworks.ai/serverless/priority-and-fast
//   - https://fireworks.ai/blog/glm-5p2-fast
//
// Fire Pass and normal Fast catalogs are independently sourced and may
// overlap. Each entry is an exact router/model ID. Manual entry is always
// available alongside these curated lists.

// fireworksFirePassCatalog returns the current Fire Pass-eligible router IDs.
// These are reviewed against the official Fire Pass documentation and the
// canonical router accounts/fireworks/routers/glm-5p2-fast must be present.
// Availability and pricing are mutable and experimental.
func fireworksFirePassCatalog() []CatalogEntry {
	return []CatalogEntry{
		{ID: "accounts/fireworks/routers/glm-5p2-fast", Label: "GLM-5.2 Fast (Fire Pass)"},
	}
}

// FireworksFastCatalog returns the current Standard-key Fast router IDs.
// These use the same inference base as Standard Fireworks but with router
// model IDs rather than ordinary model IDs. Baseline Fast onboarding is
// tier-absent (no service_tier).
func FireworksFastCatalog() []CatalogEntry {
	return []CatalogEntry{
		{ID: "accounts/fireworks/routers/glm-5p2-fast", Label: "GLM-5.2 Fast"},
	}
}
