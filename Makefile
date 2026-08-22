.PHONY: deploy deploy-dry-run prometheus-bin migrate migrate-dry-run validate-mibs check-tracked ci ci-fast fmt-check security test-e2e build-backup test-api bench-api coverage-api test-logging bench-logging coverage-logging test-webui-websocket bench-webui-api bench-webui-websocket coverage-webui-api coverage-webui-websocket test-metrics bench-metrics coverage-metrics all build build-cli install-cli test-cli test-client test-mib bench-cli bench-client bench-mib coverage-cli coverage-client coverage-mib test-mib-search bench-mib-search coverage-mib-search test-mib-selector bench-mib-selector coverage-mib-selector test-mib-loader bench-mib-loader coverage-mib-loader build-all build-webui test test-unit test-integration coverage lint clean run run-webui stop stop-webui restart restart-webui redeploy-webui-fresh logs logs-webui import generate-dashboards docker-build docker-build-webui docker-up docker-down docker-test integration-up integration-down integration-logs integration-shell help status network-clean backup backup-verify restore restore-help stack-capture stack-verify test-scripts lint-scripts test-toolchain print-golangci-version test-teardown

# Variables
BINARY_NAME=go-netmon
MIB_IMPORTER=mib-importer
WEBUI=webui
CLI=netmon
BACKUP=netmon-backup
CMD_PATH=./cmd/go-netmon
MIB_IMPORTER_PATH=./cmd/mib-importer
WEBUI_PATH=./cmd/webui
CLI_PATH=./cmd/netmon
BACKUP_PATH=./cmd/netmon-backup
BUILD_DIR=./bin
COVERAGE_DIR=./coverage
DOCKER_COMPOSE=docker-compose.yml

# Version metadata stamped into the CLI
VERSION?=$(shell cat VERSION 2>/dev/null || echo dev)
GIT_COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE?=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOCOVER=$(GOCMD) tool cover

# Directories holding first-party Go source. Scoped rather than "." so a
# formatting check does not descend into tests/e2e/node_modules.
GOFMT_PATHS=cmd internal pkg test

# Build flags
LDFLAGS=-ldflags "-s -w"
BUILD_FLAGS=-v

all: lint test build ## Run lint, test, and build

help: ## Display this help screen
	@grep -h -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

build: ## Build the application
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(BUILD_FLAGS) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_PATH)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

build-mib-importer: ## Build the MIB importer tool
	@echo "Building $(MIB_IMPORTER)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(BUILD_FLAGS) $(LDFLAGS) -o $(BUILD_DIR)/$(MIB_IMPORTER) $(MIB_IMPORTER_PATH)
	@echo "Build complete: $(BUILD_DIR)/$(MIB_IMPORTER)"

build-webui: ## Build the Web UI server
	@echo "Building $(WEBUI)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(BUILD_FLAGS) $(LDFLAGS) -o $(BUILD_DIR)/$(WEBUI) $(WEBUI_PATH)
	@echo "Build complete: $(BUILD_DIR)/$(WEBUI)"

build-backup: ## Build the backup and restore tool
	@echo "Building $(BACKUP)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(BUILD_FLAGS) $(LDFLAGS) -o $(BUILD_DIR)/$(BACKUP) $(BACKUP_PATH)
	@echo "Build complete: $(BUILD_DIR)/$(BACKUP)"

build-cli: ## Build the netmon CLI
	@echo "Building $(CLI)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(BUILD_FLAGS) -ldflags "-s -w \
		-X main.version=$(VERSION) \
		-X main.gitCommit=$(GIT_COMMIT) \
		-X main.buildDate=$(BUILD_DATE)" \
		-o $(BUILD_DIR)/$(CLI) $(CLI_PATH)
	@echo "Build complete: $(BUILD_DIR)/$(CLI)"

install-cli: build-cli ## Install the netmon CLI to /usr/local/bin
	@echo "Installing $(CLI) to /usr/local/bin..."
	@install -m 0755 $(BUILD_DIR)/$(CLI) /usr/local/bin/$(CLI)
	@echo "Installed: /usr/local/bin/$(CLI)"

build-all: build build-mib-importer build-webui build-cli build-backup ## Build all binaries

test: ## Run unit tests only (fast, no Docker required)
	@echo "Running unit tests..."
	$(GOTEST) -v -race -coverprofile=coverage.out ./...

test-unit: test ## Alias for test

test-integration: ## Run integration tests in Docker environment
	@./test/integration/run-tests.sh

# CLAUDE.md documents "make integration-test" as the entry point, and the
# per-package targets already use that order (integration-test-parser). Both
# names now work rather than one of them being a documented command that fails.
integration-test: test-integration ## Alias for test-integration (documented name)

test-e2e: ## Run the Playwright browser suite against a running stack
	@echo "Running end-to-end browser tests..."
	@cd tests/e2e && npm ci && npx playwright test

security: ## Scan dependencies and the linked standard library for known vulnerabilities
	@echo "Running govulncheck..."
	@command -v govulncheck >/dev/null 2>&1 || \
		(echo "govulncheck not installed. Run: make install-tools" && exit 1)
	govulncheck ./...

# --- CI entry points ------------------------------------------------------
#
# CI runs these same targets. A check that exists only in a workflow file
# cannot be reproduced locally and ends up debugged by pushing commits.

