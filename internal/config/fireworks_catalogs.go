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

// CatalogSource records the official URL and retrieval date for a curated
// catalog. Deterministic validation compares catalogs and docs against these
// source records; no live documentation or provider request is made.
type CatalogSource struct {
	URL   string
	AsOf  string
	Label string
}

// fireworksFirePassSource is the official source for the Fire Pass catalog.
var fireworksFirePassSource = CatalogSource{
	URL:   "https://docs.fireworks.ai/firepass",
	AsOf:  "2026-07-15",
	Label: "Fireworks Fire Pass documentation",
}

// fireworksFastSource is the official source for the Standard-key Fast router
// catalog. Fast and Priority serving paths are documented together.
var fireworksFastSource = CatalogSource{
	URL:   "https://docs.fireworks.ai/serverless/priority-and-fast",
	AsOf:  "2026-07-15",
	Label: "Fireworks Priority and Fast documentation",
}

// FireworksFirePassCatalogSource returns the official source record for the
// Fire Pass router catalog.
func FireworksFirePassCatalogSource() CatalogSource {
	return fireworksFirePassSource
}

// FireworksFastCatalogSource returns the official source record for the
// Standard-key Fast router catalog.
func FireworksFastCatalogSource() CatalogSource {
	return fireworksFastSource
}

// FireworksFirePassCatalog returns the current Fire Pass-eligible router IDs.
// These are reviewed against the official Fire Pass documentation and the
// canonical router accounts/fireworks/routers/glm-5p2-fast must be present.
// Availability and pricing are mutable and experimental.
func FireworksFirePassCatalog() []CatalogEntry {
	return []CatalogEntry{
		{ID: "accounts/fireworks/routers/glm-5p2-fast", Label: "GLM-5.2 Fast (Fire Pass)"},
	}
}

// fireworksFirePassCatalog returns the current Fire Pass-eligible router IDs.
// (Unexported alias used by the registry during hydration.)
func fireworksFirePassCatalog() []CatalogEntry {
	return FireworksFirePassCatalog()
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

// FireworksSnapshotSupportedFastPriority reports the exact router/tier
// combinations the committed official snapshot marks as supported. The proxy
// never infers or synthesizes a combination; it preserves these explicitly
// configured values unchanged.
func FireworksSnapshotSupportedFastPriority() []string {
	return []string{
		"accounts/fireworks/routers/glm-5p2-fast",
	}
}
