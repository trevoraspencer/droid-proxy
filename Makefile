.PHONY: build test test-race vet fmt clean run lint

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
