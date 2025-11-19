# Simple developer workflow for goa-ai

GO ?= go
HTTP_PORT ?= 8888

GOPATH ?= $(shell go env GOPATH)
GOLANGCI_LINT := $(shell command -v golangci-lint 2>/dev/null)
PROTOC := $(shell command -v protoc 2>/dev/null)
PROTOC_GEN_GO := protoc-gen-go
PROTOC_GEN_GO_GRPC := protoc-gen-go-grpc

.PHONY: all build lint test itest ci tools ensure-golangci ensure-protoc-plugins protoc-check run-example example-gen

all: build lint test

build: tools
	$(GO) build ./...

lint: tools
	golangci-lint run --timeout=5m

test: tools
	$(GO) test -race -covermode=atomic -coverprofile=cover.out `$(GO) list ./... | grep -v '/integration_tests'`

# Run integration tests (scenarios under integration_tests/)
itest: tools
	$(GO) test -race -vet=off ./integration_tests/...

ci: build lint test

tools: ensure-golangci ensure-protoc-plugins protoc-check

ensure-golangci:
	@if [ -z "$(GOLANGCI_LINT)" ]; then \
		echo "Installing golangci-lint v2..."; \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(GOPATH)/bin v2.0.0; \
	else \
		echo "golangci-lint found: $(GOLANGCI_LINT)"; \
	fi

ensure-protoc-plugins:
	@if ! command -v $(PROTOC_GEN_GO) >/dev/null 2>&1; then \
		echo "Installing protoc-gen-go (latest)..."; \
		$(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@latest; \
	else \
		echo "protoc-gen-go found at: $$(command -v $(PROTOC_GEN_GO))"; \
	fi
	@if ! command -v $(PROTOC_GEN_GO_GRPC) >/dev/null 2>&1; then \
		echo "Installing protoc-gen-go-grpc (latest)..."; \
		$(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest; \
	else \
		echo "protoc-gen-go-grpc found at: $$(command -v $(PROTOC_GEN_GO_GRPC))"; \
	fi

protoc-check:
	@if [ -z "$(PROTOC)" ]; then \
		echo "Error: protoc is not installed or not in PATH."; \
		echo "Install via your package manager (e.g., 'brew install protobuf' or 'apt-get install protobuf-compiler')."; \
		exit 1; \
	fi

run-example:
	cd example/complete && $(GO) run ./cmd/orchestrator --http-port $(HTTP_PORT)

gen-example:
	cd example/complete && goa gen example.com/assistant/design
