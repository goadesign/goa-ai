# Simple developer workflow for goa-ai

GO ?= go
HTTP_PORT ?= 8888

PROTOC := $(shell command -v protoc 2>/dev/null)
GOLANGCI_LINT ?= golangci-lint
GOLANGCI_LINT_TARGET := $(shell grep '^github.com/golangci/golangci-lint/v2/cmd/golangci-lint@' .go-install)
GOLANGCI_LINT_VERSION := $(patsubst v%,%,$(word 2,$(subst @, ,$(GOLANGCI_LINT_TARGET))))

PROTOC_GEN_GO := protoc-gen-go
PROTOC_GEN_GO_GRPC := protoc-gen-go-grpc
PROTOC_VERSION := $(shell awk '$$1 == "protoc" { print $$2; exit }' .tool-versions)
PROTOC_GEN_GO_TARGET := $(shell grep '^google.golang.org/protobuf/cmd/protoc-gen-go@' .go-install)
PROTOC_GEN_GO_GRPC_TARGET := $(shell grep '^google.golang.org/grpc/cmd/protoc-gen-go-grpc@' .go-install)
PROTOC_GEN_GO_VERSION := $(word 2,$(subst @, ,$(PROTOC_GEN_GO_TARGET)))
PROTOC_GEN_GO_GRPC_VERSION := $(word 2,$(subst @, ,$(PROTOC_GEN_GO_GRPC_TARGET)))

.PHONY: all setup build lint test itest ci tools ensure-golangci ensure-protoc-plugins protoc-check run-example gen-example

all: build lint test

setup:
	./scripts/setup

build: tools
	$(GO) build ./...

lint: tools
	$(GOLANGCI_LINT) run --timeout=5m

# Generator tests compile other repository packages in separate modules. Go's
# test cache cannot track those subprocess inputs, so acceptance runs uncached.
test: tools
	$(GO) test -count=1 -race -covermode=atomic -coverprofile=cover.out `$(GO) list ./... | grep -v '/integration_tests'`
	cd quickstart && $(GO) test -count=1 ./...

# Run integration tests: end-to-end scenarios under integration_tests/ and
# Docker-backed tests guarded by the `integration` build tag (registry health
# tracking against real Redis). `make test` excludes both so the default suite
# stays fast and deterministic.
itest: tools
	$(GO) test -race -vet=off -parallel 1 ./integration_tests/...
	$(GO) test -race -tags integration ./registry/...

ci: build lint test

tools: ensure-golangci ensure-protoc-plugins protoc-check

ensure-golangci:
	@version="$$( $(GOLANGCI_LINT) version 2>/dev/null | awk '{ print $$4 }' || true)"; \
	if [ "$$version" != "$(GOLANGCI_LINT_VERSION)" ]; then \
		echo "Error: golangci-lint $(GOLANGCI_LINT_VERSION) is required, but $${version:-none} is in PATH."; \
		echo "Run 'make setup' and ensure GOPATH/bin is in PATH."; \
		exit 1; \
	fi

ensure-protoc-plugins:
	@installed="$$(command -v $(PROTOC_GEN_GO) 2>/dev/null || true)"; \
	version="$$( $(PROTOC_GEN_GO) --version 2>/dev/null | awk '{ print $$2 }' || true)"; \
	if [ "$$version" != "$(PROTOC_GEN_GO_VERSION)" ]; then \
		echo "Error: protoc-gen-go $(PROTOC_GEN_GO_VERSION) is required, but $${version:-none} is in PATH."; \
		echo "Run 'make setup' and ensure GOPATH/bin is in PATH."; \
		exit 1; \
	fi; \
	echo "protoc-gen-go $(PROTOC_GEN_GO_VERSION) found at: $$installed"
	@installed="$$(command -v $(PROTOC_GEN_GO_GRPC) 2>/dev/null || true)"; \
	version="$$( $(PROTOC_GEN_GO_GRPC) --version 2>/dev/null | awk '{ print "v" $$2 }' || true)"; \
	if [ "$$version" != "$(PROTOC_GEN_GO_GRPC_VERSION)" ]; then \
		echo "Error: protoc-gen-go-grpc $(PROTOC_GEN_GO_GRPC_VERSION) is required, but $${version:-none} is in PATH."; \
		echo "Run 'make setup' and ensure GOPATH/bin is in PATH."; \
		exit 1; \
	fi; \
	echo "protoc-gen-go-grpc $(PROTOC_GEN_GO_GRPC_VERSION) found at: $$installed"

protoc-check:
	@if [ -z "$(PROTOC)" ]; then \
		echo "Error: protoc is not installed or not in PATH."; \
		echo "Run 'make setup' to install protoc $(PROTOC_VERSION)."; \
		exit 1; \
	fi
	@version="$$( $(PROTOC) --version | awk '{ print $$2 }')"; \
	if [ "$$version" != "$(PROTOC_VERSION)" ]; then \
		echo "Error: protoc $(PROTOC_VERSION) is required, but $$version is in PATH."; \
		echo "Run 'make setup' to install the required version."; \
		exit 1; \
	fi

run-example:
	cd quickstart && $(GO) run ./cmd/orchestrator --http-port $(HTTP_PORT)

gen-example:
	cd quickstart && $(GO) run goa.design/goa/v3/cmd/goa gen example.com/quickstart/design

gen-registry:
	$(GO) run goa.design/goa/v3/cmd/goa gen goa.design/goa-ai/registry/design -o registry
