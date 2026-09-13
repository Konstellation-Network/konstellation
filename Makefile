#!/usr/bin/make -f
SHELL := /bin/bash

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

# ENGINEERING.md §2.1 / §2.2 / §2.3.
#  - the four upstream modules may never be the LHS of a replace, whatever the RHS
#  - the full set of replaces (block or standalone) must equal the pinned cosmos/evm go.mod's
#  - cosmos/evm must resolve inside GOMODCACHE and every core dep must be a semver tag
# Works on a cold module cache: downloads only cosmos/evm first. Needs jq (as does local_node.sh).
PROTECTED := github.com/cosmos/evm github.com/cosmos/cosmos-sdk github.com/cometbft/cometbft github.com/cosmos/ibc-go
REPLACES_JQ := '[.Replace[]? | "\(.Old.Path) \(.Old.Version // "") => \(.New.Path) \(.New.Version // "")"] | sort | .[]'
verify-deps:
	@command -v jq >/dev/null || { echo "ERROR: jq is required"; exit 1; }
	@for m in $(PROTECTED); do \
	  if go mod edit -json | jq -e --arg m "$$m" '.Replace[]? | select((.Old.Path | ascii_downcase) as $$p | $$p == ($$m|ascii_downcase) or ($$p | startswith(($$m|ascii_downcase)+"/v")))' >/dev/null; then \
	    echo "ERROR: replace directive for $$m (ENGINEERING.md §2.1)"; exit 1; fi; done
	@go mod download github.com/cosmos/evm
	@upstream="$$(go list -m -f '{{.Dir}}' github.com/cosmos/evm)/go.mod"; test -f "$$upstream" || { echo "ERROR: cannot locate upstream cosmos/evm go.mod"; exit 1; }; \
	  diff <(go mod edit -json | jq -r $(REPLACES_JQ)) <(go mod edit -json "$$upstream" | jq -r $(REPLACES_JQ)) \
	  || { echo "ERROR: replace set differs from upstream cosmos/evm go.mod (ENGINEERING.md §2.2)"; exit 1; }
	@dir="$$(go list -m -f '{{.Dir}}' github.com/cosmos/evm)"; case "$$dir" in "$$(go env GOMODCACHE)"/*) ;; *) echo "ERROR: cosmos/evm resolves to $$dir, not the module cache"; exit 1;; esac
	@for m in $(PROTECTED); do go list -m all | grep -E "^$$m(/v[0-9]+)? " ; done \
	  | grep -vE " v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$$" \
	  && { echo "ERROR: core dependency not pinned to a semver tag (ENGINEERING.md §2.3)"; exit 1; } || true
	@echo "deps OK"

localnet: build
	./local_node.sh -y
