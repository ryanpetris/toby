# Toby build, generation, test, and analysis targets.
export CGO_ENABLED := 0

GO       ?= go
PROTOC   ?= protoc
GOFMT    ?= gofmt
BINDIR   ?= bin

GO_TOOL_BINDIR ?= $(shell d="$$($(GO) env GOBIN)"; if [ -n "$$d" ]; then printf '%s' "$$d"; else printf '%s/bin' "$$($(GO) env GOPATH)"; fi)
PROTOC_GEN_GO ?= $(GO_TOOL_BINDIR)/protoc-gen-go
PROTOC_GEN_GO_GRPC ?= $(GO_TOOL_BINDIR)/protoc-gen-go-grpc
STATICCHECK ?= $(GO_TOOL_BINDIR)/staticcheck
DEADCODE ?= $(GO_TOOL_BINDIR)/deadcode
GORELEASER ?= $(GO_TOOL_BINDIR)/goreleaser
DEADCODE_TEST_ONLY_PACKAGES := petris.dev/toby/internal/tools/fake

PROTOC_GEN_GO_VERSION ?= v1.36.11
PROTOC_GEN_GO_GRPC_VERSION ?= v1.6.2
PROTOC_GEN_GO_GRPC_VERSION_NUMBER := $(patsubst v%,%,$(PROTOC_GEN_GO_GRPC_VERSION))
GORELEASER_VERSION ?= v2.16.0
PROTO_SOURCES := \
	proto/toby/agent/v1/agent.proto \
	proto/toby/sandbox/v1/sandbox.proto
GENERATED_ROOT := internal/gen

TOBY_VERSION ?= $(shell v="$$(git describe --tags --dirty --always --match 'v*' 2>/dev/null || printf dev)"; printf '%s' "$$v")
GO_LDFLAGS ?= -X petris.dev/toby/internal/version.Current=$(TOBY_VERSION)

.PHONY: all build gen gen/grpc
.PHONY: test vet fmt coverage
.PHONY: go/install go/test go/vet go/fmt go/coverage
.PHONY: check check/all check/fmt check/staticcheck check/deadcode
.PHONY: release/check release/snapshot
.PHONY: dev/tools dev/tools/protoc dev/tools/protoc-gen-go
.PHONY: dev/tools/protoc-gen-go-grpc dev/tools/staticcheck dev/tools/deadcode
.PHONY: dev/tools/goreleaser
.PHONY: clean clean/bin clean/generated clean/coverage

all: build

build: gen/grpc
	$(GO) build ./...
	mkdir -p $(BINDIR)
	$(GO) build -trimpath -ldflags "$(GO_LDFLAGS)" -o $(BINDIR)/toby ./cmd/toby
	$(GO) build -trimpath -ldflags "$(GO_LDFLAGS)" -o $(BINDIR)/tobyd ./cmd/tobyd
	$(GO) build -trimpath -ldflags "$(GO_LDFLAGS)" -o $(BINDIR)/tobys ./cmd/tobys

go/install: gen/grpc
	$(GO) install -trimpath -ldflags "$(GO_LDFLAGS)" \
		./cmd/toby ./cmd/tobyd ./cmd/tobys

gen: gen/grpc

gen/grpc: dev/tools/protoc dev/tools/protoc-gen-go dev/tools/protoc-gen-go-grpc
	PATH="$(GO_TOOL_BINDIR):$$PATH" $(PROTOC) \
		--go_out=. \
		--go_opt=module=petris.dev/toby \
		--go-grpc_out=. \
		--go-grpc_opt=module=petris.dev/toby \
		$(PROTO_SOURCES)

test: go/test

vet: go/vet

fmt: go/fmt

coverage: go/coverage

go/test: gen/grpc
	$(GO) test ./...

go/vet: gen/grpc
	$(GO) vet ./...

go/fmt:
	find . -name '*.go' -type f -print0 | xargs -0 -r $(GOFMT) -w

go/coverage: gen/grpc
	mkdir -p dist
	$(GO) test -covermode=atomic -coverprofile=dist/coverage.out ./...
	$(GO) tool cover -func=dist/coverage.out > dist/coverage.txt
	$(GO) tool cover -html=dist/coverage.out -o dist/coverage.html

check: go/vet go/test

check/all: check/fmt build go/vet go/test check/staticcheck check/deadcode

check/fmt:
	files="$$(find . -name '*.go' -type f -print0 | xargs -0 -r $(GOFMT) -l)"; \
	if [ -n "$$files" ]; then \
		printf '%s\n' "$$files"; \
		exit 1; \
	fi

