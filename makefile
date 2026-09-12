# recipes use bash for pipefail support (ubuntu's default sh is dash)
SHELL := /bin/bash

GIT_COMMIT=$(shell git describe --always --long --dirty)
# the fallback has to guard git describe itself, not the pipeline: sed exits 0
# on empty input, so "describe | sed || echo dev" yields an empty string in a
# clone with no tags, which then overrides the "dev" default in lib/version
GIT_DESCRIBE=$(shell git describe --tags --dirty 2>/dev/null || echo dev)
GIT_VERSION=$(shell printf '%s' '$(GIT_DESCRIBE)' | sed 's/-\([0-9]*\)-g/+\1@g/')
TEST_TIMEOUT?=15m

# dev tool binaries are built into .tools/bin (gitignored) from the versions pinned in
# .tools/go.mod (and .tools/actionlint/go.mod) - the single source of truth for make and CI;
# dependabot keeps them updated.
# overridable because binaries cannot execute from a noexec mount (e.g. an smb checkout):
# make TOOLS_BIN=~/.cache/abs-mcp/bin lint
TOOLS_BIN?=.tools/bin
ACTIONLINT=$(TOOLS_BIN)/actionlint
GOFUMPT=$(TOOLS_BIN)/gofumpt
GOLANGCI_LINT=$(TOOLS_BIN)/golangci-lint

# non-Go tools also live in .tools/bin at pinned versions, but the pins are here (dependabot
# cannot bump them): shellcheck is a static haskell binary downloaded from its github release,
# yamllint is python installed into a repo-local venv. both rebuild when this makefile changes.
SHELLCHECK_VERSION=v0.11.0
YAMLLINT_VERSION=1.38.0
SHELLCHECK=$(TOOLS_BIN)/shellcheck
YAMLLINT=$(TOOLS_BIN)/yamllint

# golangci-lint with the azproviderlint module plugin compiled in (.tools/.custom-gcl.yml);
# lint runs use this binary, the plain go.mod one exists to bootstrap `golangci-lint custom`
GOLANGCI_LINT_MODULES=$(TOOLS_BIN)/golangci-with-modules

# one rule builds any Go tool: the import path comes from the tool directives in .tools/go.mod
# (via go list tool), so the makefile never repeats it - add a tool there and a variable above
$(TOOLS_BIN)/%: .tools/go.mod .tools/go.sum
	@echo "==> building $* (version pinned in .tools/go.mod)..."
	@mkdir -p $(TOOLS_BIN)
	@cd .tools && go build -o $(abspath $@) $$(go list tool | grep "/$*$$")

# actionlint lives in its own module (.tools/actionlint/go.mod): it pins an older go.yaml.in/yaml/v4
# release candidate than golangci-lint's dependencies and does not compile against the newer one
$(ACTIONLINT): .tools/actionlint/go.mod .tools/actionlint/go.sum
	@echo "==> building actionlint (version pinned in .tools/actionlint/go.mod)..."
	@mkdir -p $(TOOLS_BIN)
	@cd .tools/actionlint && go build -o $(abspath $@) $$(go list tool)

# explicit rules take precedence over the pattern rule above for the non-Go tools and actionlint.
# `golangci-lint custom` always writes to .tools/bin (destination in .custom-gcl.yml), so move
# the result when TOOLS_BIN points elsewhere (e.g. a local dir because the checkout is noexec)
$(GOLANGCI_LINT_MODULES): .tools/.custom-gcl.yml $(GOLANGCI_LINT)
	@echo "==> building golangci-lint with plugins (versions pinned in .tools/.custom-gcl.yml)..."
	@cd .tools && mkdir -p bin && $(abspath $(GOLANGCI_LINT)) custom
	@if [ "$(abspath $(TOOLS_BIN))" != "$(abspath .tools/bin)" ]; then mv .tools/bin/golangci-with-modules $@; fi

$(SHELLCHECK): makefile
	@echo "==> downloading shellcheck $(SHELLCHECK_VERSION)..."
	@mkdir -p $(TOOLS_BIN)
	@os=$$(uname | tr 'A-Z' 'a-z'); arch=$$(uname -m); [ "$$arch" = "arm64" ] && arch=aarch64; \
		curl -sSfL "https://github.com/koalaman/shellcheck/releases/download/$(SHELLCHECK_VERSION)/shellcheck-$(SHELLCHECK_VERSION).$$os.$$arch.tar.xz" \
		| tar -xJ -O shellcheck-$(SHELLCHECK_VERSION)/shellcheck > $@ && chmod +x $@

