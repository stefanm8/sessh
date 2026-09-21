GO ?= go
VERSION ?= dev

.PHONY: build install fmt fmt-check tidy-check vet test check dist

build:
	mkdir -p bin
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/sessh ./cmd/sessh

install:
	$(GO) install -trimpath -ldflags "-X main.version=$(VERSION)" ./cmd/sessh

fmt:
	gofmt -w cmd format internal plugin plugins session

fmt-check:
	@test -z "$$(gofmt -l cmd format internal plugin plugins session)" || { gofmt -l cmd format internal plugin plugins session; exit 1; }
	sh -n install.sh
	sh -n scripts/setup-browser.sh
	sh -n scripts/dist.sh

tidy-check:
	$(GO) mod tidy -diff

vet:
	$(GO) vet ./...

test:
	$(GO) test -race -count=1 ./...

check: fmt-check tidy-check vet test

dist:
	sh scripts/dist.sh "$(VERSION)"
