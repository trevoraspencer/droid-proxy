# Project Scope

`droid-proxy` is a local bridge between Factory Droid and model providers that a user configures. The project is intentionally focused: it runs on the user's machine, keeps credentials local, and maps Droid's supported provider modes to upstream APIs with clear compatibility limits.

## Goals

- Let Factory Droid use BYOK providers, local models, OpenAI-compatible endpoints, and supported OAuth accounts.
- Keep the runtime simple: one Go binary, local config, local credentials, and no hosted service.
- Prefer explicit provider profiles and examples over broad automatic provider support.
- Mark model capability honestly through provider tiers and the `agent_ready` field.
- Keep install, upgrade, service setup, and diagnostics usable from release binaries without requiring a source checkout.

## Non-Goals

- No hosted, shared, multi-user, or reseller service.
- No first-class support for clients other than Factory Droid.
- No web dashboard; the supported UI is the terminal-based setup flow.
- No public-internet deployment guide beyond documenting the security responsibilities of binding outside localhost.
- No generic plugin system or speculative provider abstraction beyond the existing curated profiles.

## Compatibility Priorities

- Preserve documented config fields, CLI commands, runtime paths, and service behavior within the `v0.x` compatibility expectations.
- Treat security defaults as part of the public interface: localhost bind, redaction on, restrictive credential file permissions, and optional downstream client authentication.
- Update docs and examples in the same change as behavior that affects install, config, provider routing, OAuth, or Factory settings.

## Release Standard

A release should be installable from GitHub assets, pass the local and CI gates, include current docs, and avoid committing real credentials, personal paths, local runtime files, or private operational notes.
