## Fix all four minor review findings from `d039bb3`

### Finding #1 — Correct stale backup doc comment (`internal/factory/settings.go`)
The `Save` doc says the backup goes to `settings.json.bak-<timestamp>`, but the code (and `TestSaveBackupIsSingleRollingFile`) writes a single rolling `settings.json.bak`. Update only the comment to match the implemented/tested behavior, and note that a second save overwrites the prior backup. No code change.

### Finding #2 — Preserve comments/unknown lines in the managed env file (`internal/secrets/secrets.go`)
Replace the map-rebuild `writeAll` with line-oriented edits so user comments, blank lines, ordering, and unrecognized lines survive `Set`/`Delete`.

- Add `writeRaw(content string) error` extracted from current `writeAll` (atomic temp file in `stateDirFn()`, `chmod 0600`, rename to `Path()`).
- Rewrite `Set` to read raw lines, replace the first line whose `daemon.ParseEnvLine` key matches (dropping any later duplicate of that key), else append `export KEY=%q`. When the file is empty/new, prepend the existing managed header. Preserve every other line verbatim.
- Rewrite `Delete` to drop only lines whose parsed key matches, preserving the rest; no-op when the file/key is absent.
- Remove the now-unused `sort` import; keep `Read`/`readFile`/`Has` unchanged (still `%q` + `daemon.ParseEnvValue` round-trip).

Sketch:
```go
func (s) Set(key, value string) error {
    // read raw -> split lines -> replace first matching key (drop dup keys)
    // else append export KEY=%q (prepend managed header if file was empty)
    // writeRaw(joined + "\n")
}
```
Existing secrets tests assert via `Read()`, so they keep passing. Add tests: `TestSetPreservesCommentsAndOtherKeys` (a hand-written file with a `# comment` and unrelated key keeps both after `Set`) and `TestDeletePreservesOtherLines`.

### Finding #3 — Sync the real client_auth key to Factory when enabled (`internal/tui/backend.go`)
Today `syncFactory` always passes `"x"`, which fails when the proxy enforces `client_auth`. Thread the configured key through.

- In `newBackend`, load config once and derive a key: add field `factoryKey string`.
```go
func newBackend(configPath string) *backend {
    cfg := loadConfigBestEffort(configPath)
    return &backend{configPath: configPath, factoryPath: factory.DefaultSettingsPath(),
        baseURL: proxyBaseURL(configPath), manager: oauth.NewManager(cfg),
        factoryKey: factoryAPIKey(cfg)}
}
func factoryAPIKey(cfg *config.Config) string {
    if cfg != nil && cfg.ClientAuth.Enabled {
        for _, k := range cfg.ClientAuth.APIKeys {
            if strings.TrimSpace(k) != "" { return k }
        }
    }
    return "x"
}
```
- `syncFactory` uses `factory.EntryFromModel(m, b.baseURL, b.factoryKey)` instead of `"x"` (single chokepoint for the `s`/`S`/post-add sync paths). `config.Load` already env-expands `client_auth.api_keys`, so the synced value is the literal key.
- Add `TestFactoryAPIKey` to `tui_test.go` covering: client auth disabled -> `"x"`, enabled with a key -> that key, enabled but blank/no keys -> `"x"`.

### Finding #4 — Close the remaining test gaps
- `internal/providerapi/list_test.go`: add `TestParseModelIDsBareObjectArray` (`[{"id":"b"},{"id":"a"}]` -> sorted `["a","b"]`) and `TestParseModelIDsUnrecognizedShape` (e.g. `{"foo":1}` and `42` -> error).
- `internal/configedit/configedit_test.go`: add `TestUpsertOAuthModel` — upsert a model with `OAuthProvider=xai`, `FactoryProvider=openai`, `UpstreamProtocol=xai-responses` (no `known_auth`/`base_url`), `Save`, then `LoadModels` and assert it validates, persists, and round-trips `oauth_provider`.

### Verification
Run `go build ./...`, `go vet ./...`, and `go test ./internal/secrets/... ./internal/configedit/... ./internal/factory/... ./internal/providerapi/... ./internal/tui/... ./internal/config/...`; fix any fallout before finishing.
