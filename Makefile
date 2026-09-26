GO ?= go
VERSION ?= dev

.PHONY: build install check e2e dist

build:
	mkdir -p bin
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/sessh ./cmd/sessh

install:
	GO="$(GO)" SESSH_BUILD_VERSION="$(VERSION)" sh install.sh --source

check:
	@test -z "$$(gofmt -l cmd e2e format internal plugin plugins session)" || { gofmt -l cmd e2e format internal plugin plugins session; exit 1; }
	sh -n install.sh
	sh -n scripts/setup-browser.sh
	sh -n scripts/dist.sh
	$(GO) mod tidy -diff
	$(GO) vet ./...
	$(GO) vet -tags e2e ./e2e/
	$(GO) test -race ./...

# Opens real browser windows against a local fake tenant; needs the browser
# setup and a display. Not part of check.
e2e:
	$(GO) test -tags e2e -count=1 -timeout 20m ./e2e/

dist:
	sh scripts/dist.sh "$(VERSION)"
