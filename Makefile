GO ?= go
VERSION ?= dev

.PHONY: build install fmt fmt-check tidy-check vet test check dist

build:
	mkdir -p bin
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/sessh ./cmd/sessh

install:
	$(GO) install -trimpath -ldflags "-X main.version=$(VERSION)" ./cmd/sessh

fmt:
	gofmt -w cmd format plugin plugins session

fmt-check:
	@test -z "$$(gofmt -l cmd format plugin plugins session)" || { gofmt -l cmd format plugin plugins session; exit 1; }

tidy-check:
	$(GO) mod tidy -diff

vet:
	$(GO) vet ./...

test:
	$(GO) test -race ./...

check: fmt-check tidy-check vet test

dist:
	sh scripts/dist.sh "$(VERSION)"
