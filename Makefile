#!/usr/bin/make -f

BINARY   := konstellationd
MAIN_PKG := ./cmd/konstellationd
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   := $(shell git log -1 --format='%H' 2>/dev/null || echo unknown)
BUILDDIR ?= $(CURDIR)/build
BINDIR   ?= $(GOPATH)/bin
TMVERSION := $(shell go list -m github.com/cometbft/cometbft | sed 's:.* ::')

export CGO_ENABLED = 1

build_tags := netgo $(BUILD_TAGS)
comma := ,
empty :=
space := $(empty) $(empty)
build_tags_comma_sep := $(subst $(space),$(comma),$(strip $(build_tags)))

ldflags := -X github.com/cosmos/cosmos-sdk/version.Name=konstellation \
           -X github.com/cosmos/cosmos-sdk/version.AppName=$(BINARY) \
           -X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
           -X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT) \
           -X "github.com/cosmos/cosmos-sdk/version.BuildTags=$(build_tags_comma_sep)" \
           -X github.com/cometbft/cometbft/version.TMCoreSemVer=$(TMVERSION)
ifeq (,$(findstring nostrip,$(COSMOS_BUILD_OPTIONS)))
  ldflags += -w -s
endif
ldflags += $(LDFLAGS)

# -trimpath is required for reproducible builds (ENGINEERING.md §2.6)
BUILD_FLAGS := -tags "$(strip $(build_tags))" -ldflags '$(strip $(ldflags))' -trimpath
ifneq (,$(findstring nooptimization,$(COSMOS_BUILD_OPTIONS)))
  BUILD_FLAGS += -gcflags "all=-N -l"
endif

.PHONY: all build build-linux install clean test test-unit lint vulncheck verify-deps localnet

all: build

$(BUILDDIR)/:
	mkdir -p $(BUILDDIR)/

build: go.sum $(BUILDDIR)/
	go build $(BUILD_FLAGS) -o $(BUILDDIR)/$(BINARY) $(MAIN_PKG)
	@shasum -a 256 $(BUILDDIR)/$(BINARY)

build-linux:
	GOOS=linux GOARCH=amd64 $(MAKE) build

install: go.sum
	go install $(BUILD_FLAGS) $(MAIN_PKG)

clean:
	rm -rf $(BUILDDIR)/

test: test-unit

test-unit:
	go test -mod=readonly -timeout 15m ./...

lint:
	golangci-lint run ./...

# ENGINEERING.md §4.3
vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# ENGINEERING.md §2.2 / §2.3: no local replaces, no branch pins, cosmos/evm resolves to the module cache.
verify-deps:
	@echo "replace directives:"; grep -n "=>" go.mod | grep -v "//" || true
	@! grep -E "=>.*(Konstellation-Network|\.\./|\./)" go.mod || (echo "ERROR: local replace directive found (ENGINEERING.md §2.2)"; exit 1)
	@go list -m -f '{{.Path}} {{.Version}} {{.Dir}}' github.com/cosmos/evm | grep -q "pkg/mod" || (echo "ERROR: cosmos/evm does not resolve to the module cache"; exit 1)
	@go list -m all | grep -E "cosmos/evm|cosmos-sdk |cometbft/cometbft |ibc-go" | grep -vE "v[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?( |$$)" && (echo "ERROR: dependency pinned to a non-tag version (ENGINEERING.md §2.3)"; exit 1) || true
	@echo "deps OK"

localnet: build
	./local_node.sh -y
