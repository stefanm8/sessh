GO ?= go
VERSION ?= dev

.PHONY: build install check dist

build:
	mkdir -p bin
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/sessh ./cmd/sessh

install:
	$(GO) install -trimpath -ldflags "-X main.version=$(VERSION)" ./cmd/sessh

check:
	@test -z "$$(gofmt -l cmd format plugin plugins session)" || { gofmt -l cmd format plugin plugins session; exit 1; }
	sh -n install.sh
	sh -n scripts/setup-browser.sh
	sh -n scripts/dist.sh
	$(GO) mod tidy -diff
	$(GO) vet ./...
	$(GO) test -race ./...

dist:
	sh scripts/dist.sh "$(VERSION)"
