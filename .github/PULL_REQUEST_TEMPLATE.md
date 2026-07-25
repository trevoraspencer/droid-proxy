<!--
Thanks for contributing! Keep PRs small and focused. See CONTRIBUTING.md.
Do not commit secrets — example files must stay placeholder-only.
-->

## What changed

<!-- Describe what changed and why. Link related issues (e.g. Closes #123). -->

## How was it tested

<!-- Commands run, providers exercised, manual checks. -->

```bash
make lint
make test
```

## Checklist

- [ ] `make lint` passes (gofmt + `go vet`)
- [ ] `make test` passes
- [ ] Docs updated for any behavior, flag, config, or provider change (`make docs-audit` if relevant)
- [ ] No secrets, real API keys, OAuth tokens, or personal paths added
