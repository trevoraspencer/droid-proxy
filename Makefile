.PHONY: build test test-race vet fmt clean run lint audit-secrets pre-public-audit

BIN := droid-proxy

build:
	go build -o $(BIN) ./cmd/droid-proxy

test:
	go test ./...

test-race:
	go test -race ./...

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

pre-public-audit:
	@bash scripts/pre-public-audit.sh