check-tracked: ## Fail if anything the build needs is untracked or ignored by git
	@echo "Checking that the build's inputs are in the repository..."
	@missing=""; \
	for f in $$(grep -hoE 'dockerfile:[[:space:]]*[^[:space:]]+' docker-compose*.yml 2>/dev/null | awk '{print $$2}' | sort -u); do \
		git ls-files --error-unmatch "$$f" >/dev/null 2>&1 || missing="$$missing $$f"; \
	done; \
	for d in cmd/*/; do \
		git ls-files --error-unmatch "$$d" >/dev/null 2>&1 || missing="$$missing $$d"; \
	done; \
	if [ -n "$$missing" ]; then \
		echo "These are required to build but are not tracked by git:"; \
		for f in $$missing; do echo "  $$f  ($$(git check-ignore -v "$$f" 2>/dev/null || echo 'untracked'))"; done; \
		echo "A clean checkout cannot build. Add them, or narrow the .gitignore rule."; \
		exit 1; \
	fi
	@echo "All build inputs are tracked."

validate-mibs: build-cli ## Check that each bundled MIB tree parses and resolves on its own
	@echo "Validating the Cisco tree..."
	@$(BUILD_DIR)/$(CLI) mib validate --quiet mibs/asr1001 mibs/standard mibs/cisco mibs/vendor
	@echo "Validating the Dell tree..."
	@$(BUILD_DIR)/$(CLI) mib validate --quiet mibs/r640 mibs/standard

ci-fast: check-tracked fmt-check vet lint test-toolchain test-teardown test build-all validate-mibs ## Everything that does not need Docker
	@echo "ci-fast: passed"

ci: ci-fast integration-test ## Everything a pull request must pass
	@echo "ci: passed"

test-config: ## Run config package tests
	@echo "Running config tests..."
	$(GOTEST) -v -race -coverprofile=coverage-config.out ./internal/config/...

test-logging: ## Run logging package tests
	@echo "Running logging tests..."
	$(GOTEST) -v -race -coverprofile=coverage-logging.out ./internal/logging/...

test-snmp: ## Run SNMP package tests
	@echo "Running SNMP tests..."
	$(GOTEST) -v -race -coverprofile=coverage-snmp.out ./internal/snmp/...

test-poller: ## Run poller package tests
	@echo "Running poller tests..."
	$(GOTEST) -v -race -coverprofile=coverage-poller.out ./internal/poller/...

test-api: ## Run poller control API package tests
	@echo "Running poller control API tests..."
	$(GOTEST) -v -race -coverprofile=coverage-api.out ./internal/api/...

test-mib-importer: ## Run MIB importer package tests
	@echo "Running MIB importer tests..."
	$(GOTEST) -v -race -coverprofile=coverage-mib-importer.out ./internal/mib/importer/...

test-grafana-template: ## Run Grafana template package tests
	@echo "Running Grafana template tests..."
	$(GOTEST) -v -race -coverprofile=coverage-grafana-template.out ./internal/grafana/template/...

bench: ## Run benchmarks
	@echo "Running benchmarks..."
	$(GOTEST) -bench=. -benchmem ./...

bench-config: ## Run config package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/config/...

bench-logging: ## Run logging package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/logging/...

bench-snmp: ## Run SNMP package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/snmp/...

bench-poller: ## Run poller package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/poller/...

bench-api: ## Run poller control API package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/api/...

bench-grafana-template: ## Run Grafana template package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/grafana/template/...

coverage: ## Generate coverage report
	@echo "Generating coverage report..."
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage.out ./...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage.out -o $(COVERAGE_DIR)/coverage.html
	@echo "Coverage report: $(COVERAGE_DIR)/coverage.html"

coverage-config: ## Generate config package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-config.out ./internal/config/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-config.out -o $(COVERAGE_DIR)/coverage-config.html
	@echo "Config coverage: $(COVERAGE_DIR)/coverage-config.html"

coverage-logging: ## Generate logging package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-logging.out ./internal/logging/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-logging.out -o $(COVERAGE_DIR)/coverage-logging.html
	@echo "Logging coverage: $(COVERAGE_DIR)/coverage-logging.html"

coverage-snmp: ## Generate SNMP package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-snmp.out ./internal/snmp/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-snmp.out -o $(COVERAGE_DIR)/coverage-snmp.html
	@echo "SNMP coverage: $(COVERAGE_DIR)/coverage-snmp.html"

coverage-poller: ## Generate poller package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-poller.out ./internal/poller/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-poller.out -o $(COVERAGE_DIR)/coverage-poller.html
	@echo "Poller coverage: $(COVERAGE_DIR)/coverage-poller.html"

coverage-api: ## Generate poller control API package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-api.out ./internal/api/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-api.out -o $(COVERAGE_DIR)/coverage-api.html
	@echo "Poller control API coverage: $(COVERAGE_DIR)/coverage-api.html"

coverage-mib-importer: ## Generate MIB importer package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-mib-importer.out ./internal/mib/importer/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-mib-importer.out -o $(COVERAGE_DIR)/coverage-mib-importer.html
	@echo "MIB importer coverage: $(COVERAGE_DIR)/coverage-mib-importer.html"

coverage-grafana-template: ## Generate Grafana template package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-grafana-template.out ./internal/grafana/template/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-grafana-template.out -o $(COVERAGE_DIR)/coverage-grafana-template.html
	@echo "Grafana template coverage: $(COVERAGE_DIR)/coverage-grafana-template.html"

# The golangci-lint version this repository lints with. MUST match the pin in
# .github/workflows/ci.yml, which installs its own copy -- if you change one,
# change the other. The pin is deliberate (devops.md 9): v2.13.0 ships
# honnef.co/go/tools v0.8.0-rc.1, whose nilness pass panics ("unhandled builtin
# recover"), producing a RED run with ZERO findings.
GOLANGCI_LINT_VERSION := v2.12.2

# GOPATH/bin is where "make install-tools" puts golangci-lint, but it is not on
# every shell's PATH. Looking there explicitly means "make lint" works straight
# after "make install-tools" without the caller having to fix their profile.
GOBIN_DIR := $(shell $(GOCMD) env GOPATH)/bin

# GOBIN_DIR is searched BEFORE PATH, which is the opposite of the usual
# preference and is the point: GOPATH/bin holds the copy install-tools pinned,
# while PATH holds whatever the machine happens to have. On a shared
# workstation GOPATH/bin is itself shared between every checkout and every
# agent, so neither location is trustworthy on its own -- which is why lint
# verifies the version rather than assuming either.
GOLANGCI_LINT := $(shell test -x $(GOBIN_DIR)/golangci-lint && echo $(GOBIN_DIR)/golangci-lint || command -v golangci-lint 2>/dev/null || echo $(GOBIN_DIR)/golangci-lint)

# Where install-tools fetches the installer from. A variable so the guard tests
# can point it at a URL that fails, rather than reaching the network to prove
# that a failed download is refused.
GOLANGCI_LINT_INSTALLER_URL ?= https://raw.githubusercontent.com/golangci/golangci-lint/main/install.sh

# version_of <path> prints the version the binary reports, and FAILS if the
# binary is missing, not executable, or exits nonzero.
#
# The exit status is captured before the output is parsed. Piping the binary
# straight into awk yields awk's status, not the binary's, so a binary that
# prints a plausible banner and then fails its own version command would be
# accepted as good -- a fail-open of exactly the kind these targets exist to
# close. Callers treat empty output as "could not determine" and refuse.
VERSION_OF = version_of() { test -x "$$1" || return 1; out=$$("$$1" version 2>/dev/null) || return 1; printf '%s\n' "$$out" | awk '{ for (i = 1; i < NF; i++) if ($$i == "version") { print $$(i+1); exit } }'; }

lint: ## Run linters
	@echo "Running linters..."
	@test -x "$(GOLANGCI_LINT)" || (echo "golangci-lint not installed. Run: make install-tools" && exit 1)
	@# Refuse to lint with an unpinned version rather than report its results.
	@# A lint run at the wrong version is worse than none, because it is
	@# believed: v2.13.x fails red with zero findings, so the reader goes
	@# looking for a defect that is not there.
	@#
	@# The version is read as the field after the literal word "version"
	@# ("golangci-lint has version 2.12.2 built with go1.26.6 from ...") rather
	@# than by column, so a change to the surrounding wording does not silently
	@# turn this check into a no-op. version_of also fails on a binary that
	@# prints a banner and then exits nonzero; see its definition.
	@$(VERSION_OF); \
	have=$$(version_of "$(GOLANGCI_LINT)") || have=""; \
	want="$(GOLANGCI_LINT_VERSION:v%=%)"; \
	if [ -z "$$have" ]; then \
		echo "Could not read a version from $(GOLANGCI_LINT)."; \
		echo "Refusing to lint with an unidentified binary. Run: make install-tools"; \
		exit 1; \
	fi; \
	if [ "$$have" != "$$want" ]; then \
		echo "golangci-lint is $$have, but this repository pins $$want."; \
		echo "  binary: $(GOLANGCI_LINT)"; \
		echo "Run 'make install-tools' to install the pinned version."; \
		exit 1; \
	fi
	"$(GOLANGCI_LINT)" run --timeout=10m ./...

fmt: ## Format code
	@echo "Formatting code..."
	$(GOCMD) fmt ./...

fmt-check: ## Fail if any file is not gofmt-clean (CI runs this, not fmt)
	@echo "Checking formatting..."
	@unformatted="$$(gofmt -l $(GOFMT_PATHS))"; \
	if [ -n "$$unformatted" ]; then \
		echo "These files are not gofmt-clean:"; \
		echo "$$unformatted"; \
		echo "Run: make fmt"; \
		exit 1; \
	fi
	@echo "All files are gofmt-clean."

vet: ## Run go vet
	@echo "Running go vet..."
	$(GOCMD) vet ./...

# --- Backup and restore ---------------------------------------------------
#
# BACKUPS ARE COLD. "make backup" stops the stack's services, copies the
# volumes at rest and starts them again. That is deliberate: the web UI and
# poller stores are PebbleDB (single-writer, exclusive LOCK) and Prometheus
# runs without --web.enable-admin-api, so there is no supported way to snapshot
# either of them while they are running. See docs/operations/backup-restore.md.
#
# The backup also carries the MIB source tree (./mibs), which is a bind mount
# from this working directory rather than a Docker volume, and lives in no
# volume and under no version control. Restoring it therefore writes into your
# checkout, so it needs ALLOW_TREE_OVERWRITE=1 on top of YES=1 whenever that
# directory already holds files.

BACKUP_ARGS ?=
RESTORE_ARGS ?=
FROM ?=

backup: build-backup ## Back up the deployed stack (volumes, config, MIB source tree, logical export). BACKUP_ARGS='--only metrics'
	@$(BUILD_DIR)/$(BACKUP) backup $(BACKUP_ARGS)

backup-verify: build-backup ## Verify a backup's integrity without changing anything. FROM=backups/<name>
	@test -n "$(FROM)" || (echo "FROM=backups/<name> is required" && exit 1)
	@$(BUILD_DIR)/$(BACKUP) verify --from "$(FROM)"

restore: build-backup ## Restore the stack from a backup. FROM=backups/<name>; YES=1 to apply
	@test -n "$(FROM)" || (echo "FROM=backups/<name> is required" && exit 1)
	@$(BUILD_DIR)/$(BACKUP) restore --from "$(FROM)" $(if $(YES),--apply,) \
		$(if $(ALLOW_TREE_OVERWRITE),--allow-tree-overwrite,) \
		$(if $(MIB_TREE),--mib-tree "$(MIB_TREE)",) $(RESTORE_ARGS)

restore-help: ## Explain the restore flags, including the ./mibs overwrite gate
	@echo "make restore FROM=backups/<name>"
	@echo "    Print the plan and change nothing."
	@echo ""
	@echo "make restore FROM=backups/<name> YES=1"
	@echo "    Apply. Restores Docker volumes and config.yaml/prometheus.yml."
	@echo ""
	@echo "make restore FROM=backups/<name> YES=1 ALLOW_TREE_OVERWRITE=1"
	@echo "    Also replace the MIB source tree (./mibs). Required whenever that"
	@echo "    directory already holds files: it is a bind mount from this working"
	@echo "    copy, not a Docker volume, and the restore EMPTIES it before"
	@echo "    extracting - files added since the backup are deleted, not merged."
	@echo ""
	@echo "make restore FROM=backups/<name> YES=1 MIB_TREE=/tmp/somewhere"
	@echo "    Put the MIB tree somewhere else, e.g. to inspect it first."
	@echo ""
	@echo "make restore FROM=backups/<name> YES=1 RESTORE_ARGS='--skip mibs'"
	@echo "    Restore everything except the MIB source tree."

# --- native deployment ------------------------------------------------------
# Deploys go-netmon to a host as systemd services. The target needs SSH and
# sudo and nothing else — no Docker, no Go toolchain. See docs/deployment/.
#
# HOST is the target; everything else has a working default. The scripts own
# the defaults, so an unset variable here means "the script decides".
HOST ?=
DEPLOY_ARGS ?=
MIGRATE_ARGS ?=
# The backup directory to migrate is named by FROM, already declared above for
# `make restore` and meaning the same thing here. Not BACKUP — that is the
# netmon-backup binary's name.
# The metrics backend. go-netmon stores no samples itself, so a standalone
# appliance needs this; leave it empty only when remote_write points at a
# Prometheus that already exists elsewhere.
WITH_PROMETHEUS ?= 1
PROMETHEUS_IMAGE ?= prom/prometheus:latest

deploy: ## Deploy go-netmon as native systemd services. HOST=<host> [USER=<user>] [WITH_PROMETHEUS=]
	@test -n "$(HOST)" || (echo "HOST=<host> is required, e.g. make deploy HOST=monitor.example USER=ops" && exit 1)
	@./scripts/deploy.sh --host "$(HOST)" \
		$(if $(USER),--user "$(USER)",) \
		$(if $(WITH_PROMETHEUS),--with-prometheus,) $(DEPLOY_ARGS)

deploy-dry-run: ## Show what 'make deploy' would do, changing nothing. HOST=<host>
	@test -n "$(HOST)" || (echo "HOST=<host> is required" && exit 1)
	@./scripts/deploy.sh --host "$(HOST)" \
		$(if $(USER),--user "$(USER)",) \
		$(if $(WITH_PROMETHEUS),--with-prometheus,) --dry-run $(DEPLOY_ARGS)

prometheus-bin: ## Extract the Prometheus binary from its image into dist/
	@mkdir -p dist
	@echo "Extracting prometheus from $(PROMETHEUS_IMAGE)"
	@cid=$$(docker create $(PROMETHEUS_IMAGE)) && \
		docker cp $$cid:/bin/prometheus dist/prometheus && \
		docker rm $$cid >/dev/null
	@chmod 0755 dist/prometheus
	@./dist/prometheus --version 2>&1 | head -1

migrate: ## Restore a backup of the containerised deployment onto the appliance. HOST=<host> FROM=<dir>
	@test -n "$(HOST)" || (echo "HOST=<host> is required, e.g. make migrate HOST=monitor.example FROM=backups/prod" && exit 1)
	@test -n "$(FROM)" || (echo "FROM=<dir> is required" && exit 1)
	@./scripts/migrate.sh --host "$(HOST)" --backup "$(FROM)" \
		$(if $(USER),--user "$(USER)",) $(MIGRATE_ARGS)

migrate-dry-run: ## Show the migration plan without moving anything. HOST=<host> FROM=<dir>
	@test -n "$(HOST)" || (echo "HOST=<host> is required" && exit 1)
	@test -n "$(FROM)" || (echo "FROM=<dir> is required" && exit 1)
	@./scripts/migrate.sh --host "$(HOST)" --backup "$(FROM)" \
		$(if $(USER),--user "$(USER)",) --dry-run $(MIGRATE_ARGS)

stack-capture: ## Capture the running stack's state as JSON. OUT=<file> (default: stdout)
	@./scripts/verify-stack.sh capture $(if $(OUT),"$(OUT)",-)

stack-verify: ## Compare two stack captures. BEFORE=<file> AFTER=<file>
	@test -n "$(BEFORE)" -a -n "$(AFTER)" || (echo "BEFORE=<file> AFTER=<file> are required" && exit 1)
	@./scripts/verify-stack.sh compare "$(BEFORE)" "$(AFTER)"

test-scripts: ## Run the backup/restore script tests (no Docker daemon, no live stack)
	@./scripts/tests/run-tests.sh $(RUN)

test-teardown: ## Test the integration cleanup trap (stub docker, no daemon)
	@./test/integration/teardown-tests.sh $(RUN)

test-toolchain: ## Run the golangci-lint pin guard tests (no network, no shared GOPATH/bin)
	@./scripts/tests/toolchain-tests.sh $(RUN)

# print-golangci-version exists so the guard tests can read the pin rather than
# hardcode it: a test carrying its own copy of the version stops covering the
# pin the moment the pin moves.
print-golangci-version: ## Print the pinned golangci-lint version, without the leading v
	@echo "$(GOLANGCI_LINT_VERSION:v%=%)"

lint-scripts: ## Shellcheck every shell script in the repository
	@command -v shellcheck >/dev/null 2>&1 || (echo "shellcheck not installed" && exit 1)
	@shellcheck -x -P scripts scripts/*.sh scripts/lib/*.sh scripts/tests/run-tests.sh scripts/tests/toolchain-tests.sh scripts/tests/fake-docker
	@shellcheck -x test/integration/run-tests.sh test/integration/teardown-tests.sh
	@echo "shellcheck: clean"

# The compose project this repository deploys into. Every docker compose
# invocation below pins it explicitly. Without -p, compose derives the project
# from the directory name, which is also "go-netmon" — so a command aimed at
# the test stack reaches the deployed one instead. That is how the integration
# runner came to delete live containers; it is not a hypothetical.
COMPOSE_PROJECT?=go-netmon

# Binaries built into the repository root rather than bin/ — "go build ./cmd/x"
# with no -o leaves one behind, and "go test -c" leaves the .test. They are
# named explicitly rather than globbed: a pattern here would eventually match
# something that is not a build artifact, and this repository has no git to
# recover it from.
STRAY_ARTIFACTS=api.test go-netmon webui mib-importer netmon

clean: ## Remove build artifacts (does not touch Docker or backups)
	@echo "Cleaning build artifacts..."
	@rm -rf $(BUILD_DIR)
	@rm -rf $(COVERAGE_DIR)
	@rm -f coverage*.out
	@for f in $(STRAY_ARTIFACTS); do \
		if [ -f "$$f" ]; then echo "  removing stray binary: $$f"; rm -f "$$f"; fi; \
	done
	@rm -rf test_mib.db test-results
	@echo "Clean complete. Docker, backups/ and mibs/ are untouched."

clean-docker: ## Stop this project's containers and remove its images (keeps data)
	@echo "Stopping containers in project $(COMPOSE_PROJECT)..."
	@docker compose -p $(COMPOSE_PROJECT) -f $(DOCKER_COMPOSE) down --remove-orphans 2>/dev/null || true
	@echo "Removing images..."
	@docker rmi go-netmon:latest go-netmon-webui:latest 2>/dev/null || true
	@echo "Containers and images removed. Volumes kept — use clean-data to delete them."

# Deleting the volumes deletes the MIB catalog, every device and profile, and
# the whole metric history. It is gated on an explicit CONFIRM rather than a
# flag that is easy to type by accident, and it names what will go first.
clean-data: ## DESTRUCTIVE: delete this project's volumes (requires CONFIRM=yes)
	@echo "This deletes all data in compose project $(COMPOSE_PROJECT):"
	@echo "  devices, profiles and MIB assignments"
	@echo "  the imported MIB catalog"
	@echo "  all metric history"
	@echo "  Grafana dashboards"
	@echo ""
	@if [ "$(CONFIRM)" != "yes" ]; then \
		echo "Refusing to run. Take a backup first (make backup), then:"; \
		echo "    make clean-data CONFIRM=yes"; \
		exit 1; \
	fi
	@docker compose -p $(COMPOSE_PROJECT) -f $(DOCKER_COMPOSE) down -v --remove-orphans
	@echo "Data removed. Restore with: make restore BACKUP=<dir>"

deps: ## Download dependencies
	@echo "Downloading dependencies..."
	$(GOMOD) download
	$(GOMOD) tidy

network-clean: ## Clean up stale Docker networks
	@echo "Cleaning up Docker networks..."
	@docker compose down --remove-orphans 2>/dev/null || true
	@docker network rm go-netmon_netmon-network 2>/dev/null || true
	@docker network rm go-netmon_trusted0 2>/dev/null || true
	@docker network prune -f 2>/dev/null || true

run: network-clean ## Build and run the entire Docker stack (go-netmon + Prometheus + Grafana + Web UI)
	@echo "Building Docker images..."
	@docker compose build
	@echo "Starting Docker Compose stack..."
	@docker compose up -d
	@echo ""
	@echo "╔═══════════════════════════════════════════════════════════════╗"
	@echo "║                    go-netmon Stack Running                    ║"
	@echo "╚═══════════════════════════════════════════════════════════════╝"
	@echo ""
	@echo "Services:"
	@echo "  ┌─────────────────────────────────────────────────────────────┐"
	@echo "  │ Web UI:        http://localhost:8081                        │"
	@echo "  │ Prometheus:    http://localhost:9091                        │"
	@echo "  │ Grafana:       http://localhost:3000  (admin/admin)         │"
	@echo "  │ go-netmon API: http://localhost:9080                        │"
	@echo "  │ Metrics:       http://localhost:9100/metrics                │"
	@echo "  └─────────────────────────────────────────────────────────────┘"
	@echo ""
	@echo "Quick Start:"
	@echo "  1. Open http://localhost:8081 (Web UI)"
	@echo "  2. Add devices in the Devices tab"
	@echo "  3. Import MIBs in the MIB Manager tab"
	@echo "  4. Assign MIBs to devices"
	@echo "  5. View dashboards by selecting a device on Dashboard tab"
	@echo ""
	@echo "Commands:"
	@echo "  make stop           - Stop all services"
	@echo "  make logs           - View all logs"
	@echo "  make logs-webui     - View Web UI logs only"
	@echo "  make restart        - Rebuild and restart all"
	@echo "  make status         - Show service status"
	@echo ""
	@echo "Waiting for services to be ready..."
	@sleep 5
	@docker compose ps

run-webui: ## Build and run only the Web UI with Prometheus (no SNMP polling)
	@echo "Building Web UI Docker image..."
	@docker compose build webui
	@echo "Starting Web UI and Prometheus..."
	@docker compose up -d prometheus webui
	@echo ""
	@echo "╔═══════════════════════════════════════════════════════════════╗"
	@echo "║              Web UI Running (Development Mode)                ║"
	@echo "╚═══════════════════════════════════════════════════════════════╝"
	@echo ""
	@echo "Services:"
	@echo "  ┌─────────────────────────────────────────────────────────────┐"
	@echo "  │ Web UI:        http://localhost:8081                        │"
	@echo "  │ Prometheus:    http://localhost:9091                        │"
	@echo "  └─────────────────────────────────────────────────────────────┘"
	@echo ""
	@echo "Note: SNMP polling is NOT running. Use 'make run' for full stack."
	@echo ""
	@echo "Commands:"
	@echo "  make stop-webui     - Stop Web UI"
	@echo "  make logs-webui     - View Web UI logs"
	@echo "  make restart-webui  - Restart Web UI"
	@echo ""
	@sleep 3
	@docker compose ps webui prometheus

status: ## Show status of all services
	@echo "Service Status:"
	@docker compose ps

stop: ## Stop all Docker services and clean up networks
	@echo "Stopping Docker Compose stack..."
	@docker compose down --remove-orphans
	@docker network rm go-netmon_netmon-network 2>/dev/null || true
	@docker network rm go-netmon_trusted0 2>/dev/null || true
	@echo "Stack stopped"

stop-webui: ## Stop only Web UI
	@echo "Stopping Web UI..."
	@docker compose stop webui
	@echo "Web UI stopped"

restart: network-clean ## Restart the entire stack
	@echo "Restarting Docker Compose stack..."
	@docker compose build
	@docker compose up -d
	@echo "Stack restarted successfully"

restart-webui: ## Restart only Web UI
	@echo "Restarting Web UI..."
	@docker compose restart webui
	@echo "Web UI restarted"

logs: ## Show logs from all services
	@docker compose logs -f

logs-webui: ## Show Web UI logs
	@docker compose logs -f webui

import: ## Import MIBs into running container (usage: make import DEVICE=asr1001)
	@if [ -z "$(DEVICE)" ]; then \
		echo "Usage: make import DEVICE=<device-name>"; \
		echo ""; \
		echo "Examples:"; \
		echo "  make import DEVICE=asr1001"; \
		echo "  make import DEVICE=nexus9000"; \
		echo ""; \
		echo "This will:"; \
		echo "  1. Download MIBs for the device"; \
		echo "  2. Copy them into the container"; \
		echo "  3. Restart go-netmon to load them"; \
		exit 1; \
	fi
	@echo "Importing MIBs for $(DEVICE)..."
	@if [ ! -f "mibs/cisco/support-list/$(DEVICE)-supportlist.html" ]; then \
		echo "Error: Support list not found: mibs/cisco/support-list/$(DEVICE)-supportlist.html"; \
		exit 1; \
	fi
	@echo "Step 1: Downloading MIBs..."
	@./bin/mib-importer download --device $(DEVICE) \
		--support-list mibs/cisco/support-list/$(DEVICE)-supportlist.html \
		--concurrency 5
	@echo "Step 2: Copying MIBs to container..."
	@docker cp mibs/$(DEVICE) go-netmon:/app/mibs/$(DEVICE)
	@echo "Step 3: Updating config..."
	@echo "Step 4: Restarting go-netmon..."
	@docker restart go-netmon
	@echo "✓ MIBs imported successfully!"
	@echo "Check logs: docker logs go-netmon"

generate-dashboards: ## Generate Grafana dashboards from loaded MIBs
	@echo "Generating Grafana dashboards..."
	@docker exec go-netmon /app/mib-importer dashboard generate-all \
		--storage-path /app/data/mib-storage \
		--output-dir /app/grafana/provisioning/dashboards/_generated \
		--overwrite \
		--index || \
	(echo ""; \
	 echo "Note: Dashboard generation may fail if go-netmon has locked the database."; \
	 echo "Alternative: Use the HTTP API:"; \
	 echo "  curl -X POST http://localhost:8080/api/dashboards/generate"; \
	 echo ""; \
	 exit 1)
	@echo "✓ Dashboards generated!"
	@echo "Restarting Grafana to load new dashboards..."
	@docker restart grafana
	@sleep 5
	@echo "✓ View dashboards at: http://localhost:3000"

docker-build: ## Build Docker image
	@echo "Building Docker image..."
	docker buildx build --load -t go-netmon:latest .

docker-build-webui: ## Build Web UI Docker image
	@echo "Building Web UI Docker image..."
	docker buildx build --load -f Dockerfile.webui -t go-netmon-webui:latest .

redeploy: docker-build ## Rebuild and restart main service
	@echo "Redeploying go-netmon..."
	@docker compose up -d go-netmon
	@echo "go-netmon redeployed"

redeploy-webui: docker-build-webui ## Rebuild and restart Web UI
	@echo "Redeploying Web UI..."
	@docker compose up -d webui
	@echo "Web UI redeployed"

redeploy-webui-fresh: ## Rebuild Web UI, clear MIB database, and restart with fresh data
	@echo "=== Rebuilding and Redeploying Web UI with Fresh MIB Database ==="
	@echo ""
	@echo "Step 1: Stopping and removing Web UI container..."
	@docker compose down webui 2>/dev/null || true
	@docker rm -f go-netmon-webui 2>/dev/null || true
	@echo ""
	@echo "Step 2: Clearing MIB and Web UI databases..."
	@rm -rf data/mib.db
	@docker volume rm go-netmon_webui-data 2>/dev/null || true
	@echo ""
	@echo "Step 3: Building Web UI Docker image..."
	@docker compose build webui
	@echo ""
	@echo "Step 4: Starting Web UI with fresh database..."
	@docker compose up -d webui
	@echo ""
	@echo "Step 5: Waiting for Web UI to initialize..."
	@sleep 5
	@echo ""
	@echo "✓ Web UI Redeployed Successfully!"
	@echo ""
	@echo "Services:"
	@echo "  - Web UI: http://localhost:8081"
	@echo ""
	@echo "Useful commands:"
	@echo "  make logs-webui     - View Web UI logs"
	@echo "  make restart-webui  - Restart Web UI"
	@echo ""
	@docker compose ps webui

docker-up: ## Start Docker Compose stack
	@echo "Starting Docker Compose stack..."
	docker compose -f $(DOCKER_COMPOSE) up -d

docker-down: ## Stop Docker Compose stack
	@echo "Stopping Docker Compose stack..."
	docker compose -f $(DOCKER_COMPOSE) down

docker-logs: ## Show Docker Compose logs
	docker compose -f $(DOCKER_COMPOSE) logs -f

docker-test: test-integration ## Alias for test-integration (deprecated, use test-integration)

integration-up: ## Start integration test services (Prometheus + SNMP)
	@echo "Starting integration test services..."
	docker compose -f docker-compose.test.yml up -d prometheus snmp-simulator
	@echo "Waiting for services to be healthy..."
	@sleep 5
	@echo "Services started. Prometheus: http://localhost:9090, SNMP: localhost:1161"

integration-down: ## Stop integration test services
	@echo "Stopping integration test services..."
	docker compose -f docker-compose.test.yml down -v

integration-logs: ## Show integration test service logs
	docker compose -f docker-compose.test.yml logs -f

integration-shell: ## Open shell in integration test environment
	docker compose -f docker-compose.test.yml run --rm integration-test /bin/sh

install-tools: ## Install development tools
	@echo "Installing development tools..."
	@# .golangci.yml uses the v2 config schema, which only golangci-lint v2 reads.
	@#
	@# Installs whenever GOPATH/bin does not ALREADY hold the pinned version --
	@# the question is "is the right version here", not "is something here".
	@# The old check was `which golangci-lint > /dev/null ||`, which asked the
	@# second question: with any golangci-lint on PATH it installed nothing and
	@# still printed "Tools installed", so someone running this precisely to get
	@# the pinned version was told it had worked while the wrong one stayed.
	@# The installer is downloaded to a file and run as a separate step rather
	@# than piped into sh. `curl ... | sh` reports the exit status of sh, and sh
	@# given empty stdin exits 0, so a failed download printed "Tools installed"
	@# and left nothing behind -- the same fail-open this target exists to close.
	@# Measured: `curl -sSfL https://nonexistent.invalid/x | sh -s -- args` exits 0.
	@#
	@# The installed binary is then asked its version, so a download that
	@# succeeds while delivering the wrong build cannot pass either.
	@$(VERSION_OF); \
	want="$(GOLANGCI_LINT_VERSION:v%=%)"; \
	if [ "$$(version_of $(GOBIN_DIR)/golangci-lint || true)" = "$$want" ]; then \
		echo "golangci-lint $$want already installed"; \
	else \
		echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION) into $(GOBIN_DIR)..."; \
		installer=$$(mktemp) || exit 1; \
		trap 'rm -f "$$installer"' EXIT INT TERM; \
		if ! curl -sSfL $(GOLANGCI_LINT_INSTALLER_URL) -o "$$installer"; then \
			echo "Could not download the golangci-lint installer."; \
			exit 1; \
		fi; \
		if ! sh "$$installer" -b $(GOBIN_DIR) $(GOLANGCI_LINT_VERSION); then \
			echo "The golangci-lint installer failed."; \
			exit 1; \
		fi; \
		got=$$(version_of $(GOBIN_DIR)/golangci-lint) || got=""; \
		if [ "$$got" != "$$want" ]; then \
			echo "Installed golangci-lint reports '$$got', want '$$want'."; \
			echo "  binary: $(GOBIN_DIR)/golangci-lint"; \
			exit 1; \
		fi; \
		echo "Installed golangci-lint $$want"; \
	fi
	@echo "Tools installed"

.DEFAULT_GOAL := help

test-promname: ## Run Prometheus naming package tests
	@echo "Running promname tests..."
	$(GOTEST) -v -race -coverprofile=coverage-promname.out ./internal/promname/...

bench-promname: ## Run Prometheus naming package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/promname/...

coverage-promname: ## Generate Prometheus naming package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-promname.out ./internal/promname/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-promname.out -o $(COVERAGE_DIR)/coverage-promname.html
	@echo "promname coverage: $(COVERAGE_DIR)/coverage-promname.html"

test-grafana: ## Run Grafana package tests
	@echo "Running Grafana tests..."
	$(GOTEST) -v -race -coverprofile=coverage-grafana.out ./internal/grafana/...

test-grafana-builder: ## Run Grafana builder package tests
	@echo "Running Grafana builder tests..."
	$(GOTEST) -v -race -coverprofile=coverage-grafana-builder.out ./internal/grafana/builder/...

bench-grafana: ## Run Grafana package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/grafana/...

bench-grafana-builder: ## Run Grafana builder package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/grafana/builder/...

coverage-grafana: ## Generate Grafana package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-grafana.out ./internal/grafana/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-grafana.out -o $(COVERAGE_DIR)/coverage-grafana.html
	@echo "Grafana coverage: $(COVERAGE_DIR)/coverage-grafana.html"

coverage-grafana-builder: ## Generate Grafana builder package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-grafana-builder.out ./internal/grafana/builder/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-grafana-builder.out -o $(COVERAGE_DIR)/coverage-grafana-builder.html
	@echo "Grafana builder coverage: $(COVERAGE_DIR)/coverage-grafana-builder.html"

test-webui: ## Run Web UI package tests
	@echo "Running Web UI tests..."
	$(GOTEST) -v -race -coverprofile=coverage-webui.out ./internal/webui/...

test-webui-storage: ## Run Web UI storage package tests
	@echo "Running Web UI storage tests..."
	$(GOTEST) -v -race -coverprofile=coverage-webui-storage.out ./internal/webui/storage/...

test-webui-client: ## Run Web UI poller-client package tests
	@echo "Running Web UI client tests..."
	$(GOTEST) -v -race -coverprofile=coverage-webui-client.out ./internal/webui/client/...

bench-webui-client: ## Run Web UI poller-client package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/webui/client/...

coverage-webui-client: ## Generate Web UI poller-client package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-webui-client.out ./internal/webui/client/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-webui-client.out -o $(COVERAGE_DIR)/coverage-webui-client.html
	@echo "Web UI client coverage: $(COVERAGE_DIR)/coverage-webui-client.html"

test-webui-api: ## Run Web UI API package tests
	@echo "Running Web UI API tests..."
	$(GOTEST) -v -race -coverprofile=coverage-webui-api.out ./internal/webui/api/...


test-webui-websocket: ## Run Web UI websocket package tests
	@echo "Running Web UI websocket tests..."
	$(GOTEST) -v -race -coverprofile=coverage-webui-websocket.out ./internal/webui/websocket/...

test-dashboard: ## Run dashboard generator package tests
	@echo "Running dashboard generator tests..."
	$(GOTEST) -v -race -coverprofile=coverage-dashboard.out ./internal/webui/dashboard/...

bench-dashboard: ## Run dashboard generator package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/webui/dashboard/...

coverage-dashboard: ## Generate dashboard generator package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-dashboard.out ./internal/webui/dashboard/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-dashboard.out -o $(COVERAGE_DIR)/coverage-dashboard.html
	@echo "Dashboard generator coverage: $(COVERAGE_DIR)/coverage-dashboard.html"
bench-webui: ## Run Web UI package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/webui/...

bench-webui-storage: ## Run Web UI storage package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/webui/storage/...

coverage-webui: ## Generate Web UI package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-webui.out ./internal/webui/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-webui.out -o $(COVERAGE_DIR)/coverage-webui.html
	@echo "Web UI coverage: $(COVERAGE_DIR)/coverage-webui.html"

coverage-webui-storage: ## Generate Web UI storage package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-webui-storage.out ./internal/webui/storage/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-webui-storage.out -o $(COVERAGE_DIR)/coverage-webui-storage.html
	@echo "Web UI storage coverage: $(COVERAGE_DIR)/coverage-webui-storage.html"

bench-webui-api: ## Run Web UI API package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/webui/api/...

bench-webui-websocket: ## Run Web UI websocket package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/webui/websocket/...

coverage-webui-api: ## Generate Web UI API package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-webui-api.out ./internal/webui/api/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-webui-api.out -o $(COVERAGE_DIR)/coverage-webui-api.html
	@echo "Web UI API coverage: $(COVERAGE_DIR)/coverage-webui-api.html"

coverage-webui-websocket: ## Generate Web UI websocket package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-webui-websocket.out ./internal/webui/websocket/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-webui-websocket.out -o $(COVERAGE_DIR)/coverage-webui-websocket.html
	@echo "Web UI websocket coverage: $(COVERAGE_DIR)/coverage-webui-websocket.html"

test-parser: ## Run MIB parser package tests
	@echo "Running MIB parser tests..."
	$(GOTEST) -v -race -coverprofile=coverage-parser.out ./internal/mib/parser/...

test-parser-enhanced: ## Run enhanced MIB parser tests (requires SNMP MIBs installed)
	@echo "Running enhanced MIB parser tests..."
	@echo "NOTE: These tests require standard IETF MIBs to be installed."
	@echo "      Install with: sudo apt-get install snmp-mibs-downloader && sudo download-mibs"
	@echo ""
	$(GOTEST) -v -race ./internal/mib/parser/... -run "TestEnum|TestTable|TestObject|TestChart|TestIndex|TestDisplay|TestBackward|TestSNMP"

integration-test-parser: ## Run MIB parser integration tests with real MIB files
	@echo "Running MIB parser integration tests..."
	@echo "NOTE: These tests use MIB files in mibs/asr1001/"
	@echo ""
	$(GOTEST) -v -tags=integration -timeout=10m ./internal/mib/parser/...

integration-test-parser-cisco-bgp: ## Run CISCO-BGP4-MIB integration test (catches empty OID bug)
	@echo "Running CISCO-BGP4-MIB integration test..."
	@echo "This test specifically checks for the empty OID bug."
	@echo ""
	$(GOTEST) -v -tags=integration -timeout=5m ./internal/mib/parser/... -run "TestIntegration_CISCO_BGP4_MIB_NoEmptyOIDs"

integration-test-parser-critical: ## Run integration tests for critical MIBs only
	@echo "Running critical MIB integration tests..."
	@echo ""
	$(GOTEST) -v -tags=integration -timeout=5m ./internal/mib/parser/... -run "TestIntegration_CriticalMIBs"

bench-parser: ## Run MIB parser package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/mib/parser/...

bench-parser-integration: ## Run MIB parser integration benchmarks
	@echo "Running MIB parser integration benchmarks..."
	$(GOTEST) -bench=. -benchmem -tags=integration -timeout=10m ./internal/mib/parser/...

coverage-parser: ## Generate MIB parser package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-parser.out ./internal/mib/parser/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-parser.out -o $(COVERAGE_DIR)/coverage-parser.html
	@echo "MIB parser coverage: $(COVERAGE_DIR)/coverage-parser.html"

coverage-parser-integration: ## Generate MIB parser integration test coverage
	@mkdir -p $(COVERAGE_DIR)
	@echo "Generating integration test coverage for MIB parser..."
	$(GOTEST) -tags=integration -coverprofile=$(COVERAGE_DIR)/coverage-parser-integration.out ./internal/mib/parser/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-parser-integration.out -o $(COVERAGE_DIR)/coverage-parser-integration.html
	@echo "MIB parser integration coverage: $(COVERAGE_DIR)/coverage-parser-integration.html"

test-storage: ## Run MIB storage package tests
	@echo "Running MIB storage tests..."
	$(GOTEST) -v -race -coverprofile=coverage-storage.out ./internal/mib/storage/...

bench-storage: ## Run MIB storage package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/mib/storage/...

coverage-storage: ## Generate MIB storage package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-storage.out ./internal/mib/storage/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-storage.out -o $(COVERAGE_DIR)/coverage-storage.html
	@echo "MIB storage coverage: $(COVERAGE_DIR)/coverage-storage.html"

# The loader is what brings a MIB module into a store, and the poller now calls
# it on demand for whatever modules a device is assigned, so it is worth running
# on its own while changing either side.
test-mib-loader: ## Run MIB loader package tests
	@echo "Running MIB loader tests..."
	$(GOTEST) -v -race -coverprofile=coverage-mib-loader.out ./internal/mib/loader/...

bench-mib-loader: ## Run MIB loader package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/mib/loader/...

coverage-mib-loader: ## Generate MIB loader package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-mib-loader.out ./internal/mib/loader/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-mib-loader.out -o $(COVERAGE_DIR)/coverage-mib-loader.html
	@echo "MIB loader coverage: $(COVERAGE_DIR)/coverage-mib-loader.html"

test-metrics: ## Run metrics package tests
	@echo "Running metrics tests..."
	$(GOTEST) -v -race -coverprofile=coverage-metrics.out ./internal/metrics/...

bench-metrics: ## Run metrics package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/metrics/...

coverage-metrics: ## Generate metrics package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-metrics.out ./internal/metrics/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-metrics.out -o $(COVERAGE_DIR)/coverage-metrics.html
	@echo "Metrics coverage: $(COVERAGE_DIR)/coverage-metrics.html"

test-mib: ## Run all MIB package tests
	@echo "Running all MIB package tests..."
	$(GOTEST) -v -race -coverprofile=coverage-mib.out ./internal/mib/...

coverage-mib: ## Generate all MIB package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-mib.out ./internal/mib/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-mib.out -o $(COVERAGE_DIR)/coverage-mib.html
	@echo "MIB coverage: $(COVERAGE_DIR)/coverage-mib.html"

# The search package holds the one definition of what searching a MIB's objects
# or the module catalog means, shared by the API and the CLI, so it is worth
# running on its own while changing either side.
test-mib-search: ## Run MIB search package tests
	@echo "Running MIB search tests..."
	$(GOTEST) -v -race -coverprofile=coverage-mib-search.out ./internal/mib/search/...

bench-mib-search: ## Run MIB search package benchmarks
	$(GOTEST) -bench=. -benchmem ./internal/mib/search/...

coverage-mib-search: ## Generate MIB search package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/coverage-mib-search.out ./internal/mib/search/...
	$(GOCOVER) -html=$(COVERAGE_DIR)/coverage-mib-search.out -o $(COVERAGE_DIR)/coverage-mib-search.html
	@echo "MIB search coverage: $(COVERAGE_DIR)/coverage-mib-search.html"

# The selector holds the one rule deciding what a device polls, shared by the
# poller and the web UI, so it is worth running on its own while changing it.
test-mib-selector: ## Run OID selector package tests
	@echo "Running OID selector tests..."
	$(GOTEST) -v -race -coverprofile=coverage-mib-selector.out ./internal/mib/selector/...

bench-mib-selector: ## Run OID selector package benchmarks
	@echo "Running OID selector benchmarks..."
	$(GOTEST) -bench=. -benchmem -run=^$$ ./internal/mib/selector/

coverage-mib-selector: ## Generate OID selector package coverage
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/mib-selector.out ./internal/mib/selector/...
	$(GOCOVER) -func=$(COVERAGE_DIR)/mib-selector.out | tail -1
	$(GOCOVER) -html=$(COVERAGE_DIR)/mib-selector.out -o $(COVERAGE_DIR)/mib-selector.html
	@echo "Report: $(COVERAGE_DIR)/mib-selector.html"

# ---------------------------------------------------------------- CLI & SDK

test-cli: ## Run netmon CLI package tests
	@echo "Running CLI tests..."
	$(GOTEST) -v -race -coverprofile=coverage-cli.out ./internal/cli/...

test-client: ## Run API client SDK tests
	@echo "Running API client tests..."
	$(GOTEST) -v -race -coverprofile=coverage-client.out ./pkg/client/...


bench-cli: ## Run CLI benchmarks
	@echo "Running CLI benchmarks..."
	$(GOTEST) -bench=. -benchmem -run=^$$ ./internal/cli/...

bench-client: ## Run API client benchmarks
	@echo "Running API client benchmarks..."
	$(GOTEST) -bench=. -benchmem -run=^$$ ./pkg/client/...

bench-mib: ## Run MIB formatting benchmarks
	@echo "Running MIB benchmarks..."
	$(GOTEST) -bench=. -benchmem -run=^$$ ./internal/mib/

coverage-cli: ## Generate netmon CLI coverage report
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/cli.out ./internal/cli/...
	$(GOCOVER) -func=$(COVERAGE_DIR)/cli.out | tail -1
	$(GOCOVER) -html=$(COVERAGE_DIR)/cli.out -o $(COVERAGE_DIR)/cli.html
	@echo "Report: $(COVERAGE_DIR)/cli.html"

coverage-client: ## Generate API client coverage report
	@mkdir -p $(COVERAGE_DIR)
	$(GOTEST) -coverprofile=$(COVERAGE_DIR)/client.out ./pkg/client/...
	$(GOCOVER) -func=$(COVERAGE_DIR)/client.out | tail -1
	$(GOCOVER) -html=$(COVERAGE_DIR)/client.out -o $(COVERAGE_DIR)/client.html
	@echo "Report: $(COVERAGE_DIR)/client.html"

	$(GOCOVER) -html=$(COVERAGE_DIR)/mib.out -o $(COVERAGE_DIR)/mib.html
	@echo "Report: $(COVERAGE_DIR)/mib.html"
