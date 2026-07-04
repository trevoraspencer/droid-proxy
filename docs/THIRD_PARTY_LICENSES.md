# Third-party licenses

`droid-proxy` is distributed under the [MIT License](../LICENSE). The table
below lists **direct** Go module dependencies from `go.mod` and their SPDX
license identifiers, verified for public release.

| Module | SPDX license |
|--------|----------------|
| `github.com/charmbracelet/bubbles` | MIT |
| `github.com/charmbracelet/bubbletea` | MIT |
| `github.com/charmbracelet/lipgloss` | MIT |
| `github.com/fsnotify/fsnotify` | BSD-3-Clause |
| `github.com/gin-gonic/gin` | MIT |
| `github.com/sirupsen/logrus` | MIT |
| `github.com/tidwall/gjson` | MIT |
| `github.com/tidwall/sjson` | MIT |
| `github.com/tiktoken-go/tokenizer` | MIT |
| `gopkg.in/yaml.v3` | MIT |

Indirect dependencies are resolved at build time via the Go module graph. Before
each public release, run:

```bash
make legal-audit
```

If you add a new **direct** dependency, update this table and
`internal/security/testdata/direct_dependency_licenses.json`.

## Copyleft policy

Direct dependencies must use permissive licenses (MIT, Apache-2.0, BSD, ISC,
MPL-2.0). Copyleft licenses (GPL, AGPL, LGPL) are not permitted for direct
dependencies in this project.
