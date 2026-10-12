BIN      := bin/peaka
PKG      := github.com/peakacom/peaka-cli
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS  := -s -w -X $(PKG)/internal/build.Version=$(VERSION) -X $(PKG)/internal/build.Commit=$(COMMIT)

.PHONY: build test lint sync-spec run release-check release-snapshot release

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

# Releases are cut from a developer machine, no CI: `make release VERSION=0.2.0` tests, tags v0.2.0, pushes the tag
# and runs goreleaser, which builds every platform, publishes the GitHub Release (archives, deb/rpm, checksums.txt)
# and pushes the Homebrew cask to peakacom/homebrew-tap. Both tokens default to the gh CLI's login; set
# HOMEBREW_TAP_TOKEN= (empty) to publish without touching the tap.
release-check:
	goreleaser check

release-snapshot:
	goreleaser release --snapshot --clean --skip=publish

release:
	@test -n "$(VERSION)" || { echo "usage: make release VERSION=0.2.0"; exit 2; }
	@test -z "$$(git status --porcelain)" || { echo "the working tree is not clean"; exit 2; }
	@test "$$(git rev-parse --abbrev-ref HEAD)" = main || { echo "release from main"; exit 2; }
	go vet ./... && go test ./...
	git tag -a v$(VERSION) -m "peaka $(VERSION)"
	git push origin main v$(VERSION)
	GITHUB_TOKEN="$${GITHUB_TOKEN:-$$(gh auth token)}" HOMEBREW_TAP_TOKEN="$${HOMEBREW_TAP_TOKEN-$$(gh auth token)}" goreleaser release --clean
