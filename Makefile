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
PROJMUX_INSTALL_SOCKET ?= projmux
INSTALL_MV ?= mv

GO_FILES := $(shell find . -type f -name '*.go' \
	-not -path './.git/*' \
	-not -path './.wt/*')

DEADCODE_ALLOWLIST ?= .deadcode-allowlist.txt
DEADCODE_MUST_KEEP ?= .deadcode-must-keep.txt
DEADCODE_BASELINE_GATE ?= scripts/deadcode_baseline.py

SECURITY_BIN_DIR ?= $(BUILD_DIR)/security-tools
SECURITY_TOOL_MANIFEST ?= .security/security-tools.versions

DOCS_REFERENCE ?= docs/cli.md

.PHONY: fmt fmt-check mod-tidy-check fix build install npm-pack docs test test-integration test-install-smoke test-e2e test-e2e-contract test-e2e-reliability test-e2e-shards test-e2e-manifest test-e2e-coverage test-e2e-update e2e verify deadcode deadcode-contract release-contract security security-serial security-go security-static security-policy security-contract security-tools

build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build -o $(PROJMUX_BIN) ./cmd/projmux
	@echo ">> built $(PROJMUX_BIN)"

install: build
	@mkdir -p $(INSTALL_DIR)
	@echo ">> converging live config before binary publication..."
	@$(PROJMUX_BIN) config apply --bin $(INSTALL_BIN) --socket $(PROJMUX_INSTALL_SOCKET) || { \
	  echo "install pre-publication convergence failed; binary publication not started; recovery: run \`projmux config apply --socket $(PROJMUX_INSTALL_SOCKET)\`" >&2; \
	  exit 1; \
	}
	@tmpfile="$(INSTALL_BIN).tmp.$$$$"; \
	  cp $(PROJMUX_BIN) "$$tmpfile" && \
	  chmod 0755 "$$tmpfile" && \
	  $(INSTALL_MV) "$$tmpfile" $(INSTALL_BIN) || { \
	    echo "install binary publication failed; install not successful; recovery: run \`projmux config apply --socket $(PROJMUX_INSTALL_SOCKET)\`" >&2; \
	    exit 1; \
	  }
	@echo ">> atomically replaced $(INSTALL_BIN)"
	@echo ">> verifying post-publication live config..."
	@$(INSTALL_BIN) config apply --socket $(PROJMUX_INSTALL_SOCKET) || { \
	  echo "install post-publication convergence failed; install not successful; recovery: run \`projmux config apply --socket $(PROJMUX_INSTALL_SOCKET)\`" >&2; \
	  exit 1; \
	}
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

mod-tidy-check:
	$(GO) mod tidy -diff

fix:
	$(GO) fix ./...
	@$(MAKE) --no-print-directory deadcode

# The current baseline is an exact set of symbols reported by deadcode. The
# proactive file protects migration/compatibility/proof APIs whether currently
# reachable, test-only, or reported. New findings are rejected against the
# union, while stale rows are rejected from the current baseline alone.
deadcode: deadcode-contract
	@findings="$$(mktemp)"; \
	trap 'rm -f "$$findings"' EXIT HUP INT TERM; \
	if ! $(GO) tool deadcode ./... > "$$findings"; then \
		echo ">> deadcode: tool execution failed; baseline was not evaluated" >&2; \
		exit 1; \
	fi; \
	python3 $(DEADCODE_BASELINE_GATE) \
		--allowlist $(DEADCODE_ALLOWLIST) \
		--must-keep $(DEADCODE_MUST_KEEP) \
		--findings "$$findings"

deadcode-contract:
	python3 -m unittest discover -s test -p 'deadcode_baseline_test.py'

test: deadcode-contract release-contract
	$(GO) test ./...

release-contract:
	python3 -m unittest discover -s test -p 'release_workflow_contract_test.py'

test-integration:
	scripts/test-integration-docker.sh

test-install-smoke:
	scripts/test-install-smoke.sh

test-e2e: test-e2e-manifest
	scripts/test-e2e-docker.sh

test-e2e-contract:
	test/e2e/evidence-contract.sh

test-e2e-reliability:
	test/e2e/reliability-contract.sh

test-e2e-shards:
	test/e2e/shard-contract.sh
	test/e2e/shard-isolation-stress.sh

test-e2e-manifest:
	E2E_COVERAGE_SKIP_GO=1 test/e2e/coverage-contract.sh

test-e2e-coverage:
	test/e2e/coverage-contract.sh

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
