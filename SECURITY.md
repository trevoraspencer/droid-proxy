# Security Policy

## Supported versions

| Version | Supported |
|---------|-----------|
| `main`  | Yes       |

Tagged releases on `main` receive security fixes when practical. Older
untagged snapshots are not supported.

## Reporting a vulnerability

**Please do not open a public GitHub issue for security vulnerabilities.**

Report privately using [GitHub Security Advisories](https://github.com/trevoraspencer/droid-proxy/security/advisories/new).

Include:

- Affected version or commit
- Steps to reproduce
- Impact assessment (what an attacker could access or change)
- Any suggested fix, if you have one

We aim to acknowledge reports within a few business days. We will coordinate
disclosure timing with you before publishing a fix or advisory.

## Scope

In scope:

- Secret leakage in logs, errors, or HTTP responses
- Local auth/token storage permissions and path handling
- OAuth token handling and refresh flows
- Request forwarding bugs that expose upstream credentials to clients

Out of scope:

- Misconfiguration of provider API keys in your local environment files
- Attacks that require physical access to your machine
- Vulnerabilities in upstream model providers (report those to the provider)

## Safe defaults

`droid-proxy` is designed for **localhost** use. Do not expose it to the
public internet without additional access controls you understand and maintain.