$(YAMLLINT): makefile
	@command -v python3 >/dev/null || (echo "python3 is required to install yamllint (macOS: xcode CLT; Debian/Ubuntu: apt install python3-venv)" && exit 1)
	@echo "==> installing yamllint $(YAMLLINT_VERSION) into $(TOOLS_BIN)/../venv..."
	@mkdir -p $(TOOLS_BIN)
	@python3 -m venv $(TOOLS_BIN)/../venv && $(TOOLS_BIN)/../venv/bin/pip install -q yamllint==$(YAMLLINT_VERSION) && ln -sf ../venv/bin/yamllint $@

default: fmt build

all: fmt build

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m\n"} /^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-24s\033[0m%s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Build
build: ## Compile abs-mcp with version info from git
	@echo "==> building..."
	go build -ldflags "-X github.com/katbyte/abs-mcp/lib/version.GitCommit=${GIT_COMMIT} -X github.com/katbyte/abs-mcp/lib/version.Version=${GIT_VERSION}"

install: ## Install abs-mcp into GOPATH/bin with version info from git
	@echo "==> installing..."
	go install -ldflags "-X github.com/katbyte/abs-mcp/lib/version.GitCommit=${GIT_COMMIT} -X github.com/katbyte/abs-mcp/lib/version.Version=${GIT_VERSION}" .

docker: ## Build the abs-mcp container image with version info from git
	@echo "==> building docker image..."
	docker build --build-arg VERSION=${GIT_VERSION} --build-arg COMMIT=${GIT_COMMIT} -t abs-mcp .

tools: $(ACTIONLINT) $(GOFUMPT) $(GOLANGCI_LINT) $(GOLANGCI_LINT_MODULES) $(SHELLCHECK) $(YAMLLINT) ## Install all pinned dev tools into .tools/bin

##@ Formatting
fmt: $(GOFUMPT) $(GOLANGCI_LINT) ## Fix Go formatting (gofmt, gofumpt, goimports)
	@echo "==> Fixing source code with gofmt..."
	find . -name '*.go' | grep -v vendor | xargs gofmt -s -w
	@echo "==> Fixing source code with gofumpt..."
	find . -name '*.go' | grep -v vendor | xargs $(GOFUMPT) -w
	@echo "==> Fixing imports with golangci-lint (goimports)..."
	$(GOLANGCI_LINT) fmt -E goimports ./...

goimports: $(GOLANGCI_LINT) ## Fix imports with golangci-lint (goimports)
	@echo "==> Fixing imports with golangci-lint (goimports)..."
	$(GOLANGCI_LINT) fmt -E goimports ./...

##@ Linting & Dependencies
lint: $(GOLANGCI_LINT_MODULES) ## Check source code with the golangci linters (incl. azproviderlint)
	@echo "==> Checking source code against linters..."
	$(GOLANGCI_LINT_MODULES) run ./...

actionlint: $(ACTIONLINT) $(SHELLCHECK) ## Check GitHub workflows with actionlint (incl. shellcheck on run blocks)
	@echo "==> Checking workflows with actionlint..."
	@$(ACTIONLINT) -shellcheck=$(SHELLCHECK)

lint-fix: $(GOLANGCI_LINT_MODULES) ## Fix source code with all golangci linters
	@echo "==> Checking source code against linters (applying autofixes)..."
	$(GOLANGCI_LINT_MODULES) run --fix ./...

yamllint: $(YAMLLINT) ## Check YAML files with yamllint (config in .yamllint.yml)
	@echo "==> Checking YAML files with yamllint..."
	@$(YAMLLINT) -s .

