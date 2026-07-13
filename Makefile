.PHONY: build install-user test test-race test-installer vet fmt clean run lint audit-secrets security-audit legal-audit docs-audit ci-audit release-audit release-dry-run bench bench-compare

BIN := droid-proxy
VERSION ?= 0.0.0-dev
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
VERSION_PKG := github.com/trevoraspencer/droid-proxy/internal/version
VERSION_LDFLAGS := -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT)
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
CONFIG_DIR ?= $(HOME)/Library/Application Support/droid-proxy
else
XDG_CONFIG_HOME ?= $(HOME)/.config
CONFIG_DIR ?= $(XDG_CONFIG_HOME)/droid-proxy
endif

build:
	go build -ldflags "$(VERSION_LDFLAGS)" -o $(BIN) ./cmd/droid-proxy

install-user: build
	mkdir -p "$(BINDIR)"
	install -m 0755 "$(BIN)" "$(BINDIR)/droid-proxy"
	"$(BINDIR)/droid-proxy" setup --config "$(CONFIG_DIR)/config.yaml"
	@echo "installed binary: $(BINDIR)/droid-proxy"
	@echo "user config: $(CONFIG_DIR)/config.yaml"

test:
	go test ./...

# Micro-benchmarks for the per-request hot paths (translation, SSE pump,
# payload overrides). See docs/BENCHMARKS.md.
bench:
	go test -bench=. -benchmem -run='^$$' ./internal/translate/ ./internal/stream/ ./internal/handlers/

# End-to-end proxy-overhead comparison against the mock upstream, plus
# prompt-cache fidelity checks. Writes reports to bench-results/.
bench-compare:
	bash scripts/bench/local-compare.sh

test-race:
	go test -race ./...

test-installer:
	bash scripts/install_test.sh

vet:
	go vet ./...

fmt:
	gofmt -w .

lint: vet
	@gofmt -l . | (! grep .) || (echo "gofmt found unformatted files"; exit 1)

clean:
	rm -f $(BIN) coverage.out

run: build
	./$(BIN) --config config.example.yaml

audit-secrets:
	@command -v gitleaks >/dev/null 2>&1 || (echo "install gitleaks: https://github.com/gitleaks/gitleaks#installing"; exit 1)
	gitleaks detect --source . --config .gitleaks.toml --verbose --no-banner

security-audit:
	@bash scripts/security-audit.sh

legal-audit:
	@bash scripts/legal-audit.sh

docs-audit:
	@bash scripts/docs-audit.sh

ci-audit:
	@bash scripts/ci-audit.sh

release-audit:
	@bash scripts/release-audit.sh

release-dry-run:
	bash scripts/release-assets.sh --dry-run
