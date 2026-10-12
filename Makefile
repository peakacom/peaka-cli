BIN      := bin/peaka
PKG      := github.com/peakacom/peaka-cli
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS  := -s -w -X $(PKG)/internal/build.Version=$(VERSION) -X $(PKG)/internal/build.Commit=$(COMMIT)

.PHONY: build test lint sync-spec run release-check release-snapshot

build:
	go build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/peaka

test:
	go test ./...

lint:
	go vet ./...

# Refresh the OpenAPI snapshot from the test gateway; the diff shows what the API gained.
sync-spec:
	curl -fsS https://api-test.peaka.host/v3/api-docs | python3 -m json.tool --indent 2 > api/openapi.json

run: build
	./$(BIN) $(ARGS)

# Release checks: the goreleaser config, and a local snapshot build of every platform into dist/ (nothing published).
release-check:
	goreleaser check

release-snapshot:
	goreleaser release --snapshot --clean --skip=publish
