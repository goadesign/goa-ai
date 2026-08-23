# Simple developer workflow for goa-ai

GO ?= go
HTTP_PORT ?= 8888

PROTOC := $(shell command -v protoc 2>/dev/null)
PROTOC_GEN_GO := protoc-gen-go
PROTOC_GEN_GO_GRPC := protoc-gen-go-grpc
PROTOC_VERSION := $(shell awk '$$1 == "protoc" { print $$2; exit }' .tool-versions)
PROTOC_GEN_GO_TARGET := $(shell grep '^google.golang.org/protobuf/cmd/protoc-gen-go@' .go-install)
PROTOC_GEN_GO_GRPC_TARGET := $(shell grep '^google.golang.org/grpc/cmd/protoc-gen-go-grpc@' .go-install)
PROTOC_GEN_GO_VERSION := $(word 2,$(subst @, ,$(PROTOC_GEN_GO_TARGET)))
PROTOC_GEN_GO_GRPC_VERSION := $(word 2,$(subst @, ,$(PROTOC_GEN_GO_GRPC_TARGET)))

.PHONY: all setup build lint test itest ci tools ensure-golangci ensure-protoc-plugins protoc-check run-example example-gen

all: build lint test

setup:
	./scripts/setup

build: tools
	$(GO) build ./...

lint: tools
	$(GO) tool golangci-lint run --timeout=5m

test: tools
	$(GO) test -race -covermode=atomic -coverprofile=cover.out `$(GO) list ./... | grep -v '/integration_tests'`

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
	@$(GO) tool golangci-lint version >/dev/null

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
	@version="$$(protoc --version | awk '{ print $$2 }')"; \
	if [ "$$version" != "$(PROTOC_VERSION)" ]; then \
		echo "Error: protoc $(PROTOC_VERSION) is required, but $$version is in PATH."; \
		echo "Run 'make setup' to install the required version."; \
		exit 1; \
	fi

run-example:
	cd example/complete && $(GO) run ./cmd/orchestrator --http-port $(HTTP_PORT)

gen-example:
	cd example/complete && goa gen example.com/assistant/design

gen-registry:
	goa gen goa.design/goa-ai/registry/design -o registry
