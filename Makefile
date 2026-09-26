# AES — agents_environment_setup
#
# `make` targets are thin wrappers over the two scripts that matter. The build
# matrix lives in scripts/build.sh so CI and a laptop produce byte-identical
# artifacts; keeping a second copy here would be a second thing to forget.

BIN      := dist/aes
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
GOFLAGS  := -trimpath

.PHONY: all build test race vet fmt fmt-check check bootstrap-test dist clean install help

all: check build

## build: compile the host binary
build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/aes

## test: run the Go test suite
test:
	go test ./...

## race: run the Go test suite under the race detector
race:
	go test -race ./...

## vet: run go vet
vet:
	go vet ./...

## fmt: rewrite sources with gofmt
fmt:
	gofmt -w .

## fmt-check: fail if anything is unformatted
fmt-check:
	@unformatted=$$(gofmt -l . | grep -v '^\.beads/' || true); \
	if [ -n "$$unformatted" ]; then \
		echo "unformatted files:"; echo "$$unformatted"; exit 1; \
	fi

## check: everything CI runs
check: fmt-check vet race

## bootstrap-test: exercise install.sh against a local fixture, no network
bootstrap-test:
	sh scripts/install_test.sh

## dist: cross-compile the full release matrix plus checksums.txt
dist:
	AES_VERSION=$(VERSION) sh scripts/build.sh

## install: install the host binary into $$AES_PREFIX/bin
install: build
	mkdir -p "$${AES_PREFIX:-$$HOME/.aes}/bin"
	install -m 0755 $(BIN) "$${AES_PREFIX:-$$HOME/.aes}/bin/aes"

## clean: remove build artifacts
clean:
	rm -rf dist

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