check/staticcheck: gen/grpc dev/tools/staticcheck
	$(STATICCHECK) ./...

check/deadcode: gen/grpc dev/tools/deadcode
	production_output="$$($(DEADCODE) ./cmd/... 2>&1)"; \
	production_status="$$?"; \
	test_output="$$($(DEADCODE) -test ./... 2>&1)"; \
	test_status="$$?"; \
	production_dependencies="$$($(GO) list -deps ./cmd/...)"; \
	unreachable_packages=""; \
	for package in $$($(GO) list ./internal/...); do \
		case " $(DEADCODE_TEST_ONLY_PACKAGES) " in \
			*" $$package "*) continue ;; \
		esac; \
		if ! printf '%s\n' "$$production_dependencies" \
			| grep -Fqx "$$package"; then \
			unreachable_packages="$${unreachable_packages}$${unreachable_packages:+\n}unreachable package: $$package"; \
		fi; \
	done; \
	if [ -n "$$production_output" ]; then \
		printf '%s\n' "$$production_output"; \
	fi; \
	if [ -n "$$test_output" ]; then printf '%s\n' "$$test_output"; fi; \
	if [ -n "$$unreachable_packages" ]; then \
		printf '%b\n' "$$unreachable_packages"; \
	fi; \
	if [ "$$production_status" -ne 0 ] || \
		[ "$$test_status" -ne 0 ] || \
		[ -n "$$production_output" ] || \
		[ -n "$$test_output" ] || \
		[ -n "$$unreachable_packages" ]; then \
		exit 1; \
	fi

release/check: dev/tools/goreleaser
	$(GORELEASER) check

release/snapshot: dev/tools/goreleaser
	$(GORELEASER) release --clean --snapshot

dev/tools: dev/tools/protoc dev/tools/protoc-gen-go dev/tools/protoc-gen-go-grpc dev/tools/staticcheck dev/tools/deadcode dev/tools/goreleaser

dev/tools/protoc:
	@command -v $(PROTOC) >/dev/null 2>&1 || { \
		printf '%s\n' 'protoc is required; install the Protocol Buffers compiler package for this system.' >&2; \
		exit 1; \
	}

dev/tools/protoc-gen-go:
	@if [ "$$("$(PROTOC_GEN_GO)" --version 2>/dev/null)" != \
		"protoc-gen-go $(PROTOC_GEN_GO_VERSION)" ]; then \
		GOBIN="$(GO_TOOL_BINDIR)" $(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION); \
	fi

dev/tools/protoc-gen-go-grpc:
	@if [ "$$("$(PROTOC_GEN_GO_GRPC)" --version 2>/dev/null)" != \
		"protoc-gen-go-grpc $(PROTOC_GEN_GO_GRPC_VERSION_NUMBER)" ]; then \
		GOBIN="$(GO_TOOL_BINDIR)" $(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION); \
	fi

dev/tools/staticcheck:
	@if [ ! -x "$(STATICCHECK)" ]; then \
		GOBIN="$(GO_TOOL_BINDIR)" $(GO) install honnef.co/go/tools/cmd/staticcheck@latest; \
	fi

dev/tools/deadcode:
	@if [ ! -x "$(DEADCODE)" ]; then \
		GOBIN="$(GO_TOOL_BINDIR)" $(GO) install golang.org/x/tools/cmd/deadcode@latest; \
	fi

dev/tools/goreleaser:
	@if ! "$(GORELEASER)" --version 2>/dev/null \
		| grep -Eq '^GitVersion:[[:space:]]+$(GORELEASER_VERSION)$$'; then \
		GOBIN="$(GO_TOOL_BINDIR)" $(GO) install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION); \
	fi

clean: clean/bin clean/generated clean/coverage

clean/bin:
	rm -f $(BINDIR)/toby $(BINDIR)/tobyd $(BINDIR)/tobys
	-rmdir $(BINDIR)

clean/generated:
	@if [ -d "$(GENERATED_ROOT)" ]; then \
		find "$(GENERATED_ROOT)" -type f \
			\( -name '*.pb.go' -o -name '*_grpc.pb.go' \) -delete; \
		rmdir -p "$(GENERATED_ROOT)/toby/agent/v1" 2>/dev/null || true; \
		rmdir -p "$(GENERATED_ROOT)/toby/sandbox/v1" 2>/dev/null || true; \
	fi

clean/coverage:
	rm -f dist/coverage.out dist/coverage.txt dist/coverage.html
	-rmdir dist
