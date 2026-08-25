GO ?= go
GOFMT ?= gofmt
BUILD_DIR ?= .bin
PROJMUX_BIN ?= $(BUILD_DIR)/projmux

GO_INSTALL_DIR := $(strip $(shell $(GO) env GOBIN 2>/dev/null))
ifeq ($(GO_INSTALL_DIR),)
GO_INSTALL_DIR := $(strip $(shell $(GO) env GOPATH 2>/dev/null))/bin
endif
INSTALL_DIR ?= $(GO_INSTALL_DIR)
INSTALL_BIN := $(INSTALL_DIR)/projmux

GO_FILES := $(shell find . -type f -name '*.go' \
	-not -path './.git/*' \
	-not -path './.wt/*')

DEADCODE_ALLOWLIST ?= .deadcode-allowlist.txt

SECURITY_BIN_DIR ?= $(BUILD_DIR)/security-tools
SECURITY_TOOL_MANIFEST ?= .security/security-tools.versions

DOCS_REFERENCE ?= docs/cli.md

.PHONY: fmt fmt-check fix build install npm-pack docs test test-integration test-install-smoke test-e2e test-e2e-contract test-e2e-reliability test-e2e-shards test-e2e-update e2e verify deadcode security security-serial security-go security-static security-policy security-contract security-tools

build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build -o $(PROJMUX_BIN) ./cmd/projmux
	@echo ">> built $(PROJMUX_BIN)"

install: build
	@mkdir -p $(INSTALL_DIR)
	@tmpfile="$(INSTALL_BIN).tmp.$$$$"; \
	  cp $(PROJMUX_BIN) "$$tmpfile" && \
	  chmod 0755 "$$tmpfile" && \
	  mv "$$tmpfile" $(INSTALL_BIN)
	@echo ">> atomically replaced $(INSTALL_BIN)"
	@echo ">> applying live config..."
	@$(INSTALL_BIN) config apply
	@echo ">> reconciling notify queue..."
	@$(INSTALL_BIN) notification reconcile || true

npm-pack:
	scripts/package-npm.sh --pack

# docs regenerates the generated CLI reference from the command manifest. The
# render goes through a temp file so a failed generator can never leave a
# truncated page behind. `make test` fails when the checked-in page and the
# manifest disagree, so regenerating is mandatory whenever a route changes.
docs:
	@$(GO) run ./internal/tools/gendocs > $(DOCS_REFERENCE).tmp
	@mv $(DOCS_REFERENCE).tmp $(DOCS_REFERENCE)
	@echo ">> regenerated $(DOCS_REFERENCE)"

fmt:
	@if [ -n "$(GO_FILES)" ]; then \
		$(GOFMT) -w $(GO_FILES); \
	else \
		echo "no Go files to format"; \
	fi

fmt-check:
	@if [ -n "$(GO_FILES)" ]; then \
		out="$$( $(GOFMT) -l $(GO_FILES) )"; \
		if [ -n "$$out" ]; then \
			echo "$$out"; \
			exit 1; \
		fi; \
	else \
		echo "no Go files to check"; \
	fi

fix:
	$(GO) fix ./...
	@$(MAKE) --no-print-directory deadcode

# deadcode runs golang.org/x/tools/cmd/deadcode (pinned via the go.mod tool
# directive) over the module and filters findings against an allowlist of
# intentional / MUST-KEEP symbols. It exits non-zero only when a NEW
# (non-allowlisted) unreachable function appears, so the checked-in baseline
# stays green while genuinely new dead code is surfaced.
deadcode:
	@findings="$$( $(GO) tool deadcode ./... )"; \
	if [ -z "$$findings" ]; then \
		echo ">> deadcode: no unreachable functions reported"; \
		exit 0; \
	fi; \
	allow="$$(mktemp)"; \
	grep -v '^[[:space:]]*#' $(DEADCODE_ALLOWLIST) | grep -v '^[[:space:]]*$$' > "$$allow"; \
	remaining="$$( printf '%s\n' "$$findings" | while IFS= read -r line; do \
		sym="$${line##*unreachable func: }"; \
		if grep -Fxq -- "$$sym" "$$allow"; then \
			continue; \
		fi; \
		printf '%s\n' "$$line"; \
	done )"; \
	rm -f "$$allow"; \
	if [ -n "$$remaining" ]; then \
		echo ">> deadcode: NEW unreachable functions (not in $(DEADCODE_ALLOWLIST)):"; \
		printf '%s\n' "$$remaining"; \
		exit 1; \
	fi; \
	echo ">> deadcode: clean (all findings allowlisted in $(DEADCODE_ALLOWLIST))"; \
	exit 0

test:
	$(GO) test ./...

test-integration:
	scripts/test-integration-docker.sh

test-install-smoke:
	scripts/test-install-smoke.sh

test-e2e:
	scripts/test-e2e-docker.sh

test-e2e-contract:
	test/e2e/evidence-contract.sh

test-e2e-reliability:
	test/e2e/reliability-contract.sh

test-e2e-shards:
	test/e2e/shard-contract.sh
	test/e2e/shard-isolation-stress.sh

# Opt-in / local: depends on the public npm registry and published projmux
# package, so it is not part of `verify`.
test-e2e-update:
	scripts/test-e2e-update-docker.sh

e2e: test-e2e

verify: fmt-check test test-integration test-install-smoke test-e2e

# Go-based security tools are pinned to the versions used to produce the
# checked-in baselines. shellcheck, python3, and git are host dependencies;
# scripts/security.sh reports actionable installation guidance when missing.
security-tools:
	@GO="$(GO)" \
		SECURITY_TOOL_MANIFEST="$(abspath $(SECURITY_TOOL_MANIFEST))" \
		SECURITY_BIN_DIR="$(abspath $(SECURITY_BIN_DIR))" \
		scripts/security-tools.sh

security: security-tools
	@SECURITY_BIN_DIR="$(abspath $(SECURITY_BIN_DIR))" scripts/security-aggregate.sh

security-serial: security-tools
	@SECURITY_AGGREGATE_MODE=serial SECURITY_BIN_DIR="$(abspath $(SECURITY_BIN_DIR))" scripts/security-aggregate.sh

security-go: security-tools
	@SECURITY_BIN_DIR="$(abspath $(SECURITY_BIN_DIR))" scripts/security.sh go-security

security-static: security-tools
	@SECURITY_BIN_DIR="$(abspath $(SECURITY_BIN_DIR))" scripts/security.sh go-static

security-policy: security-tools
	@SECURITY_BIN_DIR="$(abspath $(SECURITY_BIN_DIR))" scripts/security.sh repository-policy

security-contract: security-tools
	@SECURITY_BIN_DIR="$(abspath $(SECURITY_BIN_DIR))" test/security-contract.sh