shellcheck: $(SHELLCHECK) ## Check shell scripts with shellcheck
	@echo "==> Checking shell scripts with shellcheck..."
	@$(SHELLCHECK) scripts/*.sh

depscheck: ## Check that go.mod/go.sum and vendor/ are in sync
	@echo "==> Checking source code with go mod tidy..."
	@go mod tidy
	@git diff --exit-code -- go.mod go.sum || \
		(echo; echo "Unexpected difference in go.mod/go.sum files. Run 'go mod tidy' command or revert any go.mod/go.sum changes and commit."; exit 1)
	@echo "==> Checking source code with go mod vendor..."
	@go mod vendor
	@git diff --compact-summary --exit-code -- vendor || \
		(echo; echo "Unexpected difference in vendor/ directory. Run 'go mod vendor' command or revert any go.mod/go.sum/vendor changes and commit."; exit 1)
	@echo "==> Checking .tools/go.mod with go mod tidy..."
	@cd .tools && go mod tidy
	@git diff --exit-code -- .tools/go.mod .tools/go.sum || \
		(echo; echo "Unexpected difference in .tools/go.mod/go.sum. Run 'cd .tools && go mod tidy' and commit."; exit 1)
	@echo "==> Checking .tools/actionlint/go.mod with go mod tidy..."
	@cd .tools/actionlint && go mod tidy
	@git diff --exit-code -- .tools/actionlint/go.mod .tools/actionlint/go.sum || \
		(echo; echo "Unexpected difference in .tools/actionlint/go.mod/go.sum. Run 'cd .tools/actionlint && go mod tidy' and commit."; exit 1)
	@echo "==> Checking .tools/.custom-gcl.yml golangci-lint version matches .tools/go.mod..."
	@modv=$$(cd .tools && go list -m -f '{{.Version}}' github.com/golangci/golangci-lint/v2); \
		gclv=$$(grep '^version:' .tools/.custom-gcl.yml | awk '{print $$2}'); \
		[ "$$modv" = "$$gclv" ] || \
		(echo; echo "golangci-lint version mismatch: .tools/go.mod has $$modv but .tools/.custom-gcl.yml has $$gclv - update .custom-gcl.yml to match."; exit 1)

##@ Testing
test: build ## Run tests
	go test ./... -timeout ${TEST_TIMEOUT}

test-integration: ## Run the SDK tests (lib/abs shapes) against an already-running server
	@[ -n "${ABS_SERVER}" ] && [ -n "${ABS_TOKEN}" ] || \
		(echo 'ABS_SERVER and ABS_TOKEN must be set; or use "make testacc"'; exit 1)
	go test -tags integration -count=1 ./integration/... -timeout ${TEST_TIMEOUT} -v

test-acceptance: ## Run the tool tests (behaviour, audits, providers) against an already-running server
	@[ -n "${ABS_SERVER}" ] && [ -n "${ABS_TOKEN}" ] || \
		(echo 'ABS_SERVER and ABS_TOKEN must be set; or use "make testacc"'; exit 1)
	go test -tags integration -count=1 ./acceptance/... -timeout ${TEST_TIMEOUT} -v

# each suite gets its own container: the SDK tests create and delete libraries
# of their own, which would trample the tool suite's fixtures
testacc-integration: ## SDK tests in a throwaway container
	@echo "==> integration (SDK) on port 13379..."
	@set -e; \
		ABS_TEST_CONTAINER=abs-mcp-sdk ABS_TEST_PORT=13379 ABS_TEST_DATA=$${HOME}/.cache/abs-mcp/sdk \
			scripts/abs-testenv.sh up | grep '^export' > .testenv-sdk.sh; \
		trap 'st=$$?; [ $$st -eq 0 ] || docker logs abs-mcp-sdk 2>&1 | tail -60; \
			ABS_TEST_CONTAINER=abs-mcp-sdk ABS_TEST_DATA=$${HOME}/.cache/abs-mcp/sdk scripts/abs-testenv.sh down; \
			rm -f .testenv-sdk.sh; exit $$st' EXIT; \
		. ./.testenv-sdk.sh; \
		go test -tags integration -count=1 ./integration/... -timeout ${TEST_TIMEOUT} -v

testacc-acceptance: ## Tool tests in a throwaway container
	@echo "==> acceptance (tools) on port 13378..."
	@set -e; \
		scripts/abs-testenv.sh up | grep '^export' > .testenv.sh; \
		trap 'st=$$?; [ $$st -eq 0 ] || docker logs abs-mcp-test 2>&1 | tail -60; \
			scripts/abs-testenv.sh down; rm -f .testenv.sh; exit $$st' EXIT; \
		. ./.testenv.sh; \
		go test -tags integration -count=1 ./acceptance/... -timeout ${TEST_TIMEOUT} -v

testacc: testacc-integration testacc-acceptance ## Run both live suites, each in its own container

# Coverage has to span all three suites or it lies: the unit tests alone report
# around 40% for tools/, because almost everything real happens in the live
# suites behind the integration tag. Each writes binary coverage into its own
# directory and covdata merges them, which is stdlib tooling rather than a
# third-party merger.
COVERDIR?=.coverage
COVERPKG=./tools/...,./lib/abs/...,./cli/...

cover: ## Run every suite with coverage and report the total
	@rm -rf $(COVERDIR)
	@mkdir -p $(COVERDIR)/unit $(COVERDIR)/integration $(COVERDIR)/acceptance
	@echo "==> unit..."
	@go test -count=1 -coverpkg=$(COVERPKG) ./tools/ ./cli/ ./lib/... \
		-args -test.gocoverdir=$(CURDIR)/$(COVERDIR)/unit >/dev/null
	@echo "==> integration (SDK) with coverage..."
	@set -e; \
		ABS_TEST_CONTAINER=abs-mcp-sdk ABS_TEST_PORT=13379 ABS_TEST_DATA=$${HOME}/.cache/abs-mcp/sdk \
			scripts/abs-testenv.sh up | grep '^export' > .testenv-sdk.sh; \
		trap 'st=$$?; ABS_TEST_CONTAINER=abs-mcp-sdk ABS_TEST_DATA=$${HOME}/.cache/abs-mcp/sdk \
			scripts/abs-testenv.sh down; rm -f .testenv-sdk.sh; exit $$st' EXIT; \
		. ./.testenv-sdk.sh; \
		go test -tags integration -count=1 -coverpkg=$(COVERPKG) ./integration/... \
			-timeout $(TEST_TIMEOUT) -args -test.gocoverdir=$(CURDIR)/$(COVERDIR)/integration
	@echo "==> acceptance (tools) with coverage..."
	@set -e; \
		scripts/abs-testenv.sh up | grep '^export' > .testenv.sh; \
		trap 'st=$$?; scripts/abs-testenv.sh down; rm -f .testenv.sh; exit $$st' EXIT; \
		. ./.testenv.sh; \
		go test -tags integration -count=1 -coverpkg=$(COVERPKG) ./acceptance/... \
			-timeout $(TEST_TIMEOUT) -args -test.gocoverdir=$(CURDIR)/$(COVERDIR)/acceptance
	@go tool covdata textfmt \
		-i=$(COVERDIR)/unit,$(COVERDIR)/integration,$(COVERDIR)/acceptance \
		-o=$(COVERDIR)/coverage.out
	@echo
	@go tool cover -func=$(COVERDIR)/coverage.out | tail -1
	@echo "==> per package"
	@go tool covdata percent -i=$(COVERDIR)/unit,$(COVERDIR)/integration,$(COVERDIR)/acceptance

cover-html: cover ## Run every suite with coverage and open the HTML report
	@go tool cover -html=$(COVERDIR)/coverage.out

record-sdk: ## Re-record the SDK suite's provider cassettes against the real providers
	@echo "==> recording the SDK cassettes (this hits the network)..."
	@set -e; \
		ABS_TEST_CONTAINER=abs-mcp-sdk ABS_TEST_PORT=13379 ABS_TEST_DATA=$${HOME}/.cache/abs-mcp/sdk \
			scripts/abs-testenv.sh up | grep '^export' > .testenv-sdk.sh; \
		trap 'ABS_TEST_CONTAINER=abs-mcp-sdk ABS_TEST_DATA=$${HOME}/.cache/abs-mcp/sdk scripts/abs-testenv.sh down; rm -f .testenv-sdk.sh' EXIT; \
		. ./.testenv-sdk.sh; \
		ABS_TEST_RECORD=1 go test -tags integration -count=1 ./integration/... -timeout ${TEST_TIMEOUT} -v

record: ## Re-record the provider cassettes against the real Audible/Audnexus/iTunes
	@echo "==> recording against the real providers (this hits the network)..."
	@set -e; \
		scripts/abs-testenv.sh up | grep '^export' > .testenv.sh; \
		trap 'scripts/abs-testenv.sh down; rm -f .testenv.sh' EXIT; \
		. ./.testenv.sh; \
		ABS_TEST_RECORD=1 go test -tags integration -count=1 ./acceptance/... -timeout ${TEST_TIMEOUT} -v

record-check: ## Check the provider cassettes still match the real APIs, without rewriting them
	@echo "==> verifying cassettes against the real providers (this hits the network)..."
	@set -e; \
		scripts/abs-testenv.sh up | grep '^export' > .testenv.sh; \
		trap 'scripts/abs-testenv.sh down; rm -f .testenv.sh' EXIT; \
		. ./.testenv.sh; \
		ABS_TEST_VERIFY=1 go test -tags integration -count=1 ./acceptance/... -timeout ${TEST_TIMEOUT}

testenv-up: ## Start and seed a throwaway Audiobookshelf container
	@scripts/abs-testenv.sh up

testenv-down: ## Remove the throwaway Audiobookshelf container
	@scripts/abs-testenv.sh down

apicheck: ## Report how much of the Audiobookshelf API lib/abs covers
	@python3 scripts/apicheck.py --list

check-all: build test testacc lint actionlint yamllint shellcheck depscheck apicheck ## Run build + tests (incl. integration) + all linters + depscheck

.PHONY: default all help fmt goimports build docker lint lint-fix actionlint yamllint shellcheck depscheck check-all install tools test test-integration testacc cover cover-html record testenv-up testenv-down apicheck
